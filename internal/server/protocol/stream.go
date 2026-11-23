package protocol

import (
	"context"
	"io"
	"net/http"
	"sync"
	"time"
	"zcode2api/internal/server/fault"
	"zcode2api/internal/server/shared"
)

type chatStreamTool struct {
	index  int
	custom bool
}

type Stream struct {
	mu                  sync.Mutex
	w                   http.ResponseWriter
	writer              *http.ResponseController
	Ctx                 context.Context `json:"-"`
	cancel              context.CancelFunc
	protocol, id, model string
	created             int64
	sequence            int
	includeUsage        bool
	started             bool
	terminal            bool
	chatTools           map[int]chatStreamTool
	responses           *ResponseOutput
	toolCatalog         *ToolCatalog
	worker              sync.WaitGroup
	writeError          error
}

func NewStream(w http.ResponseWriter, r *http.Request, protocol, id, model string, include bool, tools *ToolCatalog) *Stream {
	ctx, cancel := context.WithCancel(r.Context())
	s := &Stream{w: w, Ctx: ctx, cancel: cancel, protocol: protocol, id: id, model: model, created: time.Now().Unix(), includeUsage: include, toolCatalog: tools}
	switch protocol {
	case "responses":
		s.responses = NewResponseOutput(tools)
		s.responses.emit = s.event
	case "chat":
		s.chatTools = map[int]chatStreamTool{}
	}
	s.writer = http.NewResponseController(w)
	w.Header().Set("X-Request-Id", id)
	return s
}

func (s *Stream) start() {
	if s.started {
		return
	}
	s.started = true
	h := s.w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("X-Accel-Buffering", "no")
	s.w.WriteHeader(http.StatusOK)
	if s.protocol == "chat" {
		s.chunk(shared.M{"role": "assistant", "content": ""}, nil)
	} else if s.protocol == "responses" {
		response := shared.M{"id": s.id, "object": "response", "created_at": s.created, "status": "in_progress", "model": s.model, "output": []any{}}
		s.event("response.created", shared.M{"response": response})
		s.event("response.in_progress", shared.M{"response": response})
	}
	s.worker.Go(func() {
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-s.Ctx.Done():
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
}

func (s *Stream) write(v string) {
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

func (s *Stream) chunk(delta shared.M, finish any) {
	chunk := shared.M{"id": s.id, "object": "chat.completion.chunk", "created": s.created, "model": s.model, "choices": []any{shared.M{"index": 0, "delta": delta, "finish_reason": finish}}}
	if s.includeUsage {
		chunk["usage"] = nil
	}
	s.write("data: " + shared.JSON(chunk) + "\n\n")
}

func (s *Stream) event(typ string, data shared.M) {
	m := shared.Merge(shared.M{"type": typ, "sequence_number": s.sequence}, data)
	s.sequence++
	s.write("event: " + typ + "\ndata: " + shared.JSON(m) + "\n\n")
}

func (s *Stream) OnEvent(e Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.terminal {
		return nil
	}
	if !s.started {
		if !e.StartsStream(s.protocol) {
			return nil
		}
		s.start()
	}
	if s.protocol == "anthropic" {
		if e.Type == "native_event" {
			s.write(e.Frame.Raw)
		}
		return s.writeError
	}
	if s.responses != nil {
		if err := s.responses.OnEvent(e); err != nil {
			return err
		}
		return s.writeError
	}
	switch e.Type {
	case "text_delta", "reasoning_delta":
		if e.HasText() {
			field := "content"
			if e.Type == "reasoning_delta" {
				field = "reasoning_content"
			}
			s.chunk(shared.M{field: e.Text}, nil)
		}
	case "tool_start":
		definition := s.toolCatalog.definition(e.Name)
		tool := chatStreamTool{index: len(s.chatTools), custom: definition.Kind == "custom"}
		s.chatTools[e.Index] = tool
		call := shared.M{"index": tool.index, "id": e.ID, "type": "function", "function": shared.M{"name": definition.Name, "arguments": ""}}
		if tool.custom {
			call = shared.M{"index": tool.index, "id": e.ID, "type": "custom", "custom": shared.M{"name": definition.Name, "input": ""}}
		}
		s.chunk(shared.M{"tool_calls": []any{call}}, nil)
	case "tool_delta":
		if tool, ok := s.chatTools[e.Index]; ok && !tool.custom {
			s.chunk(shared.M{"tool_calls": []any{shared.M{"index": tool.index, "function": shared.M{"arguments": e.Delta}}}}, nil)
		}
	case "tool_end":
		if tool, ok := s.chatTools[e.Index]; ok && tool.custom {
			input, err := customToolInput(e.Block["input"])
			if err != nil {
				return err
			}
			s.chunk(shared.M{"tool_calls": []any{shared.M{"index": tool.index, "custom": shared.M{"input": input}}}}, nil)
		}
	}
	return s.writeError
}

// FinalResponse closes outstanding output items. The gateway can now store the
// exact response, including its streamed IDs, before publishing the terminal event.
func (s *Stream) FinalResponse(r *Result) shared.M {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.start()
	return responsesResult(r, s.model, s.id, s.created, s.responses.Finish(r))
}

func (s *Stream) Complete(r *Result, response shared.M) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.start()
	s.terminal = true
	switch s.protocol {
	case "chat":
		s.chunk(shared.M{}, chatFinishReason(r))
		if s.includeUsage {
			s.write("data: " + shared.JSON(shared.M{"id": s.id, "object": "chat.completion.chunk", "created": s.created, "model": s.model, "choices": []any{}, "usage": chatUsage(r.Usage)}) + "\n\n")
		}
		s.write("data: [DONE]\n\n")
	case "responses":
		s.event("response."+shared.Str(response["status"]), shared.M{"response": response})
	}
}

// Fail reports whether the error belongs to an already-open SSE stream.
func (s *Stream) Fail(f *fault.Failure) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.started {
		return false
	}
	if s.terminal {
		return true
	}
	_, shape := ErrorShape(f, s.protocol)
	switch s.protocol {
	case "anthropic":
		s.write("event: error\ndata: " + shared.JSON(shared.M{"type": "error", "error": shape}) + "\n\n")
	case "chat":
		s.write("data: " + shared.JSON(shared.M{"error": shape}) + "\n\n")
		s.write("data: [DONE]\n\n")
	default:
		s.event("response.failed", shared.M{"response": shared.M{"id": s.id, "object": "response", "created_at": s.created, "model": s.model, "status": "failed", "output": s.responses.completedItems(), "error": shape}})
	}
	s.terminal = true
	return true
}

func (s *Stream) End() {
	s.cancel()
	s.worker.Wait()
	if s.started {
		_ = s.writer.SetWriteDeadline(time.Time{})
	}
}
