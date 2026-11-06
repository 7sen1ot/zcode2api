package server

import (
	"context"
	"strings"
	"sync"
)

func profileName(p M) any {
	for _, m := range []M{p, obj(p["customer"])} {
		for _, k := range []string{"username", "userName", "customerName", "nickName"} {
			if s, ok := m[k].(string); ok && strings.TrimSpace(s) != "" {
				return s
			}
		}
	}
	return nil
}
func (a *App) platformName(ctx context.Context, r M) any {
	id := str(r["id"])
	tokens := a.tokens(id)
	for _, k := range []string{"business_token", "web_jwt", "zcode_jwt"} {
		if name := profileName(jwtPayload(str(tokens[k]))); name != nil {
			return name
		}
	}
	token := str(first(nullable(tokens["web_jwt"]), nullable(tokens["business_token"])))
	if token == "" {
		return nil
	}
	key := hash(token)
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
	if res, e := a.customer(ctx, r, token); e == nil && res.OK {
		entry.Name = profileName(obj(res.JSON["data"]))
	}
	close(entry.Done)
	return entry.Name
}
func (a *App) addPlatformNames(ctx context.Context, accounts []any) {
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for _, v := range accounts {
		r := obj(v)
		wg.Go(func() {
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			r["platform_name"] = a.platformName(ctx, r)
		})
	}
	wg.Wait()
}
