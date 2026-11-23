package test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"testing"
)

func TestAccountTransfer(t *testing.T) {
	s := startService(t, t.TempDir())
	admin := s.login(t)
	importURL := s.admin + "/api/accounts/import"
	token := "test." + base64.RawURLEncoding.EncodeToString([]byte(`{"customer_id":"fixture-customer"}`)) + ".test"
	// A disabled account round-trips without triggering an external verification request.
	account := fmt.Sprintf(`{"label":"offline","family":"zai","customer_id":"fixture-customer","device_mid":"fixture-device","disabled":true,"tokens":{"web_jwt":%q}}`, token)
	file := func(accounts string) string {
		return `{"data":{"format":"zcode2api-accounts","version":1,"accounts":[` + accounts + `]}}`
	}
	request(t, "POST", importURL, admin, file(account+`,{"label":"missing credentials","family":"zai"}`), 400)
	rows := request(t, "GET", s.admin+"/api/accounts?view=groups", admin, "", 200)
	if accounts := decode[[]struct{ ID string }](t, rows); len(accounts) != 0 {
		t.Fatal("rejecting the second record left a partially imported batch")
	}

	type counts struct{ Imported, Skipped, Total int }
	first := decode[counts](t, request(t, "POST", importURL, admin, file(account), 200))
	if first != (counts{Imported: 1, Total: 1}) {
		t.Fatalf("unexpected first import: %+v", first)
	}
	raw := request(t, "POST", s.admin+"/api/accounts/export", admin, "{}", 200)
	exported := decode[struct {
		Format   string
		Version  int
		Accounts []json.RawMessage
	}](t, raw)
	if exported.Format != "zcode2api-accounts" || exported.Version != 1 || len(exported.Accounts) != 1 {
		t.Fatal("export did not produce a versioned file with one account")
	}
	got := decode[struct {
		Label, Family string
		CustomerID    string `json:"customer_id"`
		Device        string `json:"device_mid"`
		Disabled      bool
		Tokens        struct {
			WebJWT string `json:"web_jwt"`
		}
	}](t, exported.Accounts[0])
	if got.Label != "offline" || got.Family != "zai" || got.CustomerID != "fixture-customer" || got.Device != "fixture-device" || !got.Disabled || got.Tokens.WebJWT != token {
		t.Fatal("export did not preserve the account identity, device, disabled state and credential")
	}
	again := decode[counts](t, request(t, "POST", importURL, admin, `{"data":`+string(raw)+"}", 200))
	if again != (counts{Skipped: 1, Total: 1}) {
		t.Fatalf("reimporting the exported file did not skip the existing account: %+v", again)
	}
}

func TestGroupKeyReferences(t *testing.T) {
	s := startService(t, t.TempDir())
	admin := s.login(t)
	group := decode[struct{ ID string }](t, request(t, "POST", s.admin+"/api/groups", admin, `{"name":"secondary","role":"backup"}`, 200))
	if group.ID == "" {
		t.Fatal("group creation returned an empty ID")
	}
	key := s.createKey(t, admin)
	keyURL := s.admin + "/api/keys/" + key.ID
	groupURL := s.admin + "/api/groups/" + group.ID
	request(t, "PATCH", keyURL, admin, fmt.Sprintf(`{"group_order":[%q]}`, group.ID), 200)
	request(t, "PATCH", keyURL, admin, `{"name":"invalid edit","group_order":["missing-group"]}`, 400)
	keys := decode[[]struct {
		ID, Name string
		Groups   []string `json:"group_order"`
	}](t, request(t, "GET", s.admin+"/api/keys", admin, "", 200))
	if len(keys) != 1 || keys[0].ID != key.ID || keys[0].Name != "smoke" || len(keys[0].Groups) != 1 || keys[0].Groups[0] != group.ID {
		t.Fatalf("invalid edit changed the key or its group order: %+v", keys)
	}
	request(t, "DELETE", groupURL, admin, "", 409)
	request(t, "PATCH", keyURL, admin, `{"group_order":null}`, 200)
	request(t, "DELETE", groupURL, admin, "", 200)
	request(t, "DELETE", groupURL, admin, "", 404)
}
