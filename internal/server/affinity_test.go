package server

import (
	"net/http"
	"testing"
)

const affinitySession = "3f8b1c9d2e6a4f719c305d17b8e2a4f1"

// A Claude Code user id keeps the account id after the session id. The session
// must still be extracted so conversation affinity keeps one account per chat.
func TestSessionPatternExtractsMidStringSession(t *testing.T) {
	for _, value := range []string{
		"user_ab12cd34_session_" + affinitySession + "_account_z",
		"user_x_session_" + affinitySession,
		"session_" + affinitySession,
	} {
		p := sessionPattern.FindStringSubmatch(value)
		if len(p) < 2 || p[1] != affinitySession {
			t.Fatalf("session not extracted from %q: %v", value, p)
		}
	}
	if p := sessionPattern.FindStringSubmatch("user_ab12cd34_account_z"); p != nil {
		t.Fatalf("unexpected session match: %v", p)
	}
}

func TestAffinityUsesSessionHeaderOverKeySticky(t *testing.T) {
	app := testApp(t)
	r, _ := http.NewRequest("POST", "/v1/messages", nil)
	r.Header.Set("X-Session-Id", "conv-12345678")
	key := M{"id": "key_test", "sticky_minutes": 0}
	affinity, sticky := app.affinity(r, M{}, key)
	if affinity == "key_test" || sticky <= 0 {
		t.Fatalf("session affinity not applied: %q %v", affinity, sticky)
	}
}
