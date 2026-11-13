package server

import (
	"net/http"
	"strings"
	"unicode/utf8"
)

const failureBodyLimit = 64 << 10

// Keep diagnostics separate from the short message returned by the gateway.
func failureDetail(f *Failure) M {
	detail := clone(f.Detail)
	if detail["message"] == nil {
		detail["message"] = boundedFailureText(f.Message)
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
	return detail
}

func boundedFailureText(value string) string {
	if len(value) <= failureBodyLimit {
		return value
	}
	end := failureBodyLimit
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end] + "\n…（内容已截断）"
}

func redactFailureValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := M{}
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

func failureBody(body string) string {
	if value := parse(body, nil); value != nil {
		body = js(redactFailureValue(value))
	}
	return boundedFailureText(body)
}

func failureResponseHeaders(headers http.Header) M {
	out := M{}
	// Never persist response cookies or authentication headers.
	for _, key := range []string{"Content-Type", "Retry-After", "X-Request-Id", "Request-Id", "X-Trace-Id", "Trace-Id", "X-Zcode-Trace-Id", "X-Query-Id", "Traceparent"} {
		if value := headers.Get(key); value != "" {
			out[key] = boundedFailureText(value)
		}
	}
	return out
}
