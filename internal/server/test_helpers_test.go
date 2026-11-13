package server

import (
	"encoding/base64"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func testApp(t *testing.T) *App {
	t.Helper()
	root := t.TempDir()
	testMust(t, os.WriteFile(filepath.Join(root, "config.json"), []byte(`{"web_port":0,"gateway_port":0}`), 0600))
	app, err := New(root, filepath.Join(root, "data"), fstest.MapFS{
		"index.html": {Data: []byte("console")},
	}, "test")
	testMust(t, err)
	app.log.SetOutput(io.Discard)
	t.Cleanup(app.Close)
	return app
}

func testMust(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func testEqual(t *testing.T, got, want any) {
	t.Helper()
	if js(got) != js(want) {
		t.Fatalf("got %s; want %s", js(got), js(want))
	}
}

func testJWT(payload string) string {
	return "x." + base64.RawURLEncoding.EncodeToString([]byte(payload)) + ".x"
}

type testRoundTrip func(*http.Request) (*http.Response, error)

func (fn testRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) {
	return fn(r)
}

func testAPIResponse(r *http.Request, status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}
}

const testInferenceSSE = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

func testInferenceResponse(r *http.Request) *http.Response {
	response := testAPIResponse(r, 200, testInferenceSSE)
	response.Header.Set("Content-Type", "text/event-stream")
	return response
}

func testAccount(t *testing.T, a *App, identity string) M {
	t.Helper()
	account, err := a.insertAccount(M{"family": "zai", "customer_id": identity, "device_mid": identity, "status": "active"}, M{"zcode_jwt": testJWT(js(M{"user_id": identity}))})
	testMust(t, err)
	// Scheduling requires a known positive balance, even for active test accounts.
	balance := M{"code": 0, "data": M{"balances": []any{M{"total_units": 1000, "used_units": 0, "remaining_units": 1000}}}}
	testMust(t, a.updateAccount(str(account["id"]), M{"last_balance": balance}))
	return a.account(str(account["id"]))
}

func orderedInferenceAccounts(t *testing.T, a *App, identities ...string) {
	t.Helper()
	for index, identity := range identities {
		account := testAccount(t, a, identity)
		testMust(t, a.exec("UPDATE accounts SET created_at=? WHERE id=?", index+1, account["id"]))
	}
}

func proxyTestOptions() M {
	return merge(proxyDefaults(), M{"enabled": true, "source": "manual", "urls": []any{"http://exit-a:1", "http://exit-b:2"}})
}

func testProxyScope(a *App, options M) proxyProbeScope {
	a.configMu.Lock()
	defer a.configMu.Unlock()
	a.cfg["outbound_proxy"] = options
	a.proxies.mu.Lock()
	defer a.proxies.mu.Unlock()
	a.proxies.configureLocked(options, manualProxyEndpoints(arr(options["urls"])))
	return proxyProbeScope{a.proxies.key, a.proxies.generation}
}

func testInstallClient(a *App, address string, transport testRoundTrip) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.clients[address] = &http.Client{Transport: transport}
}

func testWait(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !ready() {
		if time.Now().After(deadline) {
			t.Fatal("condition did not become ready")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
