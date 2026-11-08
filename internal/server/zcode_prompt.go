package server

import (
	"regexp"
	"strings"
)

// Fingerprints of complete default paragraphs from ZCode 3.17.2 and its older
// identity prefix. These are removal rules, never instructions sent upstream.
var zcodeClientPromptParagraphs = map[string]struct{}{
	"7779642ea44a46963bcdc9c151aa95d4761f17182929e41cb5bff571110af440": {}, // You are ZCode, an interactive agent
	"8202d4b44e0a8f12ff5798ff1d9ea6e435f6cc5426d1e87a5afef229b55af6c5": {}, // You are an agent working with the user toward their goals, using your own judgment...
	"10591efdcea75a1645b7f19037b36c979849ad4ec27f11ce87a8393423597b44": {}, // IMPORTANT: Assist with authorized security testing, defensive security, CTF challe...
	"115db80c5e20d1c18722a481484a18a710313089f9195a6688401ee465604690": {}, // # Harness
	"018d799d4f0eee5a64a230fe3d8618b40373b35ca3645e58475c761c34debe8e": {}, // # ZCode Desktop Context
	"1d0dd2ec669699a5f83ca639195594bfcea8f6486fb81512a9b2cb75bfe9752d": {}, // ### Interactive views
	"881f78cab0b192e574e54f5b1d0c2799930edad0d373ef0535a071d8e1756520": {}, // ### Files & URLs
	"c1bb2aa923a5cb6e1d016bb006a10ef2b6eb5dacb26ae5846d9c7cdb2fbe2c66": {}, // ### Inline Code Comments
	"ee43af37398581e92bde06d341c98c7b7a9ff6c56023c2bc17b9feaf2d6e31ea": {}, // Write code that reads like the surrounding code: match its comment density, naming...
	"3fcd2b200896a7163b9626229394d0192bcc900d1a94c5c45d659d5e0b42db24": {}, // When you use a pronoun for someone — the user or anyone else you mention — and the...
	"9724415357935973e69202f40e6ed02276e4f52fe1ac2c4359bf1dc8013e81f1": {}, // For actions that are hard to reverse or outward-facing, confirm first unless durab...
	"052e6c0c0d52c3ce116a62cf5b748aea69697052d9284d02b26ee00aba6f0a63": {}, // # Session-specific guidance
	"bc19d90fc9a64470818128e892aa0deba8ab793af972b5be113a9b6b3365ca66": {}, // # Context management
	"4e4e48fb23ceb764b0bfb78bc43ad0809284a1bd080f32264bb4826251e8ca38": {}, // When you have enough information to act, act. Do not re-derive facts already estab...
	"46dd360a22c87a92dfdf29ae2c3011b0f9a014209a7b607f8fcd17a920e5eafa": {}, // You are ZCode, an interactive coding agent
	"c9a12c87bf07fe121bf319f30fe9fd4271d2e7d2f8ef18e9442b0056d6374f53": {}, // You are an interactive ZCode agent that helps users with software engineering tasks.
	"3579cb8d970039273285b43e0b73ab5fc5363c5c983413470dc105d27e0e7bad": {}, // You are an interactive agent that helps users according to your "Output Style", wh...
	"4b491b3d8cb45e8412c378b1c39e09444318c6f28486c6ff29d10f519c8bceb9": {}, // # Harness
	"63be983332e0990140da3e182ef0599a8bfd29ebe849b8475d09b8f406e8513c": {}, // ### Interactive views
}

var zcodePromptParagraphBreak = regexp.MustCompile(`\r?\n[ \t]*\r?\n(?:[ \t]*\r?\n)*`)
var zcodePromptVisualizationPath = regexp.MustCompile(`(?m)^([ \t]*- Writable, durable visualization directory for this session: )"(?:[^"\\]|\\.)*"(\. Save inline fragments here, outside the project\. Other paths cannot be rendered\.)[ \t]*$`)

func zcodePromptParagraphKey(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = zcodePromptVisualizationPath.ReplaceAllString(text, `${1}"<directory>"${2}`)
	return hash(strings.Join(strings.Fields(text), " "))
}

// Derive duplicate-removal rules from the template actually sent upstream so
// restoring or updating that template cannot leave the cleaner out of sync.
var zcodeInjectedPromptDefaults = func() struct {
	paragraphs, lines, headings map[string]bool
} {
	rules := struct{ paragraphs, lines, headings map[string]bool }{
		paragraphs: map[string]bool{}, lines: map[string]bool{}, headings: map[string]bool{},
	}
	for _, value := range zcodeUpstreamSystemPrefix {
		text := str(obj(value)["text"])
		for _, paragraph := range zcodePromptParagraphBreak.Split(text, -1) {
			if strings.TrimSpace(paragraph) != "" {
				rules.paragraphs[zcodePromptParagraphKey(paragraph)] = true
			}
		}
		for _, line := range strings.Split(text, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			key := zcodePromptParagraphKey(line)
			if strings.HasPrefix(line, "#") {
				rules.headings[key] = true
			} else {
				rules.lines[key] = true
			}
		}
	}
	return rules
}()

