package captcha

import (
	_ "embed"
	"fmt"
	"strings"
	"zcode2api/internal/server/fault"
	"zcode2api/internal/server/shared"
)

//go:embed captcha.cjs
var CaptchaRuntime []byte

func CaptchaExpired(f *fault.Failure) bool {
	if f == nil || (f.Status != 401 && f.Status != 403) {
		return false
	}
	return captchaChallenge(f)
}

func ValidateCaptchaOptions(value any) error {
	options, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("http.captcha 必须为配置对象")
	}
	for key, value := range options {
		switch key {
		case "claim_runtime":
			if value != "jsdom" && value != "browser" {
				return fmt.Errorf("http.captcha.claim_runtime 须为 jsdom/browser")
			}
		case "browser_backend":
			if value != "auto" && value != "native" && value != "playwright" {
				return fmt.Errorf("http.captcha.browser_backend 须为 auto/native/playwright")
			}
		case "mode":
			if value != "off" && value != "auto" && value != "always" {
				return fmt.Errorf("http.captcha.mode 须为 off/auto/always")
			}
		case "simulate_canvas_webgl", "browser_headless":
			if _, ok := value.(bool); !ok {
				return fmt.Errorf("http.captcha.%s 必须为布尔值", key)
			}
		case "browser_display":
			text, ok := value.(string)
			if !ok || len(text) > 256 || strings.ContainsAny(text, "\r\n\x00") {
				return fmt.Errorf("http.captcha.browser_display 无效")
			}
		case "node_path", "jsdom_path", "playwright_path", "browser_path":
			text, ok := value.(string)
			if !ok || strings.ContainsAny(text, "\r\n\x00") || (key == "node_path" && strings.TrimSpace(text) == "") {
				return fmt.Errorf("http.captcha.%s 路径无效", key)
			}
		case "timeout_ms":
			if !shared.IntegerInRange(value, 5000, 120000) {
				return fmt.Errorf("http.captcha.timeout_ms 须为 5000 到 120000 的整数")
			}
		case "ticket_ttl_ms":
			if !shared.IntegerInRange(value, 0, 0) && !shared.IntegerInRange(value, 30000, float64(MaxTicketTTL.Milliseconds())) {
				return fmt.Errorf("http.captcha.ticket_ttl_ms 须为 0（关闭）或 30000 到 90000 的整数")
			}
		default:
			return fmt.Errorf("未知安全验证设置 http.captcha.%s", key)
		}
	}
	return nil
}

//go:embed captcha_browser.cjs
var CaptchaBrowserRuntime []byte

//go:embed captcha_native.cjs
var CaptchaNativeRuntime []byte

func CaptchaUsesBrowser(purpose string, options shared.M) bool {
	return purpose == CaptchaPurposeBatchClaim || purpose == CaptchaPurposeScheduledClaim || (purpose == CaptchaPurposeClaim && options["claim_runtime"] == "browser")
}
