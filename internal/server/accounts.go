package server

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

const accountFields = "label family customer_id group_id status enc_tokens device_mid app_version card_started_at card_expires_at web_cookie_expires_at business_token_expires_at zcode_jwt_last_ok_at fail_streak cooldown_until capabilities last_balance last_error proxy_url last_ok_at last_used_at"

func (a *App) updateAccount(id string, m M) error {
	return a.update("accounts", "id", id, m, accountFields)
}
func (a *App) tokens(id string) M {
	out := M{}
	r := a.account(id)
	for k, v := range obj(parse(r["enc_tokens"], nil)) {
		s, e := decrypt(str(v), a.secret)
		if e == nil {
			out[k] = s
		}
	}
	return out
}
func (a *App) setTokens(id string, m M) error {
	r := a.account(id)
	if r == nil {
		return fmt.Errorf("账号不存在")
	}
	enc := obj(parse(r["enc_tokens"], nil))
	for k, v := range m {
		if v == nil {
			delete(enc, k)
			continue
		}
		s, e := encrypt(str(v), a.secret)
		if e != nil {
			return e
		}
		enc[k] = s
	}
	return a.updateAccount(id, M{"enc_tokens": enc})
}
func (a *App) insertAccount(fields, tokens M) (M, error) {
	id := "acc_" + random(4)
	enc := M{}
	for k, v := range tokens {
		if truth(v) {
			s, e := encrypt(str(v), a.secret)
			if e != nil {
				return nil, e
			}
			enc[k] = s
		}
	}
	group := fields["group_id"]
	if _, ok := fields["group_id"]; !ok {
		for _, v := range a.groups() {
			g := obj(v)
			if truth(g["enabled"]) && str(g["role"]) == "primary" && (!truth(g["family_filter"]) || g["family_filter"] == fields["family"]) {
				group = g["id"]
				break
			}
		}
	}
	e := a.exec("INSERT INTO accounts(id,label,family,customer_id,group_id,status,enc_tokens,device_mid,app_version,web_cookie_expires_at,source,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)", id, fallback(fields["label"], "账号"), fallback(fields["family"], "bigmodel"), fields["customer_id"], group, fallback(fields["status"], "unverified"), js(enc), fallback(fields["device_mid"], uuid()), a.config()["app_version"], fields["web_cookie_expires_at"], fallback(fields["source"], "local"), nowMS())
	if e != nil {
		return nil, e
	}
	return a.account(id), nil
}
func jwtPayload(s string) M {
	p := strings.Split(s, ".")
	if len(p) < 2 {
		return nil
	}
	b, e := base64.RawURLEncoding.DecodeString(strings.TrimRight(p[1], "="))
	if e != nil {
		return nil
	}
	v := parse(string(b), nil)
	m, _ := v.(map[string]any)
	return m
}
func family(v any) string {
	if strings.Contains(lower(v), "zai") || lower(v) == "z.ai" {
		return "zai"
	}
	return "bigmodel"
}
func (a *App) publicAccount(r M) M {
	m := clone(r)
	tokens := obj(parse(m["enc_tokens"], nil))
	delete(m, "enc_tokens")
	kinds := []any{}
	for k := range tokens {
		kinds = append(kinds, k)
	}
	m["token_kinds"] = kinds
	for _, k := range []string{"zcode_jwt", "web_jwt", "business_token"} {
		m["has_"+k] = truth(tokens[k])
	}
	m["capabilities"] = parse(m["capabilities"], []any{})
	m["last_balance"] = parse(m["last_balance"], nil)
	m["platform_name"] = nil
	return m
}
func (a *App) fetchBalance(ctx context.Context, r M) (M, error) {
	t := a.tokens(str(r["id"]))
	res, e := a.api(ctx, r, "GET", "/api/v1/zcode-plan/billing/balance?app_version="+str(a.config()["app_version"]), str(t["zcode_jwt"]), nil, M{"X-Device-Mid": r["device_mid"], "X-Request-Id": uuid()}, a.jar(str(r["id"])))
	if e != nil {
		return nil, e
	}
	server := nowMS()
	if d, e := time.Parse(time.RFC1123, res.Header.Get("Date")); e == nil {
		server = d.UnixMilli()
	}
	data := obj(res.JSON["data"])
	if truth(data["server_time"]) {
		server = integer(data["server_time"]) * 1000
	}
	out := M{"ok": res.OK && codeIs(res.JSON["code"], 0), "authFailed": false, "httpStatus": res.Status, "plans": []any{}, "balances": []any{}, "capabilities": []any{}, "serverNowMs": server, "card": nil, "raw": res.JSON}
	if !truth(out["ok"]) {
		failure := apiResponseFailure(res)
		out["authFailed"] = failure.Kind == "auth"
		out["failure_kind"], out["error_code"] = failure.Kind, failure.Code
		out["retry_after_ms"] = failure.RetryAfter.Milliseconds()
		return out, nil
	}
	plans := arr(data["plans"])
	active := map[string]bool{}
	for _, v := range plans {
		p := obj(v)
		if str(p["status"]) != "active" {
			continue
		}
		if truth(p["ends_at"]) && integer(p["ends_at"])*1000 <= server {
			p["_staleExpired"] = true
			continue
		}
		active[str(first(p["user_plan_id"], p["plan_id"]))] = true
		if out["card"] == nil && (strings.Contains(lower(p["plan_id"]), "start-plan") || strings.Contains(lower(p["name"]), "start plan") || strings.Contains(lower(p["name"]), "start-plan")) {
			out["card"] = M{"userPlanId": p["user_plan_id"], "planId": p["plan_id"], "name": p["name"], "status": p["status"], "startedAt": secondsMS(p["starts_at"]), "endsAt": secondsMS(p["ends_at"])}
		}
	}
	balances, caps := []any{}, []any{}
	for _, v := range arr(data["balances"]) {
		b := obj(v)
		if truth(b["user_plan_id"]) && !active[str(b["user_plan_id"])] {
			continue
		}
		balances = append(balances, b)
		added := false
		for _, c := range arr(b["capabilities"]) {
			if s, ok := strings.CutPrefix(str(c), "model:"); ok {
				if !contains(caps, s) {
					caps = append(caps, s)
				}
				added = true
			}
		}
		if !added && truth(b["show_name"]) && !contains(caps, str(b["show_name"])) {
			caps = append(caps, b["show_name"])
		}
	}
	out["plans"], out["balances"], out["capabilities"] = plans, balances, caps
	return out, nil
}
func secondsMS(v any) any {
	if !truth(v) {
		return nil
	}
	return integer(v) * 1000
}
func (a *App) applyBalance(r, b M, snapshot bool) {
	id := str(r["id"])
	fields := M{"status": "active", "zcode_jwt_last_ok_at": nowMS(), "last_ok_at": nowMS(), "fail_streak": 0, "last_error": nil, "capabilities": b["capabilities"], "last_balance": b["raw"], "card_expires_at": nil}
	if c := obj(b["card"]); len(c) > 0 {
		fields["card_started_at"], fields["card_expires_at"] = c["startedAt"], c["endsAt"]
	}
	_ = a.updateAccount(id, fields)
	a.mu.Lock()
	if s := a.states[id]; s != nil {
		s.Cooldown = 0
	}
	a.mu.Unlock()
	if snapshot {
		for _, v := range arr(b["balances"]) {
			x := obj(v)
			_ = a.exec("INSERT INTO quota_snapshots(account_id,ts,bucket_id,entitlement_id,meter,total_units,used_units,remaining_units,expires_at) VALUES(?,?,?,?,?,?,?,?,?)", id, nowMS(), x["bucket_id"], x["entitlement_id"], x["meter"], x["total_units"], x["used_units"], x["remaining_units"], secondsMS(x["expires_at"]))
		}
		_ = a.exec("DELETE FROM quota_snapshots WHERE account_id=? AND ts<?", id, nowMS()-48*3600000)
	}
}
func (a *App) verify(ctx context.Context, r M) (M, error) {
	id := str(r["id"])
	t := a.tokens(id)
	if !truth(t["zcode_jwt"]) {
		if truth(t["web_jwt"]) {
			res, e := a.customer(ctx, r, str(t["web_jwt"]))
			if e != nil {
				return nil, e
			}
			if res.OK {
				_ = a.updateAccount(id, M{"status": "web_only", "last_ok_at": nowMS(), "last_error": nil})
			}
			return M{"ok": res.OK, "status": "web_only", "reason": "网页会话不可用于推理"}, nil
		}
		return M{"ok": false, "status": "unverified", "reason": "缺少可用凭据"}, nil
	}
	bal, e := a.fetchBalance(ctx, r)
	if e != nil {
		return nil, e
	}
	if truth(bal["ok"]) {
		a.applyBalance(r, bal, true)
		return M{"ok": true, "status": "active", "balance": bal}, nil
	}
	status := "cooldown"
	if str(r["status"]) == "unverified" {
		status = "unverified"
	}
	if truth(bal["authFailed"]) {
		status = "needs_relogin"
	}
	reason := fmt.Sprintf("balance HTTP %v", bal["httpStatus"])
	_ = a.updateAccount(id, M{"status": status, "last_error": reason, "fail_streak": integer(r["fail_streak"]) + 1})
	return M{"ok": false, "status": status, "reason": reason}, nil
}
func (a *App) importLocal(ctx context.Context, b M) (M, error) {
	home, _ := os.UserHomeDir()
	p := filepath.Join(home, ".zcode", "v2", "credentials.json")
	data, e := os.ReadFile(p)
	if e != nil {
		return M{"ok": false, "error": "本机未找到客户端凭据 " + p, "results": []any{}}, nil
	}
	u, _ := user.Current()
	username := "unknown"
	if u != nil {
		username = u.Username
		if i := strings.LastIndex(username, "\\"); i >= 0 {
			username = username[i+1:]
		}
	}
	platform := runtime.GOOS
	if platform == "windows" {
		platform = "win32"
	}
	secret := "zcode-credential-fallback:" + platform + ":" + home + ":" + username
	tokens := M{}
	for k, v := range obj(parse(string(data), nil)) {
		slot := map[string]string{"zcodejwttoken": "zcode_jwt", "oauth:bigmodel:access_token": "business_token", "oauth:zai:access_token": "business_token", "oauth:active_provider": "active_provider"}[k]
		if slot == "" || !strings.HasPrefix(str(v), "enc:v1:") {
			continue
		}
		if plain, e := decrypt(str(v), secret); e == nil {
			tokens[slot] = plain
		}
	}
	if !truth(tokens["zcode_jwt"]) {
		return M{"ok": false, "results": []any{M{"ok": false, "reason": "凭据中无可解密的 zcodejwttoken"}}}, nil
	}
	customer := jwtPayload(str(tokens["zcode_jwt"]))["user_id"]
	if dup := a.row("SELECT id FROM accounts WHERE customer_id=?", customer); dup != nil {
		return M{"ok": false, "results": []any{M{"ok": false, "reason": "账号已存在", "existing": dup["id"]}}}, nil
	}
	device := uuid()
	if d, e := os.ReadFile(filepath.Join(home, ".zcode", "v2", "telemetry-state.json")); e == nil {
		device = str(fallback(obj(parse(string(d), nil))["deviceMid"], device))
	}
	r, e := a.insertAccount(M{"label": fallback(b["label"], "本机-"+family(tokens["active_provider"])), "family": family(tokens["active_provider"]), "customer_id": customer, "source": "local", "device_mid": device}, tokens)
	if e != nil {
		return nil, e
	}
	result, e := a.verify(ctx, r)
	if e != nil {
		result = M{"ok": false, "reason": e.Error(), "status": "unverified"}
	}
	result["id"] = r["id"]
	a.scheduleNext(str(r["id"]), time.Second)
	n := 0
	if truth(result["ok"]) {
		n = 1
	}
	return M{"ok": n > 0, "imported": n, "results": []any{result}}, nil
}
func (a *App) importRegistration(ctx context.Context, b M) (M, error) {
	zcodeJWT := strings.TrimSpace(str(b["zcodeJwt"]))
	webJWT := strings.TrimSpace(str(b["webJwt"]))
	businessToken := strings.TrimSpace(str(b["businessToken"]))
	if zcodeJWT == "" {
		return M{"ok": false, "error": "缺少 zcodeJwt"}, nil
	}
	claims := jwtPayload(zcodeJWT)
	identity := str(first(claims["user_id"], claims["sub"]))
	if identity == "" {
		return M{"ok": false, "error": "zcodeJwt 缺少 user_id/sub"}, nil
	}
	provider := family(b["provider"])
	if provider != "zai" {
		provider = "bigmodel"
	}
	deviceMid := strings.TrimSpace(str(b["deviceMid"]))
	if deviceMid == "" {
		deviceMid = uuid()
	}
	if existing := a.row(
		"SELECT * FROM accounts WHERE customer_id=?",
		identity,
	); existing != nil {
		tokens := M{"zcode_jwt": zcodeJWT, "active_provider": provider}
		if webJWT != "" {
			tokens["web_jwt"] = webJWT
		}
		if businessToken != "" {
			tokens["business_token"] = businessToken
		}
		if e := a.setTokens(str(existing["id"]), tokens); e != nil {
			return nil, e
		}
		_ = a.updateAccount(str(existing["id"]), M{
			"device_mid": deviceMid,
			"status":     "active",
			"last_error": nil,
		})
		verified, e := a.verify(ctx, a.account(str(existing["id"])))
		if e != nil {
			verified = M{"ok": false, "reason": e.Error()}
		}
		result := M{
			"ok":       truth(verified["ok"]),
			"updated":  true,
			"id":       existing["id"],
			"status":   verified["status"],
			"reason":   verified["reason"],
			"verified": verified["ok"],
		}
		a.scheduleNext(str(existing["id"]), time.Second)
		return result, nil
	}
	tokens := M{
		"zcode_jwt":       zcodeJWT,
		"active_provider": provider,
	}
	if webJWT != "" {
		tokens["web_jwt"] = webJWT
	}
	if businessToken != "" {
		tokens["business_token"] = businessToken
	}
	label := strings.TrimSpace(str(b["label"]))
	if label == "" {
		label = "pro-zai-" + identity[max(0, len(identity)-6):]
	}
	account, e := a.insertAccount(M{
		"label":       label,
		"family":      provider,
		"customer_id": identity,
		"device_mid":  deviceMid,
		"source":      "pro-zai",
		"status":      "active",
	}, tokens)
	if e != nil {
		return nil, e
	}
	verified, e := a.verify(ctx, account)
	if e != nil {
		verified = M{"ok": false, "reason": e.Error()}
	}
	result := M{
		"ok":       truth(verified["ok"]),
		"updated":  false,
		"id":       account["id"],
		"status":   verified["status"],
		"reason":   verified["reason"],
		"verified": verified["ok"],
	}
	a.scheduleNext(str(account["id"]), time.Second)
	return result, nil
}

