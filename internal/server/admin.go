package server

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	accountdata "zcode2api/internal/server/account"
	captchasdk "zcode2api/internal/server/captcha"
	settings "zcode2api/internal/server/config"
	"zcode2api/internal/server/fault"
	wire "zcode2api/internal/server/protocol"
	routing "zcode2api/internal/server/proxy"
	"zcode2api/internal/server/shared"
	"zcode2api/internal/server/storage"
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
		keyAPI := r.URL.Path == "/api/keys" || strings.HasPrefix(r.URL.Path, "/api/keys/")
		if keyAPI || strings.HasPrefix(r.URL.Path, "/api/auth/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		login := r.URL.Path == "/api/auth/login" && r.Method == "POST"
		if !login {
			var token string
			if r.Method == "POST" && (keyAPI || r.URL.Path == "/api/settings/update/install") {
				token = adminBearer(r)
			} else {
				token = adminSessionToken(r)
			}
			authorized, err := a.adminAuth(r.Context(), token)
			if err != nil {
				a.log.Printf("check admin session: %v", err)
				writeJSON(w, http.StatusServiceUnavailable, shared.M{"ok": false, "error": "暂时无法验证登录状态，请重试"})
				return
			}
			if !authorized {
				writeJSON(w, 401, shared.M{"ok": false, "error": "未登录或会话过期"})
				return
			}
		}
		b := shared.M{}
		if r.Method == "POST" || r.Method == "PUT" || r.Method == "PATCH" {
			var e error
			b, e = readBody(w, r)
			if e != nil {
				f := e.(*fault.Failure)
				writeJSON(w, f.Status, shared.M{"ok": false, "error": f.Message})
				return
			}
		}
		if login {
			if !a.password(b["password"]) {
				writeJSON(w, 401, shared.M{"ok": false, "error": "密码错误"})
				return
			}
			token, expiresAt, err := a.newAdminSession(r.Context())
			if err != nil {
				a.log.Printf("create admin session: %v", err)
				writeJSON(w, http.StatusServiceUnavailable, shared.M{"ok": false, "error": "暂时无法保存登录状态，请重试"})
				return
			}
			writeJSON(w, 200, shared.M{"ok": true, "token": token, "expires_at": expiresAt})
			return
		}
		if r.URL.Path == "/api/claims/discover" || r.URL.Path == "/api/claims/verification" || r.URL.Path == "/api/accounts/export" || r.URL.Path == "/api/accounts/import" || strings.HasPrefix(r.URL.Path, "/api/settings/update") {
			w.Header().Set("Cache-Control", "no-store")
		}
		result, status, e := a.adminRoute(r, b)
		if e != nil {
			a.log.Printf("admin %s: %v", r.URL.Path, e)
			status, body := http.StatusBadGateway, shared.M{"ok": false, "error": e.Error()}
			var failure *fault.Failure
			if errors.As(e, &failure) {
				fault.Classify(failure)
				body["error"], body["code"], body["failure_kind"] = fault.ConciseFailureMessage(failure), failure.Code, failure.Kind
				body["error_reason"], body["error_detail"] = fault.FailureReason(failure), fault.FailureDetail(failure)
				if failure.Kind == "rate_limit" {
					status = http.StatusTooManyRequests
				}
				if failure.RetryAfter > 0 {
					body["retry_after_ms"] = failure.RetryAfter.Milliseconds()
					w.Header().Set("Retry-After", fmt.Sprint(max(1, int(math.Ceil(failure.RetryAfter.Seconds())))))
				}
			}
			writeJSON(w, status, body)
			return
		}
		writeJSON(w, status, result)
	}))
}

func limitQuery(r *http.Request, d, maxN int) int {
	n := int(shared.Integer(r.URL.Query().Get("limit")))
	if n <= 0 {
		n = d
	}
	return min(maxN, n)
}

func adminError(status int, message string) (any, int, error) {
	return shared.M{"ok": false, "error": message}, status, nil
}

func adminWriteResult(value any, err error) (any, int, error) {
	var failure *fault.Failure
	if errors.As(err, &failure) {
		return adminError(failure.Status, failure.Message)
	}
	return value, http.StatusOK, err
}

