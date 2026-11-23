package protocol

import (
	"math"
	"strings"
	"time"
	"zcode2api/internal/server/fault"
	"zcode2api/internal/server/shared"
)

func NormalizeUsage(u shared.M) shared.M {
	input := shared.Num(shared.First(u["inputTokens"], u["input_tokens"], u["prompt_tokens"], 0))
	output := shared.Num(shared.First(u["outputTokens"], u["output_tokens"], u["completion_tokens"], 0))
	return shared.M{"inputTokens": input, "outputTokens": output, "totalTokens": shared.Num(shared.First(u["totalTokens"], u["total_tokens"], input+output)), "reasoningTokens": shared.Num(shared.First(u["reasoningTokens"], shared.Obj(u["output_tokens_details"])["reasoning_tokens"], shared.Obj(u["completion_tokens_details"])["reasoning_tokens"], 0)), "cacheReadTokens": shared.Num(shared.First(u["cacheReadTokens"], u["cachedInputTokens"], u["cache_read_input_tokens"], shared.Obj(u["input_tokens_details"])["cached_tokens"], shared.Obj(u["prompt_tokens_details"])["cached_tokens"], 0)), "cacheWriteTokens": shared.Num(shared.First(u["cacheWriteTokens"], u["cacheCreationTokens"], u["cache_creation_input_tokens"], 0))}
}

func chatUsage(u shared.M) shared.M {
	return shared.M{"prompt_tokens": shared.Num(u["inputTokens"]), "completion_tokens": shared.Num(u["outputTokens"]), "total_tokens": shared.Num(u["totalTokens"]), "prompt_tokens_details": shared.M{"cached_tokens": shared.Num(u["cacheReadTokens"])}, "completion_tokens_details": shared.M{"reasoning_tokens": shared.Num(u["reasoningTokens"])}}
}

func responsesUsage(u shared.M) shared.M {
	return shared.M{"input_tokens": shared.Num(u["inputTokens"]), "output_tokens": shared.Num(u["outputTokens"]), "total_tokens": shared.Num(u["totalTokens"]), "input_tokens_details": shared.M{"cached_tokens": shared.Num(u["cacheReadTokens"])}, "output_tokens_details": shared.M{"reasoning_tokens": shared.Num(u["reasoningTokens"])}}
}

func lengthFinish(s string) bool {
	s = strings.ToLower(s)
	return s == "model_context_window_exceeded" || strings.Contains(s, "length") || strings.Contains(s, "max_tokens") || strings.Contains(s, "max_output_tokens")
}

func (r *Result) Incomplete() bool {
	return lengthFinish(r.FinishReason) || r.FinishReason == "refusal" || r.FinishReason == "pause_turn"
}

func (r *Result) outputStatus() string {
	if r.Incomplete() {
		return "incomplete"
	}
	return "completed"
}

func chatFinishReason(r *Result) string {
	if r.FinishReason == "refusal" {
		return "content_filter"
	}
	if r.Incomplete() {
		return "length"
	}
	if len(r.Calls) > 0 {
		return "tool_calls"
	}
	return "stop"
}

func BuildResponse(protocol string, r *Result, model, id string, tools *ToolCatalog) (shared.M, error) {
	if protocol == "anthropic" {
		return r.Native, nil
	}
	if protocol == "chat" {
		m := shared.M{"role": "assistant", "content": shared.Nullable(r.Text)}
		if r.Reasoning != "" {
			m["reasoning_content"] = r.Reasoning
		}
		if len(r.Calls) > 0 {
			calls := []any{}
			for _, v := range r.Calls {
				c := shared.Obj(v)
				call, e := tools.definition(shared.Str(c["name"])).chatCall(shared.Str(c["id"]), c["arguments"])
				if e != nil {
					return nil, e
				}
				calls = append(calls, call)
			}
			m["tool_calls"] = calls
		}
		return shared.M{"id": id, "object": "chat.completion", "created": time.Now().Unix(), "model": model, "choices": []any{shared.M{"index": 0, "message": m, "finish_reason": chatFinishReason(r)}}, "usage": chatUsage(r.Usage)}, nil
	}
	return responsesResult(r, model, id, time.Now().Unix(), r.Output), nil
}

func responsesResult(r *Result, model, id string, created int64, output []any) shared.M {
	var incomplete any
	if r.Incomplete() {
		details := shared.M{}
		switch {
		case r.FinishReason == "refusal":
			details["reason"] = "content_filter"
		case lengthFinish(r.FinishReason):
			details["reason"] = "max_output_tokens"
		}
		incomplete = details
	}
	return shared.M{"id": id, "object": "response", "created_at": created, "status": r.outputStatus(), "model": model, "output": output, "output_text": r.Text, "incomplete_details": incomplete, "usage": responsesUsage(r.Usage)}
}

func ErrorShape(f *fault.Failure, protocol string) (int, shared.M) {
	status, typ := 502, "server_error"
	switch f.Kind {
	case "exhausted":
		status = 503
	case "permission":
		status, typ = 403, "permission_error"
	case "canceled":
		status, typ = 499, "canceled"
	case "busy", "overload":
		status = 529
	case "rate_limit":
		status, typ = 429, "rate_limit_error"
	case "quota":
		status, typ = 403, "insufficient_quota"
	case "auth":
		status, typ = 401, "authentication_error"
	case "context":
		status, typ = 400, "context_length_exceeded"
	case "invalid":
		status, typ = 400, "invalid_request_error"
	case "timeout":
		status, typ = 504, "timeout"
	}
	if f.Status > 0 && f.Kind == "" {
		status = f.Status
		if status == 401 {
			typ = "authentication_error"
		}
	}
	if protocol == "anthropic" {
		if typ == "server_error" {
			typ = "api_error"
		}
		switch f.Kind {
		case "exhausted", "busy", "overload":
			typ = "overloaded_error"
		case "quota", "context":
			typ = "invalid_request_error"
		case "canceled", "timeout", "risk", "account_risk", "unknown", "server", "network":
			typ = "api_error"
		}
	}
	if f.Code == "account_pool_cooling" {
		status = 503
	}
	shape := shared.M{"type": typ, "code": shared.Fallback(f.Code, f.Kind), "message": f.Message}
	if f.RetryAfter > 0 {
		// Streaming headers are already committed. Carry the same delay in the
		// error event so streaming clients can also avoid immediate retries.
		shape["retry_after"] = max(1, int(math.Ceil(f.RetryAfter.Seconds())))
	}
	return status, shape
}
