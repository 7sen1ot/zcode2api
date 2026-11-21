package server

import (
	"context"
	"errors"
	"fmt"
	"math"
	rand "math/rand/v2"
	"time"
	"zcode2api/internal/server/fault"
	"zcode2api/internal/server/shared"
)

func (a *App) startWorkers() {
	for i, v := range a.rows("SELECT id FROM accounts ORDER BY created_at") {
		a.scheduleNext(shared.Str(shared.Obj(v)["id"]), time.Duration(i)*300*time.Millisecond)
	}
	a.restoreOAuth()
	a.startDailyClaims()
	a.startProxyHealthProbes()
	a.startBalanceSnapshotCleanup()
	a.startUsageLogCleanup()
	a.startUpdateBackupCleanup()
	a.background(func() {
		t := time.NewTimer(0)
		defer t.Stop()
		for {
			select {
			case <-a.ctx.Done():
				return
			case <-a.pollWake:
			case <-t.C:
			}
			t.Stop()
			limit := max(1, int(shared.Integer(a.configValue("poll_concurrency"))))
			now, next := shared.NowMS(), int64(0)
			a.mu.Lock()
			active := len(a.polling)
			ids := []string{}
			for id, due := range a.pollDue {
				if a.polling[id] {
					continue
				}
				if due <= now && active < limit {
					a.polling[id] = true
					delete(a.pollDue, id)
					ids = append(ids, id)
					active++
				} else if next == 0 || due < next {
					next = due
				}
			}
			a.mu.Unlock()
			// Full workers wake us when they finish; an empty queue needs no timer.
			if active < limit && next > 0 {
				t.Reset(max(time.Until(time.UnixMilli(next)), time.Millisecond))
			}
			for _, id := range ids {
				a.background(func() {
					defer func() { a.mu.Lock(); delete(a.polling, id); a.mu.Unlock(); shared.Wake(a.pollWake) }()
					a.pollAccount(a.ctx, id)
				})
			}
		}
	})
}

func (a *App) pollAccount(ctx context.Context, id string) {
	r := a.account(id)
	if r == nil || r["status"] == "disabled" {
		return
	}
	cfg := a.config("poll_interval_sec", "poll_jitter_sec", "web_only_ping_min")
	delay := time.Duration(shared.Num(cfg["poll_interval_sec"])+rand.Float64()*shared.Num(cfg["poll_jitter_sec"])) * time.Second
	defer func() { a.scheduleNext(id, max(delay, time.Second)) }()
	tokens := a.accountTokens(r)
	a.cookieNotice(r, tokens)
	if shared.Str(r["status"]) == "web_only" {
		delay = time.Duration(shared.Num(cfg["web_only_ping_min"]) * float64(time.Minute))
		if res, e := a.customer(ctx, r, tokens["web_jwt"]); e == nil && res.OK {
			a.updateAccountStatusIfCurrent(r, nil, shared.M{"last_ok_at": shared.NowMS(), "last_error": nil})
		} else {
			a.updateAccountStatusIfCurrent(r, nil, shared.M{"last_error": "网页会话检查失败"})
		}
		return
	}
	if tokens["zcode_jwt"] == "" {
		return
	}
	b, e := a.fetchBalance(ctx, r)
	if e != nil {
		var failure *fault.Failure
		if errors.As(e, &failure) && failure.Code == "balance_context_changed" {
			delay = 0
			return
		}
		if errors.As(e, &failure) && failure.RetryAfter > 0 && (failure.Kind == "rate_limit" || failure.Kind == "account_risk") {
			delay = max(delay, failure.RetryAfter)
			a.updateAccountStatusIfCurrent(r, nil, shared.M{"last_error": fault.ConciseFailureMessage(failure)})
			return
		}
		a.updateAccountStatusIfCurrent(r, nil, shared.M{"fail_streak": shared.Integer(r["fail_streak"]) + 1, "last_error": "balance 异常: " + e.Error()})
		delay *= 3
		return
	}
	if shared.Truth(b["ok"]) {
		a.applyBalance(r, b, true)
		return
	}
	if shared.Truth(b["authFailed"]) {
		if a.updateAccountStatusIfCurrent(r, b, shared.M{"status": "needs_relogin", "fail_streak": shared.Integer(r["fail_streak"]) + 1, "last_error": shared.Fallback(b["reason"], "ZCode 登录鉴权失效，请重新授权")}) {
			_, _ = a.startOAuth(ctx, a.accountLoginProvider(r), id, false)
		}
		return
	}
	if b["failure_kind"] == "rate_limit" || b["failure_kind"] == "account_risk" {
		delay = max(delay, shared.Ms(b["retry_after_ms"]))
		a.updateAccountStatusIfCurrent(r, b, shared.M{"last_error": fault.ConciseFailureMessage(&fault.Failure{Kind: shared.Str(b["failure_kind"]), Code: shared.Str(b["error_code"])})})
		return
	}
	streak := shared.Integer(r["fail_streak"]) + 1
	a.updateAccountStatusIfCurrent(r, b, shared.M{"fail_streak": streak, "last_error": fmt.Sprintf("balance HTTP %v", b["httpStatus"])})
	delay = time.Duration(math.Min(3600, shared.Num(cfg["poll_interval_sec"])*math.Pow(2, float64(min(streak, 6))))) * time.Second
}

func (a *App) cookieNotice(r shared.M, tokens map[string]string) {
	expiry := shared.Integer(r["web_cookie_expires_at"])
	if expiry <= 0 || tokens["web_jwt"] == "" {
		return
	}
	left := expiry - shared.NowMS()
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
	key := "cookie_notice:" + shared.Str(r["id"])
	state := shared.Obj(a.setting(key, shared.M{}))
	if shared.Integer(state["expiry"]) != expiry {
		state = shared.M{"expiry": expiry}
	}
	if state[stage] == nil {
		state[stage] = shared.NowMS()
		_ = a.setSetting(key, state)
		a.log.Printf("账号 %s 的网页会话到期提醒: %s", shared.Str(r["label"]), stage)
	}
}