func (a *App) importCookie(ctx context.Context, b M, target M) (M, error) {
	token := strings.TrimSpace(str(b["webJwt"]))
	p := jwtPayload(token)
	if p == nil || (p["user_id"] != nil && p["user_channel"] == nil) {
		return M{"ok": false, "error": "请输入有效的网页会话 JWT；客户端凭据请使用本机导入/OAuth"}, nil
	}
	identity := nullable(first(p["customer_id"], p["user_id"]))
	expiry := first(nullable(b["expiresAt"]), secondsMS(p["exp"]))
	if expiry != nil && integer(expiry) <= nowMS() {
		return M{"ok": false, "error": "Cookie 到期时间无效或已过期"}, nil
	}
	if truth(p["exp"]) && integer(p["exp"])*1000 <= nowMS() {
		return M{"ok": false, "error": "网页 JWT 已过期"}, nil
	}
	f := family(b["family"])
	if target != nil {
		f = str(target["family"])
		if !truth(target["customer_id"]) || identity == nil || str(identity) != str(target["customer_id"]) {
			return M{"ok": false, "error": "Cookie 与账号身份不一致，未更新"}, nil
		}
	}
	res, e := a.customer(ctx, M{"family": f, "proxy_url": target["proxy_url"]}, token)
	if e != nil {
		return nil, e
	}
	if !res.OK {
		return M{"ok": false, "error": "网页会话验证失败"}, nil
	}
	if target != nil {
		e = a.setTokens(str(target["id"]), M{"web_jwt": token})
		if e == nil {
			e = a.updateAccount(str(target["id"]), M{"web_cookie_expires_at": expiry, "last_ok_at": nowMS(), "last_error": nil})
		}
		return M{"ok": e == nil, "expires_at": expiry}, e
	}
	if identity != nil && a.row("SELECT id FROM accounts WHERE family=? AND customer_id=?", f, identity) != nil {
		return M{"ok": false, "error": "账号已存在，请使用续期更新 Cookie"}, nil
	}
	r, e := a.insertAccount(M{"family": f, "label": fallback(b["label"], "Cookie-"+f), "customer_id": identity, "status": "web_only", "source": "cookie", "web_cookie_expires_at": expiry}, M{"web_jwt": token})
	if e != nil {
		return nil, e
	}
	a.scheduleNext(str(r["id"]), time.Second)
	return M{"ok": true, "id": r["id"], "status": "web_only"}, nil
}

