package server

import (
	"context"
	"net/http"
	"time"
)

// Aliyun rejects a V3 verification parameter submitted more than 90 seconds
// after the traceless check (F019), which caps any prefetch window.
const (
	maxCaptchaTicketTTL = 90 * time.Second
	// Leave time for the business request to reach the verifier.
	captchaTicketSafetyMargin = 5 * time.Second
)

// Background SDK work only takes an idle slot after foreground waiters. The
// notification is a broadcast so canceling one waiter cannot strand others.
func (a *App) acquireCaptchaSlot(ctx context.Context, background bool) bool {
	slot := a.captchaSlot()
	a.captcha.mu.Lock()
	if !background {
		a.captcha.waiters++
		defer func() {
			a.captcha.waiters--
			a.notifyCaptchaSlotLocked()
			a.captcha.mu.Unlock()
		}()
	} else {
		defer a.captcha.mu.Unlock()
	}
	for {
		if ctx.Err() != nil {
			return false
		}
		if !background || a.captcha.waiters == 0 {
			select {
			case slot <- struct{}{}:
				return true
			default:
			}
		}
		changed := a.captcha.available
		a.captcha.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
		}
		a.captcha.mu.Lock()
	}
}

func (a *App) notifyCaptchaSlotLocked() {
	close(a.captcha.available)
	a.captcha.available = make(chan struct{})
}

func (a *App) releaseCaptchaSlot() {
	a.captcha.mu.Lock()
	<-a.captcha.slot
	a.notifyCaptchaSlotLocked()
	a.captcha.mu.Unlock()
}

// Retain the existing subscription runtime rather than selecting a new exit.
// Otherwise a settings/subscription refresh can stop its local proxy while a
// background verification is still using it.
func (a *App) retainCaptchaProxy(proxyURL string) (func(), bool) {
	a.proxies.mu.Lock()
	defer a.proxies.mu.Unlock()
	if a.proxies.closing {
		return func() {}, false
	}
	for runtime := range a.proxies.runtimes {
		for _, endpoint := range runtime.endpoints {
			if endpoint.URL == proxyURL {
				if !runtime.alive() {
					return func() {}, false
				}
				runtime.references++
				return func() { a.releaseManagedProxy(runtime) }, true
			}
		}
	}
	return func() {}, true
}

// pinnedProxyContext fixes a context to one recorded exit, mirroring what the
// original request pinned, so background work cannot drift to another proxy.
func pinnedProxyContext(parent context.Context, proxyURL string) context.Context {
	return context.WithValue(parent, proxyContextKey{}, proxyRoute{URL: proxyURL})
}

// captchaTicketTTL returns the prefetch window; an explicit 0 disables the
// ticket pool, an absent setting keeps the default.
func captchaTicketTTL(options M) time.Duration {
	raw, present := options["ticket_ttl_ms"]
	if !present {
		return 75 * time.Second
	}
	value := num(raw)
	if value <= 0 {
		return 0
	}
	ttl := time.Duration(value) * time.Millisecond
	if ttl < 30*time.Second {
		ttl = 30 * time.Second
	}
	return min(ttl, maxCaptchaTicketTTL)
}

const (
	captchaPurposeInference      = "inference"
	captchaPurposeClaim          = "claim"
	captchaPurposeScheduledClaim = "scheduled_claim"
	captchaPrefetchDemandWindow  = 30 * time.Second
	maxCaptchaPools              = 128
)

type captchaTicketScope struct {
	sceneKey, accountID, accountVersion, purpose string
	accountDevice                                string
	generation                                   uint64
	guard                                        upstreamRequestScope
	route                                        proxySelection
	pooled                                       bool
}

func (s captchaTicketScope) key() string {
	return hash(js([]any{s.sceneKey, s.accountID, s.accountVersion, s.purpose, s.generation, s.pooled, s.route.generation, s.route.identity}))
}

func (s captchaTicketScope) matchesCooldown(key string) bool {
	return key != "" && (key == s.guard.account || key == s.guard.exit || key == s.guard.device || key == s.guard.verification || key == s.accountDevice)
}

