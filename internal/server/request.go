package server

import (
	"regexp"
	"strings"
)

var modelSuffix = regexp.MustCompile(`(?i)\[(1m|200k)\]$`)
var toolName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

func blocks(v any) ([]any, error) {
	if v == nil {
		return []any{}, nil
	}
	if s, ok := v.(string); ok {
		if s == "" {
			return []any{}, nil
		}
		return []any{M{"type": "text", "text": s}}, nil
	}
	list, ok := v.([]any)
	if !ok {
		return nil, invalid("消息 content 必须为字符串或内容块数组")
	}
	out := []any{}
	for _, v := range list {
		c := obj(v)
		switch str(c["type"]) {
		case "text", "image", "document", "tool_use", "tool_result", "thinking", "redacted_thinking":
			out = append(out, clone(c))
		case "input_text", "output_text":
			out = append(out, M{"type": "text", "text": c["text"]})
		case "image_url", "input_image":
			u := str(c["image_url"])
			if m, ok := c["image_url"].(map[string]any); ok {
				u = str(m["url"])
			}
			if u == "" {
				return nil, invalid("图片缺少 image_url")
			}
			source := M{"type": "url", "url": u}
			if strings.HasPrefix(u, "data:") {
				p := strings.SplitN(strings.TrimPrefix(u, "data:"), ";base64,", 2)
				if len(p) != 2 {
					return nil, invalid("图片 data URL 无效")
				}
				source = M{"type": "base64", "media_type": p[0], "data": p[1]}
			}
			out = append(out, M{"type": "image", "source": source})
		default:
			return nil, invalid("当前 HTTP 协议转换不支持内容块 " + str(c["type"]))
		}
	}
	return out, nil
}

func toolResultContent(content any) (any, error) {
	if _, ok := content.(string); ok {
		return content, nil
	}
	return blocks(content)
}

// Anthropic requires tool results before text in the user turn following tool_use.
func conversationMessages(messages []any) []any {
	out := []any{}
	for _, value := range messages {
		m := obj(value)
		if len(out) > 0 && obj(out[len(out)-1])["role"] == m["role"] {
			previous := obj(out[len(out)-1])
			previous["content"] = append(arr(previous["content"]), arr(m["content"])...)
		} else {
			out = append(out, m)
		}
	}
	for _, value := range out {
		m := obj(value)
		if str(m["role"]) != "user" {
			continue
		}
		results, other := []any{}, []any{}
		for _, block := range arr(m["content"]) {
			if str(obj(block)["type"]) == "tool_result" {
				results = append(results, block)
			} else {
				other = append(other, block)
			}
		}
		m["content"] = append(results, other...)
	}
	return out
}

func chatMessages(items any, instructions *instructionContext, tools *toolCatalog) ([]any, error) {
	list, ok := items.([]any)
	if !ok {
		return nil, invalid("messages 必须为数组")
	}
	out := []any{}
	conversation := false
	for _, v := range list {
		m := obj(v)
		message, e := chatMessage(m, instructions, tools, conversation)
		if e != nil {
			return nil, e
		}
		if message != nil {
			out = append(out, message)
		}
		conversation = conversation || (m["role"] != "system" && m["role"] != "developer")
	}
	return out, nil
}

func chatMessage(m M, instructions *instructionContext, tools *toolCatalog, conversation bool) (M, error) {
	role := str(m["role"])
	if role == "system" || role == "developer" {
		return instructions.message(m, conversation)
	}
	if role == "tool" {
		content, e := toolResultContent(m["content"])
		if e != nil {
			return nil, e
		}
		return M{"role": "user", "content": []any{M{"type": "tool_result", "tool_use_id": m["tool_call_id"], "content": content}}}, nil
	}
	if role != "user" && role != "assistant" {
		return nil, invalid("不支持消息角色 " + role)
	}
	content, e := blocks(m["content"])
	if e != nil {
		return nil, e
	}
	if truth(m["reasoning_content"]) {
		content = append([]any{M{"type": "thinking", "thinking": m["reasoning_content"], "signature": ""}}, content...)
	}
	for _, v := range arr(m["tool_calls"]) {
		call := obj(v)
		kind := str(fallback(call["type"], "function"))
		f := obj(call[kind])
		input, e := historyToolInput(kind, f)
		if e != nil {
			return nil, e
		}
		content = append(content, M{"type": "tool_use", "id": call["id"], "name": tools.historyName(f), "input": input})
	}
	return M{"role": role, "content": content}, nil
}

