package test

import (
	"net/http"
	"strings"
	"testing"
)

func TestGatewayTemplateModes(t *testing.T) {
	const modeHeader = "X-Zcode-Template-Mode"
	const sessionHeader = "X-Zcode-Session-Type"
	const sourceHeader = "X-Zcode-Query-Source"
	const clientInstruction = "Reply with a short greeting."
	const subagentInstruction = "Use metric units for distances."
	const body = `{"model":"smoke-model","max_tokens":16,"system":"` + clientInstruction + `",
		"messages":[{"role":"user","content":[{"type":"text","text":"ping"}]}]}`

	token := fixtureAccountToken()
	observed := make(chan forwardedTemplate, 1)
	installUpstream(t, token, observed)
	s := startService(t, t.TempDir())
	admin := s.login(t)
	key := s.createKey(t, admin)
	s.importActiveAccount(t, admin, token)
	request(t, "PUT", s.admin+"/api/settings", admin,
		`{"zcode_subagent_insert_enabled":true,"zcode_subagent_template":"`+subagentInstruction+`"}`, 200)

	check := func(t *testing.T, headers http.Header, mode, source string) {
		t.Helper()
		raw, response := requestWithHeaders(t, "POST", s.gateway+"/v1/messages", key.Secret, body, headers, 200)
		reply := decode[struct{ Content []textBlock }](t, raw)
		if len(reply.Content) != 1 || reply.Content[0] != (textBlock{"text", "pong"}) {
			t.Fatalf("%s response content = %+v", mode, reply.Content)
		}
		if response.Get(modeHeader) != mode || response.Get(sessionHeader) != mode || response.Get(sourceHeader) != source {
			t.Errorf("gateway reported template %q, session %q, source %q; want %q, %q, %q",
				response.Get(modeHeader), response.Get(sessionHeader), response.Get(sourceHeader), mode, mode, source)
		}
		var sent forwardedTemplate
		select {
		case sent = <-observed:
		default:
			t.Fatal("gateway returned a response without forwarding the request")
		}
		if sent.SessionType != mode || sent.QuerySource != source {
			t.Errorf("upstream received session %q, source %q; want %q, %q", sent.SessionType, sent.QuerySource, mode, source)
		}
		blocks := 3
		if mode == "subagent" {
			blocks = 2
		}
		if len(sent.System) != blocks || sent.System[0].Type != "text" || !strings.HasPrefix(sent.System[0].Text, "You are ZCode") {
			t.Fatalf("upstream %s template has %d blocks or an unexpected identity block", mode, len(sent.System))
		}
		last := sent.System[len(sent.System)-1]
		if last.Type != "text" || !strings.Contains(last.Text, clientInstruction) {
			t.Errorf("%s template lost the client's system instruction", mode)
		}
		var custom bool
		for _, block := range sent.System {
			custom = custom || strings.Contains(block.Text, subagentInstruction)
		}
		if custom != (mode == "subagent") {
			t.Errorf("custom subagent instruction present = %t for %s", custom, mode)
		}
	}

	t.Run("default main", func(t *testing.T) {
		check(t, nil, "main", "main_turn")
	})
	t.Run("request selects subagent", func(t *testing.T) {
		check(t, http.Header{modeHeader: {"subagent"}}, "subagent", "subagent")
	})
	t.Run("saved subagent default", func(t *testing.T) {
		request(t, "PUT", s.admin+"/api/settings", admin, `{"zcode_template_mode":"subagent"}`, 200)
		check(t, nil, "subagent", "subagent")
		check(t, http.Header{modeHeader: {"main"}}, "main", "main_turn")
		check(t, http.Header{sessionHeader: {"main"}, sourceHeader: {"review"}}, "main", "review")
	})
	for _, tc := range []struct {
		name    string
		headers http.Header
	}{
		{"conflicting modes", http.Header{modeHeader: {"main"}, sessionHeader: {"subagent"}}},
		{"unknown template", http.Header{modeHeader: {"unknown"}}},
		{"unknown session type", http.Header{sessionHeader: {"unknown"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := requestWithHeaders(t, "POST", s.gateway+"/v1/messages", key.Secret, body, tc.headers, 400)
			reply := decode[struct{ Error struct{ Type string } }](t, raw)
			if reply.Error.Type != "invalid_request_error" {
				t.Errorf("unexpected Anthropic error: %s", raw)
			}
			select {
			case <-observed:
				t.Error("gateway forwarded an invalid template selection")
			default:
			}
		})
	}
}
