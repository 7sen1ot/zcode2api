package server

import (
	"testing"
	"time"
)

func rotationEndpoints() []proxyEndpoint {
	return []proxyEndpoint{
		{URL: "http://exit-a:1", Name: "a"},
		{URL: "http://exit-b:2", Name: "b"},
		{URL: "http://exit-c:3", Name: "c"},
	}
}

// Each claim must advance to the next pool exit so consecutive cards are issued
// from different IPs, skipping nodes that are cooling down.
func TestRotatingEndpointAdvancesPerClaim(t *testing.T) {
	manager := &outboundProxyManager{
		unavailable: map[string]bool{},
		retryAt:     map[string]time.Time{},
		blocked:     map[string]time.Time{},
	}
	endpoints := rotationEndpoints()
	seen := map[string]int{}
	for i := 0; i < 6; i++ {
		index, _ := manager.selectRotatingEndpoint(M{"enabled": true, "source": "manual", "urls": []any{"http://exit-a:1", "http://exit-b:2", "http://exit-c:3"}}, endpoints)
		if index < 0 {
			t.Fatalf("rotation returned no endpoint at step %d", i)
		}
		seen[endpoints[index].URL]++
	}
	for _, url := range []string{"http://exit-a:1", "http://exit-b:2", "http://exit-c:3"} {
		if seen[url] != 2 {
			t.Fatalf("expected each exit twice, got %v", seen)
		}
	}
}

func TestRotatingEndpointSkipsBlockedNodes(t *testing.T) {
	manager := &outboundProxyManager{
		unavailable: map[string]bool{},
		retryAt:     map[string]time.Time{},
		blocked:     map[string]time.Time{},
	}
	endpoints := rotationEndpoints()
	options := M{"enabled": true, "source": "manual", "urls": []any{"http://exit-a:1", "http://exit-b:2", "http://exit-c:3"}}
	// Establish the config fingerprint first: a key change resets cooldowns.
	manager.selectRotatingEndpoint(options, endpoints)
	manager.blocked["http://exit-a:1"] = time.Now().Add(time.Minute)
	for i := 0; i < 4; i++ {
		index, _ := manager.selectRotatingEndpoint(options, endpoints)
		if index < 0 || endpoints[index].URL == "http://exit-a:1" {
			t.Fatalf("rotation landed on blocked exit: index=%d", index)
		}
	}
}

// When every node is cooling down the cursor still yields a usable position so
// a claim can retry instead of hard-failing during overlapping cooldowns.
func TestRotatingEndpointFallsBackWhenAllBlocked(t *testing.T) {
	manager := &outboundProxyManager{
		unavailable: map[string]bool{},
		retryAt:     map[string]time.Time{},
		blocked: map[string]time.Time{
			"http://exit-a:1": time.Now().Add(time.Minute),
			"http://exit-b:2": time.Now().Add(time.Minute),
			"http://exit-c:3": time.Now().Add(time.Minute),
		},
	}
	endpoints := rotationEndpoints()
	index, _ := manager.selectRotatingEndpoint(M{"enabled": true, "source": "manual", "urls": []any{"http://exit-a:1", "http://exit-b:2", "http://exit-c:3"}}, endpoints)
	if index < 0 {
		t.Fatal("rotation returned no fallback endpoint")
	}
}
