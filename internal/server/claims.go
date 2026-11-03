package server

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

func (a *App) preview(ctx context.Context, r M) (M, error) {
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
	res, e := a.api(ctx, r, "GET", "/api/v1/zcode-plan/billing/preview?app_version="+url.QueryEscape(str(a.config()["app_version"]))+"&platform="+url.QueryEscape(platform()), str(a.tokens(str(r["id"]))["zcode_jwt"]), nil, M{"X-Device-Mid": r["device_mid"]}, a.jar(str(r["id"])))
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
func (a *App) discover(ctx context.Context, id string) M {
	accounts := a.rows("SELECT * FROM accounts ORDER BY created_at")
	if id != "" {
		accounts = []any{}
		if r := a.account(id); r != nil {
			accounts = append(accounts, r)
		}
	}
	previews := []any{}
	for _, v := range accounts {
		r := obj(v)
		if !truth(a.tokens(str(r["id"]))["zcode_jwt"]) {
			continue
		}
		m := M{"account_id": r["id"], "label": r["label"], "plans": []any{}}
		if p, e := a.preview(ctx, r); e == nil {
			m["plans"] = arr(p["plans"])
			if p["warning"] != nil {
				m["warning"] = p["warning"]
			}
		} else {
			m["error"] = e.Error()
		}
		if reward, e := a.incentives(ctx, r); e == nil {
			m["incentives"] = reward
		} else {
			m["incentives_error"] = e.Error()
		}
		previews = append(previews, m)
	}
	return M{"previews": previews}
}
func (a *App) claim(ctx context.Context, id, plan, trigger string, verification M) (M, error) {
	lock := id + ":" + plan
	a.mu.Lock()
	if a.claimLocks[lock] {
		a.mu.Unlock()
		return M{"ok": false, "code": "in_progress", "message": "该奖励正在领取，请稍候"}, nil
	}
	a.claimLocks[lock] = true
	a.mu.Unlock()
	defer func() { a.mu.Lock(); delete(a.claimLocks, lock); a.mu.Unlock() }()
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
	ctx, release, e := a.pinOutboundProxy(ctx, r)
	if e != nil {
		return nil, e
	}
	defer release()
	headers := M{"X-Device-Mid": r["device_mid"]}
	if param := str(verification["captcha_verify_param"]); param != "" {
		if failure := a.reserveCaptchaTicket(param); failure != nil {
			return nil, failure
		}
		headers["X-Aliyun-Captcha-Verify-Param"] = param
		headers["X-Aliyun-Captcha-Verify-Region"] = verification["captcha_region"]
	}
	res, e := a.api(ctx, r, "POST", "/api/v1/zcode-plan/billing/claim", token, M{"plan_id": plan}, headers, a.jar(id))
	if e != nil {
		return nil, e
	}
	var verificationFailure *Failure
	if trigger == "auto" && claimRequiresVerification(res.JSON["code"]) {
		a.invalidateCaptchaScene()
		var ticketHeaders http.Header
		ticketHeaders, verificationFailure = a.captchaHeaders(ctx, r, true)
		if verificationFailure == nil {
			for key := range ticketHeaders {
				headers[key] = ticketHeaders.Get(key)
			}
			res, e = a.api(ctx, r, "POST", "/api/v1/zcode-plan/billing/claim", token, M{"plan_id": plan}, headers, a.jar(id))
			if e != nil {
				return nil, e
			}
		}
	}
	data := obj(res.JSON["data"])
	p := obj(data["plan"])
	ok := res.Status == 200 && codeIs(res.JSON["code"], 0) && len(p) > 0
	var code any
	if res.JSON["code"] != nil {
		code = str(res.JSON["code"])
	}
	message := first(data["message"], res.JSON["msg"], "")
	requiresVerification := !ok && claimRequiresVerification(code)
	if verificationFailure != nil {
		code = verificationFailure.Code
		message = conciseFailureMessage(verificationFailure) + "，请在领卡中心领取"
		requiresVerification = true
	} else if requiresVerification {
		message = "请在领卡中心完成安全验证后领取（" + str(code) + "）"
	}
	end := secondsMS(p["ends_at"])
	_ = a.exec("INSERT INTO claim_log(account_id,ts,plan_id,ok,code,message,user_plan_id,ends_at,trigger) VALUES(?,?,?,?,?,?,?,?,?)", id, nowMS(), plan, integer(ok), code, message, p["user_plan_id"], end, trigger)
	a.audit("system", "claim", M{"account": id, "plan_id": plan, "ok": ok, "code": code}, "")
	if ok {
		if b, e := a.fetchBalance(ctx, r); e == nil && truth(b["ok"]) {
			a.applyBalance(r, b, true)
		}
	}
	var resultPlan any
	if len(p) > 0 {
		resultPlan = p
	}
	return M{"ok": ok, "httpStatus": res.Status, "code": code, "message": message, "plan": resultPlan, "endsAt": end, "requires_verification": requiresVerification}, nil
}
func (a *App) autoClaim(ctx context.Context, dry bool) (M, error) {
	lock := "__auto"
	a.mu.Lock()
	if a.claimLocks[lock] {
		a.mu.Unlock()
		return M{"checked": 0, "claimed": 0, "results": []any{}, "in_progress": true}, nil
	}
	a.claimLocks[lock] = true
	a.mu.Unlock()
	defer func() { a.mu.Lock(); delete(a.claimLocks, lock); a.mu.Unlock() }()
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
		p, e := a.preview(ctx, r)
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
				results = append(results, M{"account": id, "error": e.Error()})
				continue
			}
			if truth(res["ok"]) {
				claimed++
			}
			results = append(results, merge(res, M{"account": id, "plan_id": plan}))
		}
	}
	return M{"checked": checked, "claimed": claimed, "results": results}, nil
}
