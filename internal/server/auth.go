package server

import (
	"crypto/subtle"
	"net"
	"net/http"
	"strings"
)

func bearer(r *http.Request) string {
	s := r.Header.Get("Authorization")
	if strings.HasPrefix(s, "Bearer ") {
		return strings.TrimSpace(s[7:])
	}
	return strings.TrimSpace(r.Header.Get("X-Api-Key"))
}
func (a *App) adminAuth(r *http.Request) bool {
	token := bearer(r)
	if token == "" {
		token = r.URL.Query().Get("token")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	expiry := a.sessions[token]
	if expiry <= nowMS() {
		delete(a.sessions, token)
		return false
	}
	return true
}
func (a *App) password(v any) bool {
	s, ok := v.(string)
	if !ok || s == "" {
		return false
	}
	x, y := hash(s), hash(str(a.configValue("admin_password")))
	return subtle.ConstantTimeCompare([]byte(x), []byte(y)) == 1
}
func ipAllowed(ip string, patterns []any) bool {
	for _, v := range patterns {
		p := strings.TrimSpace(str(v))
		if p == "" {
			continue
		}
		if ip == p {
			return true
		}
		if strings.HasSuffix(p, ".*") && strings.HasPrefix(ip, strings.TrimSuffix(p, "*")) {
			return true
		}
		if _, n, e := net.ParseCIDR(p); e == nil && n.Contains(net.ParseIP(ip)) {
			return true
		}
	}
	return false
}
func (a *App) authenticate(r *http.Request) (M, *Failure) {
	secret := bearer(r)
	if secret == "" {
		return nil, &Failure{Status: 401, Code: "invalid_api_key", Message: "缺少 API 密钥"}
	}
	k := a.row("SELECT * FROM keys WHERE key_hash=?", hash(secret))
	if k == nil {
		return nil, &Failure{Status: 401, Code: "invalid_api_key", Message: "无效密钥"}
	}
	if !truth(k["enabled"]) {
		return nil, &Failure{Status: 403, Code: "invalid_api_key", Message: "密钥已禁用"}
	}
	if truth(k["expires_at"]) && integer(k["expires_at"]) < nowMS() {
		return nil, &Failure{Status: 403, Code: "invalid_api_key", Message: "密钥已过期"}
	}
	if k["quota_limit"] != nil && num(k["used_units"]) >= num(k["quota_limit"]) {
		return nil, &Failure{Status: 429, Code: "quota_exceeded", Message: "密钥配额已用尽"}
	}
	ips := arr(parse(k["ip_whitelist"], nil))
	if len(ips) > 0 && !ipAllowed(a.ip(r), ips) {
		return nil, &Failure{Status: 403, Code: "ip_not_allowed", Message: "IP 不在白名单"}
	}
	_ = a.exec("UPDATE keys SET last_used_at=? WHERE id=?", nowMS(), k["id"])
	return k, nil
}
func (a *App) createKey(b M) (M, error) {
	id, secret := "key_"+random(4), "zk2-"+random(20)
	level := first(b["defaultLevel"], b["default_level"])
	if !validLevel(str(level)) {
		level = nil
	}
	groups := first(b["groupOrder"], b["group_order"])
	if groups != nil {
		groups = js(groups)
	}
	e := a.exec("INSERT INTO keys(id,name,key_hash,key_tail,quota_limit,expires_at,ip_whitelist,model_whitelist,group_order,sticky_minutes,default_level,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)", id, fallback(b["name"], "default"), hash(secret), secret[len(secret)-6:], first(b["quotaLimit"], b["quota_limit"]), first(b["expiresAt"], b["expires_at"]), js(first(b["ipWhitelist"], b["ip_whitelist"], []any{})), js(first(b["modelWhitelist"], b["model_whitelist"], []any{})), groups, first(b["stickyMinutes"], b["sticky_minutes"], 0), level, nowMS())
	return M{"ok": e == nil, "id": id, "secret": secret}, e
}
func (a *App) keyChain(k M) []any {
	if s := k["group_order"]; s != nil {
		if v, ok := parse(s, nil).([]any); ok {
			return v
		}
	}
	return a.defaultChain()
}
func (a *App) modelAllowed(k M, model string) bool {
	list := arr(parse(k["model_whitelist"], nil))
	if len(list) == 0 {
		return true
	}
	for _, v := range list {
		if strings.EqualFold(a.resolveModel(str(v)), model) {
			return true
		}
	}
	return false
}
func (a *App) resolveModel(s string) string {
	s = strings.TrimSpace(s)
	s = modelSuffix.ReplaceAllString(s, "")
	for alias, v := range obj(a.configValue("aliases")) {
		if strings.EqualFold(alias, s) {
			return str(v)
		}
	}
	if s == "" {
		return "GLM-5.3-Flash"
	}
	return s
}
func validLevel(s string) bool { return s == "low" || s == "high" || s == "max" }
func effort(b M) string {
	raw := first(b["reasoning_effort"], b["thinking_level"], b["reasoningLevel"], obj(b["output_config"])["effort"])
	if raw == nil {
		if s, ok := b["reasoning"].(string); ok {
			raw = s
		} else {
			raw = obj(b["reasoning"])["effort"]
		}
	}
	if raw == nil {
		if s, ok := b["thinking"].(string); ok {
			raw = s
		} else {
			t := obj(b["thinking"])
			switch str(t["type"]) {
			case "disabled":
				return "low"
			case "enabled", "adaptive":
				n := num(t["budget_tokens"])
				if n >= 24000 {
					return "max"
				}
				if n >= 8000 || n == 0 {
					return "high"
				}
				return "low"
			}
		}
	}
	switch lower(raw) {
	case "low", "minimal", "off", "disabled", "none":
		return "low"
	case "max", "xhigh":
		return "max"
	case "high", "medium", "default", "enabled":
		return "high"
	}
	return ""
}
func (a *App) effectiveEffort(b, k M) string {
	if v := effort(b); v != "" {
		return v
	}
	if v := str(k["default_level"]); validLevel(v) {
		return v
	}
	v := str(a.configValue("default_reasoning_level"))
	if validLevel(v) {
		return v
	}
	return ""
}
