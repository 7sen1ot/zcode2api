package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAdmissionStillBoundsRejectedResponse(t *testing.T) {
	a := testApp(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(403)
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
			return
		case <-time.After(150 * time.Millisecond):
		}
		_, _ = io.WriteString(w, `{"error":{"type":"permission_error"}}`)
	}))
	defer server.Close()
	a.origin = server.URL
	account := testAccount(t, a, "rejected-response")
	_, failure := a.inferHTTP(a.ctx, account, inferOptions{ID: "rejected-response", Model: "glm-5.3-flash", Admission: 50 * time.Millisecond, Upstream: M{"messages": []any{}, "stream": true}})
	if failure == nil || failure.Code != "throttle_window_exhausted" {
		t.Fatalf("rejected response escaped admission budget: %+v", failure)
	}
}

func TestAdmissionTimerStopsWhenUpstreamAnswers(t *testing.T) {
	a := testApp(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
			return
		case <-time.After(150 * time.Millisecond):
		}
		_, _ = io.WriteString(w, testInferenceSSE)
	}))
	defer server.Close()
	a.origin = server.URL
	account := testAccount(t, a, "admission")
	result, failure := a.inferHTTP(a.ctx, account, inferOptions{ID: "admission-real-http", Model: "glm-5.3-flash", Protocol: "anthropic", Admission: 100 * time.Millisecond, Upstream: M{"messages": []any{}, "stream": true}})
	if failure != nil || result == nil {
		t.Fatalf("in-flight response canceled after headers: %+v", failure)
	}
}
func TestFirstTokenTimeoutStillOwnsWaitAfterHeaders(t *testing.T) {
	a := testApp(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	a.origin = server.URL
	a.configMu.Lock()
	obj(a.cfg["http"])["first_token_timeout_ms"] = 80
	a.configMu.Unlock()
	account := testAccount(t, a, "first-token")
	_, failure := a.inferHTTP(a.ctx, account, inferOptions{ID: "first-token", Model: "glm-5.3-flash", Admission: 40 * time.Millisecond, Upstream: M{"messages": []any{}, "stream": true}})
	if failure == nil || failure.Code != "first_token_timeout" {
		t.Fatalf("wrong timeout: %+v", failure)
	}
}
func TestRateLimitRotationCanReachThirdAccount(t *testing.T) {
	a := testApp(t)
	orderedInferenceAccounts(t, a, "account-a", "account-b", "account-c")
	calls := map[string]int{}
	testInstallClient(a, "", func(r *http.Request) (*http.Response, error) {
		id := str(jwtPayload(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))["user_id"])
		calls[id]++
		if id == "account-c" {
			return testInferenceResponse(r), nil
		}
		response := testAPIResponse(r, 429, `{"error":{"type":"rate_limit_error"}}`)
		response.Header.Set("Retry-After", "60")
		return response, nil
	})
	ctx, cancel := context.WithTimeout(a.ctx, time.Second)
	defer cancel()
	result, failure := a.infer(ctx, inferOptions{ID: "third-account", Model: "glm-5.3", Protocol: "anthropic", Key: M{}, Upstream: M{"messages": []any{}, "stream": true}})
	if failure != nil || result == nil || len(calls) != 3 || result.Attempts != 3 {
		t.Fatalf("calls=%v result=%+v failure=%+v", calls, result, failure)
	}
	for id, count := range calls {
		if count != 1 {
			t.Fatalf("account %s retried before Retry-After: %d", id, count)
		}
	}
}

func TestAccountRotationSkipsUnrecoverableAccounts(t *testing.T) {
	for _, tc := range []struct {
		kind, body string
		status     int
	}{
		{"account_risk", `{"code":"3012"}`, 403},
		{"quota", `{"code":"insufficient_quota"}`, 400},
		{"auth", `{"error":{"type":"authentication_error"}}`, 401},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			a := testApp(t)
			orderedInferenceAccounts(t, a, "failed-a", "failed-b", "healthy-c")
			calls := map[string]int{}
			testInstallClient(a, "", func(r *http.Request) (*http.Response, error) {
				if !strings.HasSuffix(r.URL.Path, "/messages") {
					return testAPIResponse(r, 503, `{}`), nil
				}
				id := str(jwtPayload(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))["user_id"])
				calls[id]++
				if id == "healthy-c" {
					return testInferenceResponse(r), nil
				}
				return testAPIResponse(r, tc.status, tc.body), nil
			})
			result, failure := a.infer(a.ctx, inferOptions{ID: "unrecoverable", Model: "glm-5.3", Key: M{}, Upstream: M{"messages": []any{}, "stream": true}})
			if failure != nil || result == nil || len(calls) != 3 || calls["failed-a"] != 1 || calls["failed-b"] != 1 || calls["healthy-c"] != 1 {
				t.Fatalf("calls=%v result=%+v failure=%+v", calls, result, failure)
			}
		})
	}
}