type captchaPrefetchJob struct{ cancel context.CancelFunc }

type captchaTicketPool struct {
	scope                  captchaTicketScope
	ticket                 *captchaTicket
	runtimeState           M
	job                    *captchaPrefetchJob
	lastUsed, lastAccepted time.Time
}

type captchaVerification struct {
	Headers  http.Header
	pool     *captchaTicketPool
	proxyURL string
	accepted bool
}

func captchaScopeChanged() *Failure {
	return &Failure{Kind: "server", Code: "captcha_config_changed", Message: "验证账号、用途或场景已更新，请重新验证", Retryable: true}
}

func (a *App) captchaAccountVersion(account M) string {
	token := str(a.tokens(str(account["id"]))["zcode_jwt"])
	return captchaCredentialVersion(account, token)
}

func captchaCredentialVersion(account M, token string) string {
	if token == "" {
		return ""
	}
	return hash(js([]any{account["id"], account["device_mid"], account["proxy_url"], token}))
}

func validateCaptchaRequestToken(verification captchaVerification, account M, token string) *Failure {
	if verification.pool != nil && verification.pool.scope.accountVersion != captchaCredentialVersion(account, token) {
		return captchaScopeChanged()
	}
	return nil
}

func (a *App) captchaScope(ctx context.Context, account M, purpose string, scene M) (captchaTicketScope, *Failure) {
	if str(account["id"]) == "" || (purpose != captchaPurposeInference && purpose != captchaPurposeClaim && purpose != captchaPurposeScheduledClaim) {
		return captchaTicketScope{}, captchaScopeChanged()
	}
	scope := captchaTicketScope{sceneKey: a.captchaSceneKey(ctx), accountID: str(account["id"]), accountVersion: a.captchaAccountVersion(account), purpose: purpose, guard: a.captchaRequestScope(ctx, account, scene), accountDevice: a.upstreamScope(ctx, account, a.origin, true).device}
	if scope.accountVersion == "" {
		return scope, &Failure{Kind: "auth", Code: "authentication_error", Message: "验证账号凭据不可用"}
	}
	scope.route, scope.pooled = ctx.Value(proxySelectionKey{}).(proxySelection)
	a.captcha.mu.Lock()
	current := a.captcha.scenes[scope.sceneKey]
	scope.generation = current.Generation
	valid := current.Generation != 0 && current.Loading == nil && js(current.Value) == js(scene)
	a.captcha.mu.Unlock()
	if !valid || !a.captchaScopeCurrent(ctx, scope) {
		return captchaTicketScope{}, captchaScopeChanged()
	}
	return scope, nil
}

func (a *App) captchaScopeCurrent(ctx context.Context, scope captchaTicketScope) bool {
	account := a.account(scope.accountID)
	route, pooled := ctx.Value(proxySelectionKey{}).(proxySelection)
	if account == nil || scope.accountVersion == "" || account["status"] == "needs_relogin" || account["status"] == "disabled" || pooled != scope.pooled || route != scope.route || scope.sceneKey != a.captchaSceneKey(ctx) || scope.accountVersion != a.captchaAccountVersion(account) {
		return false
	}
	a.captcha.mu.Lock()
	scene := a.captcha.scenes[scope.sceneKey]
	valid := scene.Generation == scope.generation && scene.Loading == nil && truth(scene.Value["enabled"]) && time.Now().Before(scene.Expires)
	a.captcha.mu.Unlock()
	return valid
}

