package server

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
	accountdata "zcode2api/internal/server/account"
	captchasdk "zcode2api/internal/server/captcha"
	settings "zcode2api/internal/server/config"
	"zcode2api/internal/server/fault"
	routing "zcode2api/internal/server/proxy"
	"zcode2api/internal/server/shared"
	"zcode2api/internal/server/storage"
)

func (a *App) preview(ctx context.Context, r shared.M, dailyOnly bool) (shared.M, error) {
	select {
	case a.claimPreviewSlots <- struct{}{}:
		defer func() { <-a.claimPreviewSlots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if dailyOnly {
		preview, err := a.marketingPreview(ctx, r)
		if err == nil {
			preview["plans"], err = a.pendingDailyClaimPlans(r, dailyFreePlans(shared.Arr(preview["plans"])))
		}
		return preview, err
	}
	plans, warnings := []any{}, []string{}
	seen := map[string]bool{}
	for _, source := range []struct {
		name string
		load func(context.Context, shared.M) (shared.M, error)
	}{{"活动卡", a.marketingPreview}, {"额度卡", a.billingPreview}} {
		p, e := source.load(ctx, r)
		if e != nil {
			warnings = append(warnings, source.name+": "+e.Error())
			continue
		}
		for _, v := range shared.Arr(p["plans"]) {
			plan := shared.Obj(v)
			id := strings.TrimSpace(shared.Str(plan["plan_id"]))
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			plan["plan_id"] = id
			plans = append(plans, plan)
		}
	}
	out := shared.M{"plans": plans}
	if len(warnings) == 2 {
		return out, fmt.Errorf("%s", strings.Join(warnings, "；"))
	}
	if len(warnings) > 0 {
		out["warning"] = strings.Join(warnings, "；")
	}
	return out, nil
}

func (a *App) billingPreview(ctx context.Context, r shared.M) (shared.M, error) {
	res, e := a.api(ctx, r, "GET", "/api/v1/zcode-plan/billing/preview?app_version="+settings.ZCodeFingerprintVersion+"&platform="+url.QueryEscape(platform()), shared.Str(a.tokens(shared.Str(r["id"]))["zcode_jwt"]), nil, shared.M{"X-Device-Mid": r["device_mid"]}, a.jar(shared.Str(r["id"])))
	if e != nil {
		return nil, e
	}
	if !res.OK || !shared.CodeIs(res.JSON["code"], 0) {
		return nil, fault.ApiFailure(res, "preview 失败")
	}
	d, ok := res.JSON["data"].(map[string]any)
	if !ok || d == nil {
		return nil, fmt.Errorf("preview 响应缺少 data，无法确认可领取清单")
	}
	if d["plans"] != nil {
		if _, ok := d["plans"].([]any); !ok {
			return nil, fmt.Errorf("preview 响应的 plans 格式无效")
		}
	}
	return d, nil
}

func (a *App) marketingPreview(ctx context.Context, r shared.M) (shared.M, error) {
	res, e := a.api(ctx, r, "GET", "/api/v1/marketing/touch", shared.Str(a.tokens(shared.Str(r["id"]))["zcode_jwt"]), nil, shared.M{"X-Device-Mid": r["device_mid"]}, a.jar(shared.Str(r["id"])))
	if e != nil {
		return nil, e
	}
	if !res.OK || !shared.CodeIs(res.JSON["code"], 0) {
		return nil, fault.ApiFailure(res, "活动卡查询失败")
	}
	deliveries, ok := shared.Obj(res.JSON["data"])["deliveries"].([]any)
	if !ok {
		return nil, fmt.Errorf("活动卡响应缺少 deliveries 清单或格式无效")
	}
	plans := []any{}
	for _, v := range deliveries {
		delivery := shared.Obj(v)
		position := shared.Str(delivery["resource_position"])
		if position != "banner" && position != "popup" {
			continue
		}
		content := shared.Obj(delivery[position])
		for _, v := range shared.Arr(content["buttons"]) {
			action := shared.Obj(shared.Obj(v)["action"])
			id := strings.TrimSpace(shared.Str(shared.Obj(action["args"])["plan_id"]))
			if shared.Str(action["type"]) != "claim_zcode_plan" || id == "" {
				continue
			}
			// Reward metadata alone does not establish claimability.
			plan := shared.M{"plan_id": id, "name": id, "entitlements": []any{}}
			for _, key := range []string{"background", "hero"} {
				details := shared.Obj(shared.Obj(shared.Obj(content[key])["args"])["zcode_plan"])
				if strings.TrimSpace(shared.Str(details["plan_id"])) == id {
					plan = shared.Merge(details, shared.M{"plan_id": id, "name": shared.Fallback(details["name"], id), "entitlements": shared.Arr(details["entitlements"])})
					break
				}
			}
			plans = append(plans, plan)
		}
	}
	return shared.M{"plans": plans}, nil
}

func (a *App) incentives(ctx context.Context, r shared.M) (shared.M, error) {
	res, e := a.api(ctx, r, "GET", "/api/v1/incentive/referral/summary", shared.Str(a.tokens(shared.Str(r["id"]))["zcode_jwt"]), nil, nil, nil)
	if e != nil {
		return nil, e
	}
	if !res.OK {
		return nil, fault.ApiFailure(res, "奖励任务查询失败")
	}
	d := shared.Obj(res.JSON["data"])
	rewards, tasks := []any{}, []any{}
	for _, role := range []string{"invitee", "inviter"} {
		section := shared.Obj(d[role])
		if section["visible"] == false {
			continue
		}
		for _, v := range shared.Arr(section["rewards"]) {
			r := shared.Obj(v)
			p := shared.Obj(r["progress"])
			var plan any
			if r["plan"] != nil {
				x := shared.Obj(r["plan"])
				plan = shared.M{"name": x["name"], "plan_id": x["plan_id"], "entitlements": shared.Arr(x["entitlements"])}
			}
			rewards = append(rewards, shared.M{"role": role, "title": shared.First(r["title"], r["reward_type"], "活动奖励"), "description": shared.Fallback(r["description"], ""), "rule": shared.Fallback(r["rule"], ""), "state": shared.Fallback(r["state"], ""), "current": shared.First(p["current"], r["current"], 0), "target": shared.First(p["target"], r["target"], 0), "issued": shared.First(r["issued"], 0), "plan": plan})
		}
	}
	for _, v := range shared.Arr(shared.Obj(d["invitee"])["tasks"]) {
		t := shared.Obj(v)
		tasks = append(tasks, shared.M{"key": t["key"], "completed": shared.Truth(t["completed"])})
	}
	return shared.M{"active": shared.Str(shared.Obj(d["gameplay"])["status"]) == "online", "ends_at": shared.Obj(d["gameplay"])["ends_at"], "tasks": tasks, "rewards": rewards}, nil
}

func (a *App) discover(ctx context.Context, id string, dailyOnly bool) (shared.M, error) {
	ctx = context.WithValue(ctx, manualClaimContextKey{}, true)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	statement := "SELECT * FROM accounts"
	args := []any{}
	if id != "" {
		statement += " WHERE id=?"
		args = append(args, id)
	}
	rows, err := storage.Query(a.db, statement+" ORDER BY created_at", args...)
	if err != nil {
		return nil, fmt.Errorf("账号清单查询失败: %w", err)
	}
	accounts := []shared.M{}
	for _, v := range rows {
		r := shared.Obj(v)
		if a.accountTokens(r)["zcode_jwt"] != "" {
			accounts = append(accounts, r)
		}
	}
	previews := make([]any, len(accounts))
	shared.ParallelFor(len(accounts), shared.AccountOperationConcurrency, func(index int) {
		// Keep account order and explicit errors for canceled or timed-out rows.
		previews[index] = a.discoverClaimAccount(ctx, accounts[index], dailyOnly)
	})
	failed := 0
	for _, preview := range previews {
		if shared.Truth(shared.Obj(preview)["error"]) {
			failed++
		}
	}
	return shared.M{"previews": previews, "checked": len(accounts), "failed": failed, "complete": failed == 0, "checked_at": shared.NowMS()}, nil
}

func dailyFreePlans(plans []any) []any {
	daily := []any{}
	seen := map[string]bool{}
	for _, v := range plans {
		plan := shared.Obj(v)
		id := strings.TrimSpace(shared.Str(plan["plan_id"]))
		// Global Build and Weekend Build use changing plan IDs. Accept only
		// these free-card campaigns from actionable upstream deliveries.
		name := strings.Join(strings.Fields(strings.ToUpper(shared.Str(plan["name"]))), " ")
		if id == "" || seen[id] || (name != "ZCODE GLOBAL BUILD" && name != "ZCODE WEEKEND BUILD") {
			continue
		}
		seen[id] = true
		daily = append(daily, plan)
	}
	return daily
}

func (a *App) claim(ctx context.Context, id, plan, trigger string, verification shared.M) (result shared.M, err error) {
	manual := trigger == "manual"
	batch := manual && shared.Truth(verification["auto_verify"])
	if manual {
		ctx = context.WithValue(ctx, manualClaimContextKey{}, true)
	}
	// Serialize all cards for one account, including manual and batch requests.
	a.mu.Lock()
	if a.claimLocks[id] {
		a.mu.Unlock()
		return shared.M{"ok": false, "code": "in_progress", "message": "该奖励正在领取，请稍候"}, nil
	}
	a.claimLocks[id] = true
	a.mu.Unlock()
	defer func() { a.mu.Lock(); delete(a.claimLocks, id); a.mu.Unlock() }()
	r := a.account(id)
	if r == nil {
		return nil, fmt.Errorf("账号不存在")
	}
	token := shared.Str(a.tokens(id)["zcode_jwt"])
	if token == "" {
		return nil, fmt.Errorf("该账号无 zcode_jwt，无法领取")
	}
	if e := captchasdk.ValidateClaimVerification(verification); e != nil {
		return nil, e
	}
	if due, scheduled := ctx.Value(scheduledClaimContextKey{}).(int64); scheduled {
		// Check under the same account lock used by manual claims, closing the
		// race between recovery's preview and a user finishing verification.
		if recorded, e := a.recordedClaim(id, plan, due); e != nil || recorded != nil {
			return recorded, e
		}
	}
	worker, releaseWorker, workerErr := a.acquireClaimWorker(ctx)
	if workerErr != nil {
		return nil, workerErr
	}
	defer releaseWorker()
	if trigger != "scheduled" {
		ctx = context.WithValue(ctx, manualClaimWorkerKey{}, worker)
	}
	diagnostic := shared.M{"stage": "route", "sdk_attempted": false, "sdk_executed": false, "sdk_initialized": false, "ticket_attached": false, "browser_ticket": shared.Str(verification["captcha_verify_param"]) != "", "claim_request_started": false, "http_status": 0, "initial_http_status": 0}
	ctx = context.WithValue(ctx, claimCaptchaDiagnosticKey{}, diagnostic)
	var requestSent atomic.Bool
	var failure *fault.Failure
	requestContext := ctx
	defer func() {
		diagnostic["claim_request_sent"] = requestSent.Load()
		if err != nil {
			failure = routing.TransportFailure(context.WithoutCancel(ctx), err)
			if failure.Code == "network" {
				failure.Code, failure.Message = "claim_request_failed", "领取请求失败，请刷新领取状态后重试"
			}
			var known *fault.Failure
			if errors.As(err, &known) {
				failure = fault.Classify(known)
			}
			if requestContext.Err() != nil {
				failure = &fault.Failure{Kind: "canceled", Code: "canceled", Message: "请求已取消"}
			}
			claimDetail := fault.FailureDetail(failure)
			claimDetail["stage"], claimDetail["claim_request_sent"] = diagnostic["stage"], requestSent.Load()
			reason := fault.FailureReason(failure)
			_ = a.exec("INSERT INTO claim_log(account_id,ts,plan_id,ok,code,message,trigger,error_detail) VALUES(?,?,?,?,?,?,?,?)", id, shared.NowMS(), plan, 0, failure.Code, reason, trigger, shared.JSON(claimDetail))
			result = shared.Merge(result, shared.M{"ok": false, "code": failure.Code, "message": fault.ConciseFailureMessage(failure), "error_reason": reason, "error_detail": claimDetail, "failure_kind": failure.Kind, "retryable": failure.Retryable, "retry_after_ms": failure.RetryAfter.Milliseconds(), "verification": diagnostic})
			err = failure
		}
		if !shared.Truth(result["ok"]) {
			diagnostic["failure_stage"] = diagnostic["stage"]
			if result != nil && failure != nil {
				if !manual {
					result["requires_verification"] = shared.Truth(result["requires_verification"]) || captchasdk.ClaimBrowserFallback(failure)
				}
				result["retry_after_ms"] = failure.RetryAfter.Milliseconds()
				if !manual && shared.Truth(result["requires_verification"]) {
					result["verification_retry_at"] = shared.NowMS() + max(int64(0), failure.RetryAfter.Milliseconds())
					result["manual_verification_failed"] = shared.Truth(diagnostic["browser_ticket"])
				}
			}
		}
		diagnostic["retry_after_ms"] = result["retry_after_ms"]
		diagnostic["requires_verification"] = shared.Truth(result["requires_verification"])
	}()
	ctx, release, e := a.claimVerificationContext(ctx, r, plan, token, verification)
	if e != nil {
		return nil, e
	}
	defer release()
	diagnostic["stage"] = "admission"
	ctx, finish, gateFailure := a.beginUpstreamRequest(ctx, a.upstreamScope(ctx, r, a.origin, true))
	if gateFailure != nil {
		return nil, gateFailure
	}
	var sdkVerification captchaVerification
	defer func() {
		if err != nil || failure != nil {
			a.rejectCaptchaVerification(sdkVerification)
		}
		if err != nil {
			finish(upstreamRequestFailure(ctx, err))
		} else {
			finish(failure)
		}
	}()
	headers := shared.M{"X-Device-Mid": r["device_mid"]}
	if param := shared.Str(verification["captcha_verify_param"]); param != "" {
		if failure := a.reserveCaptchaTicket(param); failure != nil {
			return nil, failure
		}
		headers["X-Aliyun-Captcha-Verify-Param"] = param
		headers["X-Aliyun-Captcha-Verify-Region"] = verification["captcha_region"]
	}
	var verificationFailure *fault.Failure
	// In an enabled scene, a single manual claim uses the page's ticket.
	// A manual batch can use the SDK in a headless browser.
	if shared.Str(verification["captcha_verify_param"]) == "" {
		diagnostic["stage"] = "scene"
		var scene shared.M
		scene, verificationFailure = a.captchaScene(ctx, r)
		if manual && !batch && verificationFailure == nil && shared.Truth(scene["enabled"]) {
			verificationFailure = &fault.Failure{Kind: "risk", Code: "coding_plan_security_verification_required", Message: "请重新点击领取并完成安全验证"}
		} else if verificationFailure == nil && shared.Truth(scene["enabled"]) {
			diagnostic["sdk_attempted"], diagnostic["stage"] = true, "sdk"
			purpose := captchasdk.CaptchaPurposeClaim
			if batch {
				purpose = captchasdk.CaptchaPurposeBatchClaim
			} else if trigger == "scheduled" {
				purpose = captchasdk.CaptchaPurposeScheduledClaim
			}
			sdkVerification, verificationFailure = a.captchaHeaders(ctx, r, purpose, true)
		}
		a.prepareProxyFailover(ctx, verificationFailure)
		if verificationFailure == nil {
			token = shared.Str(a.tokens(id)["zcode_jwt"])
			if failure := validateCaptchaRequestToken(sdkVerification, r, token); failure != nil {
				return nil, failure
			}
			for key := range sdkVerification.Headers {
				headers[key] = sdkVerification.Headers.Get(key)
			}
		}
	}
	res := fault.ApiResult{}
	diagnostic["ticket_attached"] = shared.Str(headers["X-Aliyun-Captcha-Verify-Param"]) != ""
	if verificationFailure == nil {
		diagnostic["stage"], diagnostic["claim_request_started"] = "claim_request", true
		ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{WroteRequest: func(info httptrace.WroteRequestInfo) {
			if info.Err == nil {
				requestSent.Store(true)
			}
		}})
		res, e = a.api(ctx, r, "POST", "/api/v1/zcode-plan/billing/claim", token, shared.M{"plan_id": plan}, headers, a.jar(id))
		if e != nil {
			return nil, e
		}
	}
	if captchasdk.ClaimResponseRequiresVerification(res) {
		if shared.Str(headers["X-Aliyun-Captcha-Verify-Param"]) != "" {
			a.rejectCaptchaVerification(sdkVerification)
			if failure := fault.ApiResponseFailure(res); failure.Code == "F014" || failure.Code == "F020" {
				a.invalidateCaptchaScene(a.captchaSceneKey(ctx))
			}
		} else {
			a.prepareCaptchaChallenge(ctx)
		}
	}
	data := shared.Obj(res.JSON["data"])
	p := shared.Obj(data["plan"])
	ok := res.Status == 200 && shared.CodeIs(res.JSON["code"], 0) && len(p) > 0
	var code any
	if raw := shared.First(shared.Obj(res.JSON["error"])["code"], res.JSON["code"]); raw != nil {
		code = shared.Str(raw)
	}
	alreadyClaimed := manual && !ok && code == "1003" && (res.Status == 200 || res.Status == 400 || res.Status == 409)
	completed := ok || alreadyClaimed
	message := shared.First(data["message"], res.JSON["msg"], "")
	if !completed {
		failure = fault.ApiResponseFailure(res)
		if res.Status > 0 {
			// Keep the redacted upstream response before the shared classifier's
			// short message hides the claim endpoint's actual business error.
			diagnostic["upstream_error"] = fault.FailureDetail(failure)
		}
		if res.OK && shared.CodeIs(res.JSON["code"], 0) && len(p) == 0 {
			failure = &fault.Failure{Kind: "server", Code: "claim_invalid_response", Message: "领取响应缺少卡片信息，请刷新领取状态"}
		}
	}
	requiresVerification := !completed && captchasdk.ClaimResponseRequiresVerification(res)
	if alreadyClaimed {
		message = "该账号已领取过"
	} else if verificationFailure != nil {
		failure = verificationFailure
		code = verificationFailure.Code
		message = fault.ConciseFailureMessage(verificationFailure)
		if manual {
			requiresVerification = verificationFailure.Code == "coding_plan_security_verification_required"
			if batch && verificationFailure.Kind != "account_risk" && captchasdk.ClaimBrowserFallback(verificationFailure) {
				requiresVerification = true
				message = "需要人工验证后领取"
			}
		} else {
			requiresVerification = captchasdk.ClaimBrowserFallback(verificationFailure)
			if requiresVerification {
				message = shared.Str(message) + "，请完成浏览器验证后领取"
			}
		}
	} else if requiresVerification {
		failure.Kind, failure.Retryable = "risk", false
		message = "请完成浏览器安全验证后领取（" + shared.Str(code) + "）"
		if shared.Truth(diagnostic["sdk_attempted"]) {
			message = "服务器验证后仍需安全验证，请在浏览器完成（" + shared.Str(code) + "）"
		}
	} else if failure != nil {
		code, message = failure.Code, fault.ConciseFailureMessage(failure)
		// Billing quota can mean campaign capacity rather than account balance.
		// Prefer this endpoint's business explanation over the inference summary.
		if businessMessage, ok := data["message"].(string); ok {
			text := []rune(strings.Join(strings.Fields(businessMessage), " "))
			if len(text) > 160 {
				text = append(text[:159], '…')
			}
			if len(text) > 0 {
				message = string(text)
			}
		}
	}
	end := accountdata.SecondsMS(p["ends_at"])
	claimDetail := shared.M{}
	if failure != nil {
		claimDetail = fault.FailureDetail(failure)
	}
	claimDetail["stage"], claimDetail["claim_request_sent"] = diagnostic["stage"], requestSent.Load()
	if res.Status > 0 {
		claimDetail["http_status"], claimDetail["upstream_code"] = res.Status, code
	}
	reason := shared.Str(message)
	if failure != nil {
		reason = fault.FailureReason(failure)
	}
	_ = a.exec("INSERT INTO claim_log(account_id,ts,plan_id,ok,code,message,user_plan_id,ends_at,trigger,error_detail) VALUES(?,?,?,?,?,?,?,?,?,?)", id, shared.NowMS(), plan, shared.Integer(completed), code, reason, p["user_plan_id"], end, trigger, shared.JSON(claimDetail))
	diagnostic["initial_http_status"], diagnostic["http_status"], diagnostic["requires_verification"] = res.Status, res.Status, requiresVerification
	if verificationFailure != nil {
		diagnostic["sdk_error"] = verificationFailure.Code
	}
	if ok {
		a.acceptCaptchaVerification(ctx, &sdkVerification)
	}
	if completed {
		a.invalidateBalance(id)
		a.scheduleNext(id, 0)
	}
	var resultPlan any
	if len(p) > 0 {
		resultPlan = p
	}
	result = shared.M{"ok": completed, "httpStatus": res.Status, "code": code, "message": message, "error_reason": reason, "error_detail": claimDetail, "plan": resultPlan, "endsAt": end, "requires_verification": requiresVerification, "verification": diagnostic}
	if res.Status > 0 {
		result["http_status"] = res.Status
	}
	if alreadyClaimed {
		result["already_claimed"] = true
	}
	if failure != nil {
		result["failure_kind"], result["retryable"] = failure.Kind, failure.Retryable
		if failure.RetryAfter > 0 {
			result["retry_after_ms"] = failure.RetryAfter.Milliseconds()
		}
	}
	return result, nil
}

func (a *App) autoClaim(ctx context.Context, dry bool) (shared.M, error) {
	return a.autoClaimScope(ctx, dry, time.Time{})
}

func (a *App) autoClaimScope(ctx context.Context, dry bool, scheduledAt time.Time) (result shared.M, err error) {
	if !a.claimBatchMu.TryLock() {
		return shared.M{"checked": 0, "claimed": 0, "results": []any{}, "in_progress": true}, nil
	}
	defer a.claimBatchMu.Unlock()
	if !scheduledAt.IsZero() {
		return a.runScheduledClaims(ctx, scheduledAt)
	}
	checked, claimed := 0, 0
	results := []any{}
	for _, v := range a.rows("SELECT * FROM accounts WHERE status IN ('active','unverified','cooldown') ORDER BY created_at") {
		if ctx.Err() != nil {
			break
		}
		r := shared.Obj(v)
		id := shared.Str(r["id"])
		if !shared.Truth(a.tokens(id)["zcode_jwt"]) {
			continue
		}
		checked++
		p, e := a.preview(ctx, r, false)
		if e != nil {
			results = append(results, shared.M{"account": id, "error": e.Error()})
			continue
		}
		if p["warning"] != nil {
			results = append(results, shared.M{"account": id, "warning": p["warning"]})
		}
		for _, v := range shared.Arr(p["plans"]) {
			plan := shared.Str(shared.Obj(v)["plan_id"])
			if dry {
				results = append(results, shared.M{"account": id, "plan_id": plan, "would_claim": true})
				continue
			}
			res, e := a.claim(ctx, id, plan, "auto", nil)
			if e != nil {
				results = append(results, shared.M{"account": id, "plan_id": plan, "error": e.Error()})
				break
			}
			if shared.Truth(res["ok"]) {
				claimed++
			}
			results = append(results, shared.Merge(res, shared.M{"account": id, "plan_id": plan}))
			if !shared.Truth(res["ok"]) && (shared.Truth(res["requires_verification"]) || res["failure_kind"] == "account_risk" || res["failure_kind"] == "auth" || res["failure_kind"] == "network" || res["failure_kind"] == "rate_limit") {
				break
			}
		}
	}
	return shared.M{"checked": checked, "claimed": claimed, "results": results}, nil
}

func (a *App) recordedClaim(id, plan string, since int64) (shared.M, error) {
	rows, err := storage.Query(a.db, "SELECT trigger FROM claim_log WHERE account_id=? AND plan_id=? AND ok=1 AND ts>=? ORDER BY ts DESC LIMIT 1", id, plan, since)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return shared.M{"ok": true, "already_claimed": true, "external_claim": shared.Obj(rows[0])["trigger"] != "scheduled", "message": "已确认领取成功"}, nil
}
