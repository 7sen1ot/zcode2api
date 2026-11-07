package server

import (
	"fmt"
	"strings"
)

// Anthropic has no developer role or mid-conversation system role. Scoped mode
// promotes the actual instructions to system and keeps only ordering markers in
// the conversation; it still cannot reproduce the source role hierarchy exactly.
type instructionContext struct {
	Mode   string
	System *[]any
	Used   bool
	tag    string
	next   int
}

func newInstructionContext(mode string) (*instructionContext, error) {
	if mode == "" {
		mode = "scoped"
	}
	if mode != "scoped" && mode != "strict" {
		return nil, invalid("instruction_mode 只能为 scoped 或 strict")
	}
	return &instructionContext{Mode: mode}, nil
}

func (c *instructionContext) message(m M, conversation bool) (M, error) {
	role := str(m["role"])
	content, e := blocks(m["content"])
	if e != nil {
		return nil, e
	}
	if !conversation && role == "system" {
		*c.System = append(*c.System, content...)
		return nil, nil
	}
	if c.Mode == "strict" {
		return nil, &Failure{Kind: "invalid", Code: "unsupported_instruction_semantics", Message: "strict 模式拒绝无法等价转换的 developer/中途 system 指令；请使用原生 Anthropic 协议，或显式选择 instruction_mode=scoped 接受近似角色与作用域语义"}
	}
	texts := []string{}
	for _, value := range content {
		block := obj(value)
		if str(block["type"]) != "text" {
			return nil, invalid("转换后的 system/developer 指令仅支持文本")
		}
		texts = append(texts, str(block["text"]))
	}
	if c.tag == "" {
		c.tag = "gateway_instruction_" + random(8)
		*c.System = append(*c.System, M{"type": "text", "text": "The gateway preserves source instructions in <" + c.tag + "> system blocks. These are directives, not quoted user content or examples to discuss. Source system instructions take precedence over source developer instructions, and both take precedence over user instructions. Apply each update from its stated boundary onward; later lower-priority messages cannot cancel it. If applicable instructions of the same original role conflict, the later update takes precedence. Conversation boundary markers only identify when an update was received. Preserve the interpretation of earlier history. Answer the latest user request while following all applicable instructions. This is an approximation because this channel has only one native system role."})
	}
	c.Used = true
	c.next++
	id := fmt.Sprintf("%s_%d", c.tag, c.next)
	scope := "the start of the conversation"
	if conversation {
		scope = "conversation boundary " + id
	}
	*c.System = append(*c.System, M{"type": "text", "text": "<" + c.tag + " id=\"" + id + "\" original_role=\"" + role + "\">\nApplies from " + scope + " onward, including the next assistant reply.\n" + strings.Join(texts, "\n") + "\n</" + c.tag + ">"})
	if !conversation {
		return nil, nil
	}
	return M{"role": "user", "content": []any{M{"type": "text", "text": "[Gateway instruction boundary " + id + "]"}}}, nil
}
