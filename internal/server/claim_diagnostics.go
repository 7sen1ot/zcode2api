package server

import (
	"context"
	"strings"
	"zcode2api/internal/server/fault"
	routing "zcode2api/internal/server/proxy"
	"zcode2api/internal/server/shared"
)

// Browser diagnostics are client observations, kept separate from server facts.
// Copy only measurements; never accept tickets, cookies or arbitrary SDK payloads.
func claimBrowserDiagnostics(value any) shared.M {
	in, out := shared.Obj(value), shared.M{}
	for _, key := range []string{"sdk_stage", "browser_visibility", "verify_code"} {
		if value, ok := in[key].(string); ok && len(value) <= 64 && !strings.ContainsAny(value, "\r\n\x00") {
			out[key] = value
		}
	}
	for _, key := range []string{"sdk_initialized", "sdk_executed"} {
		if value, ok := in[key].(bool); ok {
			out[key] = value
		}
	}
	for _, key := range []string{"sdk_load_ms", "sdk_init_ms", "sdk_ready_wait_ms", "sdk_verify_ms", "sdk_total_ms"} {
		if value, exists := in[key]; exists && shared.IntegerInRange(value, 0, 600000) {
			out[key] = shared.Integer(value)
		}
	}
	return out
}

func claimLogDetail(detail, diagnostic shared.M) shared.M {
	out := shared.Clone(detail)
	shared.CopyFields(out, diagnostic, "batch_id", "verification_mode", "stage", "failure_stage", "sdk_stage", "claim_request_sent", "claim_request_started", "sdk_attempted", "sdk_executed", "sdk_initialized", "ticket_attached", "browser_ticket", "runtime", "sdk_error", "sdk_error_code", "verify_code", "sdk_http_status", "sdk_load_ms", "sdk_init_ms", "sdk_ready_wait_ms", "sdk_prepared_age_ms", "sdk_verify_ms", "request_id", "browser_version", "browser_visibility", "session_reused", "browser_backend", "browser_headless", "browser_webdriver", "browser_origin", "browser_launch_reason", "fallback_code", "browser_verification", "proxy_exit_hash", "proxy_node")
	if status := shared.Integer(diagnostic["http_status"]); status > 0 {
		out["http_status"] = status
	}
	if started := shared.Integer(diagnostic["started_at"]); started > 0 {
		out["total_ms"] = max(int64(0), shared.NowMS()-started)
	}
	return out
}

func (a *App) claimDiagnosticRoute(ctx context.Context, diagnostic shared.M) {
	proxy := routing.SelectedProxy(ctx)
	diagnostic["proxy_node"], diagnostic["proxy_exit_hash"] = "直连", shared.Hash("direct")
	if proxy != "" {
		diagnostic["proxy_node"], diagnostic["proxy_exit_hash"] = "账号代理", shared.Hash(proxy)
	}
	if selection, ok := ctx.Value(routing.ProxySelectionKey{}).(routing.ProxySelection); ok {
		diagnostic["proxy_node"], diagnostic["proxy_exit_hash"] = "代理节点", selection.Identity
		a.proxies.mu.Lock()
		for _, endpoint := range a.proxies.endpoints {
			if endpoint.Identity() == selection.Identity {
				diagnostic["proxy_node"] = endpoint.Name
				break
			}
		}
		a.proxies.mu.Unlock()
	}
}

// A session can be consumed by a claim or canceled once, preventing duplicate logs.
func (a *App) logBrowserClaimVerification(session *claimCaptchaSession, body shared.M) error {
	outcome := shared.Obj(body["verification_error"])
	if session == nil || len(outcome) == 0 {
		return nil
	}
	code := shared.Str(outcome["code"])
	if len(code) == 0 || len(code) > 64 || strings.ContainsAny(code, "\r\n\x00") {
		code = "browser_verification_failed"
	}
	message := []rune(shared.Str(outcome["message"]))
	if len(message) > 160 {
		message = message[:160]
	}
	failure := &fault.Failure{Code: code, Message: string(message)}
	if shared.Truth(outcome["canceled"]) {
		failure.Kind, failure.Code = "canceled", "canceled"
	} else if code == "captcha_timeout" {
		failure.Kind = "timeout"
	} else {
		fault.Classify(failure)
	}
	diagnostic := shared.M{"stage": "browser_verification", "verification_mode": "browser", "claim_request_sent": false, "browser_ticket": false,
		"started_at": session.expires.Add(-claimCaptchaSessionTTL).UnixMilli(), "browser_verification": claimBrowserDiagnostics(body["verification_diagnostics"])}
	if batchID := shared.Str(body["batch_id"]); routing.HexIDPattern.MatchString(batchID) {
		diagnostic["batch_id"] = batchID
	}
	ctx := routing.PinnedProxyContext(a.ctx, session.proxyURL)
	if session.scope.pooled {
		ctx = context.WithValue(ctx, routing.ProxySelectionKey{}, session.scope.route)
	}
	a.claimDiagnosticRoute(ctx, diagnostic)
	detail := claimLogDetail(fault.FailureDetail(failure), diagnostic)
	return a.exec("INSERT INTO claim_log(account_id,ts,plan_id,ok,code,message,trigger,error_detail) VALUES(?,?,?,?,?,?,?,?)", session.scope.accountID, shared.NowMS(), session.planID, 0, failure.Code, fault.FailureReason(failure), "manual", shared.JSON(detail))
}