type zcodePromptCleaner struct {
	started, stopped bool
}

func isZCodePromptIdentity(text string) bool {
	switch strings.TrimSuffix(strings.Join(strings.Fields(text), " "), ".") {
	case "You are ZCode, an interactive agent", "You are ZCode, an interactive coding agent":
		return true
	}
	return false
}

func isZCodeIdentityParagraph(text string) bool {
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return false
	}
	// Some clients join separate default blocks without a blank line.
	for text != "" {
		end := len(text)
		for _, prefix := range []string{" You are ZCode,", " You are an agent working", " IMPORTANT: Assist"} {
			if at := strings.Index(text, prefix); at >= 0 && at < end {
				end = at
			}
		}
		if !isZCodeIdentityText(text[:end]) {
			return false
		}
		text = strings.TrimSpace(text[end:])
	}
	return true
}

func isZCodeIdentityText(text string) bool {
	if isZCodePromptIdentity(text) {
		return true
	}
	switch zcodePromptParagraphKey(text) {
	case "8202d4b44e0a8f12ff5798ff1d9ea6e435f6cc5426d1e87a5afef229b55af6c5", // Default agent identity.
		"10591efdcea75a1645b7f19037b36c979849ad4ec27f11ce87a8393423597b44": // Default security paragraph.
		return true
	}
	return false
}

// These are client-generated branding/model declarations, not user personas.
// Match complete known text so custom identities and quoted examples survive.
func isClaudeClientDeclaration(text string) bool {
	switch strings.TrimSuffix(strings.Join(strings.Fields(text), " "), ".") {
	case "You are Claude Code, Anthropic's official CLI for Claude",
		"You are Claude Code, Anthropic's official CLI for Claude, running within the Claude Agent SDK",
		"You are a Claude agent, built on Anthropic's Claude Agent SDK":
		return true
	}
	// Complete Fable 5 default model paragraph captured from Claude Code 2.1.316.
	// It describes a different model from the one selected by this gateway.
	return zcodePromptParagraphKey(text) == "7e9a82786262846a47b1a011be4d5ac6cbc11d79328a6acea94ad6075c3a2eea"
}

func zcodePromptFence(fence, line string) string {
	line = strings.TrimSpace(line)
	if len(line) < 3 || (line[0] != '`' && line[0] != '~') {
		return fence
	}
	width := 1
	for width < len(line) && line[width] == line[0] {
		width++
	}
	if width < 3 {
		return fence
	}
	if fence == "" {
		return line[:width]
	}
	if line[0] == fence[0] && width >= len(fence) && strings.TrimSpace(line[width:]) == "" {
		return ""
	}
	return fence
}

// A client may extend a default section, for example with an additional command
// bullet. Remove matching default lines while retaining the extension verbatim.
func stripZCodeDefaultLines(text string, fence *string) (string, bool) {
	var out strings.Builder
	changed := false
	for _, line := range strings.SplitAfter(text, "\n") {
		nextFence := zcodePromptFence(*fence, line)
		if *fence == "" && nextFence == "" && !strings.HasPrefix(line, "    ") && !strings.HasPrefix(line, "\t") &&
			(zcodeInjectedPromptDefaults.lines[zcodePromptParagraphKey(line)] || isClaudeClientDeclaration(line)) {
			changed = true
		} else {
			out.WriteString(line)
		}
		*fence = nextFence
	}
	return out.String(), changed
}

func normalizeZCodeHeadings(text string) string {
	lines := strings.SplitAfter(text, "\n")
	var out strings.Builder
	fence := ""
	for i, line := range lines {
		nextFence := zcodePromptFence(fence, line)
		if fence == "" && nextFence == "" && !strings.HasPrefix(line, "    ") && !strings.HasPrefix(line, "\t") &&
			zcodeInjectedPromptDefaults.headings[zcodePromptParagraphKey(line)] {
			next := i + 1
			for next < len(lines) && strings.TrimSpace(lines[next]) == "" {
				next++
			}
			if next == len(lines) || strings.HasPrefix(strings.TrimSpace(lines[next]), "#") {
				continue
			}
			// Remaining content belongs to the caller, such as the interactive !
			// command rule. Keep it distinct from the injected template's section.
			out.WriteString(strings.Replace(line, "# ", "# Client ", 1))
			continue
		}
		out.WriteString(line)
		fence = nextFence
	}
	return out.String()
}

