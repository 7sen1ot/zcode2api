package protocol

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"zcode2api/internal/server/fault"
	"zcode2api/internal/server/shared"
)

type toolDefinition struct {
	Name, Namespace, Kind, UpstreamName string
	Grammar                             *toolGrammar
}

type ToolCatalog struct {
	Tools      []any
	ByName     map[string]toolDefinition `json:"-"`
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
	value := "ns_" + shared.Hash(namespace + "\x00" + name)[:16] + "_" + name
	return value[:min(len(value), 128)]
}

func NewToolCatalog(ctx context.Context, root string, value any, protocol string) (*ToolCatalog, error) {
	c := &ToolCatalog{Tools: []any{}, ByName: map[string]toolDefinition{}, byIdentity: map[string]toolDefinition{}, ctx: ctx, root: root, protocol: protocol}
	if value == nil {
		return c, nil
	}
	list, ok := value.([]any)
	if !ok {
		return nil, fault.Invalid("tools 必须为工具数组")
	}
	for _, value := range list {
		t := shared.Obj(value)
		if protocol == "responses" && shared.Str(t["type"]) == "namespace" {
			namespace := shared.Str(t["name"])
			children, ok := t["tools"].([]any)
			if !toolName.MatchString(namespace) || !ok || len(children) == 0 {
				return nil, fault.Invalid("namespace 必须包含有效名称和工具数组")
			}
			for _, child := range children {
				if e := c.add(shared.Obj(child), protocol, namespace, shared.Str(t["description"])); e != nil {
					return nil, e
				}
			}
			continue
		}
		if e := c.add(t, protocol, "", ""); e != nil {
			return nil, e
		}
	}
	// Keep each explicit cache breakpoint on the same tool and after the same
	// set of definitions. Unmarked tools can use a consistent upstream order.
	start := 0
	for i, tool := range c.Tools {
		if shared.Obj(tool)["cache_control"] != nil {
			slices.SortFunc(c.Tools[start:i], compareToolNames)
			start = i + 1
		}
	}
	slices.SortFunc(c.Tools[start:], compareToolNames)
	return c, nil
}

func compareToolNames(a, b any) int {
	return strings.Compare(shared.Str(shared.Obj(a)["name"]), shared.Str(shared.Obj(b)["name"]))
}

func (c *ToolCatalog) add(t shared.M, protocol, namespace, namespaceDescription string) error {
	f, kind := t, shared.Str(t["type"])
	if protocol == "chat" {
		f = shared.Obj(t[kind])
	}
	if protocol != "anthropic" && kind != "function" && kind != "custom" {
		return fault.Invalid(fmt.Sprintf("当前协议转换不支持 %s 类型工具", kind))
	}
	name := shared.Str(f["name"])
	identity := toolIdentity(namespace, name)
	upstreamName := upstreamToolName(namespace, name)
	if !toolName.MatchString(name) {
		return fault.Invalid("工具名称无效")
	}
	if _, exists := c.byIdentity[identity]; exists {
		return fault.Invalid("工具名称重复：" + identity)
	}
	if _, exists := c.ByName[upstreamName]; exists {
		return fault.Invalid("工具映射名称冲突：" + identity)
	}
	definition := toolDefinition{Name: name, Namespace: namespace, Kind: kind, UpstreamName: upstreamName}
	var converted shared.M
	if protocol == "anthropic" {
		converted = shared.Clone(t)
	} else {
		description := shared.Str(f["description"])
		if namespace != "" {
			description = strings.TrimSpace("Namespace " + namespace + ": " + namespaceDescription + "\n" + description)
		}
		schema := shared.First(f["parameters"], shared.M{"type": "object", "properties": shared.M{}})
		if kind == "custom" {
			schema = shared.M{"type": "object", "properties": shared.M{"input": shared.M{"type": "string", "description": "The exact raw text to pass to the client tool."}}, "required": []any{"input"}, "additionalProperties": false}
			description += "\nPass the tool's raw text unchanged in the input string."
			if f["format"] != nil {
				if _, ok := f["format"].(map[string]any); !ok {
					return fault.Invalid("custom 工具的 format 必须为对象")
				}
			}
			format := shared.Obj(f["format"])
			switch shared.Str(format["type"]) {
			case "", "text":
			case "grammar":
				if protocol == "chat" {
					format = shared.Obj(format["grammar"])
				}
				grammar, e := newToolGrammar(c.ctx, c.root, format)
				if e != nil {
					return e
				}
				definition.Grammar = grammar
				description += "\nThe input string must conform to this " + grammar.Syntax + " grammar. The gateway validates the entire input and rejects nonconforming calls:\n" + grammar.Definition
			default:
				return fault.Invalid("不支持的 custom 工具输入格式")
			}
		}
		converted = shared.M{"name": upstreamName, "description": description, "input_schema": schema}
		if strict, ok := f["strict"]; ok && kind == "function" {
			converted["strict"] = strict
		}
	}
	c.ByName[upstreamName], c.byIdentity[identity] = definition, definition
	c.Tools = append(c.Tools, converted)
	return nil
}

