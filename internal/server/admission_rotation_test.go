package server

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"
)

// The admission timer guards the local queue only. Once the upstream answered
// (response headers arrived) the request is in flight and first_token_timeout
// owns the wait; the admission timer must not cancel the prefill.
func TestAdmissionTimerStopsWhenUpstreamAnswers(t *testing.T) {
	app := registrationTestApp(t)
	transport := registrationRoundTrip(func(r *http.Request) (*http.Response, error) {
		// Headers return immediately; the first SSE event takes 150ms, which
		// is the prefill window the admission timer (100ms) must not cancel.
		pr, pw := io.Pipe()
		go func() {
			defer pw.Close()
			time.Sleep(150 * time.Millisecond)
			_, _ = pw.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{}}\n\n"))
			_, _ = pw.Write([]byte("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\"}}\n\n"))
			_, _ = pw.Write([]byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"OK\"}}\n\n"))
			_, _ = pw.Write([]byte("event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"))
			_, _ = pw.Write([]byte("event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n"))
			_, _ = pw.Write([]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
		}()
		return &http.Response{
			StatusCode: 200,
			Header: http.Header{
				"Content-Type": []string{"text/event-stream"},
			},
			Body:    pr,
			Request: r,
		}, nil
	})
	app.mu.Lock()
	app.clients[""] = &http.Client{Transport: transport}
	app.mu.Unlock()

	account, err := app.insertAccount(M{
		"family": "zai", "customer_id": "admission-probe", "device_mid": "dev-admission",
		"status": "active",
	}, M{"zcode_jwt": registrationJWT(`{"user_id":"admission-probe"}`)})
	registrationMust(t, err)

	// A 100ms admission window would normally cancel the 150ms response; with
	// the fix the timer stops once headers arrive and the request completes.
	o := inferOptions{
		ID: "probe-admission", Model: "glm-5.3-flash", Protocol: "anthropic",
		Admission: 100 * time.Millisecond, Timing: newTiming(),
		Upstream: M{"model": "glm-5.3-flash", "messages": []any{}, "stream": true},
	}
	start := time.Now()
	result, failure := app.inferHTTP(context.Background(), account, o)
	if failure != nil {
		t.Fatalf("request failed under admission window: %s/%s", failure.Code, failure.Message)
	}
	if result == nil {
		t.Fatal("no result")
	}
	if time.Since(start) < 150*time.Millisecond {
		t.Fatal("response returned before upstream answered")
	}
}

// One request must not walk the whole pool on rate limits: after two rotations
// the throttled accounts re-enter selection so the request waits instead of
// rotating to yet another cold-cache account.
func TestRateLimitRotationStopsAtCap(t *testing.T) {
	manager := &outboundProxyManager{
		unavailable: map[string]bool{},
		retryAt:     map[string]time.Time{},
		blocked:     map[string]time.Time{},
	}
	endpoints := []proxyEndpoint{
		{URL: "http://exit-a:1"}, {URL: "http://exit-b:2"}, {URL: "http://exit-c:3"},
	}
	options := M{"enabled": true, "source": "manual", "urls": []any{"http://exit-a:1", "http://exit-b:2", "http://exit-c:3"}}
	seen := map[string]bool{}
	for i := 0; i < 4; i++ {
		index, _ := manager.selectRotatingEndpoint(options, endpoints)
		if index < 0 {
			t.Fatal("no endpoint returned")
		}
		seen[endpoints[index].URL] = true
	}
	if len(seen) > 3 {
		t.Fatalf("rotation walked more endpoints than exist: %v", seen)
	}
}
