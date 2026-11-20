package fault

import (
	"net/http"
	"strings"
	"unicode/utf8"
	"zcode2api/internal/server/shared"
)

const FailureBodyLimit = 64 << 10

// Keep diagnostics separate from the short message returned by the gateway.
func FailureDetail(f *Failure) shared.M {
	detail := shared.Clone(f.Detail)
	if detail["message"] == nil {
		detail["message"] = BoundedFailureText(f.Message)
	}
	if detail["code"] == nil && f.Code != "" {
		detail["code"] = f.Code
	}
	if f.Kind != "" {
		detail["kind"] = f.Kind
	}
	if f.Status > 0 {
		detail["status"] = f.Status
	}
	if f.VerifyCode != "" {
		detail["verify_code"] = f.VerifyCode
	}
	return detail
}

func BoundedFailureText(value string) string {
	if len(value) <= FailureBodyLimit {
		return value
	}
	end := FailureBodyLimit
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end] + "\n…（内容已截断）"
}

func redactFailureValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := shared.M{}
		for key, item := range v {
			name := strings.ToLower(strings.NewReplacer("-", "", "_", "").Replace(key))
			if strings.Contains(name, "token") && !strings.Contains(name, "tokens") || strings.Contains(name, "password") || strings.Contains(name, "secret") || strings.Contains(name, "cookie") || strings.Contains(name, "authorization") || strings.Contains(name, "apikey") || strings.Contains(name, "verifyparam") {
				out[key] = "[REDACTED]"
			} else {
				out[key] = redactFailureValue(item)
			}
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = redactFailureValue(item)
		}
		return out
	default:
		return value
	}
}

func FailureBody(body string) string {
	if value := shared.Parse(body, nil); value != nil {
		body = shared.JSON(redactFailureValue(value))
	}
	return BoundedFailureText(body)
}

func FailureResponseHeaders(headers http.Header) shared.M {
	out := shared.M{}
	// Never persist response cookies or authentication headers.
	for _, key := range []string{"Content-Type", "Retry-After", "X-Request-Id", "Request-Id", "X-Trace-Id", "Trace-Id", "X-Zcode-Trace-Id", "X-Query-Id", "Traceparent"} {
		if value := headers.Get(key); value != "" {
			out[key] = BoundedFailureText(value)
		}
	}
	return out
}
