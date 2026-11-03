package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

func (a *App) AdminHandler() http.Handler {
	files := http.FileServer(http.FS(a.web))
	return a.guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.health(w, r, "web") {
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			files.ServeHTTP(w, r)
			return
		}
		b := M{}
		if r.Method == "POST" || r.Method == "PUT" || r.Method == "PATCH" {
			var e error
			b, e = readBody(w, r)
			if e != nil {
				f := e.(*Failure)
				writeJSON(w, f.Status, M{"ok": false, "error": f.Message})
				return
			}
		}
		if r.URL.Path == "/api/auth/login" && r.Method == "POST" {
			if !a.password(b["password"]) {
				a.audit("anon", "login.fail", "", a.ip(r))
				writeJSON(w, 401, M{"ok": false, "error": "密码错误"})
				return
			}
			token := random(24)
			a.mu.Lock()
			for k, v := range a.sessions {
				if v < nowMS() {
					delete(a.sessions, k)
				}
			}
			a.sessions[token] = nowMS() + 7*86400000
			a.mu.Unlock()
			a.audit("admin", "login.ok", "", a.ip(r))
			writeJSON(w, 200, M{"ok": true, "token": token})
			return
		}
		if !a.adminAuth(r) {
			writeJSON(w, 401, M{"ok": false, "error": "未登录或会话过期"})
			return
		}
		result, status, e := a.adminRoute(r, b)
		if e != nil {
			a.log.Printf("admin %s: %v", r.URL.Path, e)
			writeJSON(w, 502, M{"ok": false, "error": e.Error()})
			return
		}
		if r.Method != "GET" && status < 400 {
			a.audit("admin", r.Method+" "+r.URL.Path, M{"ok": true}, a.ip(r))
		}
		writeJSON(w, status, result)
	}))
}
func limitQuery(r *http.Request, d, maxN int) int {
	n := int(integer(r.URL.Query().Get("limit")))
	if n <= 0 {
		n = d
	}
	return min(maxN, n)
}
func (a *App) adminRoute(r *http.Request, b M) (any, int, error) {
	p, method := r.URL.Path, r.Method
	ctx := r.Context()
	ok := M{"ok": true}
	wrap := func(v any, e error) (any, int, error) { return v, 200, e }
	cfg := a.config()
	switch method + " " + p {
	case "POST /api/auth/logout":
		a.mu.Lock()
		delete(a.sessions, bearer(r))
		a.mu.Unlock()
		return ok, 200, nil
	case "GET /api/runtime-info":
		return M{"web_port": a.webPort, "gateway_port": a.gatewayPort, "host": cfg["host"], "app_version": cfg["app_version"], "inference_driver": "http", "backend": "go", "poll_interval_sec": cfg["poll_interval_sec"], "claim_auto_min": cfg["claim_auto_min"]}, 200, nil
	case "GET /api/accounts":
		accounts := arr(a.summary()["accounts"])
		a.addPlatformNames(ctx, accounts)
		return accounts, 200, nil
	case "POST /api/accounts/import-local":
		v, e := a.importLocal(ctx, b)
		return wrap(v, e)
	case "POST /api/accounts/import-cookie":
		v, e := a.importCookie(ctx, b, nil)
		return wrap(v, e)
	case "POST /api/accounts/oauth/start":
		provider := str(fallback(b["provider"], "bigmodel"))
		if provider != "bigmodel" && provider != "zai" {
			return M{"ok": false, "error": "provider 须为 bigmodel|zai"}, 400, nil
		}
		if truth(b["account_id"]) {
			target := a.account(str(b["account_id"]))
			if target == nil || str(target["family"]) != provider {
				return M{"ok": false, "error": "账号不存在或授权平台不匹配"}, 400, nil
			}
		}
		v, e := a.startOAuth(ctx, provider, str(b["account_id"]), true)
		return wrap(v, e)
	case "GET /api/accounts/oauth/pending":
		return a.pendingFlows(), 200, nil
	case "GET /api/accounts/oauth/watch":
		notified := []any{}
		for _, v := range a.rows("SELECT * FROM oauth_flows WHERE status IN ('done','failed','expired') AND noticed_at IS NULL AND updated_at>=? ORDER BY updated_at DESC LIMIT 20", nowMS()-15*60000) {
			notified = append(notified, publicFlow(obj(v)))
		}
		return M{"pending": a.pendingFlows(), "notified": notified}, 200, nil
	case "POST /api/accounts/oauth/ack":
		ids := arr(b["flow_ids"])
		n := 0
		for _, v := range ids {
			if n >= 50 {
				break
			}
			if id, ok := v.(string); ok && id != "" {
				_ = a.exec("UPDATE oauth_flows SET noticed_at=? WHERE flow_id=?", nowMS(), id)
				n++
			}
		}
		return M{"ok": true, "acked": n}, 200, nil
	case "GET /api/dashboard":
		busy := a.row("SELECT COUNT(*) total,COALESCE(SUM(CASE WHEN status='busy' THEN 1 ELSE 0 END),0) busy FROM usage_log WHERE ts>?", nowMS()-86400000)
		summary := a.summary()
		a.addPlatformNames(ctx, arr(summary["accounts"]))
		return M{"summary": summary, "groups": a.groups(), "busy_24h": busy, "default_chain": a.defaultChain()}, 200, nil
	case "GET /api/claims/discover":
		return a.discover(ctx, r.URL.Query().Get("account_id")), 200, nil
	case "GET /api/claims/verification":
		return wrap(a.claimVerification(ctx, r.URL.Query().Get("account_id")))
	case "POST /api/claims/claim":
		if !truth(b["account_id"]) || !truth(b["plan_id"]) {
			return M{"ok": false, "error": "缺少 account_id / plan_id"}, 400, nil
		}
		if e := validateClaimVerification(b); e != nil {
			return M{"ok": false, "error": e.Error()}, 400, nil
		}
		v, e := a.claim(ctx, str(b["account_id"]), str(b["plan_id"]), "manual", b)
		return wrap(v, e)
	case "POST /api/claims/auto-run":
		v, e := a.autoClaim(ctx, truth(b["dry_run"]))
		return wrap(v, e)
	case "GET /api/claims/history":
		return a.rows("SELECT c.*,a.label account_label FROM claim_log c LEFT JOIN accounts a ON a.id=c.account_id ORDER BY c.ts DESC LIMIT ?", limitQuery(r, 100, 500)), 200, nil
	case "GET /api/usage/stats":
		return a.usageStats(r), 200, nil
	case "GET /api/usage/logs":
		if r.URL.Query().Has("page") {
			return a.pagedLogs(r, "logs")
		}
		rows := a.rows("SELECT u.*,k.name key_name,a.label account_label FROM usage_log u LEFT JOIN keys k ON k.id=u.key_id LEFT JOIN accounts a ON a.id=u.account_id ORDER BY u.ts DESC LIMIT ?", limitQuery(r, 100, 500))
		for _, v := range rows {
			m := obj(v)
			m["timings"] = parse(m["timings"], nil)
			if str(m["error_message"]) != "" {
				m["error_message"] = conciseFailureMessage(&Failure{Code: str(m["error_code"]), Message: str(m["error_message"])})
			}
		}
		return rows, 200, nil
	case "GET /api/performance":
		return a.performance(limitQuery(r, 100, 1000)), 200, nil
	case "GET /api/schedule-logs":
		if r.URL.Query().Has("page") {
			return a.pagedLogs(r, "sched")
		}
		return a.rows("SELECT * FROM schedule_log ORDER BY ts DESC LIMIT ?", limitQuery(r, 50, 200)), 200, nil
	case "GET /api/keys":
		rows := a.rows("SELECT * FROM keys ORDER BY created_at")
		for _, v := range rows {
			m := obj(v)
			delete(m, "key_hash")
			for _, k := range []string{"ip_whitelist", "model_whitelist", "group_order"} {
				var d any
				if k != "group_order" {
					d = []any{}
				}
				m[k] = parse(m[k], d)
			}
		}
		return rows, 200, nil
	case "POST /api/keys":
		if !truth(b["name"]) {
			return M{"ok": false, "error": "缺少 name"}, 400, nil
		}
		v, e := a.createKey(b)
		return wrap(v, e)
	case "GET /api/groups":
		return a.groups(), 200, nil
	case "POST /api/groups":
		if !truth(b["name"]) {
			return M{"ok": false, "error": "缺少 name"}, 400, nil
		}
		id := "g_" + random(4)
		enabled := 1
		if b["enabled"] == false {
			enabled = 0
		}
		e := a.exec("INSERT INTO groups(id,name,role,family_filter,policy,urgent_window_min,enabled,created_at) VALUES(?,?,?,?,?,?,?,?)", id, b["name"], fallback(b["role"], "primary"), nullable(b["family_filter"]), fallback(b["policy"], "balanced"), fallback(b["urgent_window_min"], 180), enabled, nowMS())
		return M{"ok": e == nil, "id": id}, 200, e
	case "GET /api/settings":
		out := M{}
		for _, k := range []string{"poll_interval_sec", "poll_jitter_sec", "poll_concurrency", "claim_auto_enabled", "claim_auto_min", "default_reasoning_level", "http", "outbound_proxy", "inference_driver", "app_version", "aliases", "retry", "web_port", "gateway_port"} {
			out[k] = cfg[k]
		}
		over := obj(a.setting("config_overrides", M{}))
		delete(over, "notify_webhook")
		out["overrides"] = over
		return out, 200, nil
	case "GET /api/settings/captcha/runtime":
		options := obj(obj(cfg["http"])["captcha"])
		for _, key := range []string{"node_path", "jsdom_path"} {
			if r.URL.Query().Has(key) {
				value := r.URL.Query().Get(key)
				if len(value) > 4096 || validateCaptchaOptions(M{key: value}) != nil {
					return M{"ok": false, "error": "Node.js 或 jsdom 路径无效"}, 400, nil
				}
				options[key] = value
			}
		}
		return a.captchaRuntimeStatus(ctx, options), 200, nil
	case "GET /api/settings/proxy/mihomo":
		if r.URL.Query().Has("path") {
			path := r.URL.Query().Get("path")
			if len(path) > 4096 || validateOutboundProxyFields(M{"mihomo_path": path}) != nil {
				return M{"ok": false, "error": "Mihomo 可执行文件路径无效"}, 400, nil
			}
			return a.mihomoStatusForPath(path), 200, nil
		}
		return a.mihomoStatus(), 200, nil
	case "POST /api/settings/proxy/nodes":
		if e := validateOutboundProxyFields(b["outbound_proxy"]); e != nil {
			return M{"ok": false, "error": e.Error()}, 400, nil
		}
		options := merge(obj(cfg["outbound_proxy"]), obj(b["outbound_proxy"]))
		result, e := a.outboundProxyNodes(ctx, options)
		if e != nil {
			return M{"ok": false, "error": asProxyFailure(e).Message}, 400, nil
		}
		return result, 200, nil
	case "POST /api/settings/proxy/mihomo/install":
		result, err := a.installMihomo(ctx)
		if err != nil {
			return M{"ok": false, "error": err.Error()}, 400, nil
		}
		return result, 200, nil
	case "POST /api/settings/proxy/test":
		if e := validateOutboundProxy(b["outbound_proxy"]); e != nil {
			return M{"ok": false, "error": e.Error()}, 400, nil
		}
		result, e := a.testOutboundProxies(ctx, obj(b["outbound_proxy"]))
		if e != nil {
			return M{"ok": false, "error": asProxyFailure(e).Message}, 502, nil
		}
		return result, 200, nil
	case "PUT /api/settings":
		if e := validateSettings(b); e != nil {
			return M{"ok": false, "error": e.Error()}, 400, nil
		}
		allowed := map[string]bool{}
		for _, k := range strings.Fields("poll_interval_sec poll_jitter_sec poll_concurrency claim_auto_enabled claim_auto_min default_reasoning_level app_version aliases retry inference_driver http outbound_proxy") {
			allowed[k] = true
		}
		a.configMu.Lock()
		over := obj(a.setting("config_overrides", M{}))
		delete(over, "notify_webhook")
		next := clone(a.cfg)
		for k, v := range b {
			if !allowed[k] {
				continue
			}
			if k == "retry" || k == "http" || k == "outbound_proxy" {
				v = deepMerge(obj(next[k]), obj(v), false)
			}
			over[k] = v
			next[k] = v
		}
		if e := validateOutboundProxy(next["outbound_proxy"]); e != nil {
			a.configMu.Unlock()
			return M{"ok": false, "error": e.Error()}, 400, nil
		}
		proxyChanged := js(next["outbound_proxy"]) != js(a.cfg["outbound_proxy"])
		e := a.setSetting("config_overrides", over)
		if e == nil {
			a.cfg = next
		}
		a.configMu.Unlock()
		if proxyChanged && e == nil {
			a.resetOutboundProxies()
		}
		return ok, 200, e
	case "GET /api/audit":
		if r.URL.Query().Has("page") {
			return a.pagedLogs(r, "audit")
		}
		return a.rows("SELECT * FROM audit_log ORDER BY ts DESC LIMIT ?", limitQuery(r, 100, 300)), 200, nil
	}
	if strings.HasPrefix(p, "/api/accounts/oauth/status/") && method == "GET" {
		row := a.row("SELECT * FROM oauth_flows WHERE flow_id=?", strings.TrimPrefix(p, "/api/accounts/oauth/status/"))
		if row == nil {
			return M{"ok": false, "error": "授权流程不存在"}, 404, nil
		}
		return publicFlow(row), 200, nil
	}
	if strings.HasPrefix(p, "/api/accounts/oauth/cancel/") && method == "POST" {
		id := strings.TrimPrefix(p, "/api/accounts/oauth/cancel/")
		res, e := a.db.Exec("UPDATE oauth_flows SET status='cancelled',error='已取消',updated_at=? WHERE flow_id=? AND status='pending'", nowMS(), id)
		if e != nil {
			return nil, 500, e
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			if r := a.row("SELECT status FROM oauth_flows WHERE flow_id=?", id); str(r["status"]) == "exchanging" {
				return M{"ok": false, "error": "正在处理授权结果，无法取消"}, 409, nil
			}
		}
		return ok, 200, nil
	}
	if strings.HasPrefix(p, "/api/accounts/") {
		parts := strings.Split(strings.TrimPrefix(p, "/api/accounts/"), "/")
		id := parts[0]
		account := a.account(id)
		if account == nil {
			return M{"ok": false, "error": "账号不存在"}, 404, nil
		}
		if len(parts) == 1 {
			switch method {
			case "DELETE":
				e := a.exec("DELETE FROM accounts WHERE id=?", id)
				if e != nil {
					return nil, 500, e
				}
				_ = a.exec("DELETE FROM quota_snapshots WHERE account_id=?", id)
				a.mu.Lock()
				delete(a.pollDue, id)
				delete(a.jars, id)
				delete(a.states, id)
				delete(a.profiles, id)
				delete(a.reauth, id)
				a.notifyAvailableLocked()
				a.mu.Unlock()
				_ = os.Remove(filepath.Join(a.data, "cookies", id+".json"))
				return ok, 200, nil
			case "PATCH":
				fields := M{}
				for _, k := range []string{"label", "group_id", "proxy_url"} {
					if v, exists := b[k]; exists {
						fields[k] = v
					}
				}
				if v, exists := fields["label"]; exists {
					s := []rune(str(v))
					fields["label"] = string(s[:min(len(s), 60)])
				}
				proxyChanged := false
				if v, exists := fields["proxy_url"]; exists {
					p := strings.TrimSpace(str(v))
					if p != "" && !validProxy(p) {
						return M{"ok": false, "error": "proxy_url 仅支持 http(s)://host:port"}, 400, nil
					}
					fields["proxy_url"] = nullable(p)
					proxyChanged = p != str(account["proxy_url"])
				}
				if truth(fields["group_id"]) && a.row("SELECT id FROM groups WHERE id=?", fields["group_id"]) == nil {
					return M{"ok": false, "error": "分组不存在"}, 400, nil
				}
				e := a.updateAccount(id, fields)
				return M{"ok": e == nil, "proxy_updated": proxyChanged}, 200, e
			}
		}
		if len(parts) == 2 && method == "POST" {
			switch parts[1] {
			case "verify":
				v, e := a.verify(ctx, account)
				return wrap(v, e)
			case "reauth":
				v, e := a.startOAuth(ctx, str(account["family"]), id, true)
				return wrap(v, e)
			case "refresh-cookie":
				v, e := a.importCookie(ctx, b, account)
				return wrap(v, e)
			}
		}
	}
	if strings.HasPrefix(p, "/api/keys/") {
		id := strings.TrimPrefix(p, "/api/keys/")
		switch method {
		case "DELETE":
			return ok, 200, a.exec("DELETE FROM keys WHERE id=?", id)
		case "PATCH":
			fields := clone(b)
			if v, exists := fields["default_level"]; exists && !validLevel(str(v)) {
				fields["default_level"] = nil
			}
			return ok, 200, a.update("keys", "id", id, fields, "name quota_limit expires_at ip_whitelist model_whitelist group_order sticky_minutes default_level enabled")
		}
	}
	if strings.HasPrefix(p, "/api/groups/") {
		id := strings.TrimPrefix(p, "/api/groups/")
		switch method {
		case "DELETE":
			n := integer(a.row("SELECT COUNT(*) n FROM accounts WHERE group_id=?", id)["n"])
			if n > 0 {
				return M{"ok": false, "error": fmt.Sprintf("分组下还有 %d 个账号，先移出再删除", n)}, 200, nil
			}
			return ok, 200, a.exec("DELETE FROM groups WHERE id=?", id)
		case "PATCH":
			return ok, 200, a.update("groups", "id", id, b, "name role family_filter policy urgent_window_min enabled")
		}
	}
	return M{"ok": false, "error": "接口不存在"}, 404, nil
}
func (a *App) pendingFlows() []any {
	out := []any{}
	for _, v := range a.rows("SELECT * FROM oauth_flows WHERE status='pending' AND expires_at>? ORDER BY created_at DESC", nowMS()) {
		out = append(out, publicFlow(obj(v)))
	}
	return out
}

