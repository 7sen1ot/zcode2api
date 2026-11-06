package server

import (
	"context"
	"strings"
	"time"
)

// Listing a draft does not start Mihomo, select a route or save configuration.
func (a *App) outboundProxyNodes(ctx context.Context, options M) (M, error) {
	if err := validateOutboundProxy(options); err != nil {
		return nil, err
	}
	options = merge(proxyDefaults(), options)
	var endpoints []proxyEndpoint
	protocols := map[string]string{}
	if options["source"] == "subscription" {
		nodes, err := a.subscriptionNodes(ctx, options)
		if err != nil {
			return nil, err
		}
		for _, node := range nodes {
			endpoint := proxyEndpoint{Name: node.name, ID: node.identity()}
			endpoints = append(endpoints, endpoint)
			protocols[endpoint.selectionID()] = strings.ToUpper(str(node.config["type"]))
		}
	} else {
		seen := map[string]bool{}
		for _, value := range arr(options["urls"]) {
			address := str(value)
			if !seen[address] {
				endpoints = append(endpoints, proxyEndpoint{URL: address, Name: maskedProxy(address)})
				seen[address] = true
			}
		}
	}
	a.proxies.mu.Lock()
	defer a.proxies.mu.Unlock()
	active := a.proxies.key == hash(js(options))
	rows := []any{}
	for _, endpoint := range endpoints {
		blocked := active && time.Now().Before(a.proxies.blocked[endpoint.identity()])
		unavailable := active && a.proxies.unavailable[endpoint.identity()] && time.Now().Before(a.proxies.retryAt[endpoint.identity()])
		rows = append(rows, M{
			"id": endpoint.selectionID(), "name": endpoint.Name, "protocol": protocols[endpoint.selectionID()],
			"current":     active && endpoint.identity() == a.proxies.selected && !unavailable && !blocked,
			"unavailable": unavailable,
			"blocked":     blocked,
		})
	}
	return M{"ok": true, "nodes": rows}, nil
}
