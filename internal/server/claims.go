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
)

func (a *App) preview(ctx context.Context, r M, dailyOnly bool) (M, error) {
	if dailyOnly {
		preview, err := a.marketingPreview(ctx, r)
		if err == nil {
			preview["plans"], err = a.pendingDailyClaimPlans(r, dailyFreePlans(arr(preview["plans"])))
		}
		return preview, err
	}
	plans, warnings := []any{}, []string{}
	seen := map[string]bool{}
	for _, source := range []struct {
		name string
		load func(context.Context, M) (M, error)
	}{{"活动卡", a.marketingPreview}, {"额度卡", a.billingPreview}} {
		p, e := source.load(ctx, r)
		if e != nil {
			warnings = append(warnings, source.name+": "+e.Error())
			continue
		}
		for _, v := range arr(p["plans"]) {
			plan := obj(v)
			id := strings.TrimSpace(str(plan["plan_id"]))
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			plan["plan_id"] = id
			plans = append(plans, plan)
		}
	}
	out := M{"plans": plans}
	if len(warnings) == 2 {
		return out, fmt.Errorf("%s", strings.Join(warnings, "；"))
	}
	if len(warnings) > 0 {
		out["warning"] = strings.Join(warnings, "；")
	}
	return out, nil
}

func (a *App) billingPreview(ctx context.Context, r M) (M, error) {
	res, e := a.api(ctx, r, "GET", "/api/v1/zcode-plan/billing/preview?app_version="+url.QueryEscape(str(a.configValue("app_version")))+"&platform="+url.QueryEscape(platform()), str(a.tokens(str(r["id"]))["zcode_jwt"]), nil, M{"X-Device-Mid": r["device_mid"]}, a.jar(str(r["id"])))
	if e != nil {
		return nil, e
	}
	if !res.OK || !codeIs(res.JSON["code"], 0) {
		return nil, apiFailure(res, "preview 失败")
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

func (a *App) marketingPreview(ctx context.Context, r M) (M, error) {
	res, e := a.api(ctx, r, "GET", "/api/v1/marketing/touch", str(a.tokens(str(r["id"]))["zcode_jwt"]), nil, M{"X-Device-Mid": r["device_mid"]}, a.jar(str(r["id"])))
	if e != nil {
		return nil, e
	}
	if !res.OK || !codeIs(res.JSON["code"], 0) {
		return nil, apiFailure(res, "活动卡查询失败")
	}
	deliveries, ok := obj(res.JSON["data"])["deliveries"].([]any)
	if !ok {
		return nil, fmt.Errorf("活动卡响应缺少 deliveries 清单或格式无效")
	}
	plans := []any{}
	for _, v := range deliveries {
		delivery := obj(v)
		position := str(delivery["resource_position"])
		if position != "banner" && position != "popup" {
			continue
		}
		content := obj(delivery[position])
		for _, v := range arr(content["buttons"]) {
			action := obj(obj(v)["action"])
			id := strings.TrimSpace(str(obj(action["args"])["plan_id"]))
			if str(action["type"]) != "claim_zcode_plan" || id == "" {
				continue
			}
			// Reward metadata alone does not establish claimability.
			plan := M{"plan_id": id, "name": id, "entitlements": []any{}}
			for _, key := range []string{"background", "hero"} {
				details := obj(obj(obj(content[key])["args"])["zcode_plan"])
				if strings.TrimSpace(str(details["plan_id"])) == id {
					plan = merge(details, M{"plan_id": id, "name": fallback(details["name"], id), "entitlements": arr(details["entitlements"])})
					break
				}
			}
			plans = append(plans, plan)
		}
	}
	return M{"plans": plans}, nil
}
func (a *App) incentives(ctx context.Context, r M) (M, error) {
	res, e := a.api(ctx, r, "GET", "/api/v1/incentive/referral/summary", str(a.tokens(str(r["id"]))["zcode_jwt"]), nil, nil, nil)
	if e != nil {
		return nil, e
	}
	if !res.OK {
		return nil, apiFailure(res, "奖励任务查询失败")
	}
	d := obj(res.JSON["data"])
	rewards, tasks := []any{}, []any{}
	for _, role := range []string{"invitee", "inviter"} {
		section := obj(d[role])
		if section["visible"] == false {
			continue
		}
		for _, v := range arr(section["rewards"]) {
			r := obj(v)
			p := obj(r["progress"])
			var plan any
			if r["plan"] != nil {
				x := obj(r["plan"])
				plan = M{"name": x["name"], "plan_id": x["plan_id"], "entitlements": arr(x["entitlements"])}
			}
			rewards = append(rewards, M{"role": role, "title": first(r["title"], r["reward_type"], "活动奖励"), "description": fallback(r["description"], ""), "rule": fallback(r["rule"], ""), "state": fallback(r["state"], ""), "current": first(p["current"], r["current"], 0), "target": first(p["target"], r["target"], 0), "issued": first(r["issued"], 0), "plan": plan})
		}
	}
	for _, v := range arr(obj(d["invitee"])["tasks"]) {
		t := obj(v)
		tasks = append(tasks, M{"key": t["key"], "completed": truth(t["completed"])})
	}
	return M{"active": str(obj(d["gameplay"])["status"]) == "online", "ends_at": obj(d["gameplay"])["ends_at"], "tasks": tasks, "rewards": rewards}, nil
}
func (a *App) discover(ctx context.Context, id string, dailyOnly bool) M {
	query := "SELECT * FROM accounts"
	args := []any{}
	if id != "" {
		query += " WHERE id=?"
		args = append(args, id)
	}
	accounts := a.rows(query+" ORDER BY created_at", args...)
	previews := []any{}
	checked := 0
	for _, v := range accounts {
		if ctx.Err() != nil {
			break
		}
		r := obj(v)
		if a.accountTokens(r)["zcode_jwt"] == "" {
			continue
		}
		checked++
		m := M{"account_id": r["id"], "label": r["label"], "plans": []any{}}
		if p, e := a.preview(ctx, r, dailyOnly); e == nil {
			m["plans"] = arr(p["plans"])
			if p["warning"] != nil {
				m["warning"] = p["warning"]
			}
		} else {
			m["error"] = e.Error()
		}
		if !dailyOnly {
			if reward, e := a.incentives(ctx, r); e == nil {
				m["incentives"] = reward
			} else {
				m["incentives_error"] = e.Error()
			}
		}
		if dailyOnly && len(arr(m["plans"])) == 0 && m["error"] == nil {
			continue
		}
		previews = append(previews, m)
	}
	return M{"previews": previews, "checked": checked}
}

func dailyFreePlans(plans []any) []any {
	daily := []any{}
	seen := map[string]bool{}
	for _, v := range plans {
		plan := obj(v)
		id := strings.TrimSpace(str(plan["plan_id"]))
		// Global Build and Weekend Build use changing plan IDs. Accept only
		// these free-card campaigns from actionable upstream deliveries.
		name := strings.Join(strings.Fields(strings.ToUpper(str(plan["name"]))), " ")
		if id == "" || seen[id] || (name != "ZCODE GLOBAL BUILD" && name != "ZCODE WEEKEND BUILD") {
			continue
		}
		seen[id] = true
		daily = append(daily, plan)
	}
	return daily
}
func (a *App) claim(ctx context.Context, id, plan, trigger string, verification M) (result M, err error) {
	// Serialize all cards for one account, including manual and batch requests.
	a.mu.Lock()
	if a.claimLocks[id] {
		a.mu.Unlock()
		return M{"ok": false, "code": "in_progress", "message": "该奖励正在领取，请稍候"}, nil
	}
	a.claimLocks[id] = true
	a.mu.Unlock()
	defer func() { a.mu.Lock(); delete(a.claimLocks, id); a.mu.Unlock() }()
	r := a.account(id)
	if r == nil {
		return nil, fmt.Errorf("账号不存在")
	}
	token := str(a.tokens(id)["zcode_jwt"])
	if token == "" {
		return nil, fmt.Errorf("该账号无 zcode_jwt，无法领取")
	}
	if e := validateClaimVerification(verification); e != nil {
		return nil, e
	}
	if due, scheduled := ctx.Value(scheduledClaimContextKey{}).(int64); scheduled {
		// Check under the same account lock used by manual claims, closing the
		// race between recovery's preview and a user finishing verification.
		if recorded, e := a.recordedClaim(id, plan, due); e != nil || recorded != nil {
			return recorded, e
		}
	}
	if trigger != "scheduled" {
		workerCtx, releaseWorker, workerErr := a.acquireManualClaimWorker(ctx)
		if workerErr != nil {
			return nil, workerErr
		}
		defer releaseWorker()
		ctx = workerCtx
	}
	diagnostic := M{"stage": "route", "sdk_attempted": false, "sdk_executed": false, "sdk_initialized": false, "ticket_attached": false, "browser_ticket": str(verification["captcha_verify_param"]) != "", "claim_request_started": false, "http_status": 0, "initial_http_status": 0}
	ctx = context.WithValue(ctx, claimCaptchaDiagnosticKey{}, diagnostic)
	var requestSent atomic.Bool
	requestContext := ctx
	defer func() {
		diagnostic["claim_request_sent"] = requestSent.Load()
		if err != nil {
			failure := transportFailure(context.WithoutCancel(ctx), err)
			if failure.Code == "network" {
				failure.Code, failure.Message = "claim_request_failed", "领取请求失败，请刷新领取状态后重试"
			}
			var known *Failure
			if errors.As(err, &known) {
				failure = classify(known)
			}
			if requestContext.Err() != nil {
				failure = &Failure{Kind: "canceled", Code: "canceled", Message: "请求已取消"}
			}
			_ = a.exec("INSERT INTO claim_log(account_id,ts,plan_id,ok,code,message,trigger) VALUES(?,?,?,?,?,?,?)", id, nowMS(), plan, 0, failure.Code, conciseFailureMessage(failure), trigger)
			result = merge(result, M{"ok": false, "code": failure.Code, "message": conciseFailureMessage(failure), "failure_kind": failure.Kind, "retryable": failure.Retryable, "retry_after_ms": failure.RetryAfter.Milliseconds(), "verification": diagnostic})
			err = failure
		}
		if !truth(result["ok"]) {
			diagnostic["failure_stage"] = diagnostic["stage"]
		}
		diagnostic["retry_after_ms"] = result["retry_after_ms"]
		diagnostic["requires_verification"] = truth(result["requires_verification"])
		a.audit("system", "claim", M{"account": id, "plan_id": plan, "ok": truth(result["ok"]), "code": result["code"], "verification": diagnostic}, "")
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
	var failure *Failure
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
	headers := M{"X-Device-Mid": r["device_mid"]}
	if param := str(verification["captcha_verify_param"]); param != "" {
		if failure := a.reserveCaptchaTicket(param); failure != nil {
			return nil, failure
		}
		headers["X-Aliyun-Captcha-Verify-Param"] = param
		headers["X-Aliyun-Captcha-Verify-Region"] = verification["captcha_region"]
	}
	var verificationFailure *Failure
	// Every claim without a browser ticket resolves verification first. The
	// trigger and legacy auto_verify flag must not permit unverified probes.
	if str(verification["captcha_verify_param"]) == "" {
		diagnostic["stage"] = "scene"
		var scene M
		scene, verificationFailure = a.captchaScene(ctx, r)
		if verificationFailure == nil && truth(scene["enabled"]) {
			diagnostic["sdk_attempted"], diagnostic["stage"] = true, "sdk"
			purpose := captchaPurposeClaim
			if trigger == "scheduled" {
				purpose = captchaPurposeScheduledClaim
			}
			sdkVerification, verificationFailure = a.captchaHeaders(ctx, r, purpose, true)
		}
		a.prepareProxyFailover(ctx, verificationFailure)
		if verificationFailure == nil {
			token = str(a.tokens(id)["zcode_jwt"])
			if failure := validateCaptchaRequestToken(sdkVerification, r, token); failure != nil {
				return nil, failure
			}
			for key := range sdkVerification.Headers {
				headers[key] = sdkVerification.Headers.Get(key)
			}
		}
	}
	res := apiResult{}
	diagnostic["ticket_attached"] = str(headers["X-Aliyun-Captcha-Verify-Param"]) != ""
	if verificationFailure == nil {
		diagnostic["stage"], diagnostic["claim_request_started"] = "claim_request", true
		ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{WroteRequest: func(info httptrace.WroteRequestInfo) {
			if info.Err == nil {
				requestSent.Store(true)
			}
		}})
		res, e = a.api(ctx, r, "POST", "/api/v1/zcode-plan/billing/claim", token, M{"plan_id": plan}, headers, a.jar(id))
		if e != nil {
			return nil, e
		}
	}
	if claimResponseRequiresVerification(res) {
		if str(headers["X-Aliyun-Captcha-Verify-Param"]) != "" {
			a.rejectCaptchaVerification(sdkVerification)
			if failure := apiResponseFailure(res); failure.Code == "F014" || failure.Code == "F020" {
				a.invalidateCaptchaScene(a.captchaSceneKey(ctx))
			}
		} else {
			a.prepareCaptchaChallenge(ctx)
		}
	}
	data := obj(res.JSON["data"])
	p := obj(data["plan"])
	ok := res.Status == 200 && codeIs(res.JSON["code"], 0) && len(p) > 0
	var code any
	if raw := first(obj(res.JSON["error"])["code"], res.JSON["code"]); raw != nil {
		code = str(raw)
	}
	message := first(data["message"], res.JSON["msg"], "")
	if !ok {
		failure = apiResponseFailure(res)
		if res.OK && codeIs(res.JSON["code"], 0) && len(p) == 0 {
			failure = &Failure{Kind: "server", Code: "claim_invalid_response", Message: "领取响应缺少卡片信息，请刷新领取状态"}
		}
	}
	requiresVerification := !ok && claimResponseRequiresVerification(res)
	if verificationFailure != nil {
		failure = verificationFailure
		code = verificationFailure.Code
		message = conciseFailureMessage(verificationFailure)
		requiresVerification = claimBrowserFallback(verificationFailure)
		if requiresVerification {
			message = str(message) + "，请完成浏览器验证后领取"
		}
	} else if requiresVerification {
		failure.Kind, failure.Retryable = "risk", false
		message = "请完成浏览器安全验证后领取（" + str(code) + "）"
		if truth(diagnostic["sdk_attempted"]) {
			message = "服务器验证后仍需安全验证，请在浏览器完成（" + str(code) + "）"
		}
	} else if failure != nil {
		code, message = failure.Code, conciseFailureMessage(failure)
	}
	end := secondsMS(p["ends_at"])
	_ = a.exec("INSERT INTO claim_log(account_id,ts,plan_id,ok,code,message,user_plan_id,ends_at,trigger) VALUES(?,?,?,?,?,?,?,?,?)", id, nowMS(), plan, integer(ok), code, message, p["user_plan_id"], end, trigger)
	diagnostic["initial_http_status"], diagnostic["http_status"], diagnostic["requires_verification"] = res.Status, res.Status, requiresVerification
	if verificationFailure != nil {
		diagnostic["sdk_error"] = verificationFailure.Code
	}
	if ok {
		a.acceptCaptchaVerification(ctx, &sdkVerification)
		a.invalidateBalance(id)
		a.scheduleNext(id, 0)
	}
	var resultPlan any
	if len(p) > 0 {
		resultPlan = p
	}
	result = M{"ok": ok, "httpStatus": res.Status, "code": code, "message": message, "plan": resultPlan, "endsAt": end, "requires_verification": requiresVerification, "verification": diagnostic}
	if failure != nil {
		result["failure_kind"], result["retryable"] = failure.Kind, failure.Retryable
		if failure.RetryAfter > 0 {
			result["retry_after_ms"] = failure.RetryAfter.Milliseconds()
		}
	}
	return result, nil
}
func (a *App) autoClaim(ctx context.Context, dry bool) (M, error) {
	return a.autoClaimScope(ctx, dry, time.Time{})
}

func (a *App) autoClaimScope(ctx context.Context, dry bool, scheduledAt time.Time) (result M, err error) {
	if !a.claimBatchMu.TryLock() {
		return M{"checked": 0, "claimed": 0, "results": []any{}, "in_progress": true}, nil
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
		r := obj(v)
		id := str(r["id"])
		if !truth(a.tokens(id)["zcode_jwt"]) {
			continue
		}
		checked++
		p, e := a.preview(ctx, r, false)
		if e != nil {
			results = append(results, M{"account": id, "error": e.Error()})
			continue
		}
		if p["warning"] != nil {
			results = append(results, M{"account": id, "warning": p["warning"]})
		}
		for _, v := range arr(p["plans"]) {
			plan := str(obj(v)["plan_id"])
			if dry {
				results = append(results, M{"account": id, "plan_id": plan, "would_claim": true})
				continue
			}
			res, e := a.claim(ctx, id, plan, "auto", nil)
			if e != nil {
				results = append(results, M{"account": id, "plan_id": plan, "error": e.Error()})
				break
			}
			if truth(res["ok"]) {
				claimed++
			}
			results = append(results, merge(res, M{"account": id, "plan_id": plan}))
			if !truth(res["ok"]) && (truth(res["requires_verification"]) || res["failure_kind"] == "account_risk" || res["failure_kind"] == "auth" || res["failure_kind"] == "network" || res["failure_kind"] == "rate_limit") {
				break
			}
		}
	}
	return M{"checked": checked, "claimed": claimed, "results": results}, nil
}

func (a *App) recordedClaim(id, plan string, since int64) (M, error) {
	rows, err := query(a.db, "SELECT trigger FROM claim_log WHERE account_id=? AND plan_id=? AND ok=1 AND ts>=? ORDER BY ts DESC LIMIT 1", id, plan, since)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return M{"ok": true, "already_claimed": true, "external_claim": obj(rows[0])["trigger"] != "scheduled", "message": "已确认领取成功"}, nil
}