func historyToolInput(kind string, call M) (any, error) {
	switch kind {
	case "custom":
		text, ok := call["input"].(string)
		if !ok {
			return nil, invalid("历史 custom 工具的 input 必须为字符串")
		}
		return M{"input": text}, nil
	case "function":
		if input := parse(fallback(call["arguments"], "{}"), nil); input != nil {
			return input, nil
		}
		return nil, invalid("历史工具参数不是有效 JSON")
	default:
		return nil, invalid("不支持的历史工具调用类型 " + kind)
	}
}
func responseMessages(input any, instructions *instructionContext, tools *toolCatalog) ([]any, error) {
	if s, ok := input.(string); ok {
		b, e := blocks(s)
		return []any{M{"role": "user", "content": b}}, e
	}
	if input != nil {
		if _, ok := input.([]any); !ok {
			return nil, invalid("input 必须为字符串或数组")
		}
	}
	out := []any{}
	conversation := false
	for _, v := range arr(input) {
		m := obj(v)
		switch str(m["type"]) {
		case "function_call", "custom_tool_call":
			kind := "function"
			if str(m["type"]) == "custom_tool_call" {
				kind = "custom"
			}
			value, e := historyToolInput(kind, m)
			if e != nil {
				return nil, e
			}
			out = append(out, M{"role": "assistant", "content": []any{M{"type": "tool_use", "id": m["call_id"], "name": tools.historyName(m), "input": value}}})
		case "function_call_output", "custom_tool_call_output":
			content, e := toolResultContent(m["output"])
			if e != nil {
				return nil, e
			}
			out = append(out, M{"role": "user", "content": []any{M{"type": "tool_result", "tool_use_id": m["call_id"], "content": content}}})
		case "reasoning":
			text := ""
			for _, v := range arr(m["summary"]) {
				text += str(obj(v)["text"])
			}
			if text != "" {
				out = append(out, M{"role": "assistant", "content": []any{M{"type": "thinking", "thinking": text, "signature": ""}}})
			}
		case "", "message":
			message, e := chatMessage(m, instructions, tools, conversation)
			if e != nil {
				return nil, e
			}
			if message != nil {
				out = append(out, message)
			}
		default:
			return nil, invalid("当前 HTTP 协议转换不支持 Responses 输入 " + str(m["type"]))
		}
		if str(m["role"]) != "system" && str(m["role"]) != "developer" {
			conversation = true
		}
	}
	return out, nil
}
func buildRequest(body M, protocol, model, level string, catalog *toolCatalog, instructions *instructionContext) (M, error) {
	body = cleanZCodeClientPrompt(body, protocol)
	if len(arr(body["functions"])) > 0 || body["function_call"] != nil {
		return nil, invalid("请使用 tools，不支持旧版 functions")
	}
	if body["n"] != nil && num(body["n"]) != 1 {
		return nil, invalid("仅支持 n=1")
	}
	system := []any{}
	instructions.System = &system
	if protocol != "anthropic" && instructions.Mode == "history" {
		// Preinstall the bridge so the first later update cannot change system.
		instructions.tag = historyInstructionTag
		system = append(system, M{"type": "text", "text": historyInstructionBridge})
	}
	tools := catalog.Tools
	var request M
	if protocol == "anthropic" {
		b, e := blocks(body["system"])
		if e != nil {
			return nil, e
		}
		system = append(system, b...)
		request = clone(body)
		delete(request, "instruction_mode")
		if body["tools"] != nil {
			request["tools"] = tools
		}
	} else {
		if truth(body["instructions"]) {
			if _, e := instructions.message(M{"role": "developer", "content": body["instructions"]}, false); e != nil {
				return nil, e
			}
		}
		var messages []any
		var e error
		if protocol == "chat" {
			messages, e = chatMessages(body["messages"], instructions, catalog)
		} else {
			messages, e = responseMessages(body["input"], instructions, catalog)
		}
		if e != nil {
			return nil, e
		}
		request = M{"messages": conversationMessages(messages), "max_tokens": first(body["max_completion_tokens"], body["max_tokens"], body["max_output_tokens"], 128000)}
		for _, k := range []string{"temperature", "top_p", "top_k", "metadata", "thinking"} {
			if v, ok := body[k]; ok {
				request[k] = v
			}
		}
		if stop, ok := body["stop"]; ok {
			if s, ok := stop.(string); ok {
				stop = []any{s}
			}
			request["stop_sequences"] = stop
		}
		if len(tools) > 0 {
			request["tools"] = tools
		}
		choice := body["tool_choice"]
		parallel := body["parallel_tool_calls"]
		if choice != nil || parallel != nil {
			c := M{}
			switch str(choice) {
			case "", "auto":
				c["type"] = "auto"
			case "none":
				c["type"] = "none"
			case "required":
				c["type"] = "any"
			default:
				m := obj(choice)
				if str(m["type"]) != "function" && str(m["type"]) != "custom" {
					return nil, invalid("无法转换 tool_choice")
				}
				name, e := catalog.choiceName(m)
				if e != nil {
					return nil, e
				}
				c = M{"type": "tool", "name": name}
			}
			if parallel != nil && str(c["type"]) != "none" {
				c["disable_parallel_tool_use"] = !truth(parallel)
			}
			request["tool_choice"] = c
		}
		format := obj(first(body["response_format"], obj(body["text"])["format"]))
		if len(format) > 0 && str(format["type"]) != "text" {
			if str(format["type"]) != "json_schema" {
				return nil, invalid("上游只转换 json_schema 结构化输出")
			}
			request["output_config"] = M{"format": M{"type": "json_schema", "schema": first(obj(format["json_schema"])["schema"], format["schema"])}}
		}
	}
	upstreamSystem, e := upstreamSystemBlocks(system)
	if e != nil {
		return nil, e
	}
	request["system"] = upstreamSystem
	request["model"], request["stream"] = model, true
	if request["max_tokens"] == nil {
		request["max_tokens"] = 128000
	}
	if len(arr(request["messages"])) == 0 {
		return nil, invalid("messages/input 不能为空")
	}
	if request["thinking"] == nil {
		request["thinking"] = M{"type": "enabled"}
	}
	if level != "" && str(obj(request["thinking"])["type"]) != "disabled" {
		output := clone(obj(request["output_config"]))
		if output["effort"] == nil {
			output["effort"] = level
		}
		request["output_config"] = output
	}
	return request, nil
}
