package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

var claimTimezone = time.FixedZone("Asia/Shanghai", 8*60*60)

const defaultDailyClaimTime = "19:02"

// dailyClaimClock parses a "HH:MM" claim time, falling back to the default.
func dailyClaimClock(value string) (int, int) {
	hh, mm, found := strings.Cut(strings.TrimSpace(value), ":")
	h, e1 := strconv.Atoi(hh)
	m, e2 := strconv.Atoi(mm)
	if !found || e1 != nil || e2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 19, 2
	}
	return h, m
}

func validDailyClaimTime(value string) bool {
	h, m := dailyClaimClock(value)
	return fmt.Sprintf("%02d:%02d", h, m) == strings.TrimSpace(value)
}

func (a *App) dailyClaimTime() string {
	value := strings.TrimSpace(str(a.configValue("daily_claim_time")))
	if value == "" {
		return defaultDailyClaimTime
	}
	h, m := dailyClaimClock(value)
	return fmt.Sprintf("%02d:%02d", h, m)
}

func claimRunRecorded(last M, due time.Time) bool {
	// Older records have only started_at. A later configured time on the same
	// day is a new occurrence, while restarting must not replay the same one.
	return integer(first(last["scheduled_at"], last["started_at"])) >= due.UnixMilli()
}

func nextDailyClaim(now time.Time, hour, minute int, last M) time.Time {
	local := now.In(claimTimezone)
	next := time.Date(local.Year(), local.Month(), local.Day(), hour, minute, 0, 0, claimTimezone)
	if integer(last["version"]) == 2 && scheduledClaimOpen(str(last["status"])) {
		saved := time.UnixMilli(integer(last["scheduled_at"]))
		if !now.Before(saved) && now.UnixMilli() < integer(last["retry_deadline"]) && saved.In(claimTimezone).Format("15:04") == fmt.Sprintf("%02d:%02d", hour, minute) {
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
	resume := integer(last["version"]) == 2 && integer(last["scheduled_at"]) == next.UnixMilli() && scheduledClaimOpen(str(last["status"]))
	if !now.Before(next.Add(dailyClaimRecoveryWindow)) || (claimRunRecorded(last, next) && !resume) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}

func (a *App) dailyClaimSchedule() M {
	enabled := truth(a.configValue("daily_claim_enabled"))
	at := a.dailyClaimTime()
	hour, minute := dailyClaimClock(at)
	last := a.setting("daily_claim_last_run", nil)
	if record, ok := last.(map[string]any); ok {
		last = scheduledClaimReport(record)
	}
	var next, browserWindow any
	if enabled {
		due := nextDailyClaim(time.Now(), hour, minute, obj(last))
		browserWindow = M{"start_at": due.Add(-claimBrowserWarmup).UnixMilli(), "end_at": due.Add(dailyClaimRecoveryWindow).UnixMilli(), "idle_seconds": int(claimBrowserIdle.Seconds())}
		if integer(obj(last)["scheduled_at"]) == due.UnixMilli() {
			due = time.UnixMilli(max(due.UnixMilli(), integer(obj(last)["next_retry_at"])))
		}
		next = due.UnixMilli()
	}
	return M{"enabled": enabled, "time": at, "timezone": "Asia/Shanghai", "next_run_at": next, "last_run": last, "browser_window": browserWindow, "next_retry_at": obj(last)["next_retry_at"], "retry_deadline": obj(last)["retry_deadline"], "pending": obj(last)["pending"], "pending_manual": obj(last)["pending_manual"], "failed": obj(last)["failed"]}
}

// rescheduleClaims wakes the worker after its time or enabled state changes.
func (a *App) rescheduleClaims() {
	a.mu.Lock()
	cancel := a.claimScheduleCancel
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	wake(a.claimReschedule)
}

func (a *App) startDailyClaims() {
	a.background(func() {
		defer a.claimBrowserWindow(time.Time{})
		var unavailableUntil, preparedAt time.Time
		for a.ctx.Err() == nil {
			if !truth(a.configValue("daily_claim_enabled")) {
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
			hour, minute := dailyClaimClock(at)
			last := obj(a.setting("daily_claim_last_run", nil))
			if integer(last["version"]) == 2 && scheduledClaimOpen(str(last["status"])) && nowMS() >= integer(last["retry_deadline"]) {
				var expired scheduledClaimRun
				if err := json.Unmarshal([]byte(js(last)), &expired); err == nil {
					if err := a.reconcileScheduledClaims(&expired); err != nil {
						a.log.Printf("自动领卡领取记录核对失败: %v", err)
					} else {
						expired.recoverInterrupted(time.Now())
						if err := a.saveScheduledClaims(&expired); err != nil {
							a.log.Printf("自动领卡结束状态保存失败: %v", err)
						}
						last = obj(parse(js(expired), nil))
					}
				}
			}
			due := nextDailyClaim(time.Now(), hour, minute, last)
			if time.Now().Before(due.Add(-claimBrowserWarmup)) {
				a.claimBrowserWindow(time.Time{})
			}
			wake := maxTime(due, unavailableUntil)
			if integer(last["scheduled_at"]) == due.UnixMilli() {
				wake = maxTime(wake, time.UnixMilli(integer(last["next_retry_at"])))
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
			var result M
			var err error
			if warming {
				preparedAt = due
				err = a.warmClaimBrowser(ctx, obj(a.configValue("http", "captcha")), due)
				if err == nil {
					err = a.prepareScheduledClaimBrowsers(ctx, due)
				}
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
			if err == nil && !truth(result["in_progress"]) && integer(result["pending"]) == 0 {
				a.claimBrowserWindow(time.Time{})
			}
			if err != nil {
				unavailableUntil = time.Now().Add(dailyClaimRecoveryDelay)
				a.log.Printf("每日 %s 自动领卡执行失败: %v", at, err)
			} else if truth(result["in_progress"]) {
				unavailableUntil = time.Now().Add(15 * time.Second)
			} else {
				unavailableUntil = time.Time{}
				if !truth(result["skipped"]) {
					a.log.Printf("每日 %s 自动领卡状态 %v: 检查 %v 个账号，领取 %v 张，失败 %v 项，需人工 %v 项，待恢复 %v 个账号", at, result["status"], result["checked"], result["claimed"], result["failed"], result["pending_manual"], result["pending"])
				}
			}
		}
	})
}
