package server

import (
	"context"
	"math"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"
)

func retryAfter(s string, now time.Time) time.Duration {
	if s == "" {
		return 0
	}
	if n, e := strconv.ParseFloat(s, 64); e == nil && n >= 0 && !math.IsNaN(n) && !math.IsInf(n, 0) && n < float64(1<<63-1)/float64(time.Second) {
		return time.Duration(n * float64(time.Second))
	}
	if t, e := http.ParseTime(s); e == nil {
		return max(0, t.Sub(now))
	}
	return 0
}
func classify(f *Failure) *Failure {
	if f == nil {
		return nil
	}
	if f.Code == "captcha_verification_failed" {
		switch f.VerifyCode {
		case "F001":
			f.Kind, f.Code, f.Retryable = "risk", "captcha_risk_blocked", false
		case "F010":
			f.Kind, f.Code, f.Retryable = "rate_limit", "captcha_ip_rate_limited", true
		case "F011":
			f.Kind, f.Code, f.Retryable = "rate_limit", "captcha_device_rate_limited", true
		}
	}
	if f.Kind != "" {
		return f
	}
	if f.Status == http.StatusTooManyRequests {
		f.Kind, f.Retryable = "rate_limit", true
		return f
	}
	kind := ""
	retry := false
	switch f.Code {
	case "rate_limit_error", "1302", "1303", "1305", "3002", "F010", "F011", "captcha_ip_rate_limited", "captcha_device_rate_limited":
		kind, retry = "rate_limit", true
	case "authentication_error":
		kind = "auth"
	case "3007":
		kind = "auth"
		if explicitCaptchaChallenge(f.Message) {
			kind = "risk"
		}
	case "permission_error":
		kind = "permission"
	case "invalid_request_error", "3001", "3006":
		kind = "invalid"
	case "500", "2007", "1120", "1230":
		kind, retry = "server", true
	case "1234", "network", "upstream_stream_error", "proxy_auth_failed", "proxy_connection_failed":
		kind, retry = "network", true
	case "503", "529", "1312", "engine_overloaded_error", "overloaded_error":
		kind, retry = "overload", true
	case "3008", "3009", "3010":
		kind, retry = "busy", true
	case "1005", "1006", "3103", "2056", "20097", "1316", "1317", "1318", "1319", "1320", "1321", "insufficient_quota":
		kind = "quota"
	case "1261":
		kind = "context"
	case "1113", "1309", "1311", "1304", "1308", "1310", "1313", "1008", "1314", "1315":
		kind = "terminal"
	case "3012", "upstream_risk_blocked":
		kind = "account_risk"
	case "CAPTCHA_VERIFY_FAILED", "coding_plan_security_verification_required", "captcha_expired", "captcha_invalid", "captcha_verification_failed", "captcha_duplicate_result", "captcha_config_risk_blocked", "captcha_risk_blocked", "upstream_waf_blocked", "F001", "F008", "F014", "F018", "F019", "F020":
		kind = "risk"
	case "timeout", "first_token_timeout", "stream_idle_timeout", "inference_timeout", "captcha_config_timeout", "retry_budget_exhausted":
		kind, retry = "timeout", true
	case "throttle_window_exhausted":
		kind, retry = "rate_limit", true
	}
	if kind == "" {
		switch {
		case riskBlockMessage(f.Message):
			kind = "account_risk"
		case f.Status == 429:
			kind, retry = "rate_limit", true
		case f.Status == 401:
			kind = "auth"
		case f.Status == 403:
			kind = "permission"
		case f.Status == 503 || f.Status == 529:
			kind, retry = "overload", true
		case f.Status >= 500:
			kind, retry = "server", true
		case f.Status == 400:
			kind = "invalid"
		default:
			kind = "unknown"
		}
	}
	f.Kind, f.Retryable = kind, retry
	return f
}
func (a *App) retryParams() M { return merge(obj(a.config()["retry"]), obj(a.setting("retry", M{}))) }
func busyWait(p M) time.Duration {
	s := num(p["busy_wait_sec"])
	if s <= 0 {
		s = 10
	}
	return time.Duration(math.Min(600, s) * float64(time.Second))
}
func (a *App) state(id string) *accountState {
	s := a.states[id]
	if s == nil {
		s = &accountState{}
		a.states[id] = s
	}
	return s
}
func (a *App) infer(ctx context.Context, o inferOptions) (*Result, *Failure) {
	if o.Timing == nil {
		o.Timing = newTiming()
	}
	p := a.retryParams()
	remainingBudget := max(10*time.Second, ms(p["total_budget_ms"]))
	var throttleDeadline time.Time
	excluded, round := map[string]bool{}, map[string]bool{}
	throttled := map[string]time.Time{}
	accountFailures := map[string]int{}
	attempts, regular := 0, 0
	rotated := []any{}
	var last, lastThrottle *Failure
	var lastAccount M
	fail := func(f *Failure) (*Result, *Failure) {
		if f == nil {
			f = &Failure{Kind: "exhausted", Code: "no_account", Message: "所有账号不可用（到期/冷却/额度/模型不符）"}
		}
		if ctx.Err() != nil {
			f = &Failure{Kind: "canceled", Code: "canceled", Message: "客户端已断开"}
		}
		f.Attempts = attempts
		if len(throttled) > 0 && (f.Kind == "busy" || f.Kind == "rate_limit" || f.Kind == "overload") {
			remaining := 24 * time.Hour
			for _, ready := range throttled {
				remaining = min(remaining, max(time.Second, time.Until(ready)))
			}
			f.RetryAfter = remaining
		}
		a.logUsage(o, nil, f, lastAccount, attempts, rotated)
		return nil, f
	}
	wait := func(d time.Duration) error {
		start := time.Now()
		e := pause(ctx, d)
		o.Timing.retry += time.Since(start)
		return e
	}
	for {
		if ctx.Err() != nil {
			return fail(nil)
		}
		deadline := time.Now().Add(remainingBudget)
		if !throttleDeadline.IsZero() {
			deadline = throttleDeadline
			if !time.Now().Before(deadline) {
				return fail(lastThrottle)
			}
		} else if remainingBudget <= 0 {
			if last == nil {
				last = &Failure{Kind: "timeout", Code: "retry_budget_exhausted", Message: "故障重试超时"}
			}
			return fail(last)
		}
		skip := map[string]bool{}
		for id := range excluded {
			skip[id] = true
		}
		for id, t := range throttled {
			if throttleDeadline.IsZero() || t.After(time.Now()) || round[id] {
				skip[id] = true
			}
		}
		start := time.Now()
		o.Admission = 0
		if !throttleDeadline.IsZero() {
			o.Admission = max(time.Millisecond, time.Until(throttleDeadline))
		}
		account, busy, candidates, policy, e := a.acquire(ctx, o, skip, max(0, min(busyWait(p), time.Until(deadline))))
		o.Timing.queue += time.Since(start)
		if e != nil {
			if f, ok := e.(*Failure); ok {
				return fail(f)
			}
			return fail(nil)
		}
		var picked any
		if account != nil {
			picked = account["id"]
		}
		_ = a.exec("INSERT INTO schedule_log(ts,request_id,chain,candidates,policy,picked) VALUES(?,?,?,?,?,?)", nowMS(), o.ID, js(a.keyChain(o.Key)), js(candidates), policy, picked)
		if account == nil {
			if len(throttled) > 0 {
				if throttleDeadline.IsZero() {
					throttleDeadline = time.Now().Add(10 * time.Second)
					continue
				}
				if len(round) > 0 {
					round = map[string]bool{}
					continue
				}
				next := time.Now().Add(time.Hour)
				for _, t := range throttled {
					if t.Before(next) {
						next = t
					}
				}
				if wait(min(max(50*time.Millisecond, time.Until(next)), max(0, time.Until(throttleDeadline)))) != nil {
					return fail(nil)
				}
				continue
			}
			if last != nil {
				return fail(last)
			}
			if busy {
				return fail(&Failure{Kind: "busy", Code: "account_busy", Message: "账号忙碌，等待超时"})
			}
			return fail(nil)
		}
		id := str(account["id"])
		if lastAccount != nil && str(lastAccount["id"]) != id {
			rotated = append(rotated, id)
		}
		lastAccount = account
		attempts++
		if !throttleDeadline.IsZero() {
			o.Admission = max(time.Millisecond, time.Until(throttleDeadline))
		}
		o.OnAdmitted = func() { a.admitAccount(account) }
		attemptStart := time.Now()
		result, f := a.inferHTTP(ctx, account, o)
		a.finishAccountAttempt(account, f, true, p)
		if f == nil {
			a.mu.Lock()
			a.state(id).Cooldown = 0
			if o.Sticky > 0 {
				a.sticky[o.Affinity] = stickyEntry{id, nowMS()}
			}
			if len(a.sticky) > 1000 {
				for k, v := range a.sticky {
					if nowMS()-v.At > 86400000 {
						delete(a.sticky, k)
					}
				}
			}
			a.mu.Unlock()
			result.Attempts = attempts
			_ = a.exec("UPDATE keys SET used_units=used_units+? WHERE id=?", result.Usage["totalTokens"], o.Key["id"])
			a.logUsage(o, result, nil, account, attempts, rotated)
			return result, nil
		}
		last = classify(f)
		if f.Committed {
			return fail(f)
		}
		if ctx.Err() != nil {
			return fail(f)
		}
		switch f.Kind {
		case "account_risk":
			// Shared admission already cooled this account. It remains enabled;
			// this request may use another account within its existing budget.
			excluded[id] = true
			delete(throttled, id)
			remainingBudget -= time.Since(attemptStart)
			continue
		case "auth":
			_ = a.updateAccount(id, M{"status": "needs_relogin", "fail_streak": integer(account["fail_streak"]) + 1, "last_error": "推理鉴权失效"})
			a.mu.Lock()
			a.state(id).Cooldown = nowMS() + 86400000
			a.mu.Unlock()
			excluded[id] = true
			delete(throttled, id)
			a.background(func() { _, _ = a.startOAuth(a.ctx, str(account["family"]), id, false) })
			continue
		case "quota":
			excluded[id] = true
			delete(throttled, id)
			a.background(func() {
				if b, e := a.fetchBalance(a.ctx, account); e == nil {
					if truth(b["ok"]) {
						_ = a.updateAccount(id, M{"last_balance": b["raw"], "capabilities": b["capabilities"], "last_ok_at": nowMS()})
					} else if truth(b["authFailed"]) {
						_ = a.updateAccount(id, M{"status": "needs_relogin"})
					}
				}
			})
			continue
		case "busy":
			// Account concurrency does not consume the ordinary retry budget.
			continue
		case "rate_limit", "overload":
			lastThrottle = f
			delay := f.RetryAfter
			if delay <= 0 {
				delay = ms(fallback(p["rate_limit_retry_interval_ms"], 1000))
			}
			throttled[id] = time.Now().Add(max(50*time.Millisecond, delay))
			round[id] = true
			continue
		}
		if !f.Retryable {
			return fail(f)
		}
		remainingBudget -= time.Since(attemptStart)
		if remainingBudget <= 0 {
			return fail(f)
		}
		regular++
		if regular >= max(1, int(integer(p["max_attempts"]))) {
			return fail(f)
		}
		if f.ProxyFailover {
			continue
		}
		accountFailures[id]++
		if accountFailures[id] >= 3 {
			excluded[id] = true
			delete(throttled, id)
			continue
		}
		delay := math.Min(num(p["max_delay_ms"]), num(p["base_delay_ms"])*math.Pow(num(p["backoff_factor"]), float64(regular-1)))
		if truth(p["jitter"]) {
			delay *= 0.8 + rand.Float64()*0.4
		}
		delayStart := time.Now()
		if wait(min(ms(delay), remainingBudget, max(0, time.Until(deadline)))) != nil {
			return fail(f)
		}
		remainingBudget -= time.Since(delayStart)
	}
}
func (a *App) logUsage(o inferOptions, r *Result, f *Failure, account M, attempts int, rotated []any) {
	status := "success"
	var code, message any
	u := M{}
	trace := o.ID
	if r != nil {
		u = r.Usage
		trace = r.SessionID
	}
	if f != nil {
		status = "failed"
		if f.Kind == "canceled" {
			status = "canceled"
		}
		if f.Kind == "busy" {
			status = "busy"
		}
		code, message = f.Code, conciseFailureMessage(f)
	}
	timings := o.Timing.snapshot()
	_ = a.exec("INSERT INTO usage_log(request_id,ts,key_id,client_ip,account_id,family,model,reasoning_level,stream,attempt_count,rotated_accounts,status,error_code,error_message,ttft_ms,total_ms,tokens_in,tokens_out,tokens_reasoning,cache_read,cache_write,prompt_hash,timings,trace_id) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)", o.ID, nowMS(), o.Key["id"], o.IP, account["id"], account["family"], o.Model, nullable(o.Level), integer(o.Stream), attempts, js(rotated), status, code, message, timings["ttft_ms"], timings["total_ms"], u["inputTokens"], u["outputTokens"], u["reasoningTokens"], u["cacheReadTokens"], u["cacheWriteTokens"], hash(js(o.Body))[:12], js(timings), trace)
}
