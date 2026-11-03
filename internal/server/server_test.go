package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"
)

func testApp(t *testing.T) *App {
	t.Helper()
	root := t.TempDir()
	must(t, os.WriteFile(filepath.Join(root, "config.json"), []byte(`{"web_port":0,"gateway_port":0}`), 0600))
	a, e := New(root, filepath.Join(root, "data"), fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("console")}})
	if e != nil {
		t.Fatal(e)
	}
	a.log.SetOutput(io.Discard)
	t.Cleanup(a.Close)
	return a
}
func must(t *testing.T, e error) {
	t.Helper()
	if e != nil {
		t.Fatal(e)
	}
}
func equal(t *testing.T, got, want any) {
	t.Helper()
	if js(got) != js(want) {
		t.Fatalf("got %s; want %s", js(got), js(want))
	}
}
func testPool(t *testing.T, a *App, n int) (M, string, []M) {
	t.Helper()
	key, e := a.createKey(M{"name": "test", "stickyMinutes": 10})
	must(t, e)
	k := a.row("SELECT * FROM keys WHERE id=?", key["id"])
	accounts := []M{}
	for i := 0; i < n; i++ {
		r, e := a.insertAccount(M{"label": fmt.Sprintf("test-%d", i), "family": "bigmodel", "status": "active", "source": "test", "customer_id": fmt.Sprint(i)}, M{"zcode_jwt": fmt.Sprintf("synthetic-%d", i)})
		must(t, e)
		accounts = append(accounts, r)
	}
	return k, str(key["secret"]), accounts
}
func syntheticEvents(tool bool) []any {
	events := []any{
		M{"type": "message_start", "message": M{"id": "msg_upstream", "type": "message", "role": "assistant", "model": "m", "content": []any{}, "usage": M{"input_tokens": 11, "cache_read_input_tokens": 9}}},
		M{"type": "content_block_start", "index": 0, "content_block": M{"type": "thinking", "thinking": "", "signature": ""}},
		M{"type": "content_block_delta", "index": 0, "delta": M{"type": "thinking_delta", "thinking": "思考"}},
		M{"type": "content_block_delta", "index": 0, "delta": M{"type": "signature_delta", "signature": "original-signature"}},
		M{"type": "content_block_stop", "index": 0},
	}
	stop := "end_turn"
	if tool {
		stop = "tool_use"
		events = append(events, M{"type": "content_block_start", "index": 1, "content_block": M{"type": "tool_use", "id": "call_real", "name": "lookup", "input": M{}}}, M{"type": "content_block_delta", "index": 1, "delta": M{"type": "input_json_delta", "partial_json": "{\"value\":"}}, M{"type": "content_block_delta", "index": 1, "delta": M{"type": "input_json_delta", "partial_json": "\"中文\"}"}}, M{"type": "content_block_stop", "index": 1})
	} else {
		events = append(events, M{"type": "content_block_start", "index": 1, "content_block": M{"type": "text", "text": ""}}, M{"type": "content_block_delta", "index": 1, "delta": M{"type": "text_delta", "text": "你好"}}, M{"type": "content_block_stop", "index": 1})
	}
	return append(events, M{"type": "message_delta", "delta": M{"stop_reason": stop}, "usage": M{"output_tokens": 7}}, M{"type": "message_stop"})
}
func wire(events []any) string {
	var b strings.Builder
	for _, v := range events {
		fmt.Fprintf(&b, "event: %s\r\ndata: %s\r\n\r\n", obj(v)["type"], js(v))
	}
	return b.String()
}
func installUpstream(t *testing.T, a *App, h http.HandlerFunc) {
	t.Helper()
	s := httptest.NewServer(h)
	a.origin = s.URL
	t.Cleanup(s.Close)
}
func baseBody(protocol string, stream bool) M {
	b := M{"model": "m", "stream": stream, "messages": []any{M{"role": "user", "content": "hello"}}}
	if protocol == "responses" {
		delete(b, "messages")
		b["input"] = "hello"
	}
	return b
}
func route(protocol string) string {
	switch protocol {
	case "anthropic":
		return "/v1/messages"
	case "responses":
		return "/v1/responses"
	default:
		return "/v1/chat/completions"
	}
}
func request(h http.Handler, method, path, token string, body any) *httptest.ResponseRecorder {
	var reader io.Reader
	if body != nil {
		reader = strings.NewReader(js(body))
	}
	r := httptest.NewRequest(method, path, reader)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestNodeCompatibilityFixtures(t *testing.T) {
	b, e := os.ReadFile("testdata/node-compatibility.json")
	must(t, e)
	f := obj(parse(string(b), nil))
	decrypted, e := decrypt(str(f["encrypted"]), str(f["secret"]))
	must(t, e)
	equal(t, decrypted, f["plaintext"])
	for _, v := range arr(f["cases"]) {
		c := obj(v)
		t.Run(str(c["protocol"]), func(t *testing.T) {
			original := js(c["body"])
			got, e := buildRequest(obj(c["body"]), str(c["protocol"]), "m", "max")
			must(t, e)
			equal(t, got, c["expected"])
			equal(t, js(c["body"]), original)
		})
	}
	for _, s := range []string{"", "中文", "synthetic"} {
		enc, e := encrypt(s, "key")
		must(t, e)
		dec, e := decrypt(enc, "key")
		must(t, e)
		equal(t, dec, s)
		if _, e = decrypt(enc, "wrong-key"); e == nil {
			t.Fatal("wrong key accepted")
		}
	}
	if _, e = decrypt("enc:v1:YQ.Yg.Yw", "key"); e == nil {
		t.Fatal("invalid IV accepted")
	}
}
func TestLegacyDatabaseAndConfigMigration(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "data")
	must(t, os.MkdirAll(data, 0700))
	must(t, os.WriteFile(filepath.Join(root, "config.json"), []byte(`{"inference_driver":"runtime","runtime_bin":"nonexistent","web_port":0,"http":{"first_token_timeout_ms":3210}}`), 0600))
	fixture, _ := os.ReadFile("testdata/node-compatibility.json")
	f := obj(parse(string(fixture), nil))
	must(t, os.WriteFile(filepath.Join(data, "secret.key"), []byte(str(f["secret"])), 0600))
	d, e := sql.Open("sqlite", filepath.Join(data, "zcode2api.db"))
	must(t, e)
	_, e = d.Exec("CREATE TABLE settings(k TEXT PRIMARY KEY,v TEXT NOT NULL);CREATE TABLE usage_log(id INTEGER PRIMARY KEY,request_id TEXT NOT NULL,ts INTEGER NOT NULL,key_id TEXT,client_ip TEXT,account_id TEXT,family TEXT,model TEXT,reasoning_level TEXT,stream INTEGER,attempt_count INTEGER,rotated_accounts TEXT,status TEXT NOT NULL,error_code TEXT,error_message TEXT,ttft_ms INTEGER,total_ms INTEGER,tokens_in INTEGER,tokens_out INTEGER,tokens_reasoning INTEGER,cache_read INTEGER,cache_write INTEGER,prompt_hash TEXT);")
	must(t, e)
	must(t, d.Close())
	a, e := New(root, data, fstest.MapFS{})
	must(t, e)
	defer a.Close()
	equal(t, a.config()["inference_driver"], "http")
	equal(t, obj(a.config()["http"])["first_token_timeout_ms"], 3210)
	r, e := a.insertAccount(M{"family": "bigmodel", "status": "active", "customer_id": "27001793154019774"}, M{})
	must(t, e)
	must(t, a.updateAccount(str(r["id"]), M{"enc_tokens": M{"zcode_jwt": f["encrypted"]}}))
	equal(t, a.tokens(str(r["id"]))["zcode_jwt"], f["plaintext"])
	key, e := a.createKey(M{"name": "retained", "quotaLimit": 1000})
	must(t, e)
	must(t, a.exec("UPDATE keys SET used_units=123 WHERE id=?", key["id"]))
	equal(t, a.row("SELECT used_units FROM keys WHERE id=?", key["id"])["used_units"], 123)
	cols := a.rows("PRAGMA table_info(usage_log)")
	found := false
	for _, v := range cols {
		if obj(v)["name"] == "timings" {
			found = true
		}
	}
	if !found {
		t.Fatal("missing migrated timings")
	}
	claims := jwtPayload("x." + base64.RawURLEncoding.EncodeToString([]byte(`{"user_id":27001793154019774}`)) + ".x")
	equal(t, str(claims["user_id"]), "27001793154019774")
}
func TestInvalidRequestsBeforeStreaming(t *testing.T) {
	a := testApp(t)
	_, secret, _ := testPool(t, a, 0)
	for _, protocol := range []string{"chat", "anthropic", "responses"} {
		for _, b := range []M{{"stream": true}, merge(baseBody(protocol, true), M{"tools": []any{M{"type": "web_search"}}}), merge(baseBody(protocol, true), M{"n": 2})} {
			w := request(a.GatewayHandler(), "POST", route(protocol), secret, b)
			if w.Code != 400 || strings.Contains(w.Header().Get("Content-Type"), "event-stream") {
				t.Fatalf("invalid accepted: %d %s", w.Code, w.Body.String())
			}
		}
	}
	b := baseBody("anthropic", false)
	b["system"] = []any{M{"type": "text", "text": "a", "cache_control": M{"type": "ephemeral", "ttl": "5m"}}, M{"type": "text", "text": "b", "cache_control": M{"type": "ephemeral", "ttl": "1h"}}}
	if _, e := buildRequest(b, "anthropic", "m", "max"); e == nil {
		t.Fatal("conflicting cache controls accepted")
	}
}
func TestThreeProtocolsStreamingAndJSON(t *testing.T) {
	for _, protocol := range []string{"chat", "anthropic", "responses"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%t", protocol, stream), func(t *testing.T) {
				a := testApp(t)
				_, secret, _ := testPool(t, a, 1)
				installUpstream(t, a, func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("X-Zcode-Session-Type") != "main" {
						t.Error("missing profile")
					}
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, wire(syntheticEvents(true)))
				})
				b := baseBody(protocol, stream)
				b["stream_options"] = M{"include_usage": true}
				w := request(a.GatewayHandler(), "POST", route(protocol), secret, b)
				if w.Code != 200 {
					t.Fatalf("%d %s", w.Code, w.Body.String())
				}
				if !strings.Contains(w.Body.String(), "思考") || !strings.Contains(w.Body.String(), "call_real") {
					t.Fatal(w.Body.String())
				}
				if stream {
					if protocol == "responses" && !strings.Contains(w.Body.String(), "response.completed") {
						t.Fatal(w.Body.String())
					}
					if protocol == "chat" {
						if !strings.Contains(w.Body.String(), "[DONE]") {
							t.Fatal("missing DONE")
						}
						frames := []M{}
						_ = readSSE(strings.NewReader(w.Body.String()), func(f Frame) error {
							if f.Data != "[DONE]" {
								frames = append(frames, obj(parse(f.Data, nil)))
							}
							return nil
						})
						starts := 0
						for _, f := range frames {
							choices := arr(f["choices"])
							if len(choices) > 0 {
								calls := arr(obj(obj(choices[0])["delta"])["tool_calls"])
								if len(calls) > 0 && truth(obj(calls[0])["id"]) {
									starts++
								}
							}
						}
						equal(t, starts, 1)
					}
				} else {
					j := obj(parse(w.Body.String(), nil))
					if protocol == "anthropic" {
						equal(t, obj(arr(j["content"])[0])["signature"], "original-signature")
					} else {
						u := obj(j["usage"])
						equal(t, u["total_tokens"], 27)
					}
				}
				row := a.row("SELECT * FROM usage_log ORDER BY id DESC LIMIT 1")
				equal(t, row["status"], "success")
				equal(t, row["tokens_in"], 20)
				equal(t, row["tokens_out"], 7)
				equal(t, a.row("SELECT used_units FROM keys")["used_units"], 27)
			})
		}
	}
}

