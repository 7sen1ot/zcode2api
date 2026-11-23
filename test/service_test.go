package test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"zcode2api/internal/server"
)

type service struct {
	admin, gateway string
	stop           func()
}

func configureTestService(t *testing.T, root string) {
	t.Helper()
	t.Setenv("ZCODE2API_HOST", "127.0.0.1")
	t.Setenv("ZCODE2API_WEB_PORT", "0")
	t.Setenv("ZCODE2API_GATEWAY_PORT", "0")
	t.Setenv("ZCODE2API_ADMIN_PASSWORD", "test-password")
	if err := os.WriteFile(filepath.Join(root, "config.json"), []byte(`{"daily_claim_enabled":false}`), 0600); err != nil {
		t.Fatal(err)
	}
}

// Each start uses new loopback ports. Reusing root exercises a real SQLite restart.
func startService(t *testing.T, root string) service {
	t.Helper()
	configureTestService(t, root)
	data := filepath.Join(root, "data")
	portsFile := filepath.Join(data, "ports.json")
	if err := os.Remove(portsFile); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	app, err := server.New(root, data, os.DirFS(filepath.Join("..", "src", "web")), "smoke-test")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	var runErr error
	go func() {
		runErr = app.Run(ctx)
		close(done)
	}()
	stop := sync.OnceFunc(func() {
		cancel()
		<-done
		app.Close()
		if runErr != nil {
			t.Errorf("service exited: %v", runErr)
		}
	})
	t.Cleanup(stop)
	s := waitForListeners(t, portsFile, done)
	s.stop = stop
	return s
}

func waitForListeners(t *testing.T, portsFile string, done <-chan struct{}) service {
	t.Helper()
	timeout := time.After(10 * time.Second)
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-done:
			t.Fatal("service exited before publishing its listening ports")
		case <-timeout:
			t.Fatal("service did not publish its listening ports")
		case <-tick.C:
			raw, err := os.ReadFile(portsFile)
			var ports struct {
				Web     int `json:"web_port"`
				Gateway int `json:"gateway_port"`
			}
			if err == nil && json.Unmarshal(raw, &ports) == nil && ports.Web > 0 && ports.Gateway > 0 {
				return service{
					admin:   fmt.Sprintf("http://127.0.0.1:%d", ports.Web),
					gateway: fmt.Sprintf("http://127.0.0.1:%d", ports.Gateway),
				}
			}
		}
	}
}

var localClient = &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}

func request(t *testing.T, method, url, token, body string, wantStatus int) []byte {
	t.Helper()
	raw, _ := requestWithHeaders(t, method, url, token, body, nil, wantStatus)
	return raw
}

func requestWithHeaders(t *testing.T, method, url, token, body string, headers http.Header, wantStatus int) ([]byte, http.Header) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if headers != nil {
		req.Header = headers.Clone()
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := localClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, req.URL.Path, err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != wantStatus {
		t.Fatalf("%s %s returned %d, want %d: %.500s", method, req.URL.Path, res.StatusCode, wantStatus, raw)
	}
	return raw, res.Header
}

func decode[T any](t *testing.T, raw []byte) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("decode JSON response: %v", err)
	}
	return value
}

func (s service) login(t *testing.T) string {
	t.Helper()
	raw := request(t, "POST", s.admin+"/api/auth/login", "", `{"password":"test-password"}`, 200)
	session := decode[struct{ Token string }](t, raw)
	if session.Token == "" {
		t.Fatal("login returned an empty session token")
	}
	return session.Token
}

type apiKey struct{ ID, Secret string }

func (s service) createKey(t *testing.T, admin string) apiKey {
	t.Helper()
	raw := request(t, "POST", s.admin+"/api/keys", admin, `{"name":"smoke"}`, 200)
	key := decode[apiKey](t, raw)
	if key.ID == "" || key.Secret == "" {
		t.Fatal("key creation returned an empty ID or secret")
	}
	return key
}

func TestServiceLifecycle(t *testing.T) {
	root := t.TempDir()
	s := startService(t, root)
	for scope, url := range map[string]string{"web": s.admin, "gateway": s.gateway} {
		health := decode[struct {
			OK             bool
			Scope, Version string
		}](t, request(t, "GET", url+"/healthz", "", "", 200))
		if !health.OK || health.Scope != scope || health.Version != "smoke-test" {
			t.Fatalf("%s health: %+v", scope, health)
		}
	}
	page := request(t, "GET", s.admin+"/", "", "", 200)
	if !bytes.Contains(bytes.ToLower(page), []byte("<html")) {
		t.Fatal("console root did not serve HTML")
	}
	request(t, "GET", s.gateway+"/readyz", "", "", 503)
	request(t, "GET", s.gateway+"/v1/models", "", "", 401)
	request(t, "POST", s.admin+"/api/keys", "", `{"name":"smoke"}`, 401)
	request(t, "POST", s.admin+"/api/auth/login", "", `{"password":"wrong"}`, 401)
	key := s.createKey(t, s.login(t))
	raw := request(t, "POST", s.gateway+"/v1/responses", key.Secret, `{"input":"ping"}`, 503)
	failure := decode[struct{ Error struct{ Code string } }](t, raw)
	if failure.Error.Code != "no_account" {
		t.Fatalf("empty pool returned error code %q", failure.Error.Code)
	}
	s.stop()
	s = startService(t, root)
	raw = request(t, "GET", s.gateway+"/v1/usage", key.Secret, "", 200)
	usage := decode[struct{ Key struct{ ID, Name string } }](t, raw)
	if usage.Key.ID != key.ID || usage.Key.Name != "smoke" {
		t.Fatalf("unexpected key after restart: %+v", usage.Key)
	}
	request(t, "DELETE", s.admin+"/api/keys/"+key.ID, s.login(t), "", 200)
	request(t, "GET", s.gateway+"/v1/usage", key.Secret, "", 401)
}