func (a *App) adminRoute(r *http.Request, b shared.M) (any, int, error) {
	p, method := r.URL.Path, r.Method
	ctx := r.Context()
	ok := shared.M{"ok": true}
	wrap := func(v any, e error) (any, int, error) { return v, 200, e }
	switch method + " " + p {
	case "POST /api/auth/logout":
		_, err := a.db.ExecContext(ctx, "DELETE FROM admin_sessions WHERE token_hash=?", shared.Hash(adminSessionToken(r)))
		return wrap(ok, err)
	case "GET /api/runtime-info":
		cfg := a.config("host", "poll_interval_sec")
		return shared.M{"web_port": a.webPort, "gateway_port": a.gatewayPort, "host": cfg["host"], "app_version": settings.ZCodeFingerprintVersion, "version": a.version, "inference_driver": "http", "backend": "go", "poll_interval_sec": cfg["poll_interval_sec"], "account_operation_concurrency": shared.AccountOperationConcurrency}, 200, nil
	case "GET /api/accounts":
		if r.URL.Query().Get("view") == "groups" {
			return wrap(storage.Query(a.db, "SELECT id,label,customer_id,family,group_id,status FROM accounts ORDER BY created_at,id"))
		}
		return shared.Arr(a.summary(ctx)["accounts"]), 200, nil
	case "POST /api/accounts/import-local":
		return wrap(a.importLocal(ctx, b))
	case "POST /api/accounts/export":
		return a.exportAccounts(b)
	case "POST /api/accounts/import":
		return a.importAccounts(ctx, b)
	case "POST /api/accounts/oauth/start":
		provider := shared.Str(shared.Fallback(b["provider"], "bigmodel"))
		if !accountdata.ValidLoginProvider(provider) {
			return adminError(400, "provider 须为 zai_cn、zai 或 bigmodel")
		}
		if shared.Truth(b["account_id"]) {
			target := a.account(shared.Str(b["account_id"]))
			if target == nil || a.accountLoginProvider(target) != provider {
				return adminError(400, "账号不存在或授权平台不匹配")
			}
		}
		return wrap(a.startOAuth(ctx, provider, shared.Str(b["account_id"]), true))
	case "GET /api/accounts/oauth/pending":
		return a.pendingFlows(), 200, nil
	case "GET /api/accounts/oauth/watch":
		notified := []any{}
		for _, v := range a.rows("SELECT * FROM oauth_flows WHERE status IN ('done','failed','expired') AND noticed_at IS NULL AND updated_at>=? ORDER BY updated_at DESC LIMIT 20", shared.NowMS()-15*60000) {
			notified = append(notified, publicFlow(shared.Obj(v)))
		}
		return shared.M{"pending": a.pendingFlows(), "notified": notified}, 200, nil
	case "POST /api/accounts/oauth/ack":
		ids := shared.Arr(b["flow_ids"])
		n := 0
		for _, v := range ids {
			if n >= 50 {
				break
			}
			if id, ok := v.(string); ok && id != "" {
				_ = a.exec("UPDATE oauth_flows SET noticed_at=? WHERE flow_id=?", shared.NowMS(), id)
				n++
			}
		}
		return shared.M{"ok": true, "acked": n}, 200, nil
	case "GET /api/dashboard":
		hours := int64(0)
		if raw := r.URL.Query().Get("hours"); raw != "" && raw != "all" {
			var err error
			hours, err = logInteger(r, "hours", 0)
			if err != nil || (hours != 24 && hours != 168 && hours != 720) {
				return adminError(400, "统计时间范围无效")
			}
		}
		until := shared.NowMS()
		_, offset := time.UnixMilli(until).In(time.Local).Zone()
		cacheTrend := a.rows("SELECT strftime('%Y-%m-%dT%H:00:00',ts/1000,'unixepoch','localtime') bucket,COUNT(*) reqs,COALESCE(SUM(cache_read),0) cached_tokens,COALESCE(SUM(tokens_in),0) tin,COALESCE(SUM(tokens_out),0) tout FROM usage_log WHERE ts>=? AND ts<=? GROUP BY bucket ORDER BY bucket", until-86400000, until)
		cache := shared.M{"cached_tokens": float64(0), "input_tokens": float64(0)}
		for _, value := range cacheTrend {
			row := shared.Obj(value)
			cache["cached_tokens"] = shared.Num(cache["cached_tokens"]) + shared.Num(row["cached_tokens"])
			cache["input_tokens"] = shared.Num(cache["input_tokens"]) + shared.Num(row["tin"])
		}
		usageColumns := "COALESCE(SUM(tokens_in),0) input_tokens,COALESCE(SUM(tokens_out),0) output_tokens,COALESCE(SUM(COALESCE(tokens_in,0)+COALESCE(tokens_out,0)),0) total_tokens"
		usage := a.row("SELECT "+usageColumns+",COALESCE(SUM(ts>?),0) busy_total,COALESCE(SUM(ts>? AND status='busy'),0) busy_count FROM usage_log", until-86400000, until-86400000)
		busy := shared.M{"total": usage["busy_total"], "busy": usage["busy_count"]}
		delete(usage, "busy_total")
		delete(usage, "busy_count")
		period := usage
		if hours > 0 {
			period = a.row("SELECT "+usageColumns+" FROM usage_log WHERE ts>=? AND ts<=?", until-hours*3600000, until)
		}
		summary := a.summary(ctx)
		groups := a.groups()
		return shared.M{"summary": summary, "groups": groups, "busy_24h": busy, "cache_24h": cache, "cache_trend_24h": shared.M{"hours": 24, "time_unit": "hour", "range_start": until - 86400000, "range_end": until, "utc_offset_minutes": offset / 60, "byHour": cacheTrend}, "usage_total": usage, "usage_period": shared.Merge(period, shared.M{"hours": hours}), "default_chain": defaultGroupChain(groups)}, 200, nil
	case "GET /api/claims/discover":
		return wrap(a.discover(ctx, r.URL.Query().Get("account_id"), r.URL.Query().Get("scope") == "daily"))
	case "GET /api/claims/verification":
		query := r.URL.Query()
		return wrap(a.claimVerification(ctx, query.Get("account_id"), query.Get("plan_id"), query.Get("config_only") == "1"))
	case "POST /api/claims/verification/cancel":
		a.cancelClaimVerification(shared.Str(b["verification_session"]))
		return ok, 200, nil
	case "GET /api/claims/schedule":
		return a.dailyClaimSchedule(), 200, nil
	case "POST /api/claims/claim":
		if !shared.Truth(b["account_id"]) || !shared.Truth(b["plan_id"]) {
			return adminError(400, "缺少 account_id / plan_id")
		}
		if e := captchasdk.ValidateClaimVerification(b); e != nil {
			return adminError(400, e.Error())
		}
		v, e := a.claim(ctx, shared.Str(b["account_id"]), shared.Str(b["plan_id"]), "manual", b)
		// Preserve the claim result and its verification details on failure.
		if v != nil {
			return v, 200, nil
		}
		return wrap(v, e)
	case "POST /api/claims/auto-run":
		return wrap(a.autoClaim(ctx, shared.Truth(b["dry_run"])))
	case "GET /api/claims/history":
		rows := a.rows("SELECT c.*,a.label account_label FROM claim_log c LEFT JOIN accounts a ON a.id=c.account_id ORDER BY c.ts DESC LIMIT ?", limitQuery(r, 100, 500))
		for _, value := range rows {
			row := shared.Obj(value)
			row["error_detail"] = shared.Parse(row["error_detail"], nil)
			if !shared.Truth(row["ok"]) {
				row["error_reason"] = fault.FailureReason(loggedFailure(shared.Str(row["code"]), shared.Str(row["message"]), row["error_detail"]))
			}
		}
		return rows, 200, nil
	case "GET /api/usage/stats":
		return a.usageStats(r), 200, nil
	case "GET /api/usage/logs":
		if r.URL.Query().Has("page") {
			return a.pagedUsageLogs(r)
		}
		rows := a.rows("SELECT u.*,k.name key_name,a.label account_label FROM usage_log u LEFT JOIN keys k ON k.id=u.key_id LEFT JOIN accounts a ON a.id=u.account_id ORDER BY u.ts DESC LIMIT ?", limitQuery(r, 100, 500))
		return publicUsageLogs(rows), 200, nil
	case "GET /api/performance":
		return a.performance(limitQuery(r, 100, 1000)), 200, nil
	case "GET /api/keys":
		return wrap(a.listKeys())
	case "POST /api/keys":
		fields, err := apiKeyConfig(b, true)
		if err != nil {
			return adminError(http.StatusBadRequest, err.Error())
		}
		return adminWriteResult(a.createKey(ctx, fields))
	case "GET /api/groups":
		if r.URL.Query().Get("details") == "1" {
			return wrap(a.groupDetails(ctx))
		}
		return a.groups(), 200, nil
	case "POST /api/groups":
		fields, err := groupConfig(b, true)
		if err != nil {
			return adminError(http.StatusBadRequest, err.Error())
		}
		return wrap(a.createGroup(fields))
	case "GET /api/settings/update":
		return a.updateStatus(), 200, nil
	case "POST /api/settings/update/check":
		if err := a.checkUpdate(ctx); err != nil {
			return adminError(502, err.Error())
		}
		return a.updateStatus(), 200, nil
	case "POST /api/settings/update/install":
		if err := a.startUpdate(shared.Str(b["version"])); err != nil {
			return adminError(409, err.Error())
		}
		return a.updateStatus(), 202, nil
	case "GET /api/settings":
		out := a.config(strings.Fields(settings.EditableSettings + " web_port gateway_port")...)
		over := shared.Obj(a.setting("config_overrides", shared.M{}))
		delete(over, "notify_webhook")
		out["overrides"] = over
		out["zcode_fingerprint_version"] = settings.ZCodeFingerprintVersion
		return out, 200, nil
	case "GET /api/settings/captcha/runtime", "POST /api/settings/captcha/browser/install":
		install := r.Method == "POST"
		downloadBrowser, valid := b["install_browser"].(bool)
		if install && !valid {
			return adminError(400, "须明确选择是否安装 Chromium")
		}
		options := shared.Obj(a.configValue("http", "captcha"))
		for _, key := range []string{"node_path", "jsdom_path", "playwright_path", "browser_path", "browser_backend", "browser_headless", "browser_display"} {
			value, present := b[key]
			if !install {
				present = r.URL.Query().Has(key)
				text := r.URL.Query().Get(key)
				value = text
				if present && key == "browser_headless" {
					if text != "true" && text != "false" {
						return adminError(400, "browser_headless 必须为 true 或 false")
					}
					value = text == "true"
				}
			}
			if present {
				if len(shared.Str(value)) > 4096 || captchasdk.ValidateCaptchaOptions(shared.M{key: value}) != nil {
					return adminError(400, "验证运行时设置无效")
				}
				options[key] = value
			}
		}
		if install {
			return wrap(a.installClaimBrowser(ctx, options, downloadBrowser))
		}
		return a.captchaRuntimeStatus(ctx, options), 200, nil
	case "GET /api/settings/proxy/mihomo":
		if r.URL.Query().Has("path") {
			path := r.URL.Query().Get("path")
			if len(path) > 4096 || routing.ValidateOutboundProxyFields(shared.M{"mihomo_path": path}) != nil {
				return adminError(400, "Mihomo 可执行文件路径无效")
			}
			return a.mihomoStatusForPath(path), 200, nil
		}
		return a.mihomoStatus(), 200, nil
	case "POST /api/settings/proxy/nodes":
		if e := routing.ValidateOutboundProxyFields(b["outbound_proxy"]); e != nil {
			return adminError(400, e.Error())
		}
		options := shared.Merge(shared.Obj(a.configValue("outbound_proxy")), shared.Obj(b["outbound_proxy"]))
		result, e := a.outboundProxyNodes(ctx, options)
		if e != nil {
			return adminError(400, routing.AsProxyFailure(e).Message)
		}
		return result, 200, nil
	case "POST /api/settings/proxy/mihomo/install":
		result, err := a.installMihomo(ctx)
		if err != nil {
			return adminError(400, err.Error())
		}
		return result, 200, nil
	case "POST /api/settings/proxy/test":
		if e := routing.ValidateOutboundProxy(b["outbound_proxy"]); e != nil {
			return adminError(400, e.Error())
		}
		result, e := a.testOutboundProxies(ctx, shared.Obj(b["outbound_proxy"]))
		if e != nil {
			return adminError(502, routing.AsProxyFailure(e).Message)
		}
		return result, 200, nil
	case "PUT /api/settings":
		if e := settings.ValidateSettings(b); e != nil {
			return adminError(400, e.Error())
		}
		a.configMu.Lock()
		over := shared.Obj(a.setting("config_overrides", shared.M{}))
		delete(over, "notify_webhook")
		next := shared.Clone(a.cfg)
		for _, k := range strings.Fields(settings.EditableSettings) {
			v, present := b[k]
			if !present {
				continue
			}
			if k == "retry" || k == "http" || k == "outbound_proxy" {
				v = settings.DeepMerge(shared.Obj(next[k]), shared.Obj(v), false)
			}
			over[k] = v
			next[k] = v
		}
		if _, err := wire.NewSystemTemplate(shared.Str(next["zcode_template_mode"]), shared.Str(next["zcode_subagent_template"])); err != nil {
			a.configMu.Unlock()
			return adminError(400, err.Error())
		}
		if e := routing.ValidateOutboundProxy(next["outbound_proxy"]); e != nil {
			a.configMu.Unlock()
			return adminError(400, e.Error())
		}
		proxyChanged := shared.JSON(next["outbound_proxy"]) != shared.JSON(a.cfg["outbound_proxy"])
		captchaChanged := proxyChanged || shared.JSON(shared.Obj(next["http"])["captcha"]) != shared.JSON(shared.Obj(a.cfg["http"])["captcha"])
		claimScheduleChanged := shared.JSON(next["daily_claim_time"]) != shared.JSON(a.cfg["daily_claim_time"]) || shared.JSON(next["daily_claim_enabled"]) != shared.JSON(a.cfg["daily_claim_enabled"])
		logRetentionChanged := shared.Integer(next["usage_log_retention_days"]) != shared.Integer(a.cfg["usage_log_retention_days"])
		if e := a.setSetting("config_overrides", over); e != nil {
			a.configMu.Unlock()
			return ok, 200, e
		}
		a.cfg = next
		a.configMu.Unlock()
		if b["poll_concurrency"] != nil {
			shared.Wake(a.pollWake)
		}
		if captchaChanged {
			a.resetCaptchaCache()
		}
		if proxyChanged {
			a.resetOutboundProxies()
		}
		if claimScheduleChanged || captchaChanged {
			a.rescheduleClaims()
		}
		if logRetentionChanged {
			shared.Wake(a.usageLogCleanupWake)
		}
		if b["daily_claim_enabled"] != nil || b["daily_claim_time"] != nil {
			return shared.M{"ok": true, "claim_schedule": a.dailyClaimSchedule()}, 200, nil
		}
		return ok, 200, nil
	}
	if strings.HasPrefix(p, "/api/accounts/oauth/status/") && method == "GET" {
		row := a.row("SELECT * FROM oauth_flows WHERE flow_id=?", strings.TrimPrefix(p, "/api/accounts/oauth/status/"))
		if row == nil {
			return adminError(404, "授权流程不存在")
		}
		return publicFlow(row), 200, nil
	}
	if strings.HasPrefix(p, "/api/accounts/oauth/cancel/") && method == "POST" {
		id := strings.TrimPrefix(p, "/api/accounts/oauth/cancel/")
		res, e := a.db.Exec("UPDATE oauth_flows SET status='cancelled',poll_token_enc='',error='已取消',updated_at=? WHERE flow_id=? AND status='pending'", shared.NowMS(), id)
		if e != nil {
			return nil, 500, e
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			if r := a.row("SELECT status FROM oauth_flows WHERE flow_id=?", id); shared.Str(r["status"]) == "exchanging" {
				return adminError(409, "正在处理授权结果，无法取消")
			}
		}
		return ok, 200, nil
	}
	if strings.HasPrefix(p, "/api/accounts/") {
		parts := strings.Split(strings.TrimPrefix(p, "/api/accounts/"), "/")
		id := parts[0]
		account := a.account(id)
		if account == nil {
			return adminError(404, "账号不存在")
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
				fields := shared.M{}
				shared.CopyFields(fields, b, "label", "group_id", "proxy_url")
				if v, exists := fields["label"]; exists {
					s := []rune(shared.Str(v))
					fields["label"] = string(s[:min(len(s), 60)])
				}
				proxyChanged := false
				if v, exists := fields["proxy_url"]; exists {
					p := strings.TrimSpace(shared.Str(v))
					if p != "" && !routing.ValidProxy(p) {
						return adminError(400, "proxy_url 仅支持 http(s)://host:port")
					}
					fields["proxy_url"] = shared.Nullable(p)
					proxyChanged = p != shared.Str(account["proxy_url"])
				}
				var err error
				if _, moves := fields["group_id"]; moves {
					err = a.updateAccountPlacement(ctx, id, fields, b)
				} else {
					err = a.updateAccount(id, fields)
				}
				return adminWriteResult(shared.M{"ok": err == nil, "proxy_updated": proxyChanged}, err)
			}
		}
		if len(parts) == 2 && method == "POST" {
			switch parts[1] {
			case "verify":
				return wrap(a.verify(ctx, account))
			case "reauth":
				return wrap(a.startOAuth(ctx, a.accountLoginProvider(account), id, true))
			}
		}
	}
	if strings.HasPrefix(p, "/api/keys/") {
		id, action, hasAction := strings.Cut(strings.TrimPrefix(p, "/api/keys/"), "/")
		if id == "" {
			return adminError(http.StatusNotFound, "密钥不存在")
		}
		if hasAction {
			if method == "POST" {
				switch action {
				case "secret":
					return a.revealKey(ctx, id)
				case "rotate":
					return a.rotateKey(ctx, id, shared.Str(b["key_tail"]))
				}
			}
			return adminError(http.StatusNotFound, "接口不存在")
		}
		switch method {
		case "DELETE":
			return ok, 200, a.exec("DELETE FROM keys WHERE id=?", id)
		case "PATCH":
			fields, err := apiKeyConfig(b, false)
			if err != nil {
				return adminError(http.StatusBadRequest, err.Error())
			}
			return adminWriteResult(ok, a.patchKey(ctx, id, fields))
		}
	}
	if strings.HasPrefix(p, "/api/groups/") {
		id := strings.TrimPrefix(p, "/api/groups/")
		switch method {
		case "DELETE":
			return a.deleteGroup(ctx, id)
		case "PATCH":
			fields, err := groupConfig(b, false)
			if err != nil {
				return adminError(http.StatusBadRequest, err.Error())
			}
			return a.updateGroup(ctx, id, fields)
		}
	}
	return adminError(404, "接口不存在")
}

