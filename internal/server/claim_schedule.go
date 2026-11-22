package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	settings "zcode2api/internal/server/config"
	"zcode2api/internal/server/shared"
)

var claimTimezone = time.FixedZone("Asia/Shanghai", 8*60*60)

func (a *App) dailyClaimTime() string {
	value := strings.TrimSpace(shared.Str(a.configValue("daily_claim_time")))
	if value == "" {
		return settings.DefaultDailyClaimTime
	}
	h, m := settings.DailyClaimClock(value)
	return fmt.Sprintf("%02d:%02d", h, m)
}

func claimRunRecorded(last shared.M, due time.Time) bool {
	// Older records have only started_at. A later configured time on the same
	// day is a new occurrence, while restarting must not replay the same one.
	return shared.Integer(shared.First(last["scheduled_at"], last["started_at"])) >= due.UnixMilli()
}

func nextDailyClaim(now time.Time, hour, minute int, last shared.M) time.Time {
	local := now.In(claimTimezone)
	next := time.Date(local.Year(), local.Month(), local.Day(), hour, minute, 0, 0, claimTimezone)
	if shared.Integer(last["version"]) == 2 && scheduledClaimOpen(shared.Str(last["status"])) {
		saved := time.UnixMilli(shared.Integer(last["scheduled_at"]))
		if !now.Before(saved) && now.UnixMilli() < shared.Integer(last["retry_deadline"]) && saved.In(claimTimezone).Format("15:04") == fmt.Sprintf("%02d:%02d", hour, minute) {
			return saved
		}
	}
	if now.Before(next) {
		previous := next.AddDate(0, 0, -1)
		if now.Before(previous.Add(dailyClaimRecoveryWindow)) && !claimRunRecorded(last, previous) {
			return previous
		}
	}
	// Only versioned, unfinished checkpoints may resume. Legacy runs retain
	// their original no-replay behavior when upgrading.
	resume := shared.Integer(last["version"]) == 2 && shared.Integer(last["scheduled_at"]) == next.UnixMilli() && scheduledClaimOpen(shared.Str(last["status"]))
	if !now.Before(next.Add(dailyClaimRecoveryWindow)) || (claimRunRecorded(last, next) && !resume) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}

func (a *App) dailyClaimSchedule() shared.M {
	enabled := shared.Truth(a.configValue("daily_claim_enabled"))
	at := a.dailyClaimTime()
	hour, minute := settings.DailyClaimClock(at)
	last := a.setting("daily_claim_last_run", nil)
	if record, ok := last.(map[string]any); ok {
		if shared.Integer(record["version"]) == 2 && shared.Str(record["day"]) == time.Now().In(claimTimezone).Format("2006-01-02") && !scheduledClaimOpen(shared.Str(record["status"])) {
			var run scheduledClaimRun
			if json.Unmarshal([]byte(shared.JSON(record)), &run) == nil && a.reconcileScheduledClaims(&run) == nil {
				// A manual completion must disappear from the pending list in
				// every browser without rewriting a scheduler-owned checkpoint.
				run.summarize()
				record = shared.Obj(shared.Parse(shared.JSON(run), nil))
			}
		}
		last = scheduledClaimReport(record)
	}
	var next, browserWindow any
	if enabled {
		due := nextDailyClaim(time.Now(), hour, minute, shared.Obj(last))
		browserWindow = shared.M{"start_at": due.Add(-claimBrowserWarmup).UnixMilli(), "end_at": due.Add(dailyClaimRecoveryWindow).UnixMilli(), "idle_seconds": int(claimBrowserIdle.Seconds())}
		if shared.Integer(shared.Obj(last)["scheduled_at"]) == due.UnixMilli() {
			due = time.UnixMilli(max(due.UnixMilli(), shared.Integer(shared.Obj(last)["next_retry_at"])))
		}
		next = due.UnixMilli()
	}
	return shared.M{"enabled": enabled, "time": at, "timezone": "Asia/Shanghai", "next_run_at": next, "last_run": last, "browser_window": browserWindow, "next_retry_at": shared.Obj(last)["next_retry_at"], "retry_deadline": shared.Obj(last)["retry_deadline"], "pending": shared.Obj(last)["pending"], "pending_manual": shared.Obj(last)["pending_manual"], "failed": shared.Obj(last)["failed"]}
}

