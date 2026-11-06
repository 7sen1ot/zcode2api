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

func nextDailyClaim(now time.Time, hour, minute int) time.Time {
	local := now.In(claimTimezone)
	next := time.Date(local.Year(), local.Month(), local.Day(), hour, minute, 0, 0, claimTimezone)
	if !next.After(now) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}

func (a *App) dailyClaimSchedule() M {
	enabled := truth(a.config()["daily_claim_enabled"])
	at := a.dailyClaimTime()
	hour, minute := dailyClaimClock(at)
	var next any
	if enabled {
		next = nextDailyClaim(time.Now(), hour, minute).UnixMilli()
	}
	return M{"enabled": enabled, "time": at, "timezone": "Asia/Shanghai", "next_run_at": next, "last_run": a.setting("daily_claim_last_run", nil)}
}

// rescheduleClaims wakes the daily-claim timer after the configured time changes.
func (a *App) rescheduleClaims() {
	select {
	case a.claimReschedule <- struct{}{}:
	default:
	}
}

func (a *App) startDailyClaims() {
	a.background(func() {
		for {
			at := a.dailyClaimTime()
			hour, minute := dailyClaimClock(at)
			due := nextDailyClaim(time.Now(), hour, minute)
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
			for truth(a.config()["daily_claim_enabled"]) && time.Now().Before(due.Add(5*time.Minute)) {
				result, err := a.autoClaimScope(a.ctx, false, due.Format("2006-01-02"))
				if err != nil {
					a.log.Printf("每日 %s 自动领卡启动失败: %v", at, err)
					break
				}
				if !truth(result["in_progress"]) {
					if !truth(result["skipped"]) {
						a.log.Printf("每日 %s 自动领卡完成: 检查 %v 个账号，领取 %v 张", at, result["checked"], result["claimed"])
					}
					break
				}
				if pause(a.ctx, 15*time.Second) != nil {
					return
				}
			}
		}
	})
}
