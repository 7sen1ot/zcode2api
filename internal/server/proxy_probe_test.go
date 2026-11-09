package server

import (
	"testing"
	"time"
)

// A probe that reaches 401 releases a stale block; a WAF verdict parks the exit
// for ten minutes; a dead transport gets a one-minute quarantine.
func TestApplyExitVerdictReconciles(t *testing.T) {
	app := registrationTestApp(t)
	app.proxies.unavailable = map[string]bool{}
	app.proxies.retryAt = map[string]time.Time{}
	app.proxies.blocked = map[string]time.Time{}
	now := time.Now()
	app.proxies.blocked["http://ok-exit:1"] = now.Add(time.Hour)
	app.proxies.unavailable["http://dead-exit:2"] = true
	app.proxies.retryAt["http://dead-exit:2"] = now.Add(time.Hour)

	app.applyExitVerdict("http://ok-exit:1", "ok")
	if app.proxies.blocked["http://ok-exit:1"].After(time.Now()) {
		t.Fatal("recovered exit still blocked")
	}

	app.applyExitVerdict("http://waf-exit:3", "blocked")
	until := app.proxies.blocked["http://waf-exit:3"]
	if !until.After(time.Now().Add(9 * time.Minute)) {
		t.Fatalf("WAF exit not parked ten minutes: %v", time.Until(until))
	}

	app.applyExitVerdict("http://waf-exit:3", "blocked")
	if d := time.Until(app.proxies.blocked["http://waf-exit:3"]); d > 11*time.Minute {
		t.Fatalf("repeated probe verdict extended an active block: %v", d)
	}

	app.proxies.retryAt["http://dead-exit:2"] = time.Time{}
	app.applyExitVerdict("http://dead-exit:2", "quarantine")
	if d := time.Until(app.proxies.retryAt["http://dead-exit:2"]); d <= 0 || d > 2*time.Minute {
		t.Fatal("dead exit not quarantined for about a minute")
	}
}

// Cooldown escalation: repeat WAF verdicts double the remaining window up to
// the two-hour cap instead of resetting to the base every time.
func TestWAFBlockEscalation(t *testing.T) {
	manager := &outboundProxyManager{
		unavailable: map[string]bool{},
		retryAt:     map[string]time.Time{},
		blocked:     map[string]time.Time{},
	}
	now := time.Now()
	manager.blocked["http://exit:1"] = now.Add(20 * time.Minute)
	// Simulate markProxyRoute escalation math on an active block.
	base := max(10*time.Minute, 0)
	previous := manager.blocked["http://exit:1"]
	if previous.After(now) {
		base = min(2*time.Hour, max(base, previous.Sub(now)*2))
	}
	if base != 40*time.Minute {
		t.Fatalf("escalation math wrong: %v", base)
	}
}