func (a *App) captchaPool(scope captchaTicketScope) *captchaTicketPool {
	a.captcha.mu.Lock()
	defer a.captcha.mu.Unlock()
	if a.captcha.pools == nil {
		a.captcha.pools = map[string]*captchaTicketPool{}
	}
	key := scope.key()
	if pool := a.captcha.pools[key]; pool != nil {
		pool.lastUsed = time.Now()
		return pool
	}
	for candidate, pool := range a.captcha.pools {
		if pool.scope.accountID == scope.accountID && pool.scope.accountVersion != scope.accountVersion {
			a.dropCaptchaPoolLocked(candidate)
		}
	}
	if len(a.captcha.pools) >= maxCaptchaPools {
		oldest := ""
		for candidate, pool := range a.captcha.pools {
			if oldest == "" || pool.lastUsed.Before(a.captcha.pools[oldest].lastUsed) {
				oldest = candidate
			}
		}
		a.dropCaptchaPoolLocked(oldest)
	}
	pool := &captchaTicketPool{scope: scope, lastUsed: time.Now()}
	a.captcha.pools[key] = pool
	return pool
}

func (a *App) dropCaptchaPoolLocked(key string) {
	if pool := a.captcha.pools[key]; pool != nil && pool.job != nil {
		pool.job.cancel()
	}
	delete(a.captcha.pools, key)
}

func (a *App) dropCaptchaScenePoolsLocked(sceneKey string) {
	for key, pool := range a.captcha.pools {
		if pool.scope.sceneKey == sceneKey {
			a.dropCaptchaPoolLocked(key)
		}
	}
}

func (a *App) takeCaptchaTicket(pool *captchaTicketPool) (captchaTicket, bool) {
	if captchaTicketTTL(obj(a.configValue("http", "captcha"))) <= 0 {
		return captchaTicket{}, false
	}
	a.captcha.mu.Lock()
	defer a.captcha.mu.Unlock()
	scene := a.captcha.scenes[pool.scope.sceneKey]
	if a.captcha.pools[pool.scope.key()] != pool || pool.ticket == nil {
		return captchaTicket{}, false
	}
	ticket := *pool.ticket
	pool.ticket = nil
	if ticket.scope != pool.scope || scene.Generation != pool.scope.generation || !time.Now().Before(scene.Expires) || !time.Now().Add(captchaTicketSafetyMargin).Before(ticket.expiresAt) {
		return captchaTicket{}, false
	}
	return ticket, true
}

// Count accepted business requests, not ticket generation. A lone request never
// starts a speculative SDK execution; demand expires after a quiet interval.
func (a *App) acceptCaptchaVerification(ctx context.Context, verification *captchaVerification) {
	pool := verification.pool
	if pool == nil || verification.accepted {
		return
	}
	verification.accepted = true
	if !a.captchaScopeCurrent(ctx, pool.scope) {
		return
	}
	a.captcha.mu.Lock()
	if a.captcha.pools[pool.scope.key()] != pool {
		a.captcha.mu.Unlock()
		return
	}
	now := time.Now()
	recent := !pool.lastAccepted.IsZero() && now.Sub(pool.lastAccepted) <= captchaPrefetchDemandWindow
	pool.lastAccepted, pool.lastUsed = now, now
	a.captcha.mu.Unlock()
	if recent {
		a.scheduleTicketRenewal(pool, verification.proxyURL)
	}
}

func (a *App) rejectCaptchaVerification(verification captchaVerification) {
	if verification.pool == nil {
		return
	}
	a.captcha.mu.Lock()
	defer a.captcha.mu.Unlock()
	key := verification.pool.scope.key()
	if a.captcha.pools[key] == verification.pool {
		a.dropCaptchaPoolLocked(key)
	}
}

// Called after a shared cooldown is published. Removing the pool also prevents
// an older in-flight success from reviving demand or storing a canceled mint.
func (a *App) pauseCaptchaPrefetch(cooldownKey string) {
	a.captcha.mu.Lock()
	defer a.captcha.mu.Unlock()
	for key, pool := range a.captcha.pools {
		if pool.scope.matchesCooldown(cooldownKey) {
			a.dropCaptchaPoolLocked(key)
		}
	}
}

func (a *App) captchaPrefetchBlocked(scope captchaTicketScope) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for key := range a.upstreamCooldowns {
		if scope.matchesCooldown(key) {
			return true
		}
	}
	return false
}