func (a *App) pendingFlows() []any {
	out := []any{}
	for _, v := range a.rows("SELECT * FROM oauth_flows WHERE status='pending' AND expires_at>? ORDER BY created_at DESC", shared.NowMS()) {
		out = append(out, publicFlow(shared.Obj(v)))
	}
	return out
}

func percentiles(values []float64) shared.M {
	if len(values) == 0 {
		return nil
	}
	sort.Float64s(values)
	return shared.M{"p50": values[len(values)/2], "p95": values[min(len(values)-1, int(float64(len(values))*0.95))]}
}

func (a *App) usageStats(r *http.Request) shared.M {
	hours := shared.Num(r.URL.Query().Get("hours"))
	if hours <= 0 {
		hours = 24
	}
	hours = min(720, hours)
	until := shared.NowMS()
	since := until - int64(hours*3600000)
	_, offset := time.UnixMilli(until).In(time.Local).Zone()
	unit, format := "hour", "%Y-%m-%dT%H:00:00"
	if hours > 24 {
		unit, format = "day", "%Y-%m-%d"
	}
	byHour := a.rows("SELECT strftime('"+format+"',ts/1000,'unixepoch','localtime') bucket,COUNT(*) reqs,COALESCE(SUM(CASE WHEN status='success' THEN 1 ELSE 0 END),0) ok,COALESCE(SUM(tokens_in),0) tin,COALESCE(SUM(tokens_out),0) tout,COALESCE(SUM(cache_read),0) cached_tokens,COALESCE(SUM(tokens_reasoning),0) treason,AVG(ttft_ms) avg_ttft FROM usage_log WHERE ts>=? AND ts<=? GROUP BY bucket ORDER BY bucket", since, until)
	ttfts := []float64{}
	for _, v := range a.rows("SELECT ttft_ms FROM usage_log WHERE ts>=? AND ts<=? AND status='success' AND ttft_ms IS NOT NULL", since, until) {
		ttfts = append(ttfts, shared.Num(shared.Obj(v)["ttft_ms"]))
	}
	ttft := percentiles(ttfts)
	return shared.M{"hours": hours, "time_unit": unit, "range_start": since, "range_end": until, "utc_offset_minutes": offset / 60, "byHour": byHour, "byModel": a.rows("SELECT model,COUNT(*) reqs,COALESCE(SUM(tokens_in),0) tin,COALESCE(SUM(tokens_out),0) tout,COALESCE(SUM(CASE WHEN status='success' THEN 1 ELSE 0 END),0) ok FROM usage_log WHERE ts>=? AND ts<=? GROUP BY model ORDER BY reqs DESC", since, until), "byKey": a.rows("SELECT k.name,u.key_id,COUNT(*) reqs,COALESCE(SUM(COALESCE(u.tokens_in,0)+COALESCE(u.tokens_out,0)),0) tokens FROM usage_log u LEFT JOIN keys k ON k.id=u.key_id WHERE u.ts>=? AND u.ts<=? GROUP BY u.key_id ORDER BY reqs DESC", since, until), "byAccount": a.rows("SELECT a.label,u.account_id,COUNT(*) reqs,SUM(CASE WHEN u.status='success' THEN 1 ELSE 0 END) ok,SUM(CASE WHEN u.status='busy' THEN 1 ELSE 0 END) busy,COALESCE(SUM(COALESCE(u.tokens_in,0)+COALESCE(u.tokens_out,0)),0) tokens FROM usage_log u LEFT JOIN accounts a ON a.id=u.account_id WHERE u.ts>=? AND u.ts<=? GROUP BY u.account_id ORDER BY reqs DESC", since, until), "byStatus": a.rows("SELECT status,COUNT(*) n FROM usage_log WHERE ts>=? AND ts<=? GROUP BY status", since, until), "ttft": ttft}
}

