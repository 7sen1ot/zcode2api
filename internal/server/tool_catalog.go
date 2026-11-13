package server

import (
	"context"
	"fmt"
	"strings"
)

type toolDefinition struct {
	Name, Namespace, Kind, UpstreamName string
	Grammar                             *toolGrammar
}

type toolCatalog struct {
	Tools      []any
	byName     map[string]toolDefinition
	byIdentity map[string]toolDefinition
	ctx        context.Context
	root       string
	protocol   string
}

func toolIdentity(namespace, name string) string {
	if namespace != "" {
		return namespace + "." + name
	}
	return name
}

func upstreamToolName(namespace, name string) string {
	if namespace == "" {
		return name
	}
	// Keep the mapping stable across requests, including history without definitions.
	value := "ns_" + hash(namespace + "\x00" + name)[:16] + "_" + name
	return value[:min(len(value), 128)]
}

func newToolCatalog(ctx context.Context, root string, value any, protocol string) (*toolCatalog, error) {
	c := &toolCatalog{Tools: []any{}, byName: map[string]toolDefinition{}, byIdentity: map[string]toolDefinition{}, ctx: ctx, root: root, protocol: protocol}
	if value == nil {
		return c, nil
	}
	list, ok := value.([]any)
	if !ok {
		return nil, invalid("tools 必须为工具数组")
	}
	for _, value := range list {
		t := obj(value)
		if protocol == "responses" && str(t["type"]) == "namespace" {
			namespace := str(t["name"])
			children, ok := t["tools"].([]any)
			if !toolName.MatchString(namespace) || !ok || len(children) == 0 {
				return nil, invalid("namespace 必须包含有效名称和工具数组")
			}
			for _, child := range children {
				if e := c.add(obj(child), protocol, namespace, str(t["description"])); e != nil {
					return nil, e
				}
			}
			continue
		}
		if e := c.add(t, protocol, "", ""); e != nil {
			return nil, e
		}
	}
	return c, nil
}

func (c *toolCatalog) add(t M, protocol, namespace, namespaceDescription string) error {
	f, kind := t, str(t["type"])
	if protocol == "chat" {
		f = obj(t[kind])
	}
	if protocol != "anthropic" && kind != "function" && kind != "custom" {
		return invalid(fmt.Sprintf("当前协议转换不支持 %s 类型工具", kind))
	}
	name := str(f["name"])
	identity := toolIdentity(namespace, name)
	upstreamName := upstreamToolName(namespace, name)
	if !toolName.MatchString(name) {
		return invalid("工具名称无效")
	}
	if _, exists := c.byIdentity[identity]; exists {
		return invalid("工具名称重复：" + identity)
	}
	if _, exists := c.byName[upstreamName]; exists {
		return invalid("工具映射名称冲突：" + identity)
	}
	definition := toolDefinition{Name: name, Namespace: namespace, Kind: kind, UpstreamName: upstreamName}
	converted := clone(t)
	if protocol != "anthropic" {
		description := str(f["description"])
		if namespace != "" {
			description = strings.TrimSpace("Namespace " + namespace + ": " + namespaceDescription + "\n" + description)
		}
		schema := first(f["parameters"], M{"type": "object", "properties": M{}})
		if kind == "custom" {
			schema = M{"type": "object", "properties": M{"input": M{"type": "string", "description": "The exact raw text to pass to the client tool."}}, "required": []any{"input"}, "additionalProperties": false}
			description += "\nPass the tool's raw text unchanged in the input string."
			if f["format"] != nil {
				if _, ok := f["format"].(map[string]any); !ok {
					return invalid("custom 工具的 format 必须为对象")
				}
			}
			format := obj(f["format"])
			switch str(format["type"]) {
			case "", "text":
			case "grammar":
				if protocol == "chat" {
					format = obj(format["grammar"])
				}
				grammar, e := newToolGrammar(c.ctx, c.root, format)
				if e != nil {
					return e
				}
				definition.Grammar = grammar
				description += "\nThe input string must conform to this " + grammar.Syntax + " grammar. The gateway validates the entire input and rejects nonconforming calls:\n" + grammar.Definition
			default:
				return invalid("不支持的 custom 工具输入格式")
			}
		}
		converted = M{"name": upstreamName, "description": description, "input_schema": schema}
		if strict, ok := f["strict"]; ok && kind == "function" {
			converted["strict"] = strict
		}
	}
	c.byName[upstreamName], c.byIdentity[identity] = definition, definition
	c.Tools = append(c.Tools, converted)
	return nil
}