func (a *App) scheduleTicketRenewal(pool *captchaTicketPool, proxyURL string) {
	options := obj(a.configValue("http", "captcha"))
	// Claim workers mint tickets on demand. Background renewal would bypass
	// their concurrency bound and race a new manual claim for the same account.
	// Inference keeps its existing prefetch and foreground-priority queue.
	if pool.scope.purpose != captchaPurposeInference {
		return
	}
	if captchaTicketTTL(options) <= 0 || a.ctx.Err() != nil || a.captchaPrefetchBlocked(pool.scope) {
		return
	}
	a.captcha.mu.Lock()
	scene := a.captcha.scenes[pool.scope.sceneKey]
	if a.captcha.pools[pool.scope.key()] != pool || pool.job != nil || time.Since(pool.lastAccepted) > captchaPrefetchDemandWindow || scene.Generation != pool.scope.generation || !truth(scene.Value["enabled"]) {
		a.captcha.mu.Unlock()
		return
	}
	if pool.ticket != nil && time.Now().Add(captchaTicketSafetyMargin).Before(pool.ticket.expiresAt) {
		a.captcha.mu.Unlock()
		return
	}
	ctx, cancel := context.WithTimeout(pinnedProxyContext(a.ctx, proxyURL), ms(fallback(options["timeout_ms"], 30000))+3*time.Second)
	if pool.scope.pooled {
		ctx = context.WithValue(ctx, proxySelectionKey{}, pool.scope.route)
	}
	job := &captchaPrefetchJob{cancel: cancel}
	pool.job = job
	a.captcha.mu.Unlock()
	release, retained := a.retainCaptchaProxy(proxyURL)
	a.background(func() {
		defer cancel()
		defer release()
		ticket, ready := captchaTicket{}, false
		if retained {
			ticket, ready = a.renewCaptchaTicket(ctx, pool, options)
		}
		a.captcha.mu.Lock()
		defer a.captcha.mu.Unlock()
		if a.captcha.pools[pool.scope.key()] != pool || pool.job != job {
			return
		}
		pool.job = nil
		scene := a.captcha.scenes[pool.scope.sceneKey]
		if !ready || ctx.Err() != nil || scene.Generation != pool.scope.generation {
			pool.lastAccepted, pool.ticket = time.Time{}, nil
			return
		}
		ticket.expiresAt = ticket.expiresAt.Add(captchaTicketTTL(options) - maxCaptchaTicketTTL)
		if _, used := a.captcha.used[hash(ticket.param)]; !used && time.Now().Add(captchaTicketSafetyMargin).Before(ticket.expiresAt) {
			pool.ticket = &ticket
		}
	})
}

func (a *App) renewCaptchaTicket(ctx context.Context, pool *captchaTicketPool, options M) (captchaTicket, bool) {
	if !a.acquireCaptchaSlot(ctx, true) {
		return captchaTicket{}, false
	}
	defer a.releaseCaptchaSlot()
	if ctx.Err() != nil || !a.captchaScopeCurrent(ctx, pool.scope) || a.captchaPrefetchBlocked(pool.scope) || js(options) != js(obj(a.configValue("http", "captcha"))) {
		return captchaTicket{}, false
	}
	a.captcha.mu.Lock()
	scene := a.captcha.scenes[pool.scope.sceneKey]
	valid := a.captcha.pools[pool.scope.key()] == pool && scene.Generation == pool.scope.generation && time.Now().Before(scene.Expires) && time.Since(pool.lastAccepted) <= captchaPrefetchDemandWindow
	a.captcha.mu.Unlock()
	if !valid {
		return captchaTicket{}, false
	}
	account := a.account(pool.scope.accountID)
	ticket, failure := a.mintCaptchaTicket(ctx, account, options, scene.Value, pool.scope)
	if failure != nil {
		if ctx.Err() == nil {
			a.log.Printf("验证码预取失败: %s", conciseFailureMessage(failure))
		}
		return captchaTicket{}, false
	}
	return ticket, ctx.Err() == nil && a.captchaScopeCurrent(ctx, pool.scope) && !a.captchaPrefetchBlocked(pool.scope)
}