func (c *ToolCatalog) historyName(m shared.M) string {
	namespace, name := shared.Str(m["namespace"]), shared.Str(m["name"])
	if definition, ok := c.byIdentity[toolIdentity(namespace, name)]; ok {
		return definition.UpstreamName
	}
	return upstreamToolName(namespace, name)
}

func (c *ToolCatalog) choiceName(choice shared.M) (string, error) {
	name := shared.Str(shared.First(shared.Obj(choice[shared.Str(choice["type"])])["name"], choice["name"]))
	identity := toolIdentity(shared.Str(choice["namespace"]), name)
	if definition, ok := c.byIdentity[identity]; ok && definition.Kind == shared.Str(choice["type"]) {
		return definition.UpstreamName, nil
	}
	// Some clients select a namespaced tool by its unqualified leaf name.
	match := ""
	for _, definition := range c.byIdentity {
		if definition.Name == name && definition.Kind == shared.Str(choice["type"]) && shared.Str(choice["namespace"]) == "" {
			if match != "" {
				return "", fault.Invalid("tool_choice 工具名称有歧义，请指定 namespace")
			}
			match = definition.UpstreamName
		}
	}
	if match == "" {
		return "", fault.Invalid("tool_choice 未匹配到已声明的工具")
	}
	return match, nil
}

func (c *ToolCatalog) definition(name string) toolDefinition {
	if definition, ok := c.ByName[name]; ok {
		return definition
	}
	return toolDefinition{Name: name, Kind: "function", UpstreamName: name}
}

func (c *ToolCatalog) ValidateCall(ctx context.Context, name string, arguments any) error {
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
		arguments = shared.Parse(s, nil)
	}
	input, ok := shared.Obj(arguments)["input"].(string)
	if !ok {
		return "", &fault.Failure{Kind: "server", Code: "invalid_tool_input", Message: "上游 custom 工具参数缺少 input 字符串"}
	}
	return input, nil
}

func (d toolDefinition) chatCall(id string, arguments any) (shared.M, error) {
	if d.Kind == "custom" {
		input, e := customToolInput(arguments)
		if e != nil {
			return nil, e
		}
		return shared.M{"id": id, "type": "custom", "custom": shared.M{"name": d.Name, "input": input}}, nil
	}
	value, ok := arguments.(string)
	if !ok {
		value = shared.JSON(arguments)
	}
	return shared.M{"id": id, "type": "function", "function": shared.M{"name": d.Name, "arguments": value}}, nil
}

func (d toolDefinition) startItem(id, callID string) shared.M {
	item := shared.M{"id": id, "call_id": callID, "name": d.Name, "status": "in_progress"}
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

func (d toolDefinition) completedItem(id, callID string, arguments any) (shared.M, error) {
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
		item["arguments"] = shared.JSON(arguments)
	}
	return item, nil
}