func (c *toolCatalog) historyName(m M) string {
	namespace, name := str(m["namespace"]), str(m["name"])
	if definition, ok := c.byIdentity[toolIdentity(namespace, name)]; ok {
		return definition.UpstreamName
	}
	return upstreamToolName(namespace, name)
}

func (c *toolCatalog) choiceName(choice M) (string, error) {
	name := str(first(obj(choice[str(choice["type"])])["name"], choice["name"]))
	identity := toolIdentity(str(choice["namespace"]), name)
	if definition, ok := c.byIdentity[identity]; ok && definition.Kind == str(choice["type"]) {
		return definition.UpstreamName, nil
	}
	// Some clients select a namespaced tool by its unqualified leaf name.
	match := ""
	for _, definition := range c.byIdentity {
		if definition.Name == name && definition.Kind == str(choice["type"]) && str(choice["namespace"]) == "" {
			if match != "" {
				return "", invalid("tool_choice 工具名称有歧义，请指定 namespace")
			}
			match = definition.UpstreamName
		}
	}
	if match == "" {
		return "", invalid("tool_choice 未匹配到已声明的工具")
	}
	return match, nil
}

func (c *toolCatalog) definition(name string) toolDefinition {
	if definition, ok := c.byName[name]; ok {
		return definition
	}
	return toolDefinition{Name: name, Kind: "function", UpstreamName: name}
}

func (c *toolCatalog) validateCall(ctx context.Context, name string, arguments any) error {
	if c.protocol == "anthropic" {
		return nil
	}
	d := c.definition(name)
	if d.Kind != "custom" {
		return nil
	}
	input, e := customToolInput(arguments)
	if e != nil {
		return e
	}
	if d.Grammar != nil {
		return d.Grammar.check(ctx, &input)
	}
	return nil
}

func customToolInput(arguments any) (string, error) {
	if s, ok := arguments.(string); ok {
		arguments = parse(s, nil)
	}
	input, ok := obj(arguments)["input"].(string)
	if !ok {
		return "", &Failure{Kind: "server", Code: "invalid_tool_input", Message: "上游 custom 工具参数缺少 input 字符串"}
	}
	return input, nil
}

func (d toolDefinition) chatCall(id string, arguments any) (M, error) {
	if d.Kind == "custom" {
		input, e := customToolInput(arguments)
		if e != nil {
			return nil, e
		}
		return M{"id": id, "type": "custom", "custom": M{"name": d.Name, "input": input}}, nil
	}
	value := js(arguments)
	if s, ok := arguments.(string); ok {
		value = s
	}
	return M{"id": id, "type": "function", "function": M{"name": d.Name, "arguments": value}}, nil
}

func (d toolDefinition) startItem(id, callID string) M {
	item := M{"id": id, "call_id": callID, "name": d.Name, "status": "in_progress"}
	if d.Namespace != "" {
		item["namespace"] = d.Namespace
	}
	if d.Kind == "custom" {
		item["type"], item["input"] = "custom_tool_call", ""
	} else {
		item["type"], item["arguments"] = "function_call", ""
	}
	return item
}

func (d toolDefinition) completedItem(id, callID string, arguments any) (M, error) {
	item := d.startItem(id, callID)
	item["status"] = "completed"
	if d.Kind == "custom" {
		input, e := customToolInput(arguments)
		if e != nil {
			return nil, e
		}
		item["input"] = input
	} else if s, ok := arguments.(string); ok {
		item["arguments"] = s
	} else {
		item["arguments"] = js(arguments)
	}
	return item, nil
}
