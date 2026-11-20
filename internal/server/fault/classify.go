package fault

import (
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func RetryAfter(s string, now time.Time) time.Duration {
	if s == "" {
		return 0
	}
	if n, e := strconv.ParseFloat(s, 64); e == nil && n >= 0 && !math.IsNaN(n) && !math.IsInf(n, 0) && n < float64(1<<63-1)/float64(time.Second) {
		return time.Duration(n * float64(time.Second))
	}
	if t, e := http.ParseTime(s); e == nil {
		return max(0, t.Sub(now))
	}
	return 0
}

func Classify(f *Failure) *Failure {
	if f == nil {
		return nil
	}
	if f.Code == "captcha_verification_failed" {
		switch f.VerifyCode {
		case "F001":
			f.Kind, f.Code, f.Retryable = "risk", "captcha_risk_blocked", false
		case "F010":
			f.Kind, f.Code, f.Retryable = "rate_limit", "captcha_ip_rate_limited", true
		case "F011":
			f.Kind, f.Code, f.Retryable = "rate_limit", "captcha_device_rate_limited", true
		}
	}
	if f.Kind != "" {
		return f
	}
	switch f.Code {
	case "3008", "3009", "3010":
		f.Kind, f.Retryable = "busy", true
		return f
	}
	if f.Status == http.StatusTooManyRequests {
		f.Kind, f.Retryable = "rate_limit", true
		return f
	}
	kind := ""
	retry := false
	switch f.Code {
	case "rate_limit_error", "1302", "1303", "1305", "3002", "F010", "F011", "captcha_ip_rate_limited", "captcha_device_rate_limited":
		kind, retry = "rate_limit", true
	case "authentication_error":
		kind = "auth"
	case "3007":
		kind = "auth"
		if ExplicitCaptchaChallenge(f.Message) {
			kind = "risk"
		}
	case "permission_error":
		kind = "permission"
	case "invalid_request_error", "1210", "3001", "3006":
		kind = "invalid"
	case "500", "2007", "1120", "1230":
		kind, retry = "server", true
	case "1234", "network", "upstream_stream_error", "proxy_auth_failed", "proxy_connection_failed":
		kind, retry = "network", true
	case "503", "529", "1312", "engine_overloaded_error", "overloaded_error":
		kind, retry = "overload", true
	case "1005", "1006", "3103", "2056", "20097", "1316", "1317", "1318", "1319", "1320", "1321", "insufficient_quota":
		kind = "quota"
	case "1261":
		kind = "context"
	case "1113", "1309", "1311", "1304", "1308", "1310", "1313", "1008", "1314", "1315":
		kind = "terminal"
	case "3012", "upstream_risk_blocked":
		kind = "account_risk"
	case "CAPTCHA_VERIFY_FAILED", "coding_plan_security_verification_required", "captcha_expired", "captcha_invalid", "captcha_verification_failed", "captcha_duplicate_result", "captcha_config_risk_blocked", "captcha_risk_blocked", "upstream_waf_blocked", "F001", "F008", "F014", "F018", "F019", "F020":
		kind = "risk"
	case "timeout", "first_token_timeout", "stream_idle_timeout", "inference_timeout", "captcha_config_timeout", "retry_budget_exhausted":
		kind, retry = "timeout", true
	case "throttle_window_exhausted":
		kind, retry = "rate_limit", true
	}
	if kind == "" {
		switch {
		case riskBlockMessage(f.Message):
			kind = "account_risk"
		case f.Status == 401:
			kind = "auth"
		case f.Status == 403:
			kind = "permission"
		case f.Status == 503 || f.Status == 529:
			kind, retry = "overload", true
		case f.Status >= 500:
			kind, retry = "server", true
		case f.Status == 400:
			kind = "invalid"
		default:
			kind = "unknown"
		}
	}
	f.Kind, f.Retryable = kind, retry
	return f
}

func ExplicitCaptchaChallenge(message string) bool {
	message = strings.ToLower(message)
	return strings.Contains(message, "captcha") || strings.Contains(message, "security verification") || strings.Contains(message, "安全验证")
}
