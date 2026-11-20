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
}

func (e Event) HasText() bool {
	return (e.Type == "text_delta" || e.Type == "reasoning_delta") && e.Text != ""
}

func (e Event) StartsStream(protocol string) bool {
	if protocol == "anthropic" {
		return e.Type == "native_event"
	}
	return e.HasText() || e.Type == "tool_start"
}

type Result struct {
	Text, Reasoning, FinishReason string
	TraceID, UsageWarning         string
	Native, Usage                 shared.M
	Calls                         []any
	Attempts                      int
	HTTPStatus                    int `json:"-"`
}
