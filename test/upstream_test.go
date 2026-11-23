package test

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func fixtureAccountToken() string {
	return "test." + base64.RawURLEncoding.EncodeToString([]byte(`{"user_id":"smoke-account"}`)) + ".test"
}

// The gateway clones the default transport and sets its own TCP dialer.
// Route its HTTPS requests to a loopback fixture. These tests run serially.
func serveLocalUpstream(t *testing.T, handler http.Handler) {
	t.Helper()
	upstream := httptest.NewTLSServer(handler)
	t.Cleanup(upstream.Close)
	original := http.DefaultTransport
	dialer := &tls.Dialer{Config: upstream.Client().Transport.(*http.Transport).TLSClientConfig.Clone()}
	http.DefaultTransport = &http.Transport{DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr != "zcode.z.ai:443" {
			return nil, fmt.Errorf("unexpected upstream address %q", addr)
		}
		return dialer.DialContext(ctx, network, upstream.Listener.Addr().String())
	}}
	t.Cleanup(func() { http.DefaultTransport = original })
}

func (s service) importActiveAccount(t *testing.T, admin, token string) string {
	t.Helper()
	file := fmt.Sprintf(`{"data":{"format":"zcode2api-accounts","version":1,"accounts":[
		{"family":"zai","tokens":{"active_provider":"zai","zcode_jwt":%q}}]}}`, token)
	imported := decode[struct{ Imported int }](t, request(t, "POST", s.admin+"/api/accounts/import", admin, file, 200))
	if imported.Imported != 1 {
		t.Fatalf("imported %d accounts, want 1", imported.Imported)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		raw := request(t, "GET", s.admin+"/api/accounts?view=groups", admin, "", 200)
		accounts := decode[[]struct{ ID, Status string }](t, raw)
		if len(accounts) == 1 && accounts[0].ID != "" && accounts[0].Status == "active" {
			return accounts[0].ID
		}
		if time.Now().After(deadline) {
			t.Fatalf("account did not become active after import: %+v", accounts)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
