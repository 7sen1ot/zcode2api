package server

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func fingerprintTestApp(t *testing.T) *App {
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

func fingerprintMust(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func fingerprintEqual(t *testing.T, got, want any) {
	t.Helper()
	if js(got) != js(want) {
		t.Fatalf("got %s; want %s", js(got), js(want))
	}
}

func TestUpstreamFingerprintMatchesOfficialClient(t *testing.T) {
	app := fingerprintTestApp(t)
	headers := app.upstreamHeaders(
		"zcode-token",
		"session-123",
		"request-123",
	)
	fingerprintEqual(t, headers.Get("User-Agent"), "ZCode/3.17.2")
	fingerprintEqual(
		t,
		headers.Get("X-Zcode-App-Version"),
		"3.17.2",
	)
	fingerprintEqual(t, headers.Get("X-Request-Id"), "request-123")
	fingerprintEqual(t, headers.Get("X-Session-Id"), "session-123")
	fingerprintEqual(t, headers.Get("X-Platform"), "win32-x64")
	fingerprintEqual(t, headers.Get("X-Os-Category"), "windows")
	queryID := headers.Get("X-Query-Id")
	if len(queryID) != 36 || queryID[14] != '7' {
		t.Fatalf("query id is not UUIDv7: %q", queryID)
	}
	fingerprintEqual(
		t,
		headers.Get("X-Zcode-Query-Source"),
		"main_turn",
	)
}

func TestOfficialSystemBlocksRemainThreeWithClientPrompt(t *testing.T) {
	request, err := buildRequest(
		M{
			"messages": []any{
				M{"role": "system", "content": "client system"},
				M{"role": "user", "content": "hello"},
			},
		},
		"chat",
		"GLM-5.3-Flash",
		"max",
		&toolCatalog{},
		&instructionContext{},
	)
	fingerprintMust(t, err)
	system := arr(request["system"])
	fingerprintEqual(t, len(system), 3)
	fingerprintEqual(
		t,
		str(obj(system[0])["text"]),
		"You are ZCode, an interactive agent",
	)
	if !strings.Contains(str(obj(system[2])["text"]), "client system") {
		t.Fatal("client system prompt was not merged into the official third block")
	}
	applyOfficialSystemSession(request, "device-123", "session-123")
	if strings.Contains(js(request["system"]), "{{VISUALIZATION_ID}}") {
		t.Fatal("visualization placeholder was not replaced")
	}
}

func TestRequestMetadataUserIDMatchesOfficialShape(t *testing.T) {
	raw := requestMetadataUserID("device-mid", "session-123")
	var got map[string]string
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatal(err)
	}
	fingerprintEqual(t, got["device_id"], "device-mid")
	fingerprintEqual(t, got["account_uuid"], "")
	fingerprintEqual(t, got["session_id"], "session-123")
	if len(got) != 3 {
		t.Fatalf("unexpected metadata keys: %v", got)
	}
}
