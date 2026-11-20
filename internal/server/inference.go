package server

import (
	"context"
	"math"
	rand "math/rand/v2"
	"time"
	"zcode2api/internal/server/fault"
	wire "zcode2api/internal/server/protocol"
	"zcode2api/internal/server/shared"
)

func (a *App) retryParams() shared.M {
	return shared.Merge(shared.Obj(a.configValue("retry")), shared.Obj(a.setting("retry", shared.M{})))
}

func busyWait(p shared.M) time.Duration {
	s := shared.Num(p["busy_wait_sec"])
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

func (a *App) infer(ctx context.Context, o inferOptions) (*wire.Result, *fault.Failure) {
	if o.Timing == nil {
		o.Timing = newTiming()
	}
	o.Recovery = &poolRecoveryLease{}
	defer a.releasePoolRecovery(o.Recovery)
	p := a.retryParams()
	budgetDeadline := time.Now().Add(max(10*time.Second, shared.Ms(p["total_budget_ms"])))
	var throttleDeadline time.Time
	excluded, round := map[string]bool{}, map[string]bool{}
	throttled := map[string]time.Time{}
	accountFailures := map[string]int{}
	var last, lastThrottle *fault.Failure
	clearThrottle := func(id string) {
		delete(throttled, id)
		delete(round, id)
		if len(throttled) == 0 {
			throttleDeadline = time.Time{}
			lastThrottle = nil
		}
	}
	attempts, regular := 0, 0
	rotated := []any{}
	var lastAccount shared.M
	fail := func(f *fault.Failure) (*wire.Result, *fault.Failure) {
		if f == nil {
			f = &fault.Failure{Kind: "exhausted", Code: "no_account", Message: "所有账号不可用（到期/冷却/额度/模型不符）"}
		}
		if ctx.Err() != nil {
			f = &fault.Failure{Kind: "canceled", Code: "canceled", Message: "客户端已断开", Detail: fault.FailureDetail(f)}
		}
		if f.LocalOnly && last != nil && !last.LocalOnly {
			f.Detail = fault.FailureDetail(f)
			f.Detail["last_upstream_error"] = fault.FailureDetail(last)
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
		e := shared.Pause(ctx, d)
		o.Timing.retry += time.Since(start)
		return e
	}
	for {
		if ctx.Err() != nil {
			return fail(nil)
		}
		deadline := budgetDeadline
		if !throttleDeadline.IsZero() {
			if throttleDeadline.Before(deadline) {
				deadline = throttleDeadline
			}
			if !time.Now().Before(throttleDeadline) {
				return fail(lastThrottle)
			}
		}
		if !time.Now().Before(deadline) {
			if last == nil {
				last = &fault.Failure{Kind: "timeout", Code: "retry_budget_exhausted", Message: "故障重试超时"}
			}
			return fail(last)
		}
		skip := map[string]bool{}
		for id := range excluded {
			skip[id] = true
		}
		// A queued busy account must not hide another account's recovery or
		// extend an already active throttle window.
		o.RecheckAt = deadline
		now := time.Now()
		for id, t := range throttled {
			if throttleDeadline.IsZero() || t.After(now) || round[id] {
				skip[id] = true
				if o.RecheckAt.IsZero() || t.Before(o.RecheckAt) {
					o.RecheckAt = t
				}
			}
		}
		start := time.Now()
		account, busy, e := a.acquire(ctx, o, skip, max(0, min(busyWait(p), time.Until(deadline))))
		o.Timing.queue += time.Since(start)
		if e != nil {
			if f, ok := e.(*fault.Failure); ok {
				return fail(f)
			}
			return fail(nil)
		}
		if !time.Now().Before(deadline) {
			if account != nil {
				a.finishAccountAttempt(account, nil, false, nil)
			}
			if last != nil {
				return fail(last)
			}
			return fail(&fault.Failure{Kind: "timeout", Code: "retry_budget_exhausted", Message: "故障重试超时"})
		}
		if account == nil {
			if len(throttled) > 0 {
				if throttleDeadline.IsZero() {
					throttleDeadline = time.Now().Add(min(10*time.Second, time.Until(budgetDeadline)))
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
				return fail(&fault.Failure{Kind: "busy", Code: "account_busy", Message: "账号忙碌，等待超时"})
			}
			return fail(nil)
		}
		id := shared.Str(account["id"])
		if lastAccount != nil && shared.Str(lastAccount["id"]) != id {
			rotated = append(rotated, id)
		}
		lastAccount = account
		o.Admission = max(time.Millisecond, time.Until(deadline))
		o.OnAdmitted = func() { a.admitAccount(account) }
		// Count real model HTTP attempts, including verification resubmissions;
		// scheduler/SDK admission refusals must not inflate usage statistics.
		o.OnAttempt = func() { attempts++ }
		result, f := a.inferHTTP(ctx, account, o)
		if f != nil && fault.Classify(f).Kind == "quota" {
			// Publish exhaustion before releasing the slot and waking queued requests.
			a.markQuotaExhausted(id)
		}
		a.finishAccountAttempt(account, f, true, p)
		if f == nil {
			a.mu.Lock()
			if o.Sticky > 0 {
				a.sticky[o.Affinity] = stickyEntry{id, shared.NowMS()}
			}
			if len(a.sticky) > 1000 {
				for k, v := range a.sticky {
					if shared.NowMS()-v.At > 86400000 {
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
		last = fault.Classify(f)
		if f.Committed {
			return fail(f)
		}
		if ctx.Err() != nil {
			return fail(f)
		}
		switch f.Kind {
		case "risk":
			if !f.ProxyFailover && (f.Code == "captcha_risk_blocked" || f.Code == "F001" || f.Code == "upstream_waf_blocked") {
				// A different account may use an independent exit/SDK scope.
				// Shared scopes are rejected locally by admission, never re-sent.
				excluded[id] = true
				clearThrottle(id)
				continue
			}
		case "account_risk":
			// Shared admission already cooled this account. It remains enabled;
			// this request may use another account within its existing budget.
			excluded[id] = true
			clearThrottle(id)
			continue
		case "auth":
			if a.updateAccountStatusIfCurrent(account, nil, shared.M{"status": "needs_relogin", "fail_streak": shared.Integer(account["fail_streak"]) + 1, "last_error": "ZCode 登录鉴权失效，请重新授权"}) {
				a.background(func() { _, _ = a.startOAuth(a.ctx, a.accountLoginProvider(account), id, false) })
			}
			excluded[id] = true
			clearThrottle(id)
			continue
		case "quota":
			excluded[id] = true
			clearThrottle(id)
			a.background(func() {
				if b, e := a.fetchBalanceFresh(a.ctx, account); e == nil {
					if shared.Truth(b["ok"]) {
						a.applyBalance(account, b, true)
					} else if shared.Truth(b["authFailed"]) {
						a.updateAccountStatusIfCurrent(account, b, shared.M{"status": "needs_relogin"})
					}
				}
			})
			continue
		case "busy":
			// Busy responses do not consume the ordinary attempt count, but
			// still share the overall admission deadline.
			clearThrottle(id)
			continue
		case "rate_limit", "overload":
			lastThrottle = f
			delay := f.RetryAfter
			if delay <= 0 {
				delay = shared.Ms(shared.Fallback(p["rate_limit_retry_interval_ms"], 1000))
			}
			throttled[id] = time.Now().Add(max(50*time.Millisecond, delay))
			round[id] = true
			continue
		}
		if !f.Retryable {
			return fail(f)
		}
		if !time.Now().Before(deadline) {
			return fail(f)
		}
		regular++
		if regular >= max(1, int(shared.Integer(p["max_attempts"]))) {
			return fail(f)
		}
		if f.ProxyFailover {
			continue
		}
		accountFailures[id]++
		if accountFailures[id] >= 3 {
			excluded[id] = true
			clearThrottle(id)
			continue
		}
		delay := math.Min(shared.Num(p["max_delay_ms"]), shared.Num(p["base_delay_ms"])*math.Pow(shared.Num(p["backoff_factor"]), float64(regular-1)))
		if shared.Truth(p["jitter"]) {
			delay *= 0.8 + rand.Float64()*0.4
		}
		if wait(min(shared.Ms(delay), max(0, time.Until(deadline)))) != nil {
			return fail(f)
		}
	}
}

func (a *App) logUsage(o inferOptions, r *wire.Result, f *fault.Failure, account shared.M, attempts int, rotated []any) {
	status := "success"
	var code, message, detail any
	u := shared.M{}
	trace := ""
	if r != nil {
		u = r.Usage
		trace = r.TraceID
		logCode := r.UsageWarning
		if r.FinishReason == "refusal" {
			status = "failed"
			logCode = "upstream_content_refusal"
			detail = shared.JSON(shared.M{"stop_reason": r.FinishReason})
		}
		if logCode != "" {
			code, message = logCode, fault.ConciseFailureMessage(&fault.Failure{Code: logCode})
			if r.HTTPStatus > 0 {
				diagnostic := shared.Obj(shared.Parse(detail, nil))
				diagnostic["http_status"], diagnostic["upstream_response_received"] = r.HTTPStatus, true
				detail = shared.JSON(diagnostic)
			}
		}
	}
	if f != nil {
		status = "failed"
		if f.Kind == "canceled" {
			status = "canceled"
		}
		if f.Kind == "busy" {
			status = "busy"
		}
		code, message = shared.Fallback(f.Code, f.Kind), fault.FailureReason(f)
		diagnostic := fault.FailureDetail(f)
		detail = shared.JSON(diagnostic)
		trace = shared.Str(diagnostic["trace_id"])
	}
	timings := o.Timing.snapshot()
	_ = a.exec("INSERT INTO usage_log(request_id,ts,key_id,client_ip,account_id,family,model,reasoning_level,stream,attempt_count,rotated_accounts,status,error_code,error_message,ttft_ms,total_ms,tokens_in,tokens_out,tokens_reasoning,cache_read,cache_write,prompt_hash,timings,trace_id,error_detail) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)", o.ID, shared.NowMS(), o.Key["id"], o.IP, account["id"], account["family"], o.Model, shared.Nullable(o.Level), shared.Integer(o.Stream), attempts, shared.JSON(rotated), status, code, message, timings["ttft_ms"], timings["total_ms"], u["inputTokens"], u["outputTokens"], u["reasoningTokens"], u["cacheReadTokens"], u["cacheWriteTokens"], shared.Hash(shared.JSON(o.Body))[:12], shared.JSON(timings), shared.Nullable(trace), detail)
}