// Remove only known copies of the injected defaults, even after custom content.
// Unknown text and fenced examples are not duplicate-removal candidates.
func stripZCodeDefaultParagraphs(text string) string {
	var out strings.Builder
	fence := ""
	remaining, changed := text, false
	for remaining != "" {
		end, next := len(remaining), len(remaining)
		if boundary := zcodePromptParagraphBreak.FindStringIndex(remaining); boundary != nil {
			end, next = boundary[0], boundary[1]
		}
		paragraph := remaining[:end]
		if fence == "" && !strings.HasPrefix(paragraph, "    ") && !strings.HasPrefix(paragraph, "\t") &&
			(isZCodeIdentityParagraph(paragraph) || isClaudeClientDeclaration(paragraph) ||
				zcodeInjectedPromptDefaults.paragraphs[zcodePromptParagraphKey(paragraph)]) {
			changed = true
		} else {
			cleaned, removed := stripZCodeDefaultLines(paragraph, &fence)
			changed = changed || removed
			if !removed || strings.TrimSpace(cleaned) != "" {
				out.WriteString(cleaned)
				out.WriteString(remaining[end:next])
			}
		}
		remaining = remaining[next:]
	}
	cleaned := normalizeZCodeHeadings(out.String())
	if !changed && cleaned == text {
		return text
	}
	if strings.TrimSpace(cleaned) == "" {
		return ""
	}
	return cleaned
}

func (c *zcodePromptCleaner) text(text string) string {
	return stripZCodeDefaultParagraphs(c.prefix(text))
}

func (c *zcodePromptCleaner) prefix(text string) string {
	if c.stopped {
		return text
	}
	remaining := text
	for {
		paragraph := strings.TrimLeft(remaining, " \t\r\n")
		if paragraph == "" {
			if c.started {
				return ""
			}
			return text
		}
		boundary := zcodePromptParagraphBreak.FindStringIndex(paragraph)
		part := paragraph
		if boundary != nil {
			part = paragraph[:boundary[0]]
		}
		identity := isZCodePromptIdentity(part)
		if !c.started {
			if !identity {
				c.stopped = true
				return text
			}
			c.started = true
		}
		if _, known := zcodeClientPromptParagraphs[zcodePromptParagraphKey(part)]; !known && !identity {
			c.stopped = true
			return remaining
		}
		if boundary == nil {
			return ""
		}
		remaining = paragraph[boundary[1]:]
	}
}

// Unknown/custom content remains intact. Copies of the injected defaults are
// removed independently of whether prefix cleanup has already stopped.
func (c *zcodePromptCleaner) content(value any) (any, bool) {
	if text, ok := value.(string); ok {
		cleaned := c.text(text)
		if cleaned == text {
			return value, false
		}
		if cleaned == "" {
			return nil, true
		}
		return cleaned, true
	}
	list, ok := value.([]any)
	if !ok {
		c.stopped = true
		return value, false
	}
	out := make([]any, 0, len(list))
	changed := false
	for _, item := range list {
		block := obj(item)
		kind := str(block["type"])
		text, ok := block["text"].(string)
		if !ok || (kind != "text" && kind != "input_text" && kind != "output_text") {
			c.stopped = true
			out = append(out, item)
			continue
		}
		cleaned := c.text(text)
		if cleaned == text {
			out = append(out, item)
			continue
		}
		changed = true
		if cleaned != "" {
			copy := clone(block)
			copy["text"] = cleaned
			out = append(out, copy)
		}
	}
	if !changed {
		return value, false
	}
	if len(out) == 0 {
		return nil, true
	}
	return out, true
}

func cleanZCodeClientPrompt(body M, protocol string) M {
	cleaner := &zcodePromptCleaner{}
	if protocol == "anthropic" {
		cleaned, changed := cleaner.content(body["system"])
		if !changed {
			return body
		}
		out := clone(body)
		if cleaned == nil {
			delete(out, "system")
		} else {
			out["system"] = cleaned
		}
		return out
	}
	out := clone(body)
	if value, present := body["instructions"]; present && value != nil && truth(value) {
		if cleaned, changed := cleaner.content(value); changed {
			if cleaned == nil {
				delete(out, "instructions")
			} else {
				out["instructions"] = cleaned
			}
		}
	}
	field := "input"
	if protocol == "chat" {
		field = "messages"
	}
	list, ok := body[field].([]any)
	if !ok {
		return out
	}
	messages := make([]any, 0, len(list))
	leading, changed := true, false
	for _, item := range list {
		message := obj(item)
		role := str(message["role"])
		if !leading || (role != "system" && role != "developer") {
			leading = false
			messages = append(messages, item)
			continue
		}
		cleaned, removed := cleaner.content(message["content"])
		if !removed {
			messages = append(messages, item)
			continue
		}
		changed = true
		if cleaned != nil {
			copy := clone(message)
			copy["content"] = cleaned
			messages = append(messages, copy)
		}
	}
	if changed {
		out[field] = messages
	}
	return out
}
