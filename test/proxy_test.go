package test

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"zcode2api/internal/server/proxy"
)

func TestManualProxyProbe(t *testing.T) {
	accountToken := "test." + base64.RawURLEncoding.EncodeToString([]byte(`{"user_id":"proxy-account"}`)) + ".test"
	balanceSeen := make(chan struct{}, 1)
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Connection", "close")
		if r.Method == "GET" && r.URL.Path == "/api/v1/zcode-plan/billing/balance" {
			if r.Header.Get("Authorization") != "Bearer "+accountToken || r.Header.Get("Proxy-Authorization") != "" {
				t.Error("the balance request mixed up account and proxy credentials")
			}
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"code":0,"data":{"balances":[{"total_units":10,"remaining_units":10}]}}`)
			select {
			case balanceSeen <- struct{}{}:
			default:
			}
			return
		}
		if r.Method != "POST" || r.URL.Path != "/api/v1/zcode-plan/anthropic/v1/messages" {
			t.Errorf("unexpected probe: %s %s", r.Method, r.URL.Path)
		}
		for _, name := range []string{"Authorization", "X-Api-Key", "Proxy-Authorization", "Cookie"} {
			if r.Header.Get(name) != "" {
				t.Errorf("anonymous probe sent %s to the upstream", name)
			}
		}
		http.Error(w, "authentication required", http.StatusUnauthorized)
	}))
	t.Cleanup(origin.Close)
	originHost, _, err := net.SplitHostPort(origin.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	trust := origin.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	trust.ServerName = originHost
	original := http.DefaultTransport
	// The gateway clones this transport. A direct TLS dial would bypass the test proxy.
	http.DefaultTransport = &http.Transport{TLSClientConfig: trust, DialTLSContext: func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("unexpected direct connection")
	}}
	t.Cleanup(func() { http.DefaultTransport = original })

	localProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "CONNECT" || r.Host != "zcode.z.ai:443" {
			t.Errorf("unexpected proxy request: %s %s", r.Method, r.Host)
			http.Error(w, "unexpected target", 400)
			return
		}
		if r.Header.Get("Proxy-Authorization") != "Basic "+base64.StdEncoding.EncodeToString([]byte("tester:fixture-secret")) {
			http.Error(w, "proxy authentication required", http.StatusProxyAuthRequired)
			return
		}
		// Only the loopback fixture can receive traffic from this tunnel.
		up, err := net.DialTimeout("tcp", origin.Listener.Addr().String(), time.Second)
		if err != nil {
			t.Error(err)
			http.Error(w, "fixture unavailable", 502)
			return
		}
		defer up.Close()
		up.SetDeadline(time.Now().Add(10 * time.Second))
		conn, rw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		if _, err := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
			t.Error(err)
			return
		}
		finished := make(chan struct{})
		go func() {
			io.Copy(up, rw.Reader)
			close(finished)
		}()
		io.Copy(conn, up)
		conn.Close()
		<-finished
	}))
	t.Cleanup(localProxy.Close)
	s := startService(t, t.TempDir())
	admin := s.login(t)
	address := strings.Replace(localProxy.URL, "http://", "http://tester:fixture-secret@", 1)
	body := fmt.Sprintf(`{"outbound_proxy":{"enabled":true,"source":"manual","urls":[%q,%q,%q]}}`, address, address, localProxy.URL)
	nodes := request(t, "POST", s.admin+"/api/settings/proxy/nodes", admin, body, 200)
	list := decode[struct{ Nodes []struct{ ID, Name string } }](t, nodes)
	if len(list.Nodes) != 2 || list.Nodes[0].ID == "" || list.Nodes[1].ID == "" || list.Nodes[0].ID == list.Nodes[1].ID {
		t.Fatalf("expected two distinct nodes for the draft, got %+v", list.Nodes)
	}
	raw := request(t, "POST", s.admin+"/api/settings/proxy/test", admin, body, 200)
	if bytes.Contains(nodes, []byte("fixture-secret")) || bytes.Contains(raw, []byte("fixture-secret")) {
		t.Fatal("proxy results exposed the proxy password")
	}
	reply := decode[struct {
		Results []struct {
			Reachable bool
			Transport bool `json:"transport_reachable"`
			Status    int
			Code      string
		}
	}](t, raw)
	if len(reply.Results) != 2 {
		t.Fatalf("expected two probe results, got %s", raw)
	}
	good, bad := reply.Results[0], reply.Results[1]
	if !good.Reachable || !good.Transport || good.Status != 401 || good.Code != "" {
		t.Errorf("unexpected anonymous upstream probe: %+v", good)
	}
	if bad.Reachable || bad.Transport || bad.Code != "proxy_auth_failed" {
		t.Errorf("unexpected refused CONNECT: %+v", bad)
	}
	settings := decode[struct {
		Proxy *struct {
			Enabled bool
			URLs    []string
		} `json:"outbound_proxy"`
	}](t, request(t, "GET", s.admin+"/api/settings", admin, "", 200))
	if settings.Proxy == nil || settings.Proxy.Enabled || len(settings.Proxy.URLs) != 0 {
		t.Fatal("probing a draft changed the saved proxy configuration")
	}

	// Saving the setting must route ordinary account traffic through the proxy too.
	request(t, "PUT", s.admin+"/api/settings", admin, fmt.Sprintf(`{"outbound_proxy":{"enabled":true,"urls":[%q]}}`, address), 200)
	account := fmt.Sprintf(`{"data":{"format":"zcode2api-accounts","version":1,"accounts":[{"family":"zai","tokens":{"zcode_jwt":%q}}]}}`, accountToken)
	request(t, "POST", s.admin+"/api/accounts/import", admin, account, 200)
	select {
	case <-balanceSeen:
	case <-time.After(10 * time.Second):
		t.Fatal("the imported account's balance request did not reach the upstream through the saved proxy")
	}
}

func TestProxySubscription(t *testing.T) {
	const nodes = "https://one.invalid:8443#east-1\r\nhttps://one.invalid:8443#east-copy\r\nhttp://two.invalid#west"
	for _, tc := range []struct{ name, input string }{
		{"plain", nodes}, {"base64", base64.StdEncoding.EncodeToString([]byte(nodes))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := proxy.DecodeSubscription([]byte(tc.input), "^east")
			if err != nil || len(got) != 1 {
				t.Fatalf("expected one matching endpoint, got %d, err=%v", len(got), err)
			}
			node := got[0]
			if node.Name != "east-1" || node.Config["server"] != "one.invalid" || node.Config["port"] != 8443 || node.Config["tls"] != true {
				t.Fatalf("unexpected subscription node: %+v", node)
			}
		})
	}
	if _, err := proxy.DecodeSubscription([]byte(nodes), "^missing$"); err == nil {
		t.Fatal("empty node selection was accepted")
	}
}
