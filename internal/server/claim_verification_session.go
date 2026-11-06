package server

import (
	"context"
	"time"
)

const claimCaptchaSessionTTL = 3 * time.Minute
const maxClaimCaptchaSessions = 64

type claimCaptchaSession struct {
	scope                    captchaTicketScope
	planID, region, proxyURL string
	expires                  time.Time
	done                     chan struct{}
	release                  func()
}

func validClaimCaptchaSessionID(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !(character >= '0' && character <= '9' || character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}

func claimCaptchaSessionInvalid() *Failure {
	return &Failure{Kind: "risk", Code: "captcha_session_invalid", Message: "领取验证会话已失效或与当前账号、计划不匹配，请重新验证"}
}

func (a *App) newClaimCaptchaSession(ctx context.Context, scope captchaTicketScope, plan, region string) (string, *Failure) {
	release, retained := a.retainCaptchaProxy(selectedProxy(ctx))
	if !retained {
		return "", claimCaptchaSessionInvalid()
	}
	session := &claimCaptchaSession{scope: scope, planID: plan, region: region, proxyURL: selectedProxy(ctx), expires: time.Now().Add(claimCaptchaSessionTTL), done: make(chan struct{}), release: release}
	token := random(32)
	key := hash(token)
	a.captcha.mu.Lock()
	current := a.captcha.scenes[scope.sceneKey]
	if a.ctx.Err() != nil || current.Generation != scope.generation || !time.Now().Before(current.Expires) || len(a.captcha.browser) >= maxClaimCaptchaSessions {
		a.captcha.mu.Unlock()
		release()
		return "", &Failure{Kind: "server", Code: "captcha_capacity_exceeded", Message: "领取验证会话暂不可用，请稍后重试"}
	}
	if a.captcha.browser == nil {
		a.captcha.browser = map[string]*claimCaptchaSession{}
	}
	a.captcha.browser[key] = session
	a.captcha.mu.Unlock()
	a.background(func() {
		timer := time.NewTimer(time.Until(session.expires))
		defer timer.Stop()
		select {
		case <-session.done:
			return // The consumer owns the retained route after taking the session.
		case <-a.ctx.Done():
		case <-timer.C:
		}
		a.cancelClaimVerification(token)
	})
	return token, nil
}

func (a *App) cancelClaimVerification(token string) {
	if !validClaimCaptchaSessionID(token) {
		return
	}
	key := hash(token)
	a.captcha.mu.Lock()
	session := a.captcha.browser[key]
	if session != nil {
		delete(a.captcha.browser, key)
		close(session.done)
	}
	a.captcha.mu.Unlock()
	if session != nil {
		session.release()
	}
}

// The nonce is local request binding, not a claim about the browser's public IP.
// The upstream verifier still validates the unmodified browser-issued ticket.
func (a *App) claimVerificationContext(parent context.Context, account M, plan, token string, verification M) (context.Context, func(), error) {
	if str(verification["captcha_verify_param"]) == "" {
		return a.pinOutboundProxy(parent, account)
	}
	key := hash(str(verification["verification_session"]))
	a.captcha.mu.Lock()
	session := a.captcha.browser[key]
	a.captcha.mu.Unlock()
	if session == nil || session.scope.accountID != str(account["id"]) || session.planID != plan || session.region != str(verification["captcha_region"]) || session.scope.purpose != captchaPurposeClaim || session.scope.accountVersion != captchaCredentialVersion(account, token) {
		return parent, func() {}, claimCaptchaSessionInvalid()
	}
	ctx := pinnedProxyContext(parent, session.proxyURL)
	if session.scope.pooled {
		ctx = context.WithValue(ctx, proxySelectionKey{}, session.scope.route)
	}
	if !a.captchaScopeCurrent(ctx, session.scope) {
		a.cancelClaimVerification(str(verification["verification_session"]))
		return parent, func() {}, claimCaptchaSessionInvalid()
	}
	a.captcha.mu.Lock()
	current := a.captcha.scenes[session.scope.sceneKey]
	if a.captcha.browser[key] != session || !time.Now().Before(session.expires) || current.Generation != session.scope.generation || !time.Now().Before(current.Expires) || !truth(current.Value["enabled"]) {
		a.captcha.mu.Unlock()
		a.cancelClaimVerification(str(verification["verification_session"]))
		return parent, func() {}, claimCaptchaSessionInvalid()
	}
	delete(a.captcha.browser, key)
	close(session.done)
	a.captcha.mu.Unlock()
	return ctx, session.release, nil
}