var versionPattern = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

func validateSettings(b M) error {
	if value, present := b["outbound_proxy"]; present {
		if err := validateOutboundProxyFields(value); err != nil {
			return err
		}
	}
	if v, ok := b["app_version"]; ok && !versionPattern.MatchString(str(v)) {
		return fmt.Errorf("额度与领卡 API 版本须为三段数字，例如 3.16.2")
	}
	if v, ok := b["inference_driver"]; ok && str(v) != "http" {
		return fmt.Errorf("网关仅支持 HTTP 直连")
	}
	if v, ok := b["http"]; ok {
		h, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("http 必须为配置对象")
		}
		for _, k := range []string{"first_token_timeout_ms", "idle_timeout_ms", "total_timeout_ms"} {
			if v, ok := h[k]; ok {
				_, isFloat := v.(float64)
				_, isJSONNumber := v.(json.Number)
				n, isNumber := num(v), isFloat || isJSONNumber
				minimum := float64(1)
				if k == "total_timeout_ms" {
					minimum = 0
				}
				if !isNumber || n < minimum {
					return fmt.Errorf("%s 超时设置无效", k)
				}
			}
		}
		if v, ok := h["client_version"]; ok && !versionPattern.MatchString(str(v)) {
			return fmt.Errorf("推理 API 版本须为三段数字，例如 0.17.0")
		}
		if v, ok := h["captcha"]; ok {
			if err := validateCaptchaOptions(v); err != nil {
				return err
			}
		}
	}
	for _, k := range []string{"poll_interval_sec", "poll_concurrency", "claim_auto_min"} {
		if v, ok := b[k]; ok && num(v) < 1 {
			return fmt.Errorf("%s 必须大于等于 1", k)
		}
	}
	if v, ok := b["default_reasoning_level"]; ok && !validLevel(str(v)) {
		return fmt.Errorf("思考档位须为 low/high/max")
	}
	for _, k := range []string{"aliases", "retry"} {
		if v, ok := b[k]; ok {
			if _, ok := v.(map[string]any); !ok {
				return fmt.Errorf("%s 必须为配置对象", k)
			}
		}
	}
	return nil
}
func (a *App) usageStats(r *http.Request) M {
	hours := num(r.URL.Query().Get("hours"))
	if hours <= 0 {
		hours = 24
	}
	hours = min(720, hours)
	until := nowMS()
	since := until - int64(hours*3600000)
	_, offset := time.UnixMilli(until).In(time.Local).Zone()
	unit, format := "hour", "%Y-%m-%dT%H:00:00"
	if hours > 24 {
		unit, format = "day", "%Y-%m-%d"
	}
	byHour := a.rows("SELECT strftime('"+format+"',ts/1000,'unixepoch','localtime') bucket,COUNT(*) reqs,COALESCE(SUM(CASE WHEN status='success' THEN 1 ELSE 0 END),0) ok,COALESCE(SUM(tokens_in),0) tin,COALESCE(SUM(tokens_out),0) tout,COALESCE(SUM(tokens_reasoning),0) treason,AVG(ttft_ms) avg_ttft FROM usage_log WHERE ts>=? GROUP BY bucket ORDER BY bucket", since)
	ttfts := []float64{}
	for _, v := range a.rows("SELECT ttft_ms FROM usage_log WHERE ts>=? AND status='success' AND ttft_ms IS NOT NULL", since) {
		ttfts = append(ttfts, num(obj(v)["ttft_ms"]))
	}
	sort.Float64s(ttfts)
	var ttft any
	if len(ttfts) > 0 {
		ttft = M{"p50": ttfts[len(ttfts)/2], "p95": ttfts[min(len(ttfts)-1, int(float64(len(ttfts))*0.95))]}
	}
	return M{"hours": hours, "time_unit": unit, "range_start": since, "range_end": until, "utc_offset_minutes": offset / 60, "byHour": byHour, "byModel": a.rows("SELECT model,COUNT(*) reqs,COALESCE(SUM(tokens_in),0) tin,COALESCE(SUM(tokens_out),0) tout,COALESCE(SUM(CASE WHEN status='success' THEN 1 ELSE 0 END),0) ok FROM usage_log WHERE ts>=? GROUP BY model ORDER BY reqs DESC", since), "byKey": a.rows("SELECT k.name,u.key_id,COUNT(*) reqs,COALESCE(SUM(COALESCE(u.tokens_in,0)+COALESCE(u.tokens_out,0)),0) tokens FROM usage_log u LEFT JOIN keys k ON k.id=u.key_id WHERE u.ts>=? GROUP BY u.key_id ORDER BY reqs DESC", since), "byAccount": a.rows("SELECT a.label,u.account_id,COUNT(*) reqs,SUM(CASE WHEN u.status='success' THEN 1 ELSE 0 END) ok,SUM(CASE WHEN u.status='busy' THEN 1 ELSE 0 END) busy,COALESCE(SUM(COALESCE(u.tokens_in,0)+COALESCE(u.tokens_out,0)),0) tokens FROM usage_log u LEFT JOIN accounts a ON a.id=u.account_id WHERE u.ts>=? GROUP BY u.account_id ORDER BY reqs DESC", since), "byStatus": a.rows("SELECT status,COUNT(*) n FROM usage_log WHERE ts>=? GROUP BY status", since), "ttft": ttft}
}
func (a *App) performance(limit int) M {
	rows := a.rows("SELECT timings FROM usage_log WHERE timings IS NOT NULL ORDER BY id DESC LIMIT ?", limit)
	metrics := M{}
	for _, stage := range strings.Fields("queue_ms retry_ms ttft_ms first_reasoning_ms first_text_ms generation_ms total_ms") {
		xs := []float64{}
		for _, v := range rows {
			m := obj(parse(obj(v)["timings"], nil))
			if m[stage] != nil {
				xs = append(xs, num(m[stage]))
			}
		}
		sort.Float64s(xs)
		m := M{"samples": len(xs), "avg": nil, "p50": nil, "p95": nil}
		if len(xs) > 0 {
			sum := float64(0)
			for _, v := range xs {
				sum += v
			}
			m["avg"], m["p50"], m["p95"] = int(sum/float64(len(xs))+0.5), xs[len(xs)/2], xs[min(len(xs)-1, int(float64(len(xs))*0.95))]
		}
		metrics[stage] = m
	}
	return M{"requests": len(rows), "metrics": metrics, "note": "首字包含思考或实际工具调用；未观测到的阶段为空。generation_ms 是该 HTTP 回合首末输出事件间隔，不能单独视为模型纯生成耗时。"}
}
