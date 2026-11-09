package server

import (
	"encoding/json"
	"runtime"
	"strings"
	"testing"
)

func TestUpstreamFingerprintMatchesOfficialClient(t *testing.T) {
	app := testApp(t)
	headers := app.upstreamHeaders(
		"zcode-token",
		"session-123",
		"request-123",
	)
	testEqual(t, headers.Get("User-Agent"), "ZCode/3.17.2")
	testEqual(
		t,
		headers.Get("X-Zcode-App-Version"),
		"3.17.2",
	)
	testEqual(t, headers.Get("X-Request-Id"), "request-123")
	testEqual(t, headers.Get("X-Session-Id"), "session-123")
	testEqual(t, headers.Get("X-Platform"), platform())
	osCategory := runtime.GOOS
	if osCategory == "darwin" {
		osCategory = "macos"
	}
	testEqual(t, headers.Get("X-Os-Category"), osCategory)
	testEqual(t, headers.Get("X-Os-Version"), osVersion())
	queryID := headers.Get("X-Query-Id")
	if len(queryID) != 36 || queryID[14] != '7' {
		t.Fatalf("query id is not UUIDv7: %q", queryID)
	}
	testEqual(
		t,
		headers.Get("X-Zcode-Query-Source"),
		"main_turn",
	)
}

func TestInferenceVersionUsesAppVersion(t *testing.T) {
	app := testApp(t)
	app.configMu.Lock()
	app.cfg["app_version"] = "9.8.7"
	obj(app.cfg["http"])["client_version"] = "1.2.3"
	app.configMu.Unlock()
	headers := app.upstreamHeaders("token", "session", "request")
	testEqual(t, headers.Get("User-Agent"), "ZCode/9.8.7")
	testEqual(t, headers.Get("X-Zcode-App-Version"), "9.8.7")
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
	testMust(t, err)
	system := arr(request["system"])
	testEqual(t, len(system), 3)
	testEqual(
		t,
		str(obj(system[0])["text"]),
		"You are ZCode, an interactive agent",
	)
	if !strings.Contains(str(obj(system[2])["text"]), "client system") {
		t.Fatal("client system prompt was not merged into the official third block")
	}
	if strings.Contains(js(request["system"]), "{{VISUALIZATION_ID}}") {
		t.Fatal("upstream system template must not contain a session-specific visualization directory")
	}
}

func TestRequestMetadataUserIDMatchesOfficialShape(t *testing.T) {
	raw := requestMetadataUserID("device-mid", "session-123")
	var got map[string]string
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatal(err)
	}
	testEqual(t, got["device_id"], "device-mid")
	testEqual(t, got["account_uuid"], "")
	testEqual(t, got["session_id"], "session-123")
	if len(got) != 3 {
		t.Fatalf("unexpected metadata keys: %v", got)
	}
}
