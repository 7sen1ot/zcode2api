package server

import (
	_ "embed"
	"fmt"
	"regexp"
	"strings"
)

var modelSuffix = regexp.MustCompile(`(?i)\[(1m|200k)\]$`)
var toolName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

//go:embed zcode_system.json
var zcodeSystemJSON []byte

var systemPrefix = func() []any {
	blocks := arr(parse(string(zcodeSystemJSON), nil))
	if len(blocks) != 3 {
		panic("zcode_system.json must contain exactly three system blocks")
	}
	return blocks
}()

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
func chatMessages(items any, system *[]any) ([]any, error) {
	list, ok := items.([]any)
	if !ok {
		return nil, invalid("messages 必须为数组")
	}
	out := []any{}
	conversation := false
	for _, v := range list {
		m := obj(v)
		role := str(m["role"])
		if role == "system" || role == "developer" {
			if conversation {
				return nil, invalid("不能无损转换对话中途的 system/developer 消息")
			}
			b, e := blocks(m["content"])
			if e != nil {
				return nil, e
			}
			*system = append(*system, b...)
			continue
		}
		conversation = true
		if role == "tool" {
			content := m["content"]
			if _, ok := content.(string); !ok {
				b, e := blocks(content)
				if e != nil {
					return nil, e
				}
				content = b
			}
			out = append(out, M{"role": "user", "content": []any{M{"type": "tool_result", "tool_use_id": m["tool_call_id"], "content": content}}})
			continue
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
			f := obj(call["function"])
			input := parse(fallback(f["arguments"], "{}"), nil)
			if input == nil {
				return nil, invalid("历史工具参数不是有效 JSON")
			}
			content = append(content, M{"type": "tool_use", "id": call["id"], "name": f["name"], "input": input})
		}
		out = append(out, M{"role": role, "content": content})
	}
	return out, nil
}
func responseMessages(input any, system *[]any) ([]any, error) {
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
		case "function_call":
			value := parse(fallback(m["arguments"], "{}"), nil)
			if value == nil {
				return nil, invalid("历史工具参数不是有效 JSON")
			}
			out = append(out, M{"role": "assistant", "content": []any{M{"type": "tool_use", "id": m["call_id"], "name": m["name"], "input": value}}})
		case "function_call_output":
			content := m["output"]
			if _, ok := content.(string); !ok {
				b, e := blocks(content)
				if e != nil {
					return nil, e
				}
				content = b
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
			role := str(m["role"])
			if conversation && (role == "system" || role == "developer") {
				return nil, invalid("不能转换对话中途的 system/developer 消息")
			}
			messages, e := chatMessages([]any{m}, system)
			if e != nil {
				return nil, e
			}
			out = append(out, messages...)
		default:
			return nil, invalid("当前 HTTP 协议转换不支持 Responses 输入 " + str(m["type"]))
		}
		if str(m["role"]) != "system" && str(m["role"]) != "developer" {
			conversation = true
		}
	}
	return out, nil
}
func buildRequest(body M, protocol, model, level string) (M, error) {
	if len(arr(body["functions"])) > 0 || body["function_call"] != nil {
		return nil, invalid("请使用 tools，不支持旧版 functions")
	}
	if body["n"] != nil && num(body["n"]) != 1 {
		return nil, invalid("仅支持 n=1")
	}
	system := arr(parse(js(systemPrefix), nil))
	tools := []any{}
	if body["tools"] != nil {
		list, ok := body["tools"].([]any)
		if !ok {
			return nil, invalid("tools 必须为工具数组")
		}
		names := map[string]bool{}
		for _, v := range list {
			t := obj(v)
			f := t
			if protocol != "anthropic" && str(t["type"]) != "function" {
				return nil, invalid(fmt.Sprintf("上游 Anthropic 通道不能执行 %s 类型工具；请使用客户端 function 工具", str(t["type"])))
			}
			if protocol == "chat" {
				f = obj(t["function"])
			}
			name := str(f["name"])
			if !toolName.MatchString(name) || names[name] {
				return nil, invalid("工具名称无效或重复")
			}
			names[name] = true
			if protocol == "anthropic" {
				tools = append(tools, clone(t))
			} else {
				m := M{"name": name, "description": fallback(f["description"], ""), "input_schema": first(f["parameters"], M{"type": "object", "properties": M{}})}
				if v, ok := f["strict"]; ok {
					m["strict"] = v
				}
				tools = append(tools, m)
			}
		}
	}
	var request M
	if protocol == "anthropic" {
		b, e := blocks(body["system"])
		if e != nil {
			return nil, e
		}
		system = append(system, b...)
		request = clone(body)
		if body["tools"] != nil {
			request["tools"] = tools
		}
	} else {
		if truth(body["instructions"]) {
			b, e := blocks(body["instructions"])
			if e != nil {
				return nil, e
			}
			system = append(system, b...)
		}
		var messages []any
		var e error
		if protocol == "chat" {
			messages, e = chatMessages(body["messages"], &system)
		} else {
			messages, e = responseMessages(body["input"], &system)
		}
		if e != nil {
			return nil, e
		}
		request = M{"messages": messages, "max_tokens": first(body["max_completion_tokens"], body["max_tokens"], body["max_output_tokens"], 128000)}
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
				if str(m["type"]) != "function" {
					return nil, invalid("无法转换 tool_choice")
				}
				c = M{"type": "tool", "name": first(obj(m["function"])["name"], m["name"])}
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
	if len(system) > 3 {
		client := system[2:]
		texts := []string{}
		var control any
		for _, v := range client {
			b := obj(v)
			if str(b["type"]) != "text" {
				return nil, invalid("系统前缀之后仅支持普通 text 系统块")
			}
			for k := range b {
				if k != "type" && k != "text" && k != "cache_control" {
					return nil, invalid("系统块存在无法合并的字段")
				}
			}
			texts = append(texts, str(b["text"]))
			if b["cache_control"] != nil {
				if control != nil && js(control) != js(b["cache_control"]) {
					return nil, invalid("不能保留多个不同的 system 缓存策略")
				}
				control = b["cache_control"]
			}
		}
		b := M{"type": "text", "text": strings.Join(texts, "\n\n")}
		if control != nil {
			b["cache_control"] = control
		}
		system = append(system[:2], b)
	}
	request["system"], request["model"], request["stream"] = system, model, true
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