func balanceData(r M) M {
	d := obj(parse(r["last_balance"], nil))
	if x, ok := d["data"].(map[string]any); ok {
		return x
	}
	return d
}
func liveBalances(data M, now int64) []any {
	plans := arr(data["plans"])
	active := map[string]bool{}
	for _, v := range plans {
		p := obj(v)
		if str(p["status"]) == "active" && (!truth(p["ends_at"]) || integer(p["ends_at"])*1000 > now) {
			active[str(p["user_plan_id"])] = true
			active[str(p["plan_id"])] = true
		}
	}
	out := []any{}
	for _, v := range arr(data["balances"]) {
		b := obj(v)
		end := integer(first(b["expires_at"], b["period_end"]))
		if len(plans) > 0 && truth(b["user_plan_id"]) && !active[str(b["user_plan_id"])] {
			continue
		}
		if end > 0 && end*1000 <= now {
			continue
		}
		out = append(out, b)
	}
	return out
}
func (a *App) summary() M {
	out := M{"accounts": []any{}, "totalRemaining": 0, "totalUsed": 0, "totalGranted": 0, "totalPending": 0, "activeCount": 0, "count": 0}
	accounts := []any{}
	now := nowMS()
	deltas := map[string]any{}
	for _, v := range a.rows("SELECT account_id,SUM(delta) n FROM (SELECT account_id,MAX(0,MAX(used_units)-MIN(used_units)) delta FROM quota_snapshots WHERE ts>=? GROUP BY account_id,bucket_id) GROUP BY account_id", now-86400000) {
		r := obj(v)
		deltas[str(r["account_id"])] = r["n"]
	}
	for _, v := range a.rows("SELECT * FROM accounts ORDER BY created_at") {
		r := obj(v)
		d := balanceData(r)
		buckets, pending := []any{}, []any{}
		remaining, used, total, pendingTotal := float64(0), float64(0), float64(0), float64(0)
		ents := map[string]bool{}
		for _, x := range liveBalances(d, now) {
			b := obj(x)
			remaining += num(b["remaining_units"])
			used += num(b["used_units"])
			total += num(b["total_units"])
			buckets = append(buckets, M{"name": first(b["show_name"], b["entitlement_id"], b["meter"], "额度"), "total": num(b["total_units"]), "used": num(b["used_units"]), "remaining": num(b["remaining_units"]), "unit": fallback(b["unit_type"], ""), "expires_at": secondsMS(first(b["expires_at"], b["period_end"])), "plan_id": b["plan_id"]})
			ents[str(b["entitlement_id"])] = true
			ents[str(b["plan_id"])+":"+str(first(b["show_name"], b["meter"]))] = true
		}
		for _, v := range arr(d["plans"]) {
			p := obj(v)
			if str(p["status"]) != "active" || (truth(p["ends_at"]) && integer(p["ends_at"])*1000 <= now) {
				continue
			}
			for _, v := range arr(p["entitlements"]) {
				e := obj(v)
				if integer(e["effective_at"])*1000 <= now || ents[str(e["entitlement_id"])] || ents[str(p["plan_id"])+":"+str(first(e["show_name"], e["meter"]))] {
					continue
				}
				pendingTotal += num(e["grant_units"])
				pending = append(pending, M{"name": first(e["show_name"], e["entitlement_id"], e["meter"], "额度"), "total": num(e["grant_units"]), "unit": fallback(e["unit_type"], ""), "effective_at": secondsMS(e["effective_at"]), "plan_id": p["plan_id"], "plan_name": p["name"]})
			}
		}
		sort.Slice(pending, func(i, j int) bool {
			return integer(obj(pending[i])["effective_at"]) < integer(obj(pending[j])["effective_at"])
		})
		m := a.publicAccount(r)
		m["remaining"], m["used"], m["total"] = remaining, used, total
		if len(d) == 0 {
			m["remaining"], m["used"], m["total"] = nil, nil, nil
		}
		m["pending_buckets"], m["pending_total"], m["quota_buckets"] = pending, pendingTotal, buckets
		m["balance_checked_at"] = r["zcode_jwt_last_ok_at"]
		m["expiring_in_min"] = nil
		if truth(r["card_expires_at"]) {
			m["expiring_in_min"] = (integer(r["card_expires_at"]) - now) / 60000
		}
		m["delta24h"] = num(deltas[str(r["id"])])
		accounts = append(accounts, m)
		out["totalRemaining"] = num(out["totalRemaining"]) + remaining
		out["totalUsed"] = num(out["totalUsed"]) + used
		out["totalGranted"] = num(out["totalGranted"]) + total
		out["totalPending"] = num(out["totalPending"]) + pendingTotal
		if str(r["status"]) == "active" {
			out["activeCount"] = num(out["activeCount"]) + 1
		}
	}
	out["accounts"], out["count"] = accounts, len(accounts)
	return out
}
