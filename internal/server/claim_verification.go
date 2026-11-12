package server

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type claimCaptchaDiagnosticKey struct{}

func (a *App) claimVerification(ctx context.Context, id, plan string) (M, error) {
	if strings.TrimSpace(plan) == "" {
		return nil, invalid("缺少 plan_id")
	}
	account := a.account(id)
	if account == nil {
		return nil, fmt.Errorf("账号不存在")
	}
	// Rotate to the next pool exit per claim so each card is issued from a
	// different IP; the session pins every later request of this claim to it.
	ctx, release, err := a.pinClaimOutboundProxy(ctx, account)
	if err != nil {
		return nil, err
	}
	defer release()
	a.prepareCaptchaChallenge(ctx)
	scene, failure := a.captchaScene(ctx, account)
	if failure != nil {
		return nil, failure
	}
	if !truth(scene["enabled"]) {
		return M{"enabled": false}, nil
	}
	scope, failure := a.captchaScope(ctx, account, captchaPurposeClaim, scene)
	if failure != nil {
		return nil, failure
	}
	token, failure := a.newClaimCaptchaSession(ctx, scope, plan, str(scene["region"]))
	if failure != nil {
		return nil, failure
	}
	return M{"enabled": true, "scene_id": scene["sceneId"], "region": scene["region"], "prefix": scene["prefix"], "verification_session": token, "expires_at": nowMS() + claimCaptchaSessionTTL.Milliseconds()}, nil
}

func validCaptchaParam(param string) bool {
	if strings.TrimSpace(param) == "" || len(param) > 32<<10 {
		return false
	}
	for _, character := range param {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func validateClaimVerification(value M) error {
	if automatic, present := value["auto_verify"]; present {
		if _, ok := automatic.(bool); !ok {
			return fmt.Errorf("auto_verify 必须为布尔值")
		}
	}
	param, supplied := value["captcha_verify_param"]
	region, hasRegion := value["captcha_region"]
	session, hasSession := value["verification_session"]
	if !supplied && !hasRegion && !hasSession {
		return nil
	}
	ticket, ok := param.(string)
	if !ok || !validCaptchaParam(ticket) || (region != "cn" && region != "sgp") {
		return fmt.Errorf("领取验证票据或地域无效，请重新完成安全验证")
	}
	if token, ok := session.(string); !ok || !validClaimCaptchaSessionID(token) {
		return fmt.Errorf("领取验证票据缺少有效会话，请重新完成安全验证")
	}
	return nil
}

func (a *App) reserveCaptchaTicket(param string) *Failure {
	a.captcha.mu.Lock()
	defer a.captcha.mu.Unlock()
	if a.captcha.used == nil {
		a.captcha.used = map[string]time.Time{}
	}
	for key, expires := range a.captcha.used {
		if time.Now().After(expires) {
			delete(a.captcha.used, key)
		}
	}
	id := hash(param)
	if _, exists := a.captcha.used[id]; exists {
		return &Failure{Kind: "risk", Code: "captcha_duplicate_result", Message: "验证票据已使用，请重新完成安全验证"}
	}
	if len(a.captcha.used) >= 2048 {
		return &Failure{Kind: "server", Code: "captcha_capacity_exceeded", Message: "安全验证票据保护缓存已满，请稍后重试"}
	}
	a.captcha.used[id] = time.Now().Add(20 * time.Minute)
	return nil
}

func captchaChallenge(failure *Failure) bool {
	if failure == nil {
		return false
	}
	switch failure.Code {
	case "3007":
		return failure.Kind == "risk" || explicitCaptchaChallenge(failure.Message)
	case "CAPTCHA_VERIFY_FAILED", "coding_plan_security_verification_required", "captcha_expired", "captcha_invalid", "F008", "F014", "F018", "F019", "F020":
		return true
	}
	return false
}

func claimResponseRequiresVerification(response apiResult) bool {
	// A structured business challenge can use HTTP 400 as well as 200/401/403.
	// Redirects, proxy authentication, rate limits and server failures are not
	// verification challenges, regardless of any text in their responses.
	if response.Status < 200 || (response.Status >= 300 && response.Status < 400) || response.Status >= 500 || response.Status == 407 || response.Status == 429 {
		return false
	}
	failure := apiResponseFailure(response)
	if failure.Kind == "rate_limit" || failure.Kind == "account_risk" || failure.Kind == "network" {
		return false
	}
	return captchaChallenge(failure)
}

func claimBrowserFallback(failure *Failure) bool {
	if failure == nil || failure.Kind == "rate_limit" || failure.Kind == "account_risk" || failure.Kind == "auth" || failure.Kind == "network" {
		return false
	}
	switch failure.Code {
	case "captcha_playwright_missing", "captcha_browser_missing", "captcha_browser_launch_failed":
		return true
	case "captcha_interaction_required", "captcha_scene_disabled", "captcha_jsdom_missing", "captcha_runtime_error", "captcha_timeout", "captcha_verification_failed", "captcha_risk_blocked", "F001", "captcha_duplicate_result", "captcha_invalid_result", "captcha_proxy_unsupported", "captcha_expired", "captcha_config_changed", "captcha_session_invalid":
		return true
	}
	return false
}
