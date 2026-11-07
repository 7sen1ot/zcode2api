package server

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

type streamBlock struct {
	Kind, ID string
	Text     strings.Builder
	Index    int
}
type streamTool struct {
	ID, Name, ItemID   string
	Definition         toolDefinition
	Args               strings.Builder
	Index, OutputIndex int
	Ended              bool
}
type protocolStream struct {
	mu                  sync.Mutex
	w                   http.ResponseWriter
	writer              *http.ResponseController
	ctx                 context.Context
	cancel              context.CancelFunc
	protocol, id, model string
	created             int64
	sequence, index     int
	includeUsage        bool
	terminal            bool
	block               *streamBlock
	tools               map[int]*streamTool
	toolCatalog         *toolCatalog
	output              []any
	done                chan struct{}
	worker              sync.WaitGroup
	writeError          error
}

func newStream(w http.ResponseWriter, r *http.Request, protocol, id, model string, include bool, tools *toolCatalog) *protocolStream {
	ctx, cancel := context.WithCancel(r.Context())
	s := &protocolStream{w: w, ctx: ctx, cancel: cancel, protocol: protocol, id: id, model: model, created: time.Now().Unix(), index: -1, includeUsage: include, tools: map[int]*streamTool{}, output: []any{}, done: make(chan struct{})}
	s.toolCatalog = tools
	s.writer = http.NewResponseController(w)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("X-Accel-Buffering", "no")
	h.Set("X-Request-Id", id)
	w.WriteHeader(200)
	if protocol == "chat" {
		s.chunk(M{"role": "assistant", "content": ""}, nil)
	} else if protocol == "responses" {
		response := M{"id": id, "object": "response", "created_at": s.created, "status": "in_progress", "model": model, "output": []any{}}
		s.event("response.created", M{"response": response})
		s.event("response.in_progress", M{"response": response})
	}
	s.worker.Go(func() {
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-s.done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				s.mu.Lock()
				if !s.terminal {
					s.write(": ping\n\n")
				}
				s.mu.Unlock()
			}
		}
	})
	return s
}
func (s *protocolStream) write(v string) {
	if s.writeError != nil {
		return
	}
	_ = s.writer.SetWriteDeadline(time.Now().Add(30 * time.Second))
	_, s.writeError = io.WriteString(s.w, v)
	if s.writeError == nil {
		s.writeError = s.writer.Flush()
	}
	if s.writeError != nil {
		s.cancel()
	}
}
func (s *protocolStream) chunk(delta M, finish any) {
	s.write("data: " + js(M{"id": s.id, "object": "chat.completion.chunk", "created": s.created, "model": s.model, "choices": []any{M{"index": 0, "delta": delta, "finish_reason": finish}}}) + "\n\n")
}
func (s *protocolStream) event(typ string, data M) {
	m := merge(M{"type": typ, "sequence_number": s.sequence}, data)
	s.sequence++
	s.write("event: " + typ + "\ndata: " + js(m) + "\n\n")
}
func (s *protocolStream) location() M { return M{"item_id": s.block.ID, "output_index": s.block.Index} }
func (s *protocolStream) closeBlock() {
	b := s.block
	if b == nil {
		return
	}
	loc := s.location()
	text := b.Text.String()
	var item M
	if b.Kind == "reasoning" {
		part := M{"type": "summary_text", "text": text}
		s.event("response.reasoning_summary_text.done", merge(loc, M{"summary_index": 0, "text": text}))
		s.event("response.reasoning_summary_part.done", merge(loc, M{"summary_index": 0, "part": part}))
		item = M{"id": b.ID, "type": "reasoning", "summary": []any{part}}
	} else {
		part := M{"type": "output_text", "text": text, "annotations": []any{}}
		s.event("response.output_text.done", merge(loc, M{"content_index": 0, "text": text}))
		s.event("response.content_part.done", merge(loc, M{"content_index": 0, "part": part}))
		item = M{"id": b.ID, "type": "message", "role": "assistant", "status": "completed", "content": []any{part}}
	}
	s.event("response.output_item.done", M{"output_index": b.Index, "item": item})
	s.output[b.Index] = item
	s.block = nil
}
func (s *protocolStream) openBlock(kind string) {
	if s.block != nil && s.block.Kind == kind {
		return
	}
	s.closeBlock()
	s.index++
	prefix := "msg_"
	if kind == "reasoning" {
		prefix = "rs_"
	}
	s.block = &streamBlock{Kind: kind, ID: prefix + uuid(), Index: s.index}
	s.output = append(s.output, nil)
	var item M
	if kind == "reasoning" {
		item = M{"id": s.block.ID, "type": "reasoning", "summary": []any{}}
	} else {
		item = M{"id": s.block.ID, "type": "message", "role": "assistant", "status": "in_progress", "content": []any{}}
	}
	s.event("response.output_item.added", M{"output_index": s.index, "item": item})
	if kind == "reasoning" {
		s.event("response.reasoning_summary_part.added", merge(s.location(), M{"summary_index": 0, "part": M{"type": "summary_text", "text": ""}}))
	} else {
		s.event("response.content_part.added", merge(s.location(), M{"content_index": 0, "part": M{"type": "output_text", "text": "", "annotations": []any{}}}))
	}
}
func (s *protocolStream) onEvent(e Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.terminal {
		return nil
	}
	if s.protocol == "anthropic" {
		if e.Type == "native_event" {
			s.write(e.Frame.Raw)
		}
		return s.writeError
	}
	if e.Type == "tool_start" || e.Type == "tool_delta" || e.Type == "tool_end" {
		if err := s.toolEvent(e); err != nil {
			return err
		}
		return s.writeError
	}
	if (e.Type != "text_delta" && e.Type != "reasoning_delta") || e.Text == "" {
		return nil
	}
	reason := e.Type == "reasoning_delta"
	if s.protocol == "chat" {
		field := "content"
		if reason {
			field = "reasoning_content"
		}
		s.chunk(M{field: e.Text}, nil)
	} else {
		kind, typ, index := "text", "response.output_text.delta", "content_index"
		if reason {
			kind, typ, index = "reasoning", "response.reasoning_summary_text.delta", "summary_index"
		}
		s.openBlock(kind)
		s.block.Text.WriteString(e.Text)
		s.event(typ, merge(s.location(), M{index: 0, "delta": e.Text}))
	}
	return s.writeError
}
func (s *protocolStream) toolEvent(e Event) error {
	if e.Type == "tool_start" {
		if s.protocol == "responses" {
			s.closeBlock()
		}
		s.index++
		definition := s.toolCatalog.definition(e.Name)
		prefix := "fc_"
		if s.protocol == "responses" && definition.Kind == "custom" {
			prefix = "ctc_"
		}
		t := &streamTool{ID: e.ID, Name: e.Name, Definition: definition, Index: len(s.tools), OutputIndex: s.index, ItemID: prefix + uuid()}
		s.tools[e.Index] = t
		if s.protocol == "chat" {
			call := M{"index": t.Index, "id": t.ID, "type": "function", "function": M{"name": definition.Name, "arguments": ""}}
			if definition.Kind == "custom" {
				call = M{"index": t.Index, "id": t.ID, "type": "custom", "custom": M{"name": definition.Name, "input": ""}}
			}
			s.chunk(M{"tool_calls": []any{call}}, nil)
		} else {
			s.output = append(s.output, nil)
			s.event("response.output_item.added", M{"output_index": t.OutputIndex, "item": definition.startItem(t.ItemID, t.ID)})
		}
		return nil
	}
	t := s.tools[e.Index]
	if t == nil || t.Ended {
		return nil
	}
	loc := M{"item_id": t.ItemID, "output_index": t.OutputIndex}
	if e.Type == "tool_delta" {
		if t.Definition.Kind == "custom" {
			// Keep every custom input private until the JSON wrapper and grammar pass.
			t.Args.WriteString(e.Delta)
		} else if s.protocol == "chat" {
			s.chunk(M{"tool_calls": []any{M{"index": t.Index, "function": M{"arguments": e.Delta}}}}, nil)
		} else {
			t.Args.WriteString(e.Delta)
			s.event("response.function_call_arguments.delta", merge(loc, M{"delta": e.Delta}))
		}
	} else {
		t.Ended = true
		if s.protocol == "chat" && t.Definition.Kind == "custom" {
			input, err := customToolInput(t.Args.String())
			if err != nil {
				return err
			}
			s.chunk(M{"tool_calls": []any{M{"index": t.Index, "custom": M{"input": input}}}}, nil)
		} else if s.protocol == "responses" {
			item, err := t.Definition.completedItem(t.ItemID, t.ID, t.Args.String())
			if err != nil {
				return err
			}
			if t.Definition.Kind == "custom" {
				// Decode the complete JSON wrapper before emitting raw text, preserving escapes.
				s.event("response.custom_tool_call_input.delta", merge(loc, M{"delta": item["input"]}))
				s.event("response.custom_tool_call_input.done", merge(loc, M{"input": item["input"]}))
			} else {
				s.event("response.function_call_arguments.done", merge(loc, M{"arguments": item["arguments"]}))
			}
			s.event("response.output_item.done", M{"output_index": t.OutputIndex, "item": item})
			s.output[t.OutputIndex] = item
		}
	}
	return nil
}
func (s *protocolStream) complete(r *Result, response M) M {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.protocol == "anthropic" {
		s.terminal = true
		return response
	}
	if s.protocol == "chat" {
		s.chunk(M{}, chatFinishReason(r))
		if s.includeUsage {
			s.write("data: " + js(M{"id": s.id, "object": "chat.completion.chunk", "created": s.created, "model": s.model, "choices": []any{}, "usage": chatUsage(r.Usage)}) + "\n\n")
		}
		s.write("data: [DONE]\n\n")
	} else {
		s.closeBlock()
		response["created_at"] = s.created
		response["output"] = s.completedOutput()
		response["output_text"] = r.Text
		typ := "response.completed"
		if lengthFinish(r.FinishReason) {
			typ = "response.incomplete"
		}
		s.event(typ, M{"response": response})
	}
	s.terminal = true
	return response
}

func (s *protocolStream) completedOutput() []any {
	out := make([]any, 0, len(s.output))
	for _, item := range s.output {
		if item != nil {
			out = append(out, item)
		}
	}
	return out
}
func (s *protocolStream) fail(f *Failure) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.terminal {
		return
	}
	_, shape := errorShape(f, s.protocol)
	switch s.protocol {
	case "anthropic":
		s.write("event: error\ndata: " + js(M{"type": "error", "error": shape}) + "\n\n")
	case "chat":
		s.write("data: " + js(M{"error": shape}) + "\n\n")
		s.write("data: [DONE]\n\n")
	default:
		s.event("response.failed", M{"response": M{"id": s.id, "object": "response", "created_at": s.created, "model": s.model, "status": "failed", "output": s.completedOutput(), "error": shape}})
	}
	s.terminal = true
}
func (s *protocolStream) end() {
	close(s.done)
	s.cancel()
	s.worker.Wait()
	_ = s.writer.SetWriteDeadline(time.Time{})
}
