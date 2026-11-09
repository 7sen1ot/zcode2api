package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestReachableProbeKeepsWAFBlock(t *testing.T) {
	a := testApp(t)
	scope := testProxyScope(a, proxyTestOptions())
	id := "http://exit-a:1"
	until := time.Now().Add(time.Hour)
	a.proxies.mu.Lock()
	a.proxies.blocked[id] = until
	a.proxies.unavailable[id] = true
	a.proxies.retryAt[id] = until
	a.proxies.mu.Unlock()
	a.applyExitVerdict(a.ctx, scope, id, proxyProbeResult{verdict: "reachable", observedRetry: until})
	a.proxies.mu.Lock()
	defer a.proxies.mu.Unlock()
	if a.proxies.blocked[id] != until || a.proxies.unavailable[id] || !a.proxies.retryAt[id].IsZero() {
		t.Fatal("anonymous reachability must clear only connection quarantine")
	}
}
func TestProxyProbeVerdicts(t *testing.T) {
	for _, tc := range []struct {
		name, body, verdict string
		status              int
	}{
		{"unauthorized", `{"error":{"type":"authentication_error"}}`, "reachable", 401},
		{"waf", "errors.aliyun.com/ block_message", "blocked", 403},
		{"proxy-auth", "", "quarantine", 407},
		{"account-risk", `{"code":"3012"}`, "", 403},
		{"forbidden", `{"code":"denied"}`, "", 403},
		{"rate-limit", `{"code":"limited"}`, "", 429},
		{"server", "unavailable", "", 503},
		{"unknown", "<html>unknown</html>", "", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := testApp(t)
			testInstallClient(a, "http://exit-a:1", func(r *http.Request) (*http.Response, error) {
				if r.Header.Get("Authorization") != "" || r.Header.Get("X-Api-Key") != "" {
					t.Error("probe sent account credentials")
				}
				deadline, ok := r.Context().Deadline()
				if !ok || time.Until(deadline) > 8*time.Second {
					t.Error("unbounded probe timeout")
				}
				response := testAPIResponse(r, tc.status, tc.body)
				response.Header.Set("Retry-After", "14400")
				return response, nil
			})
			result := a.probeManualExit(a.ctx, "http://exit-a:1")
			if result.verdict != tc.verdict {
				t.Fatalf("verdict=%q, want %q", result.verdict, tc.verdict)
			}
			if tc.verdict == "blocked" && result.retryAfter != 4*time.Hour {
				t.Fatal("probe discarded Retry-After")
			}
		})
	}
}
func TestProxyProbeConnectionFailure(t *testing.T) {
	a := testApp(t)
	testInstallClient(a, "http://exit-a:1", func(r *http.Request) (*http.Response, error) {
		return nil, &proxyDialError{errors.New("connection refused")}
	})
	if result := a.probeManualExit(a.ctx, "http://exit-a:1"); result.verdict != "quarantine" {
		t.Fatalf("proxy dial failure not quarantined: %+v", result)
	}
}
func TestProxyProbeRejectsStaleAndCanceledResults(t *testing.T) {
	a := testApp(t)
	options := proxyTestOptions()
	old := testProxyScope(a, options)
	a.resetOutboundProxies()
	current := testProxyScope(a, options)
	a.applyExitVerdict(a.ctx, old, "http://exit-a:1", proxyProbeResult{verdict: "blocked"})
	ctx, cancel := context.WithCancel(a.ctx)
	cancel()
	a.applyExitVerdict(ctx, current, "http://exit-a:1", proxyProbeResult{verdict: "quarantine"})
	a.proxies.mu.Lock()
	defer a.proxies.mu.Unlock()
	if len(a.proxies.blocked) != 0 || len(a.proxies.unavailable) != 0 {
		t.Fatal("stale or canceled probe changed current state")
	}
}
func TestProxyProbeCoversWholePoolWithBoundedConcurrency(t *testing.T) {
	a := testApp(t)
	urls := []any{}
	var active, peak, calls atomic.Int32
	for i := 0; i < 64; i++ {
		address := fmt.Sprintf("http://exit-%d:1", i)
		urls = append(urls, address)
		testInstallClient(a, address, func(r *http.Request) (*http.Response, error) {
			n := active.Add(1)
			defer active.Add(-1)
			for previous := peak.Load(); n > previous; previous = peak.Load() {
				if peak.CompareAndSwap(previous, n) {
					break
				}
			}
			time.Sleep(10 * time.Millisecond)
			calls.Add(1)
			return testAPIResponse(r, 401, `{}`), nil
		})
	}
	options := merge(proxyDefaults(), M{"enabled": true, "source": "manual", "urls": urls})
	scope := testProxyScope(a, options)
	a.probeManualExits(a.ctx, options, scope)
	if calls.Load() != 64 || peak.Load() > 4 || peak.Load() < 2 {
		t.Fatalf("calls=%d concurrent=%d", calls.Load(), peak.Load())
	}
}
func TestProxyHealthWorkerEnableAndConfigurationCancellation(t *testing.T) {
	a := testApp(t)
	var blockedCalls, quickCalls atomic.Int32
	entered, canceled := make(chan struct{}, 2), make(chan struct{}, 2)
	blocked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		blockedCalls.Add(1)
		entered <- struct{}{}
		<-r.Context().Done()
		canceled <- struct{}{}
	}))
	defer blocked.Close()
	quick := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { quickCalls.Add(1); w.WriteHeader(401) }))
	defer quick.Close()
	defer a.Close()
	a.origin = "http://upstream.invalid"
	a.startProxyHealthProbes()
	a.startProxyHealthProbes()
	testWait(t, func() bool { a.proxies.mu.Lock(); defer a.proxies.mu.Unlock(); return a.proxies.probeCancel != nil })
	options := merge(proxyDefaults(), M{"enabled": true, "source": "manual", "urls": []any{blocked.URL}})
	a.configMu.Lock()
	a.cfg["outbound_proxy"] = options
	a.configMu.Unlock()
	a.resetOutboundProxies()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("enabling proxies did not start probes")
	}
	a.configMu.Lock()
	a.cfg["outbound_proxy"] = merge(options, M{"enabled": false})
	a.configMu.Unlock()
	a.resetOutboundProxies()
	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("old round was not canceled")
	}
	if blockedCalls.Load() != 1 {
		t.Fatal("duplicate health workers started")
	}
	a.configMu.Lock()
	a.cfg["outbound_proxy"] = merge(options, M{"urls": []any{quick.URL}})
	a.configMu.Unlock()
	a.resetOutboundProxies()
	testWait(t, func() bool { return quickCalls.Load() >= 1 })
}
func TestProxyCooldownHonorsRetryAfter(t *testing.T) {
	a := testApp(t)
	scope := testProxyScope(a, proxyTestOptions())
	id := "http://exit-a:1"
	a.applyExitVerdict(a.ctx, scope, id, proxyProbeResult{verdict: "blocked", retryAfter: 4 * time.Hour})
	a.proxies.mu.Lock()
	until := a.proxies.blocked[id]
	a.proxies.mu.Unlock()
	if time.Until(until) < 4*time.Hour-time.Second {
		t.Fatal("probe shortened Retry-After")
	}
	ctx := context.WithValue(a.ctx, proxySelectionKey{}, proxySelection{scope.generation, id})
	a.markProxyRoute(ctx, true, 5*time.Hour)
	a.proxies.mu.Lock()
	defer a.proxies.mu.Unlock()
	if time.Until(a.proxies.blocked[id]) < 5*time.Hour-time.Second {
		t.Fatal("repeat failure capped upstream Retry-After")
	}
}

func TestReachableProbeCannotClearNewerConnectionFailure(t *testing.T) {
	a := testApp(t)
	scope := testProxyScope(a, proxyTestOptions())
	id := "http://exit-a:1"
	a.proxies.mu.Lock()
	a.proxies.unavailable[id] = true
	a.proxies.retryAt[id] = time.Now().Add(time.Minute)
	a.proxies.mu.Unlock()
	a.applyExitVerdict(a.ctx, scope, id, proxyProbeResult{verdict: "reachable"})
	a.proxies.mu.Lock()
	defer a.proxies.mu.Unlock()
	if !a.proxies.unavailable[id] {
		t.Fatal("an earlier probe cleared a newer connection failure")
	}
}
