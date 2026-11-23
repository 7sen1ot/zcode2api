package protocol

import (
	"bufio"
	"fmt"
	"io"
	"strings"
	"zcode2api/internal/server/shared"
)

type Frame struct{ Data, Raw string }

func ReadSSE(body io.Reader, consume func(Frame) error) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 4096), 32<<20)
	lines := []string{}
	size := 0
	flush := func() error {
		if len(lines) == 0 {
			return nil
		}
		f := Frame{Raw: strings.Join(lines, "\n") + "\n\n"}
		data := []string{}
		for _, line := range lines {
			if strings.HasPrefix(line, "data:") {
				data = append(data, strings.TrimPrefix(line[5:], " "))
			}
		}
		f.Data = strings.Join(data, "\n")
		lines = nil
		size = 0
		return consume(f)
	}
	for scanner.Scan() {
		line := scanner.Text()
		size += len(line) + 1
		if size > 32<<20 {
			return fmt.Errorf("上游 SSE 事件超过 32MB")
		}
		if line == "" {
			if e := flush(); e != nil {
				return e
			}
		} else {
			lines = append(lines, line)
		}
	}
	if e := scanner.Err(); e != nil {
		return e
	}
	return flush()
}

type Event struct {
	Type, Text, Delta, ID, Name string
	Index                       int
	Frame                       Frame
	Block                       shared.M
}

func (e Event) HasText() bool {
	return (e.Type == "text_delta" || e.Type == "reasoning_delta") && e.Text != ""
}

func (e Event) StartsStream(protocol string) bool {
	if protocol == "anthropic" {
		return e.Type == "native_event"
	}
	return e.HasText() || e.Type == "tool_start" || protocol == "responses" && e.Type == "web_search_start"
}

type Result struct {
	Text, Reasoning, FinishReason string
	TraceID, UsageWarning         string
	Native, Usage                 shared.M
	Calls, Output                 []any
	IncompleteCalls               map[string]bool
	Attempts                      int
	HTTPStatus                    int `json:"-"`
}

// Only in-memory gateway history can carry native blocks into ResponseMessages.
type nativeAssistantHistory struct {
	Content []any
}

func (r *Result) ContinuationItem() any {
	content := []any{}
	hasMessage := false
	for _, value := range shared.Arr(r.Native["content"]) {
		block := shared.Obj(value)
		if block["tool_use_id"] != nil && r.IncompleteCalls[shared.Str(block["tool_use_id"])] {
			continue
		}
		switch shared.Str(block["type"]) {
		case "thinking":
			if !shared.Truth(block["signature"]) {
				continue
			}
		case "redacted_thinking":
		case "tool_use", "server_tool_use":
			if r.IncompleteCalls[shared.Str(block["id"])] {
				continue
			}
			hasMessage = true
		default:
			hasMessage = true
		}
		content = append(content, block)
	}
	if !hasMessage {
		return nil
	}
	return nativeAssistantHistory{Content: content}
}