func (a *App) rescheduleClaims() {
	a.mu.Lock()
	cancel := a.claimScheduleCancel
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	shared.Wake(a.claimReschedule)
}

func (a *App) startDailyClaims() {
	a.background(func() {
		defer a.claimBrowserWindow(time.Time{})
		var unavailableUntil, preparedAt time.Time
		for a.ctx.Err() == nil {
			if !shared.Truth(a.configValue("daily_claim_enabled")) {
				preparedAt = time.Time{}
				a.claimBrowserWindow(time.Time{})
				select {
				case <-a.ctx.Done():
					return
				case <-a.claimReschedule:
					continue
				}
			}
			at := a.dailyClaimTime()
			hour, minute := settings.DailyClaimClock(at)
			last := shared.Obj(a.setting("daily_claim_last_run", nil))
			if shared.Integer(last["version"]) == 2 && scheduledClaimOpen(shared.Str(last["status"])) && shared.NowMS() >= shared.Integer(last["retry_deadline"]) {
				var expired scheduledClaimRun
				if err := json.Unmarshal([]byte(shared.JSON(last)), &expired); err == nil {
					if err := a.reconcileScheduledClaims(&expired); err != nil {
						a.log.Printf("自动领卡领取记录核对失败: %v", err)
					} else {
						expired.recoverInterrupted(time.Now())
						if err := a.saveScheduledClaims(&expired); err != nil {
							a.log.Printf("自动领卡结束状态保存失败: %v", err)
						}
						last = shared.Obj(shared.Parse(shared.JSON(expired), nil))
					}
				}
			}
			due := nextDailyClaim(time.Now(), hour, minute, last)
			if time.Now().Before(due.Add(-claimBrowserWarmup)) {
				a.claimBrowserWindow(time.Time{})
			}
			wake := shared.MaxTime(due, unavailableUntil)
			if shared.Integer(last["scheduled_at"]) == due.UnixMilli() {
				wake = shared.MaxTime(wake, time.UnixMilli(shared.Integer(last["next_retry_at"])))
			}
			if !due.Equal(preparedAt) && time.Now().Before(due) && due.Add(-claimBrowserWarmup).Before(wake) {
				wake = due.Add(-claimBrowserWarmup)
			}
			timer := time.NewTimer(time.Until(wake))
			select {
			case <-a.ctx.Done():
				timer.Stop()
				return
			case <-a.claimReschedule:
				timer.Stop()
				preparedAt = time.Time{}
				continue
			case <-timer.C:
			}
			if !a.scheduledClaimEnabled(due) || !time.Now().Before(due.Add(dailyClaimRecoveryWindow)) {
				continue
			}
			ctx, cancel := context.WithCancel(a.ctx)
			a.mu.Lock()
			a.claimScheduleCancel = cancel
			a.mu.Unlock()
			warming := time.Now().Before(due)
			var result shared.M
			var err error
			if warming {
				preparedAt = due
				err = a.prepareScheduledClaimBrowser(ctx, due)
			} else {
				result, err = a.autoClaimScope(ctx, false, due)
			}
			cancel()
			a.mu.Lock()
			a.claimScheduleCancel = nil
			a.mu.Unlock()
			if warming {
				if err != nil && a.ctx.Err() == nil {
					a.log.Printf("自动领卡浏览器预热失败: %v", err)
				}
				continue
			}
			if err == nil && !shared.Truth(result["in_progress"]) && shared.Integer(result["pending"]) == 0 {
				a.claimBrowserWindow(time.Time{})
			}
			if err != nil {
				unavailableUntil = time.Now().Add(dailyClaimRecoveryDelay)
				a.log.Printf("每日 %s 自动领卡执行失败: %v", at, err)
			} else if shared.Truth(result["in_progress"]) {
				unavailableUntil = time.Now().Add(15 * time.Second)
			} else {
				unavailableUntil = time.Time{}
				if !shared.Truth(result["skipped"]) {
					a.log.Printf("每日 %s 自动领卡状态 %v: 检查 %v 个账号，领取 %v 张，失败 %v 项，需人工 %v 项，待恢复 %v 个账号", at, result["status"], result["checked"], result["claimed"], result["failed"], result["pending_manual"], result["pending"])
				}
			}
		}
	})
}