func TestRateLimitRotationIgnoresOrdinaryAttemptLimitAndHonorsCancellation(t *testing.T) {
	a := testApp(t)
	orderedInferenceAccounts(t, a, "one-a", "one-b")
	a.configMu.Lock()
	obj(a.cfg["retry"])["max_attempts"] = 1
	a.configMu.Unlock()
	var calls atomic.Int32
	testInstallClient(a, "", func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		response := testAPIResponse(r, 429, `{"code":"limited"}`)
		response.Header.Set("Retry-After", "60")
		return response, nil
	})
	ctx, cancel := context.WithTimeout(a.ctx, 150*time.Millisecond)
	defer cancel()
	_, failure := a.infer(ctx, inferOptions{ID: "max-one", Model: "glm-5.3", Key: M{}, Upstream: M{"messages": []any{}, "stream": true}})
	if calls.Load() != 2 || failure == nil || failure.Code != "canceled" {
		t.Fatalf("calls=%d failure=%+v", calls.Load(), failure)
	}
}

func TestQueuedRequestCanUseNewAccounts(t *testing.T) {
	a := testApp(t)
	account := testAccount(t, a, "queued")
	id := str(account["id"])
	a.mu.Lock()
	a.state(id).Inflight = accountConcurrencyLimit
	a.mu.Unlock()
	result := make(chan M, 1)
	go func() {
		picked, _, _, _, err := a.acquire(a.ctx, inferOptions{Key: M{}, Model: "glm-5.3", Admission: time.Second}, map[string]bool{}, time.Second)
		result <- M{"account": picked, "error": err}
	}()
	testWait(t, func() bool { a.scheduleMu.Lock(); defer a.scheduleMu.Unlock(); return len(a.waiters) == 1 })
	added := testAccount(t, a, "new-healthy-account")
	a.notifyAvailable()
	select {
	case value := <-result:
		picked := obj(value["account"])
		if str(picked["id"]) != str(added["id"]) || value["error"] != nil {
			t.Fatalf("queued request did not use new account: %v", value)
		}
		a.finishAccountAttempt(picked, nil, false, nil)
	case <-time.After(3 * time.Second):
		t.Fatal("queue did not pick newly available account")
	}
}

func TestRateLimitWindowCoversAllEligibleAccounts(t *testing.T) {
	a := testApp(t)
	orderedInferenceAccounts(t, a, "budget-a", "budget-b", "budget-c")
	var calls atomic.Int32
	testInstallClient(a, "", func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		response := testAPIResponse(r, 429, `{"code":"limited"}`)
		response.Header.Set("Retry-After", "120")
		return response, nil
	})
	ctx, cancel := context.WithTimeout(a.ctx, 15*time.Second)
	defer cancel()
	start := time.Now()
	_, failure := a.infer(ctx, inferOptions{ID: "retry-window", Model: "glm-5.3", Key: M{}, Upstream: M{"messages": []any{}, "stream": true}})
	if calls.Load() != 3 || failure == nil || failure.Kind != "rate_limit" || time.Since(start) > 12*time.Second || ctx.Err() != nil {
		t.Fatalf("calls=%d elapsed=%s failure=%+v", calls.Load(), time.Since(start), failure)
	}
}

func TestBusyWaitDoesNotConsumeOrdinaryRetryBudget(t *testing.T) {
	a := testApp(t)
	testAccount(t, a, "busy-budget")
	a.configMu.Lock()
	obj(a.cfg["retry"])["total_budget_ms"] = 10000
	a.configMu.Unlock()
	var calls atomic.Int32
	testInstallClient(a, "", func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		response := testAPIResponse(r, 400, `{"code":"3009"}`)
		response.Header.Set("Retry-After", "30")
		return response, nil
	})
	ctx, cancel := context.WithTimeout(a.ctx, 11200*time.Millisecond)
	defer cancel()
	_, failure := a.infer(ctx, inferOptions{ID: "busy-budget", Model: "glm-5.3", Key: M{}, Upstream: M{"messages": []any{}, "stream": true}})
	if failure == nil || failure.Code != "canceled" || ctx.Err() == nil || calls.Load() != 1 {
		t.Fatalf("busy wait consumed ordinary retry budget: calls=%d failure=%+v", calls.Load(), failure)
	}
}
