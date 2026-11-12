package server

import (
	"context"
	"strings"
)

func profileName(p M) any {
	for _, m := range []M{p, obj(p["customer"])} {
		for _, k := range []string{"username", "userName", "customerName", "nickName"} {
			if s, ok := m[k].(string); ok && strings.TrimSpace(s) != "" {
				return s
			}
		}
	}
	// Batch-registered accounts carry no official name; the masked email is
	// the only readable identity upstream returns, so surface its local part.
	for _, m := range []M{p, obj(p["customer"])} {
		if s, ok := m["email"].(string); ok {
			if local, _, found := strings.Cut(strings.TrimSpace(s), "@"); found && local != "" {
				return local
			}
		}
	}
	return nil
}
func (a *App) platformName(ctx context.Context, r M) any {
	id := str(r["id"])
	key := hash(str(r["enc_tokens"]) + "\x00" + str(r["family"]) + "\x00" + str(r["proxy_url"]))
	a.mu.Lock()
	if a.profiles == nil {
		a.profiles = map[string]*profileEntry{}
	}
	if cached := a.profiles[id]; cached != nil && cached.Key == key && cached.Until > nowMS() {
		done := cached.Done
		a.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil
		case <-done:
			return cached.Name
		}
	}
	entry := &profileEntry{Key: key, Until: nowMS() + 300000, Done: make(chan struct{})}
	a.profiles[id] = entry
	a.mu.Unlock()
	defer close(entry.Done)
	tokens := a.accountTokens(r)
	for _, k := range []string{"business_token", "web_jwt", "zcode_jwt"} {
		if entry.Name = profileName(jwtPayload(tokens[k])); entry.Name != nil {
			return entry.Name
		}
	}
	if token := str(first(nullable(tokens["web_jwt"]), nullable(tokens["business_token"]))); token != "" {
		if res, e := a.customer(ctx, r, token); e == nil && res.OK {
			entry.Name = profileName(obj(res.JSON["data"]))
		}
	}
	return entry.Name
}
func (a *App) addPlatformNames(ctx context.Context, accounts []any) {
	parallelFor(len(accounts), 4, func(i int) {
		if ctx.Err() == nil {
			r := obj(accounts[i])
			r["platform_name"] = a.platformName(ctx, r)
		}
	})
}