type byteReader struct{ b []byte }

func (r *byteReader) Read(p []byte) (int, error) {
	if len(r.b) == 0 {
		return 0, io.EOF
	}
	p[0] = r.b[0]
	r.b = r.b[1:]
	return 1, nil
}
func TestSSEFragmentationAndMultiline(t *testing.T) {
	frames := []Frame{}
	e := readSSE(&byteReader{[]byte("event: custom\r\ndata: {\"text\":\r\ndata: \"中文\"}\r\n\r\n")}, func(f Frame) error { frames = append(frames, f); return nil })
	must(t, e)
	equal(t, len(frames), 1)
	equal(t, parse(frames[0].Data, nil), M{"text": "中文"})
}
func TestResponsesHistoryIsolationAndDeletion(t *testing.T) {
	a := testApp(t)
	_, secret, _ := testPool(t, a, 1)
	_, other, _ := testPool(t, a, 0)
	var seen M
	installUpstream(t, a, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		seen = obj(parse(string(b), nil))
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, wire(syntheticEvents(false)))
	})
	h := a.GatewayHandler()
	b := baseBody("responses", true)
	b["instructions"] = "old instructions"
	w := request(h, "POST", "/v1/responses", secret, b)
	var id string
	must(t, readSSE(strings.NewReader(w.Body.String()), func(f Frame) error {
		m := obj(parse(f.Data, nil))
		if str(m["type"]) == "response.completed" {
			id = str(obj(m["response"])["id"])
		}
		return nil
	}))
	if id == "" {
		t.Fatal(w.Body.String())
	}
	w = request(h, "GET", "/v1/responses/"+id, secret, nil)
	equal(t, w.Code, 200)
	equal(t, obj(parse(w.Body.String(), nil))["id"], id)
	next := M{"model": "m", "input": "next", "previous_response_id": id}
	equal(t, request(h, "POST", "/v1/responses", other, next).Code, 404)
	equal(t, request(h, "POST", "/v1/responses", secret, next).Code, 200)
	messages := arr(seen["messages"])
	if len(messages) < 3 {
		t.Fatal(js(seen))
	}
	if strings.Contains(js(seen["system"]), "old instructions") {
		t.Fatal("instructions implicitly inherited")
	}
	equal(t, request(h, "DELETE", "/v1/responses/"+id, other, nil).Code, 200)
	equal(t, request(h, "GET", "/v1/responses/"+id, secret, nil).Code, 200)
	request(h, "DELETE", "/v1/responses/"+id, secret, nil)
	equal(t, request(h, "POST", "/v1/responses", secret, next).Code, 404)
	w = request(h, "POST", "/v1/responses", secret, M{"input": "private", "store": false})
	transient := str(obj(parse(w.Body.String(), nil))["id"])
	equal(t, request(h, "GET", "/v1/responses/"+transient, secret, nil).Code, 404)
}
func TestImmediateRotationAndNoReplay(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		code   any
	}{
		{"rate-limit", 429, 429},
		{"busy", 429, 3008},
		{"concurrency-3009", 429, 3009},
		{"concurrency-3010", 429, 3010},
		{"overload-1312", 429, 1312},
		{"quota", 429, 3103},
		{"http-503", 503, nil},
		{"http-529", 529, nil},
		{"provider-503", 503, 503},
		{"provider-529", 529, 529},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := testApp(t)
			_, secret, accounts := testPool(t, a, 2)
			a.mu.Lock()
			a.sticky[str(a.row("SELECT id FROM keys")["id"])] = stickyEntry{str(accounts[0]["id"]), nowMS()}
			a.mu.Unlock()
			a.configMu.Lock()
			obj(a.cfg["retry"])["max_attempts"] = 1
			a.configMu.Unlock()
			calls := []string{}
			installUpstream(t, a, func(w http.ResponseWriter, r *http.Request) {
				if !strings.Contains(r.URL.Path, "/anthropic/") {
					writeJSON(w, 200, M{"code": 0, "data": M{}})
					return
				}
				token := r.Header.Get("X-Api-Key")
				calls = append(calls, token)
				if token == "synthetic-0" {
					writeJSON(w, tc.status, M{"code": tc.code, "message": "limited"})
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, wire(syntheticEvents(false)))
			})
			start := time.Now()
			w := request(a.GatewayHandler(), "POST", "/v1/chat/completions", secret, baseBody("chat", false))
			equal(t, w.Code, 200)
			if time.Since(start) > time.Second {
				t.Fatal("did not rotate immediately")
			}
			equal(t, calls, []any{"synthetic-0", "synthetic-1"})
			for _, r := range accounts {
				a.mu.Lock()
				inflight := a.state(str(r["id"])).Inflight
				a.mu.Unlock()
				if inflight != 0 {
					t.Fatal("slot leaked")
				}
			}
		})
	}
	for _, protocol := range []string{"chat", "anthropic", "responses"} {
		t.Run("no-replay/"+protocol, func(t *testing.T) {
			a := testApp(t)
			_, secret, _ := testPool(t, a, 2)
			var calls atomic.Int32
			installUpstream(t, a, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, wire(syntheticEvents(false)[:3]))
				fmt.Fprint(w, wire([]any{M{"type": "error", "error": M{"type": "rate_limit_error", "message": "limited"}}}))
			})
			w := request(a.GatewayHandler(), "POST", route(protocol), secret, baseBody(protocol, true))
			equal(t, calls.Load(), 1)
			if strings.Contains(w.Body.String(), "response.completed") {
				t.Fatal("failure completed")
			}
		})
	}
}
func TestAnthropicPreludeCanRotateAndTruncationFails(t *testing.T) {
	for _, protocol := range []string{"chat", "anthropic", "responses"} {
		t.Run(protocol, func(t *testing.T) {
			a := testApp(t)
			_, secret, _ := testPool(t, a, 2)
			calls := 0
			installUpstream(t, a, func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Content-Type", "text/event-stream")
				if calls == 1 {
					fmt.Fprint(w, wire([]any{syntheticEvents(false)[0], M{"type": "error", "error": M{"type": "rate_limit_error", "message": "limited"}}}))
					return
				}
				fmt.Fprint(w, wire(syntheticEvents(false)))
			})
			w := request(a.GatewayHandler(), "POST", route(protocol), secret, baseBody(protocol, true))
			equal(t, calls, 2)
			if strings.Contains(w.Body.String(), "limited") {
				t.Fatal("prelude leaked from failed attempt")
			}
		})
	}
	a := testApp(t)
	_, _, accounts := testPool(t, a, 1)
	installUpstream(t, a, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, wire(syntheticEvents(false)[:3]))
	})
	up, e := buildRequest(baseBody("anthropic", false), "anthropic", "m", "max")
	must(t, e)
	_, f := a.inferHTTP(context.Background(), accounts[0], inferOptions{Upstream: up, Protocol: "anthropic", Stream: true, OnEvent: func(Event) error { return nil }})
	if f == nil || !f.Committed {
		t.Fatal("truncation accepted or marked replayable")
	}
}
func TestTimeoutsAndClientCancellation(t *testing.T) {
	for _, kind := range []string{"first", "idle", "total", "cancel", "admission"} {
		t.Run(kind, func(t *testing.T) {
			a := testApp(t)
			_, _, accounts := testPool(t, a, 1)
			a.configMu.Lock()
			h := obj(a.cfg["http"])
			h["first_token_timeout_ms"], h["idle_timeout_ms"] = 1000, 1000
			switch kind {
			case "first":
				h["first_token_timeout_ms"] = 30
			case "idle":
				h["idle_timeout_ms"] = 30
			case "total":
				h["total_timeout_ms"] = 30
			}
			a.configMu.Unlock()
			cancelled := make(chan struct{})
			installUpstream(t, a, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(200)
				w.(http.Flusher).Flush()
				<-r.Context().Done()
				close(cancelled)
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if kind == "cancel" {
				time.AfterFunc(30*time.Millisecond, cancel)
			}
			up, _ := buildRequest(baseBody("chat", false), "chat", "m", "max")
			o := inferOptions{Upstream: up, Protocol: "chat"}
			if kind == "admission" {
				o.Admission = 30 * time.Millisecond
			}
			start := time.Now()
			_, f := a.inferHTTP(ctx, accounts[0], o)
			if f == nil || time.Since(start) > time.Second {
				t.Fatal("timeout did not fire")
			}
			want := "timeout"
			if kind == "cancel" {
				want = "canceled"
			}
			if kind == "admission" {
				want = "rate_limit"
			}
			equal(t, f.Kind, want)
			wantCode := map[string]string{"first": "first_token_timeout", "idle": "stream_idle_timeout", "total": "inference_timeout", "cancel": "canceled", "admission": "throttle_window_exhausted"}[kind]
			equal(t, f.Code, wantCode)
			select {
			case <-cancelled:
			case <-time.After(time.Second):
				t.Fatal("upstream not cancelled")
			}
		})
	}
}
func TestLongOutputOutlivesRetryBudget(t *testing.T) {
	a := testApp(t)
	_, secret, _ := testPool(t, a, 1)
	a.configMu.Lock()
	obj(a.cfg["retry"])["total_budget_ms"] = 1
	obj(a.cfg["http"])["first_token_timeout_ms"] = 2000
	obj(a.cfg["http"])["idle_timeout_ms"] = 500
	a.configMu.Unlock()
	installUpstream(t, a, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, v := range syntheticEvents(false) {
			fmt.Fprint(w, wire([]any{v}))
			w.(http.Flusher).Flush()
			time.Sleep(5 * time.Millisecond)
		}
	})
	equal(t, request(a.GatewayHandler(), "POST", "/v1/chat/completions", secret, baseBody("chat", false)).Code, 200)
}
func TestPoolThrottleWindowAndRetryAfter(t *testing.T) {
	if testing.Short() {
		t.Skip("real 10-second retry window")
	}
	a := testApp(t)
	_, secret, _ := testPool(t, a, 2)
	var calls atomic.Int32
	installUpstream(t, a, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "30")
		writeJSON(w, 429, M{"code": 1302, "message": "limited"})
	})
	start := time.Now()
	w := request(a.GatewayHandler(), "POST", "/v1/chat/completions", secret, baseBody("chat", false))
	elapsed := time.Since(start)
	equal(t, w.Code, 429)
	equal(t, calls.Load(), 2)
	if elapsed < 9900*time.Millisecond || elapsed > 12*time.Second {
		t.Fatalf("wrong window %s", elapsed)
	}
	retry := integer(w.Header().Get("Retry-After"))
	if retry < 19 || retry > 21 {
		t.Fatalf("wrong Retry-After %d", retry)
	}
}
func TestAllPoolRetryUnlimitedAndCancel(t *testing.T) {
	a := testApp(t)
	_, secret, _ := testPool(t, a, 2)
	a.configMu.Lock()
	obj(a.cfg["retry"])["max_attempts"] = 1
	obj(a.cfg["retry"])["rate_limit_retry_interval_ms"] = 50
	a.configMu.Unlock()
	var calls atomic.Int32
	installUpstream(t, a, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) <= 12 {
			writeJSON(w, 429, M{"code": 3008})
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, wire(syntheticEvents(false)))
	})
	w := request(a.GatewayHandler(), "POST", "/v1/chat/completions", secret, baseBody("chat", false))
	equal(t, w.Code, 200)
	if calls.Load() <= 10 {
		t.Fatal("max_attempts applied to throttle")
	}
}
func TestSchedulerConcurrentQueueAndBackup(t *testing.T) {
	a := testApp(t)
	k, _, accounts := testPool(t, a, 1)
	o := inferOptions{Key: k, Model: "m"}
	r, _, _, _ := a.selectAccount(o, map[string]bool{})
	equal(t, r["id"], accounts[0]["id"])
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, _, _, e := a.acquire(ctx, o, map[string]bool{}, time.Second); e == nil {
		t.Fatal("cancel ignored")
	}
	a.mu.Lock()
	if a.state(str(r["id"])).Inflight != 1 {
		t.Fatal("cancel released another request")
	}
	a.mu.Unlock()
	for range 2 {
		selected, _, _, _ := a.selectAccount(o, nil)
		if selected == nil || selected["id"] != accounts[0]["id"] {
			t.Fatal("primary account did not accept three requests")
		}
	}
	must(t, a.exec("INSERT INTO groups(id,name,role,created_at) VALUES('backup','backup','backup',?)", nowMS()))
	backup, e := a.insertAccount(M{"family": "zai", "status": "active", "group_id": "backup"}, M{})
	must(t, e)
	r2, _, _, _ := a.selectAccount(o, map[string]bool{})
	equal(t, r2["id"], backup["id"])
	a.release(str(r2["id"]))
	for range 3 {
		a.release(str(r["id"]))
	}
	var wg sync.WaitGroup
	inflight := map[string]int{}
	var mu sync.Mutex
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, _, _, _, e := a.acquire(context.Background(), o, map[string]bool{}, 2*time.Second)
			if e != nil || r == nil {
				t.Error("failed to acquire")
				return
			}
			id := str(r["id"])
			mu.Lock()
			inflight[id]++
			if inflight[id] > 3 {
				t.Error("more than three concurrent requests on same account")
			}
			mu.Unlock()
			time.Sleep(time.Millisecond)
			mu.Lock()
			inflight[id]--
			mu.Unlock()
			a.release(id)
		}()
	}
	wg.Wait()
}
func adminToken(t *testing.T, a *App) string {
	t.Helper()
	w := request(a.AdminHandler(), "POST", "/api/auth/login", "", M{"password": a.config()["admin_password"]})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	return str(obj(parse(w.Body.String(), nil))["token"])
}
func TestAdminRoutesAndSettingsPersistence(t *testing.T) {
	a := testApp(t)
	h := a.AdminHandler()
	equal(t, request(h, "GET", "/api/accounts", "", nil).Code, 401)
	equal(t, request(h, "POST", "/api/auth/login", "", M{"password": "wrong"}).Code, 401)
	token := adminToken(t, a)
	for _, p := range []string{"/api/accounts", "/api/runtime-info", "/api/dashboard", "/api/groups", "/api/keys", "/api/settings", "/api/usage/stats", "/api/usage/logs", "/api/performance", "/api/schedule-logs", "/api/claims/history", "/api/audit", "/api/accounts/oauth/pending", "/api/accounts/oauth/watch"} {
		w := request(h, "GET", p, token, nil)
		if w.Code != 200 {
			t.Fatalf("%s: %s", p, w.Body.String())
		}
	}
	equal(t, request(h, "PUT", "/api/settings", token, M{"http": M{"first_token_timeout_ms": 0}}).Code, 400)
	equal(t, request(h, "PUT", "/api/settings", token, M{"http": M{"first_token_timeout_ms": 12345}, "inference_driver": "http"}).Code, 200)
	equal(t, obj(a.setting("config_overrides", nil))["http"], a.config()["http"])
	key := obj(parse(request(h, "POST", "/api/keys", token, M{"name": "managed"}).Body.String(), nil))
	id := str(key["id"])
	request(h, "PATCH", "/api/keys/"+id, token, M{"enabled": false, "model_whitelist": []any{"m"}, "default_level": "max"})
	row := a.row("SELECT * FROM keys WHERE id=?", id)
	equal(t, row["enabled"], 0)
	equal(t, row["model_whitelist"], `["m"]`)
	if strings.Contains(request(h, "GET", "/api/keys", token, nil).Body.String(), "key_hash") {
		t.Fatal("hash disclosed")
	}
	request(h, "DELETE", "/api/keys/"+id, token, nil)
	if a.row("SELECT * FROM keys WHERE id=?", id) != nil {
		t.Fatal("key not deleted")
	}
	request(h, "POST", "/api/auth/logout", token, nil)
	equal(t, request(h, "GET", "/api/accounts", token, nil).Code, 401)
}
func TestModelAuthAndAffinity(t *testing.T) {
	a := testApp(t)
	k, secret, accounts := testPool(t, a, 1)
	must(t, a.updateAccount(str(accounts[0]["id"]), M{"capabilities": []any{"GLM-5.3-Flash"}}))
	equal(t, request(a.GatewayHandler(), "GET", "/v1/v1/models", secret, nil).Code, 200)
	equal(t, request(a.GatewayHandler(), "GET", "/v1/models/glm-flash", secret, nil).Code, 200)
	must(t, a.update("keys", "id", str(k["id"]), M{"ip_whitelist": []any{"10.0.0.0/8"}}, "ip_whitelist"))
	equal(t, request(a.GatewayHandler(), "GET", "/v1/models", secret, nil).Code, 403)
	if !ipAllowed("10.0.0.1", []any{"10.0.0.0/8"}) || ipAllowed("11.0.0.1", []any{"10.0.0.0/8"}) {
		t.Fatal("CIDR invalid")
	}
	r := httptest.NewRequest("POST", "/", nil)
	r.Header.Set("X-Session-Id", "session-a")
	x, _ := a.affinity(r, M{}, k)
	y, _ := a.affinity(r, M{}, M{"id": "other"})
	if x == y {
		t.Fatal("cross-key affinity")
	}
	for raw, want := range map[string]string{"xhigh": "max", "minimal": "low", "medium": "high"} {
		equal(t, effort(M{"reasoning_effort": raw}), want)
	}
}
func TestClaimDedupAndStrictSuccess(t *testing.T) {
	a := testApp(t)
	_, _, accounts := testPool(t, a, 1)
	var calls atomic.Int32
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var status atomic.Int32
	status.Store(200)
	installUpstream(t, a, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/claim") {
			calls.Add(1)
			select {
			case started <- struct{}{}:
			default:
			}
			<-release
			writeJSON(w, int(status.Load()), M{"code": 0, "data": M{"plan": M{"user_plan_id": "claimed"}}})
			return
		}
		writeJSON(w, 200, M{"code": 0, "data": M{}})
	})
	id := str(accounts[0]["id"])
	done := make(chan M)
	go func() {
		r, e := a.claim(context.Background(), id, "card", "manual", nil)
		if e != nil {
			t.Error(e)
		}
		done <- r
	}()
	<-started
	r, e := a.claim(context.Background(), id, "card", "manual", nil)
	must(t, e)
	equal(t, r["code"], "in_progress")
	close(release)
	equal(t, (<-done)["ok"], true)
	equal(t, calls.Load(), 1)
	status.Store(500)
	r, e = a.claim(context.Background(), id, "other", "manual", nil)
	must(t, e)
	equal(t, r["ok"], false)
}
func TestBalanceSummaryExpiredAndPending(t *testing.T) {
	a := testApp(t)
	_, _, accounts := testPool(t, a, 1)
	n := time.Now().Unix()
	d := M{"plans": []any{M{"user_plan_id": "active", "plan_id": "start-plan", "status": "active", "ends_at": n + 3600}, M{"user_plan_id": "expired", "status": "active", "ends_at": n - 1}, M{"status": "active", "plan_id": "future", "entitlements": []any{M{"entitlement_id": "future", "grant_units": 300000000, "effective_at": n + 7200}}}}, "balances": []any{M{"user_plan_id": "active", "show_name": "GLM-5.3", "total_units": 3000, "used_units": 100, "remaining_units": 2900, "capabilities": []any{"model:GLM-5.3"}}, M{"user_plan_id": "expired", "remaining_units": 99999}}}
	installUpstream(t, a, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, M{"code": 0, "data": d}) })
	bal, e := a.fetchBalance(context.Background(), accounts[0])
	must(t, e)
	equal(t, len(arr(bal["balances"])), 1)
	a.applyBalance(accounts[0], bal, true)
	s := a.summary()
	equal(t, s["totalRemaining"], 2900)
	equal(t, s["totalPending"], 300000000)
	equal(t, len(arr(obj(arr(s["accounts"])[0])["pending_buckets"])), 1)
}
func TestOAuthIdentityAndRecovery(t *testing.T) {
	a := testApp(t)
	_, _, accounts := testPool(t, a, 1)
	id := str(accounts[0]["id"])
	must(t, a.updateAccount(id, M{"customer_id": "27001793154019774"}))
	jwt := func(id string) string {
		return "x." + base64.RawURLEncoding.EncodeToString([]byte(`{"user_id":"`+id+`"}`)) + ".x"
	}
	installUpstream(t, a, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, M{"code": 0, "data": M{}}) })
	flow := M{"provider": "bigmodel", "account_rowid": id}
	data := M{"token": jwt("wrong"), "bigmodel": M{"access_token": "synthetic-business"}}
	if _, e := a.completeOAuth(context.Background(), flow, data); e == nil {
		t.Fatal("identity mismatch accepted")
	}
	equal(t, a.tokens(id)["zcode_jwt"], "synthetic-0")
	data["token"] = jwt("27001793154019774")
	res, e := a.completeOAuth(context.Background(), flow, data)
	must(t, e)
	equal(t, res["id"], id)
	equal(t, res["updated"], true)
	equal(t, a.tokens(id)["zcode_jwt"], data["token"])
	equal(t, len(a.rows("SELECT id FROM accounts")), 1)
}
func TestUsageNullCountersAndPerformance(t *testing.T) {
	a := testApp(t)
	must(t, a.exec("INSERT INTO usage_log(request_id,ts,key_id,status,tokens_in,tokens_out,ttft_ms,timings) VALUES('one',?,'key','success',100,NULL,12,?)", nowMS(), js(M{"ttft_ms": 12, "first_text_ms": nil, "total_ms": 100})))
	r := httptest.NewRequest("GET", "/api/usage/stats?hours=168", nil)
	s := a.usageStats(r)
	equal(t, s["time_unit"], "day")
	equal(t, obj(arr(s["byKey"])[0])["tokens"], 100)
	p := a.performance(100)
	equal(t, obj(obj(p["metrics"])["first_text_ms"])["samples"], 0)
}
func TestHTTPResponseBodyStallTimeout(t *testing.T) {
	a := testApp(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	installUpstream(t, a, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	start := time.Now()
	_, e := a.api(ctx, nil, "GET", "/stall", "", nil, nil, nil)
	if e == nil || time.Since(start) > time.Second {
		t.Fatal("response body stall not cancelled")
	}
}
func TestRequestBudgetAndThinking(t *testing.T) {
	for _, tc := range []struct {
		body M
		want string
	}{{M{"thinking": M{"type": "enabled", "budget_tokens": 31999}}, "max"}, {M{"thinking": M{"type": "adaptive"}}, "high"}, {M{"thinking": M{"type": "disabled"}}, "low"}, {M{"output_config": M{"effort": "max"}}, "max"}} {
		equal(t, effort(tc.body), tc.want)
	}
	body := baseBody("anthropic", false)
	body["thinking"] = M{"type": "disabled"}
	request, e := buildRequest(body, "anthropic", "m", "max")
	must(t, e)
	if obj(request["output_config"])["effort"] != nil {
		t.Fatal("disabled thinking overridden")
	}
}
func TestConcurrentHTTPGateway(t *testing.T) {
	a := testApp(t)
	_, secret, _ := testPool(t, a, 4)
	installUpstream(t, a, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, wire(syntheticEvents(false)))
	})
	s := httptest.NewServer(a.GatewayHandler())
	defer s.Close()
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			protocol := []string{"chat", "anthropic", "responses"}[i%3]
			req, _ := http.NewRequest("POST", s.URL+route(protocol), bytes.NewBufferString(js(baseBody(protocol, i%2 == 0))))
			req.Header.Set("Authorization", "Bearer "+secret)
			res, e := http.DefaultClient.Do(req)
			if e != nil {
				t.Error(e)
				return
			}
			defer res.Body.Close()
			b, _ := io.ReadAll(res.Body)
			if res.StatusCode != 200 {
				t.Errorf("%d %s", res.StatusCode, b)
			}
		}(i)
	}
	wg.Wait()
	equal(t, a.row("SELECT COUNT(*) n FROM usage_log WHERE status='success'")["n"], 16)
}