func (a *App) performance(limit int) shared.M {
	rows := a.rows("SELECT timings FROM usage_log WHERE timings IS NOT NULL ORDER BY id DESC LIMIT ?", limit)
	for _, value := range rows {
		row := shared.Obj(value)
		row["timings"] = shared.Parse(row["timings"], nil)
	}
	metrics := shared.M{}
	for _, stage := range strings.Fields("queue_ms retry_ms ttft_ms first_reasoning_ms first_text_ms generation_ms total_ms") {
		xs := []float64{}
		for _, v := range rows {
			m := shared.Obj(shared.Obj(v)["timings"])
			if m[stage] != nil {
				xs = append(xs, shared.Num(m[stage]))
			}
		}
		m := shared.Merge(shared.M{"samples": len(xs), "avg": nil, "p50": nil, "p95": nil}, percentiles(xs))
		if len(xs) > 0 {
			sum := float64(0)
			for _, v := range xs {
				sum += v
			}
			m["avg"] = int(sum/float64(len(xs)) + 0.5)
		}
		metrics[stage] = m
	}
	return shared.M{"requests": len(rows), "metrics": metrics, "note": "首字包含思考或实际工具调用；未观测到的阶段为空。generation_ms 是该 HTTP 回合首末输出事件间隔，不能单独视为模型纯生成耗时。"}
}
