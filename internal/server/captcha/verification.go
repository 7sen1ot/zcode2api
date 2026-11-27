package captcha

import (
	"fmt"
	"strings"
	"zcode2api/internal/server/fault"
	routing "zcode2api/internal/server/proxy"
	"zcode2api/internal/server/shared"
)

func ValidCaptchaParam(param string) bool {
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

func ValidateClaimVerification(value shared.M) error {
	if raw, present := value["batch_id"]; present {
		if id, ok := raw.(string); !ok || (id != "" && !routing.HexIDPattern.MatchString(id)) {
			return fmt.Errorf("batch_id 必须为空或 64 位十六进制标识")
		}
	}
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
	if !ok || !ValidCaptchaParam(ticket) || (region != "cn" && region != "sgp") {
		return fmt.Errorf("领取验证票据或地域无效，请重新完成安全验证")
	}
	if token, ok := session.(string); !ok || !routing.HexIDPattern.MatchString(token) {
		return fmt.Errorf("领取验证票据缺少有效会话，请重新完成安全验证")
	}
	return nil
}

func captchaChallenge(failure *fault.Failure) bool {
	if failure == nil {
		return false
	}
	switch failure.Code {
	case "3007":
		return failure.Kind == "risk" || fault.ExplicitCaptchaChallenge(failure.Message)
	case "CAPTCHA_VERIFY_FAILED", "coding_plan_security_verification_required", "captcha_expired", "captcha_invalid", "F008", "F014", "F018", "F019", "F020":
		return true
	}
	return false
}

func ClaimResponseRequiresVerification(response fault.ApiResult) bool {
	// A structured business challenge can use HTTP 400 as well as 200/401/403.
	// Redirects, proxy authentication, rate limits and server failures are not
	// verification challenges, regardless of any text in their responses.
	if response.Status < 200 || (response.Status >= 300 && response.Status < 400) || response.Status >= 500 || response.Status == 407 || response.Status == 429 {
		return false
	}
	failure := fault.ApiResponseFailure(response)
	if failure.Kind == "rate_limit" || failure.Kind == "account_risk" || failure.Kind == "network" {
		return false
	}
	return captchaChallenge(failure)
}

func ClaimBrowserFallback(failure *fault.Failure) bool {
	if failure == nil || failure.Kind == "rate_limit" || failure.Kind == "auth" || failure.Kind == "network" {
		return false
	}
	// 3012 is not proof of a CAPTCHA challenge. Offer a bounded human attempt
	// without reclassifying account risk or removing its shared cooldown.
	if failure.Kind == "account_risk" {
		return true
	}
	switch failure.Code {
	case "captcha_playwright_missing", "captcha_browser_missing", "captcha_browser_launch_failed", "captcha_display_unavailable", "captcha_native_unsupported",
		"captcha_interaction_required", "captcha_scene_disabled", "captcha_jsdom_missing", "captcha_runtime_error", "captcha_timeout", "captcha_verification_failed", "captcha_risk_blocked", "F001", "captcha_duplicate_result", "captcha_invalid_result", "captcha_proxy_unsupported", "captcha_expired", "captcha_config_changed", "captcha_session_invalid":
		return true
	}
	return false
}
