package server

import (
	"testing"
	"time"
)

func rotationPool() (M, []proxyEndpoint) {
	options := M{"enabled": true, "source": "manual", "urls": []any{"http://exit-a:1", "http://exit-b:2", "http://exit-c:3"}}
	return options, manualProxyEndpoints(arr(options["urls"]))
}

// Each claim must advance to the next pool exit so consecutive cards are issued
// from different IPs, skipping nodes that are cooling down.
func TestRotatingEndpointAdvancesPerClaim(t *testing.T) {
	manager := &outboundProxyManager{}
	options, endpoints := rotationPool()
	seen := map[string]int{}
	for i := 0; i < 6; i++ {
		index, _ := manager.selectRotatingEndpoint(options, endpoints)
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
	manager := &outboundProxyManager{}
	options, endpoints := rotationPool()
	manager.selectRotatingEndpoint(options, endpoints)
	manager.blocked["http://exit-a:1"] = time.Now().Add(time.Minute)
	for i := 0; i < 4; i++ {
		index, _ := manager.selectRotatingEndpoint(options, endpoints)
		if index < 0 || endpoints[index].URL == "http://exit-a:1" {
			t.Fatalf("rotation landed on blocked exit: index=%d", index)
		}
	}
}

func TestRotatingEndpointRejectsAllBlocked(t *testing.T) {
	manager := &outboundProxyManager{
		blocked: map[string]time.Time{
			"http://exit-a:1": time.Now().Add(time.Minute),
			"http://exit-b:2": time.Now().Add(time.Minute),
			"http://exit-c:3": time.Now().Add(time.Minute),
		},
	}
	options, endpoints := rotationPool()
	index, _ := manager.selectRotatingEndpoint(options, endpoints)
	if index != -1 {
		t.Fatal("rotation bypassed active cooldowns")
	}
}
