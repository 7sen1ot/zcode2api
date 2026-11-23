package protocol

import (
	"regexp"
	"slices"
	"strings"
	"zcode2api/internal/server/fault"
	"zcode2api/internal/server/shared"
)

var ModelSuffix = regexp.MustCompile(`(?i)\[(1m|200k)\]$`)

var toolName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

func blocks(v any) ([]any, error) {
	if v == nil {
		return []any{}, nil
	}
	if s, ok := v.(string); ok {
		if s == "" {
			return []any{}, nil
		}
		return []any{shared.M{"type": "text", "text": s}}, nil
	}
	list, ok := v.([]any)
	if !ok {
		return nil, fault.Invalid("消息 content 必须为字符串或内容块数组")
	}
	out := []any{}
	for _, v := range list {
		c := shared.Obj(v)
		switch shared.Str(c["type"]) {
		case "text", "image", "document", "tool_use", "tool_result", "thinking", "redacted_thinking":
			out = append(out, shared.Clone(c))
		case "input_text", "output_text":
			out = append(out, shared.M{"type": "text", "text": c["text"]})
		case "image_url", "input_image":
			u := shared.Str(c["image_url"])
			if m, ok := c["image_url"].(map[string]any); ok {
				u = shared.Str(m["url"])
			}
			if u == "" {
				return nil, fault.Invalid("图片缺少 image_url")
			}
			source := shared.M{"type": "url", "url": u}
			if strings.HasPrefix(u, "data:") {
				p := strings.SplitN(strings.TrimPrefix(u, "data:"), ";base64,", 2)
				if len(p) != 2 {
					return nil, fault.Invalid("图片 data URL 无效")
				}
				source = shared.M{"type": "base64", "media_type": p[0], "data": p[1]}
			}
			out = append(out, shared.M{"type": "image", "source": source})
		default:
			return nil, fault.Invalid("当前 HTTP 协议转换不支持内容块 " + shared.Str(c["type"]))
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
		m := shared.Obj(value)
		if len(out) > 0 && shared.Obj(out[len(out)-1])["role"] == m["role"] {
			previous := shared.Obj(out[len(out)-1])
			previous["content"] = append(shared.Arr(previous["content"]), shared.Arr(m["content"])...)
		} else {
			out = append(out, m)
		}
	}
	for _, value := range out {
		m := shared.Obj(value)
		if shared.Str(m["role"]) != "user" {
			continue
		}
		results, other := []any{}, []any{}
		for _, block := range shared.Arr(m["content"]) {
			if shared.Str(shared.Obj(block)["type"]) == "tool_result" {
				results = append(results, block)
			} else {
				other = append(other, block)
			}
		}
		m["content"] = append(results, other...)
	}
	return out
}

func ChatMessages(items any, instructions *InstructionContext, tools *ToolCatalog) ([]any, error) {
	list, ok := items.([]any)
	if !ok {
		return nil, fault.Invalid("messages 必须为数组")
	}
	out := []any{}
	conversation := false
	for _, v := range list {
		m := shared.Obj(v)
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

func chatMessage(m shared.M, instructions *InstructionContext, tools *ToolCatalog, conversation bool) (shared.M, error) {
	role := shared.Str(m["role"])
	if role == "system" || role == "developer" {
		return instructions.message(m, conversation)
	}
	if role == "tool" {
		content, e := toolResultContent(m["content"])
		if e != nil {
			return nil, e
		}
		return shared.M{"role": "user", "content": []any{shared.M{"type": "tool_result", "tool_use_id": m["tool_call_id"], "content": content}}}, nil
	}
	if role != "user" && role != "assistant" {
		return nil, fault.Invalid("不支持消息角色 " + role)
	}
	content, e := blocks(m["content"])
	if e != nil {
		return nil, e
	}
	for _, v := range shared.Arr(m["tool_calls"]) {
		call := shared.Obj(v)
		kind := shared.Str(shared.Fallback(call["type"], "function"))
		f := shared.Obj(call[kind])
		input, e := historyToolInput(kind, f)
		if e != nil {
			return nil, e
		}
		content = append(content, shared.M{"type": "tool_use", "id": call["id"], "name": tools.historyName(f), "input": input})
	}
	// A display-only reasoning message has no signed content to replay.
	if role == "assistant" && len(content) == 0 && shared.Truth(m["reasoning_content"]) {
		return nil, nil
	}
	return shared.M{"role": role, "content": content}, nil
}

func historyToolInput(kind string, call shared.M) (any, error) {
	switch kind {
	case "custom":
		text, ok := call["input"].(string)
		if !ok {
			return nil, fault.Invalid("历史 custom 工具的 input 必须为字符串")
		}
		return shared.M{"input": text}, nil
	case "function":
		if input := shared.Parse(shared.Fallback(call["arguments"], "{}"), nil); input != nil {
			return input, nil
		}
		return nil, fault.Invalid("历史工具参数不是有效 JSON")
	default:
		return nil, fault.Invalid("不支持的历史工具调用类型 " + kind)
	}
}

func ResponseMessages(input any, instructions *InstructionContext, tools *ToolCatalog) ([]any, error) {
	if s, ok := input.(string); ok {
		b, e := blocks(s)
		return []any{shared.M{"role": "user", "content": b}}, e
	}
	if input != nil {
		if _, ok := input.([]any); !ok {
			return nil, fault.Invalid("input 必须为字符串或数组")
		}
	}
	out := []any{}
	conversation := false
	for _, v := range shared.Arr(input) {
		if history, ok := v.(nativeAssistantHistory); ok {
			out = append(out, shared.M{"role": "assistant", "content": shared.CloneValue(history.Content)})
			conversation = true
			continue
		}
		m := shared.Obj(v)
		switch shared.Str(m["type"]) {
		case "function_call", "custom_tool_call":
			if m["status"] == "incomplete" {
				continue
			}
			kind := "function"
			if shared.Str(m["type"]) == "custom_tool_call" {
				kind = "custom"
			}
			value, e := historyToolInput(kind, m)
			if e != nil {
				return nil, e
			}
			out = append(out, shared.M{"role": "assistant", "content": []any{shared.M{"type": "tool_use", "id": m["call_id"], "name": tools.historyName(m), "input": value}}})
		case "function_call_output", "custom_tool_call_output":
			content, e := toolResultContent(m["output"])
			if e != nil {
				return nil, e
			}
			out = append(out, shared.M{"role": "user", "content": []any{shared.M{"type": "tool_result", "tool_use_id": m["call_id"], "content": content}}})
		case "web_search_call":
			// Responses history carries the action and URLs, not Anthropic's
			// encrypted result blocks. Preserve the available context as text.
			if text := webSearchHistory(m); text != "" {
				out = append(out, shared.M{"role": "assistant", "content": []any{shared.M{"type": "text", "text": text}}})
			}
		case "reasoning":
			// Only gateway-stored history carries the original signed blocks.
			continue
		case "", "message":
			message, e := chatMessage(m, instructions, tools, conversation)
			if e != nil {
				return nil, e
			}
			if message != nil {
				if m["role"] == "assistant" {
					var annotations []any
					for _, value := range shared.Arr(m["content"]) {
						block := shared.Obj(value)
						if block["type"] == "output_text" {
							annotations = append(annotations, shared.Arr(block["annotations"])...)
						}
					}
					if links := webSearchLinks(annotations, "url_citation"); links != "" {
						message["content"] = append(shared.Arr(message["content"]), shared.M{"type": "text", "text": links})
					}
				}
				out = append(out, message)
			}
		default:
			return nil, fault.Invalid("当前 HTTP 协议转换不支持 Responses 输入 " + shared.Str(m["type"]))
		}
		if shared.Str(m["role"]) != "system" && shared.Str(m["role"]) != "developer" {
			conversation = true
		}
	}
	return out, nil
}

func BuildRequest(body shared.M, protocol, model, level string, catalog *ToolCatalog, instructions *InstructionContext, template SystemTemplate) (shared.M, error) {
	body = cleanZCodeClientPrompt(body, protocol)
	if len(shared.Arr(body["functions"])) > 0 || body["function_call"] != nil {
		return nil, fault.Invalid("请使用 tools，不支持旧版 functions")
	}
	if body["n"] != nil && shared.Num(body["n"]) != 1 {
		return nil, fault.Invalid("仅支持 n=1")
	}
	system := []any{}
	instructions.System = &system
	if protocol != "anthropic" && instructions.Mode == "history" {
		// Preinstall the bridge so the first later update cannot change system.
		instructions.tag = historyInstructionTag
		system = append(system, shared.M{"type": "text", "text": historyInstructionBridge})
	}
	tools := catalog.Tools
	var request shared.M
	if protocol == "anthropic" {
		b, e := blocks(body["system"])
		if e != nil {
			return nil, e
		}
		system = append(system, b...)
		request = shared.Clone(body)
		delete(request, "instruction_mode")
		if body["tools"] != nil {
			request["tools"] = tools
		}
	} else {
		if shared.Truth(body["instructions"]) {
			if _, e := instructions.message(shared.M{"role": "developer", "content": body["instructions"]}, false); e != nil {
				return nil, e
			}
		}
		var messages []any
		var e error
		if protocol == "chat" {
			messages, e = ChatMessages(body["messages"], instructions, catalog)
		} else {
			if body["conversation"] != nil {
				return nil, fault.Invalid("当前网关不支持 conversation，请使用 previous_response_id 或显式提交历史")
			}
			catalog.includeSearchSources = slices.Contains(shared.Arr(body["include"]), "web_search_call.action.sources")
			if limit := body["max_tool_calls"]; limit != nil {
				if !shared.IntegerInRange(limit, 1, 1<<53-1) {
					return nil, fault.Invalid("max_tool_calls 必须为正整数")
				}
				for i, value := range tools {
					tool := shared.Obj(value)
					if tool["type"] == nativeWebSearchType {
						tools = slices.Clone(tools)
						tools[i] = shared.Merge(tool, shared.M{"max_uses": min(shared.Integer(tool["max_uses"]), shared.Integer(limit))})
						break
					}
				}
			}
			messages, e = ResponseMessages(body["input"], instructions, catalog)
		}
		if e != nil {
			return nil, e
		}
		request = shared.M{"messages": conversationMessages(messages), "max_tokens": shared.First(body["max_completion_tokens"], body["max_tokens"], body["max_output_tokens"], 128000)}
		shared.CopyFields(request, body, "temperature", "top_p", "top_k", "metadata", "thinking")
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
			c := shared.M{}
			switch shared.Str(choice) {
			case "", "auto":
				c["type"] = "auto"
			case "none":
				c["type"] = "none"
			case "required":
				if len(tools) == 0 {
					return nil, fault.Invalid("tool_choice=required 需要至少一个工具")
				}
				c["type"] = "any"
			default:
				m := shared.Obj(choice)
				kind := shared.Str(m["type"])
				if protocol == "responses" && isWebSearch(kind) {
					if !catalog.HasWebSearch() {
						return nil, fault.Invalid("tool_choice 未匹配到已声明的 web_search")
					}
					c = shared.M{"type": "tool", "name": webSearchName}
					break
				}
				if kind != "function" && kind != "custom" {
					return nil, fault.Invalid("无法转换 tool_choice")
				}
				name, e := catalog.choiceName(m)
				if e != nil {
					return nil, e
				}
				c = shared.M{"type": "tool", "name": name}
			}
			if parallel != nil && shared.Str(c["type"]) != "none" {
				c["disable_parallel_tool_use"] = !shared.Truth(parallel)
			}
			if len(tools) > 0 {
				request["tool_choice"] = c
			}
		}
		format := shared.Obj(shared.First(body["response_format"], shared.Obj(body["text"])["format"]))
		if len(format) > 0 && shared.Str(format["type"]) != "text" {
			if shared.Str(format["type"]) != "json_schema" {
				return nil, fault.Invalid("上游只转换 json_schema 结构化输出")
			}
			request["output_config"] = shared.M{"format": shared.M{"type": "json_schema", "schema": shared.First(shared.Obj(format["json_schema"])["schema"], format["schema"])}}
		}
	}
	upstreamSystem, e := upstreamSystemBlocks(system, template)
	if e != nil {
		return nil, e
	}
	request["system"] = upstreamSystem
	request["model"], request["stream"] = model, true
	if request["max_tokens"] == nil {
		request["max_tokens"] = 128000
	}
	if len(shared.Arr(request["messages"])) == 0 {
		return nil, fault.Invalid("messages/input 不能为空")
	}
	if request["thinking"] == nil {
		request["thinking"] = shared.M{"type": "enabled"}
	}
	if level != "" && shared.Str(shared.Obj(request["thinking"])["type"]) != "disabled" {
		output := shared.Clone(shared.Obj(request["output_config"]))
		if output["effort"] == nil {
			output["effort"] = level
		}
		request["output_config"] = output
	}
	return request, nil
}
