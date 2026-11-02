package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestAutoVerificationChallengeOverridesSkipOnSameExit(t *testing.T) {
	a := testApp(t)
	_, secret, _ := testPool(t, a, 1)
	a.origin = "http://upstream.invalid"
	obj(obj(a.cfg["http"])["captcha"])["mode"] = "auto"
	var messages, tickets, otherCalls atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "configs") {
			scene := validCaptchaScene()
			scene["skip_model_request"] = true
			writeJSON(w, 200, M{"code": 0, "data": M{"configs": M{"captcha": scene}}})
			return
		}
		if messages.Add(1) == 1 {
			if r.Header.Get("X-Aliyun-Captcha-Verify-Param") != "" {
				t.Error("auto policy ignored the initial skip instruction")
			}
			writeJSON(w, 403, M{"code": "coding_plan_security_verification_required"})
			return
		}
		if r.Header.Get("X-Aliyun-Captcha-Verify-Param") != "fresh-ticket" {
			t.Error("explicit verification challenge was skipped")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, wire(syntheticEvents(false)))
	}))
	defer proxy.Close()
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { otherCalls.Add(1); w.WriteHeader(500) }))
	defer other.Close()
	a.captcha.run = func(ctx context.Context, params M) (M, error) {
		tickets.Add(1)
		if selectedProxy(ctx) != proxy.URL || params["proxy_url"] != proxy.URL {
			t.Error("verification changed exit")
		}
		return M{"ok": true, "param": "fresh-ticket"}, nil
	}
	setTestProxy(a, M{"enabled": true, "urls": []any{proxy.URL, other.URL}})
	response := request(a.GatewayHandler(), "POST", "/v1/chat/completions", secret, baseBody("chat", false))
	if response.Code != 200 || tickets.Load() != 1 || messages.Load() != 2 || otherCalls.Load() != 0 || len(a.proxies.unavailable) != 0 {
		t.Fatal("challenge did not reverify on the same route", response.Code, response.Body.String())
	}
}

func TestVerificationFailuresDoNotRetireHealthyExits(t *testing.T) {
	for _, item := range []struct{ name, code, verify string }{
		{"expired-tickets", "", ""},
		{"human-interaction", "captcha_interaction_required", "F015"},
		{"unknown-refusal", "captcha_verification_failed", "F999"},
		{"missing-runtime", "captcha_jsdom_missing", ""},
		{"sdk-initialization", "captcha_sdk_error", ""},
	} {
		t.Run(item.name, func(t *testing.T) {
			a := testApp(t)
			_, _, accounts := testPool(t, a, 1)
			a.origin = "http://upstream.invalid"
			obj(obj(a.cfg["http"])["captcha"])["mode"] = "always"
			var messages, issued atomic.Int32
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "configs") {
					writeJSON(w, 200, M{"code": 0, "data": M{"configs": M{"captcha": validCaptchaScene()}}})
					return
				}
				messages.Add(1)
				writeJSON(w, 403, M{"code": "captcha_expired"})
			}))
			defer proxy.Close()
			a.captcha.run = func(context.Context, M) (M, error) {
				if item.code == "" {
					return M{"ok": true, "param": fmt.Sprint("ticket-", issued.Add(1))}, nil
				}
				return M{"code": item.code, "verify_code": item.verify}, nil
			}
			setTestProxy(a, M{"enabled": true, "urls": []any{proxy.URL, "http://unused.invalid:80"}})
			_, failure := a.inferHTTP(t.Context(), accounts[0], inferOptions{Upstream: M{}})
			if failure == nil || failure.Retryable || len(a.proxies.unavailable) != 0 {
				t.Fatal("verification failure retired a healthy exit", failure)
			}
			if item.code == "" && (messages.Load() != 2 || issued.Load() != 2) {
				t.Fatal("expired ticket retry was not bounded")
			}
			if item.code == "captcha_interaction_required" && !strings.Contains(failure.Message, "人工") {
				t.Fatal("manual verification action was hidden")
			}
		})
	}
}

func TestSDKTransportFailureSwitchesExitAndGetsNewTicket(t *testing.T) {
	a := testApp(t)
	_, secret, _ := testPool(t, a, 1)
	a.origin = "http://upstream.invalid"
	obj(obj(a.cfg["http"])["captcha"])["mode"] = "always"
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "configs") {
			writeJSON(w, 200, M{"code": 0, "data": M{"configs": M{"captcha": validCaptchaScene()}}})
			return
		}
		if r.Header.Get("X-Aliyun-Captcha-Verify-Param") != "new-exit-ticket" {
			t.Error("fresh ticket missing")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, wire(syntheticEvents(false)))
	})
	first, second := httptest.NewServer(handler), httptest.NewServer(handler)
	defer first.Close()
	defer second.Close()
	a.captcha.run = func(ctx context.Context, params M) (M, error) {
		if params["proxy_url"] != selectedProxy(ctx) {
			t.Error("SDK route mismatch")
		}
		if selectedProxy(ctx) == first.URL {
			return M{"code": "captcha_network_error"}, nil
		}
		return M{"ok": true, "param": "new-exit-ticket"}, nil
	}
	setTestProxy(a, M{"enabled": true, "urls": []any{first.URL, second.URL}})
	response := request(a.GatewayHandler(), "POST", "/v1/chat/completions", secret, baseBody("chat", false))
	if response.Code != 200 || !a.proxies.unavailable[first.URL] || a.proxies.unavailable[second.URL] {
		t.Fatal("SDK transport failover failed", response.Body.String())
	}
}
