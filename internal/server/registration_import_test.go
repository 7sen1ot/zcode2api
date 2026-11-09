package server

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
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
	return account
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

func TestImportRegistrationStoresCredentialsAndDeviceMid(t *testing.T) {
	a := testApp(t)
	var calls atomic.Int32
	testInstallClient(a, "", func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer x.") {
			t.Fatalf("unexpected authorization: %s", r.Header.Get("Authorization"))
		}
		if r.Header.Get("X-Device-Mid") != "pro-zai-device" {
			t.Fatalf("unexpected device mid: %s", r.Header.Get("X-Device-Mid"))
		}
		return testAPIResponse(r, 200, ""), nil
	})

	zcode := testJWT(`{"user_id":"registration-user","sub":"registration-user"}`)
	web := testJWT(`{"email":"user@example.com","cli":"web"}`)
	result, err := a.importRegistration(context.Background(), M{
		"label":         "pro-zai-user@example.com",
		"provider":      "zai",
		"zcodeJwt":      zcode,
		"webJwt":        web,
		"businessToken": "business-registration",
		"deviceMid":     "pro-zai-device",
	})
	testMust(t, err)
	testEqual(t, result["updated"], false)
	id := str(result["id"])
	account := a.account(id)
	testEqual(t, account["customer_id"], "registration-user")
	testEqual(t, account["device_mid"], "pro-zai-device")
	testEqual(t, account["source"], "pro-zai")
	tokens := a.tokens(id)
	testEqual(t, tokens["zcode_jwt"], zcode)
	testEqual(t, tokens["web_jwt"], web)
	testEqual(t, tokens["business_token"], "business-registration")
	if calls.Load() == 0 {
		t.Fatal("import did not run account verification")
	}
}

func TestImportRegistrationUpdatesExistingAccount(t *testing.T) {
	a := testApp(t)
	testInstallClient(a, "", func(r *http.Request) (*http.Response, error) {
		return testAPIResponse(r, 200, ""), nil
	})
	zcode := testJWT(`{"user_id":"same-user"}`)
	first, err := a.importRegistration(context.Background(), M{
		"zcodeJwt": zcode,
		"provider": "zai",
	})
	testMust(t, err)
	second, err := a.importRegistration(context.Background(), M{
		"zcodeJwt": zcode,
		"provider": "zai",
		"webJwt":   testJWT(`{"email":"same@example.com","cli":"web"}`),
	})
	testMust(t, err)
	testEqual(t, second["id"], first["id"])
	testEqual(t, second["updated"], true)
}

func TestImportRegistrationRejectsMissingSubject(t *testing.T) {
	a := testApp(t)
	result, err := a.importRegistration(context.Background(), M{
		"zcodeJwt": testJWT(`{"token_version":0}`),
	})
	testMust(t, err)
	testEqual(t, result["ok"], false)
	if !strings.Contains(str(result["error"]), "user_id") {
		t.Fatalf("unexpected error: %v", result["error"])
	}
}
