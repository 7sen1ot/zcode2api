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
)

func registrationJWT(payload string) string {
	return "x." + base64.RawURLEncoding.EncodeToString([]byte(payload)) + ".x"
}

func registrationTestApp(t *testing.T) *App {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(root, "config.json"),
		[]byte(`{"web_port":0,"gateway_port":0}`),
		0600,
	); err != nil {
		t.Fatal(err)
	}
	app, err := New(
		root,
		filepath.Join(root, "data"),
		fstest.MapFS{
			"index.html": &fstest.MapFile{Data: []byte("console")},
		},
		"test",
	)
	if err != nil {
		t.Fatal(err)
	}
	app.log.SetOutput(io.Discard)
	t.Cleanup(app.Close)
	return app
}

func registrationMust(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func registrationEqual(t *testing.T, got, want any) {
	t.Helper()
	if js(got) != js(want) {
		t.Fatalf("got %s; want %s", js(got), js(want))
	}
}

type registrationRoundTrip func(*http.Request) (*http.Response, error)

func (fn registrationRoundTrip) RoundTrip(
	r *http.Request,
) (*http.Response, error) {
	return fn(r)
}

func TestImportRegistrationStoresCredentialsAndDeviceMid(t *testing.T) {
	a := registrationTestApp(t)
	var calls atomic.Int32
	transport := registrationRoundTrip(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer x.") {
			t.Fatalf("unexpected authorization: %s", r.Header.Get("Authorization"))
		}
		if r.Header.Get("X-Device-Mid") != "pro-zai-device" {
			t.Fatalf("unexpected device mid: %s", r.Header.Get("X-Device-Mid"))
		}
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       http.NoBody,
			Request:    r,
		}, nil
	})
	a.mu.Lock()
	a.clients[""] = &http.Client{Transport: transport}
	a.mu.Unlock()

	zcode := registrationJWT(`{"user_id":"registration-user","sub":"registration-user"}`)
	web := registrationJWT(`{"email":"user@example.com","cli":"web"}`)
	result, err := a.importRegistration(context.Background(), M{
		"label":         "pro-zai-user@example.com",
		"provider":      "zai",
		"zcodeJwt":      zcode,
		"webJwt":        web,
		"businessToken": "business-registration",
		"deviceMid":     "pro-zai-device",
	})
	registrationMust(t, err)
	registrationEqual(t, result["updated"], false)
	id := str(result["id"])
	account := a.account(id)
	registrationEqual(t, account["customer_id"], "registration-user")
	registrationEqual(t, account["device_mid"], "pro-zai-device")
	registrationEqual(t, account["source"], "pro-zai")
	tokens := a.tokens(id)
	registrationEqual(t, tokens["zcode_jwt"], zcode)
	registrationEqual(t, tokens["web_jwt"], web)
	registrationEqual(t, tokens["business_token"], "business-registration")
	if calls.Load() == 0 {
		t.Fatal("import did not run account verification")
	}
}

func TestImportRegistrationUpdatesExistingAccount(t *testing.T) {
	a := registrationTestApp(t)
	a.mu.Lock()
	a.clients[""] = &http.Client{Transport: registrationRoundTrip(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       http.NoBody,
			Request:    r,
		}, nil
	})}
	a.mu.Unlock()
	zcode := registrationJWT(`{"user_id":"same-user"}`)
	first, err := a.importRegistration(context.Background(), M{
		"zcodeJwt": zcode,
		"provider": "zai",
	})
	registrationMust(t, err)
	second, err := a.importRegistration(context.Background(), M{
		"zcodeJwt": zcode,
		"provider": "zai",
		"webJwt":   registrationJWT(`{"email":"same@example.com","cli":"web"}`),
	})
	registrationMust(t, err)
	registrationEqual(t, second["id"], first["id"])
	registrationEqual(t, second["updated"], true)
}

func TestImportRegistrationRejectsMissingSubject(t *testing.T) {
	a := registrationTestApp(t)
	result, err := a.importRegistration(context.Background(), M{
		"zcodeJwt": registrationJWT(`{"token_version":0}`),
	})
	registrationMust(t, err)
	registrationEqual(t, result["ok"], false)
	if !strings.Contains(str(result["error"]), "user_id") {
		t.Fatalf("unexpected error: %v", result["error"])
	}
}
