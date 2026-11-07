package server

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"time"
)

func (a *App) startWorkers() {
	a.startZCodeVersionUpdates()
	for i, v := range a.rows("SELECT id FROM accounts ORDER BY created_at") {
		a.scheduleNext(str(obj(v)["id"]), time.Duration(i)*300*time.Millisecond)
	}
	a.restoreOAuth()
	a.startDailyClaims()
	a.background(func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-a.ctx.Done():
				return
			case <-t.C:
				cfg := a.config()
				a.mu.Lock()
				active := len(a.polling)
				ids := []string{}
				for id, due := range a.pollDue {
					if due <= nowMS() && !a.polling[id] && active < max(1, int(integer(cfg["poll_concurrency"]))) {
						a.polling[id] = true
						delete(a.pollDue, id)
						ids = append(ids, id)
						active++
					}
				}
				a.mu.Unlock()
				for _, id := range ids {
					a.background(func() { defer func() { a.mu.Lock(); delete(a.polling, id); a.mu.Unlock() }(); a.pollAccount(a.ctx, id) })
				}
			}
		}
	})
}
func (a *App) pollAccount(ctx context.Context, id string) {
	r := a.account(id)
	if r == nil {
		return
	}
	cfg := a.config()
	delay := time.Duration(num(cfg["poll_interval_sec"])+rand.Float64()*num(cfg["poll_jitter_sec"])) * time.Second
	defer func() { a.scheduleNext(id, delay) }()
	a.cookieNotice(r)
	if str(r["status"]) == "web_only" {
		delay = time.Duration(num(cfg["web_only_ping_min"]) * float64(time.Minute))
		if res, e := a.customer(ctx, r, str(a.tokens(id)["web_jwt"])); e == nil && res.OK {
			_ = a.updateAccount(id, M{"last_ok_at": nowMS(), "last_error": nil})
		} else {
			_ = a.updateAccount(id, M{"last_error": "网页会话检查失败"})
		}
		return
	}
	if !truth(a.tokens(id)["zcode_jwt"]) {
		return
	}
	b, e := a.fetchBalance(ctx, r)
	if e != nil {
		var failure *Failure
		if errors.As(e, &failure) && failure.RetryAfter > 0 && (failure.Kind == "rate_limit" || failure.Kind == "account_risk") {
			delay = max(delay, failure.RetryAfter)
			_ = a.updateAccount(id, M{"last_error": conciseFailureMessage(failure)})
			return
		}
		_ = a.updateAccount(id, M{"fail_streak": integer(r["fail_streak"]) + 1, "last_error": "balance 异常: " + e.Error()})
		delay *= 3
		return
	}
	if truth(b["ok"]) {
		a.applyBalance(r, b, true)
		return
	}
	if truth(b["authFailed"]) {
		_ = a.updateAccount(id, M{"status": "needs_relogin", "fail_streak": integer(r["fail_streak"]) + 1, "last_error": "balance 401"})
		_, _ = a.startOAuth(ctx, str(r["family"]), id, false)
		return
	}
	if b["failure_kind"] == "rate_limit" || b["failure_kind"] == "account_risk" {
		delay = max(delay, ms(b["retry_after_ms"]))
		_ = a.updateAccount(id, M{"last_error": conciseFailureMessage(&Failure{Kind: str(b["failure_kind"]), Code: str(b["error_code"])})})
		return
	}
	streak := integer(r["fail_streak"]) + 1
	_ = a.updateAccount(id, M{"fail_streak": streak, "last_error": fmt.Sprintf("balance HTTP %v", b["httpStatus"])})
	delay = time.Duration(math.Min(3600, num(cfg["poll_interval_sec"])*math.Pow(2, float64(min(streak, 6))))) * time.Second
}
func (a *App) cookieNotice(r M) {
	expiry := integer(r["web_cookie_expires_at"])
	if expiry <= 0 || !truth(a.tokens(str(r["id"]))["web_jwt"]) {
		return
	}
	left := expiry - nowMS()
	stage := ""
	switch {
	case left <= 0:
		stage = "expired"
	case left <= 86400000:
		stage = "day"
	case left <= 7*86400000:
		stage = "week"
	}
	if stage == "" {
		return
	}
	key := "cookie_notice:" + str(r["id"])
	state := obj(a.setting(key, M{}))
	if integer(state["expiry"]) != expiry {
		state = M{"expiry": expiry}
	}
	if state[stage] == nil {
		state[stage] = nowMS()
		_ = a.setSetting(key, state)
		a.log.Printf("账号 %s 的网页会话到期提醒: %s", str(r["label"]), stage)
	}
}
