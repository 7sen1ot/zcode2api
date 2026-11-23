package test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

const upstreamSSE = `event: message_start
data: {"type":"message_start","message":{"id":"msg_fixture","type":"message","role":"assistant","model":"smoke-model","content":[],"usage":{"input_tokens":3}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"po"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ng"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}

event: message_stop
data: {"type":"message_stop"}

`

type textBlock struct{ Type, Text string }

type forwardedTemplate struct {
	SessionType, QuerySource string
	System                   []textBlock
}

func installUpstream(t *testing.T, token string, observed chan<- forwardedTemplate) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/zcode-plan/billing/balance", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"code":0,"data":{"balances":[{"total_units":100,"remaining_units":100,"capabilities":["model:smoke-model"]}]}}`)
	})
	mux.HandleFunc("POST /api/v1/zcode-plan/anthropic/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model    string
			Stream   bool
			System   []textBlock
			Messages []struct {
				Role    string
				Content []textBlock
			}
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("upstream received invalid JSON: %v", err)
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if body.Model != "smoke-model" || !body.Stream || len(body.Messages) != 1 {
			t.Errorf("upstream received model %q, stream %t, %d messages", body.Model, body.Stream, len(body.Messages))
		} else if msg := body.Messages[0]; msg.Role != "user" || len(msg.Content) != 1 || msg.Content[0] != (textBlock{"text", "ping"}) {
			t.Errorf("unexpected upstream user message: %+v", msg)
		}
		if observed != nil {
			select {
			case observed <- forwardedTemplate{r.Header.Get("X-Zcode-Session-Type"), r.Header.Get("X-Zcode-Query-Source"), body.System}:
			default:
				t.Error("gateway forwarded an extra inference request")
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, upstreamSSE)
	})
	serveLocalUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("upstream did not receive the imported account credential")
			http.Error(w, "invalid credential", http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	}))
}

func TestInferenceProtocols(t *testing.T) {
	token := fixtureAccountToken()
	installUpstream(t, token, nil)
	s := startService(t, t.TempDir())
	admin := s.login(t)
	key := s.createKey(t, admin)
	s.importActiveAccount(t, admin, token)
	request(t, "GET", s.gateway+"/readyz", "", "", 200)
	model := decode[struct{ ID string }](t, request(t, "GET", s.gateway+"/v1/models/smoke-model", key.Secret, "", 200))
	if model.ID != "smoke-model" {
		t.Fatalf("model discovery returned %q", model.ID)
	}

	const message = `"model":"smoke-model","messages":[{"role":"user","content":[{"type":"text","text":"ping"}]}]`
	completions := 0
	infer := func(t *testing.T, path, body string) []byte {
		t.Helper()
		raw := request(t, "POST", s.gateway+path, key.Secret, body, 200)
		completions++
		return raw
	}
	t.Run("Chat Completions", func(t *testing.T) {
		raw := infer(t, "/v1/chat/completions", "{"+message+"}")
		chat := decode[struct {
			Choices []struct {
				Message struct{ Role, Content string }
				Finish  string `json:"finish_reason"`
			}
		}](t, raw)
		if len(chat.Choices) != 1 || chat.Choices[0].Message.Role != "assistant" || chat.Choices[0].Message.Content != "pong" || chat.Choices[0].Finish != "stop" {
			t.Fatalf("unexpected chat completion: %s", raw)
		}
	})
	t.Run("Responses", func(t *testing.T) {
		raw := infer(t, "/v1/responses", `{"model":"smoke-model","input":"ping"}`)
		type response struct {
			ID, Status string
			Text       string `json:"output_text"`
		}
		answer := decode[response](t, raw)
		if answer.ID == "" || answer.Status != "completed" || answer.Text != "pong" {
			t.Fatalf("unexpected response: %s", raw)
		}
		saved := decode[response](t, request(t, "GET", s.gateway+"/v1/responses/"+answer.ID, key.Secret, "", 200))
		if saved != answer {
			t.Fatalf("stored response differs: %+v", saved)
		}
	})
	t.Run("Anthropic Messages", func(t *testing.T) {
		raw := infer(t, "/v1/messages", "{"+message+`,"max_tokens":16}`)
		answer := decode[struct {
			Type, Role string
			Content    []textBlock
			Stop       string `json:"stop_reason"`
		}](t, raw)
		if answer.Type != "message" || answer.Role != "assistant" || answer.Stop != "end_turn" || len(answer.Content) != 1 || answer.Content[0] != (textBlock{"text", "pong"}) {
			t.Fatalf("unexpected Anthropic message: %s", raw)
		}
	})
	t.Run("Chat Completions stream", func(t *testing.T) {
		raw := infer(t, "/v1/chat/completions", "{"+message+`,"stream":true}`)
		var content strings.Builder
		var done bool
		for line := range strings.SplitSeq(string(raw), "\n") {
			data, ok := strings.CutPrefix(strings.TrimSpace(line), "data:")
			if !ok {
				continue
			}
			if done {
				t.Fatal("received data after [DONE]")
			}
			if strings.TrimSpace(data) == "[DONE]" {
				done = true
				continue
			}
			chunk := decode[struct {
				Choices []struct{ Delta struct{ Content string } }
			}](t, []byte(data))
			for _, choice := range chunk.Choices {
				content.WriteString(choice.Delta.Content)
			}
		}
		if !done || content.String() != "pong" {
			t.Fatalf("chat stream produced %q, done=%t", content.String(), done)
		}
	})
	raw := request(t, "GET", s.gateway+"/v1/usage", key.Secret, "", 200)
	usage := decode[struct {
		Used int `json:"used_units"`
	}](t, raw)
	if want := completions * (3 + 2); usage.Used != want {
		t.Fatalf("used_units = %d, want %d for %d completions", usage.Used, want, completions)
	}
}
