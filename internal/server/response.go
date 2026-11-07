package server

import (
	"strings"
	"time"
)

func normalizeUsage(u M) M {
	input := num(first(u["inputTokens"], u["input_tokens"], u["prompt_tokens"], 0))
	output := num(first(u["outputTokens"], u["output_tokens"], u["completion_tokens"], 0))
	return M{"inputTokens": input, "outputTokens": output, "totalTokens": num(first(u["totalTokens"], u["total_tokens"], input+output)), "reasoningTokens": num(first(u["reasoningTokens"], obj(u["output_tokens_details"])["reasoning_tokens"], obj(u["completion_tokens_details"])["reasoning_tokens"], 0)), "cacheReadTokens": num(first(u["cacheReadTokens"], u["cachedInputTokens"], u["cache_read_input_tokens"], obj(u["input_tokens_details"])["cached_tokens"], obj(u["prompt_tokens_details"])["cached_tokens"], 0)), "cacheWriteTokens": num(first(u["cacheWriteTokens"], u["cacheCreationTokens"], u["cache_creation_input_tokens"], 0))}
}
func chatUsage(u M) M {
	return M{"prompt_tokens": num(u["inputTokens"]), "completion_tokens": num(u["outputTokens"]), "total_tokens": num(u["totalTokens"]), "prompt_tokens_details": M{"cached_tokens": num(u["cacheReadTokens"])}, "completion_tokens_details": M{"reasoning_tokens": num(u["reasoningTokens"])}}
}
func responsesUsage(u M) M {
	return M{"input_tokens": num(u["inputTokens"]), "output_tokens": num(u["outputTokens"]), "total_tokens": num(u["totalTokens"]), "input_tokens_details": M{"cached_tokens": num(u["cacheReadTokens"])}, "output_tokens_details": M{"reasoning_tokens": num(u["reasoningTokens"])}}
}
func lengthFinish(s string) bool {
	s = strings.ToLower(s)
	return strings.Contains(s, "length") || strings.Contains(s, "max_tokens") || strings.Contains(s, "max_output_tokens")
}
func chatFinishReason(r *Result) string {
	if len(r.Calls) > 0 {
		return "tool_calls"
	}
	if lengthFinish(r.FinishReason) {
		return "length"
	}
	return "stop"
}
func buildResponse(protocol string, r *Result, model, id string, tools *toolCatalog) (M, error) {
	if protocol == "anthropic" {
		return r.Native, nil
	}
	if protocol == "chat" {
		m := M{"role": "assistant", "content": nullable(r.Text)}
		if r.Reasoning != "" {
			m["reasoning_content"] = r.Reasoning
		}
		if len(r.Calls) > 0 {
			calls := []any{}
			for _, v := range r.Calls {
				c := obj(v)
				call, e := tools.definition(str(c["name"])).chatCall(str(c["id"]), c["arguments"])
				if e != nil {
					return nil, e
				}
				calls = append(calls, call)
			}
			m["tool_calls"] = calls
		}
		return M{"id": id, "object": "chat.completion", "created": time.Now().Unix(), "model": model, "choices": []any{M{"index": 0, "message": m, "finish_reason": chatFinishReason(r)}}, "usage": chatUsage(r.Usage)}, nil
	}
	output := []any{}
	if r.Reasoning != "" {
		output = append(output, M{"id": "rs_" + uuid(), "type": "reasoning", "summary": []any{M{"type": "summary_text", "text": r.Reasoning}}})
	}
	if r.Text != "" {
		output = append(output, M{"id": "msg_" + uuid(), "type": "message", "role": "assistant", "status": "completed", "content": []any{M{"type": "output_text", "text": r.Text, "annotations": []any{}}}})
	}
	for _, v := range r.Calls {
		c := obj(v)
		definition := tools.definition(str(c["name"]))
		prefix := "fc_"
		if definition.Kind == "custom" {
			prefix = "ctc_"
		}
		item, e := definition.completedItem(prefix+uuid(), str(c["id"]), c["arguments"])
		if e != nil {
			return nil, e
		}
		output = append(output, item)
	}
	status := "completed"
	var incomplete any
	if lengthFinish(r.FinishReason) {
		status = "incomplete"
		incomplete = M{"reason": "max_output_tokens"}
	}
	return M{"id": id, "object": "response", "created_at": time.Now().Unix(), "status": status, "model": model, "output": output, "output_text": r.Text, "incomplete_details": incomplete, "usage": responsesUsage(r.Usage)}, nil
}
func errorShape(f *Failure, protocol string) (int, M) {
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
		typ = "authentication_error"
	}
	if protocol == "anthropic" {
		switch f.Kind {
		case "exhausted", "busy", "overload":
			typ = "overloaded_error"
		case "quota", "context":
			typ = "invalid_request_error"
		case "canceled", "timeout", "risk", "account_risk", "unknown", "server", "network":
			typ = "api_error"
		}
	}
	return status, M{"type": typ, "code": fallback(f.Code, f.Kind), "message": f.Message}
}
