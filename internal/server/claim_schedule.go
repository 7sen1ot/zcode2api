package server

import (
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
	value := strings.TrimSpace(str(a.config()["daily_claim_time"]))
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
	// Saving/enabling during the selected minute must still run this occurrence.
	if !now.Before(next.Add(time.Minute)) || claimRunRecorded(last, next) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}

func (a *App) dailyClaimSchedule() M {
	enabled := truth(a.config()["daily_claim_enabled"])
	at := a.dailyClaimTime()
	hour, minute := dailyClaimClock(at)
	last := a.setting("daily_claim_last_run", nil)
	var next any
	if enabled {
		next = nextDailyClaim(time.Now(), hour, minute, obj(last)).UnixMilli()
	}
	return M{"enabled": enabled, "time": at, "timezone": "Asia/Shanghai", "next_run_at": next, "last_run": last}
}

// rescheduleClaims wakes the worker after its time or enabled state changes.
func (a *App) rescheduleClaims() {
	select {
	case a.claimReschedule <- struct{}{}:
	default:
	}
}

func (a *App) startDailyClaims() {
	a.background(func() {
		var attemptedAt time.Time
	schedule:
		for a.ctx.Err() == nil {
			if !truth(a.config()["daily_claim_enabled"]) {
				select {
				case <-a.ctx.Done():
					return
				case <-a.claimReschedule:
					continue
				}
			}
			at := a.dailyClaimTime()
			hour, minute := dailyClaimClock(at)
			due := nextDailyClaim(time.Now(), hour, minute, obj(a.setting("daily_claim_last_run", nil)))
			if !due.After(attemptedAt) {
				due = due.AddDate(0, 0, 1)
			}
			timer := time.NewTimer(time.Until(due))
			select {
			case <-a.ctx.Done():
				timer.Stop()
				return
			case <-a.claimReschedule:
				timer.Stop()
				continue
			case <-timer.C:
			}
			// Let an existing manual batch finish before taking the shared lock.
			for truth(a.config()["daily_claim_enabled"]) && a.dailyClaimTime() == at && time.Now().Before(due.Add(5*time.Minute)) {
				result, err := a.autoClaimScope(a.ctx, false, due)
				if err != nil {
					attemptedAt = due
					a.log.Printf("每日 %s 自动领卡启动失败: %v", at, err)
					break
				}
				if !truth(result["in_progress"]) {
					attemptedAt = due
					if !truth(result["skipped"]) {
						a.log.Printf("每日 %s 自动领卡完成: 检查 %v 个账号，领取 %v 张", at, result["checked"], result["claimed"])
					}
					break
				}
				retry := time.NewTimer(15 * time.Second)
				select {
				case <-a.ctx.Done():
					retry.Stop()
					return
				case <-a.claimReschedule:
					retry.Stop()
					continue schedule
				case <-retry.C:
				}
			}
		}
	})
}
