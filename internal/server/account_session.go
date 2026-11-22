package server

import (
	"context"
	"fmt"
	"strings"
	"time"
	accountdata "zcode2api/internal/server/account"
	"zcode2api/internal/server/fault"
	"zcode2api/internal/server/shared"
	"zcode2api/internal/server/storage"
)

func (a *App) accountLoginProvider(r shared.M) string {
	return accountdata.LoginProvider(shared.Str(r["family"]), a.accountTokens(r))
}

func (a *App) sameLoginSession(left, right shared.M) bool {
	if left == nil || right == nil || left["id"] != right["id"] || left["family"] != right["family"] || left["customer_id"] != right["customer_id"] {
		return false
	}
	l, r := a.accountTokens(left), a.accountTokens(right)
	return accountdata.LoginProvider(shared.Str(left["family"]), l) == accountdata.LoginProvider(shared.Str(right["family"]), r) && l["zcode_jwt"] == r["zcode_jwt"] && l["session_revision"] == r["session_revision"]
}

// Serialize result application with credential replacement and disabling. An
// old request must not mark a freshly authorized account as needing login.
func (a *App) updateAccountStatusIfCurrent(snapshot, balance, fields shared.M) bool {
	a.balances.applyMu.Lock()
	defer a.balances.applyMu.Unlock()
	id := shared.Str(snapshot["id"])
	current := a.account(id)
	if current == nil || current["status"] == "disabled" || current["enc_tokens"] != snapshot["enc_tokens"] || current["device_mid"] != snapshot["device_mid"] || current["proxy_url"] != snapshot["proxy_url"] || balance != nil && !a.balanceResultCurrent(current, balance) {
		return false
	}
	if err := a.persistAccountFields(id, fields); err != nil {
		return false
	}
	if fields["status"] == "needs_relogin" || fields["status"] == "web_only" {
		a.invalidateBalanceLocked(id)
	}
	return true
}

func (a *App) encryptedTokenChanges(account, changes shared.M) (shared.M, error) {
	enc := shared.Obj(shared.Parse(account["enc_tokens"], nil))
	for key, value := range changes {
		if value == nil || shared.Str(value) == "" {
			delete(enc, key)
			continue
		}
		ciphertext, err := accountdata.Encrypt(shared.Str(value), a.secret)
		if err != nil {
			return nil, err
		}
		enc[key] = ciphertext
	}
	return enc, nil
}

func (a *App) saveLoginAccount(target, identity string, fields, tokens shared.M) (shared.M, bool, error) {
	a.credentialMu.Lock()
	defer a.credentialMu.Unlock()
	a.balances.applyMu.Lock()
	defer a.balances.applyMu.Unlock()
	provider := shared.Str(tokens["active_provider"])
	family := accountdata.LoginFamily(provider)
	var account shared.M
	if target != "" {
		account = a.account(target)
		if account == nil || shared.Str(account["customer_id"]) != identity || a.accountLoginProvider(account) != provider {
			return nil, false, fmt.Errorf("授权账号或登录渠道与原账号不一致，未更新凭据")
		}
	} else {
		rows, err := storage.Query(a.db, "SELECT * FROM accounts WHERE family=? AND customer_id=? ORDER BY created_at", family, identity)
		if err != nil {
			return nil, false, err
		}
		for _, row := range rows {
			candidate := shared.Obj(row)
			if a.accountLoginProvider(candidate) == provider {
				account = candidate
				break
			}
		}
	}
	fields = shared.Clone(fields)
	fields["family"], fields["customer_id"] = family, identity
	if _, exists := fields["business_token_expires_at"]; !exists {
		fields["business_token_expires_at"] = accountdata.OptionalExpiry(accountdata.JwtExpiresAt(shared.Str(tokens["business_token"])))
	}
	if account == nil {
		created, err := a.insertAccount(fields, tokens)
		return created, false, err
	}
	previous := a.accountTokens(account)
	// A desktop reimport of the exact same login need not discard OAuth material
	// already retained by this gateway, but a new revision/JWT cannot reuse it.
	sameSession := previous["zcode_jwt"] == shared.Str(tokens["zcode_jwt"]) && previous["session_revision"] == shared.Str(tokens["session_revision"])
	if sameSession && !shared.Truth(tokens["oauth_access"]) && previous["oauth_access"] != "" {
		tokens["oauth_access"] = previous["oauth_access"]
	}
	if sameSession && provider == "zai" && !shared.Truth(tokens["business_token"]) && shared.Truth(tokens["oauth_access"]) && shared.Str(tokens["oauth_access"]) == previous["oauth_access"] {
		tokens["business_token"] = shared.Nullable(previous["business_token"])
		tokens["business_token_obtained_at"] = shared.Nullable(previous["business_token_obtained_at"])
		fields["business_token_expires_at"] = account["business_token_expires_at"]
	}
	if previous["business_token"] == shared.Str(tokens["business_token"]) && fields["business_token_expires_at"] == nil {
		fields["business_token_expires_at"] = account["business_token_expires_at"]
	}
	enc, err := a.encryptedTokenChanges(account, tokens)
	if err != nil {
		return nil, false, err
	}
	changes := shared.M{"enc_tokens": enc, "business_token_expires_at": fields["business_token_expires_at"]}
	if shared.Truth(fields["device_mid"]) {
		changes["device_mid"] = fields["device_mid"]
	}
	if account["status"] != "disabled" {
		changes["status"], changes["last_error"], changes["fail_streak"] = "unverified", nil, 0
	}
	id := shared.Str(account["id"])
	if err := a.persistAccountFields(id, changes); err != nil {
		return nil, false, err
	}
	a.invalidateBalanceLocked(id)
	return a.account(id), true, nil
}

func (a *App) verifyImportedAccount(ctx context.Context, account shared.M) shared.M {
	id := shared.Str(account["id"])
	if account["status"] == "disabled" {
		return shared.M{"ok": false, "id": id, "status": "disabled", "reason": "账号保持停用"}
	}
	verifyCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	result, err := a.verify(verifyCtx, account)
	if err != nil {
		reason := fault.ConciseFailureMessage(upstreamRequestFailure(verifyCtx, err))
		if verifyCtx.Err() == context.DeadlineExceeded {
			reason = "验证超时，可在账号列表重新验证"
		}
		result = shared.M{"ok": false, "status": "unverified", "reason": reason}
		// A response for an older credential must not overwrite a later login.
		if !a.updateAccountStatusIfCurrent(account, nil, shared.M{"last_error": reason}) {
			result["status"], result["reason"] = a.account(id)["status"], "账号已更新，请在账号列表查看"
		}
	}
	result["id"] = id
	a.scheduleNext(id, time.Second)
	return result
}

func profileName(p shared.M) any {
	for _, m := range []shared.M{p, shared.Obj(p["customer"])} {
		for _, k := range []string{"name", "displayName", "username", "userName", "customerName", "nickName"} {
			if s, ok := m[k].(string); ok && strings.TrimSpace(s) != "" {
				return s
			}
		}
	}
	// Batch-registered accounts carry no official name; the masked email is
	// the only readable identity upstream returns, so surface its local part.
	for _, m := range []shared.M{p, shared.Obj(p["customer"])} {
		if s, ok := m["email"].(string); ok {
			if local, _, found := strings.Cut(strings.TrimSpace(s), "@"); found && local != "" {
				return local
			}
		}
	}
	return nil
}

func profileCacheKey(r shared.M) string {
	return shared.Hash(shared.Str(r["enc_tokens"]) + "\x00" + shared.Str(r["family"]) + "\x00" + shared.Str(r["proxy_url"]))
}

// Profile is authenticated with the ZCode login, never a linked business token.
// A missing link is not proof of unlinking; only a new login clears old material.
func (a *App) refreshLoginProfile(ctx context.Context, r shared.M, tokens map[string]string) shared.M {
	if tokens["zcode_jwt"] == "" || accountdata.LoginJWTExpired(tokens) || r["status"] == "disabled" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	res, err := a.api(ctx, r, "GET", "/api/v1/oauth/profile", tokens["zcode_jwt"], nil, nil, nil)
	if err != nil || !res.OK || !shared.CodeIs(res.JSON["code"], 0) {
		return nil
	}
	data := shared.Obj(res.JSON["data"])
	profile := accountdata.LoginProfile(shared.Obj(data["user"]))
	identity := shared.Str(accountdata.JwtPayload(tokens["zcode_jwt"])["user_id"])
	if identity == "" || shared.Str(profile["user_id"]) != identity {
		return nil
	}
	changes, fields := shared.M{}, shared.M{}
	if shared.JSON(profile) != tokens["provider_user_info"] {
		changes["provider_user_info"] = shared.JSON(profile)
	}
	linked := shared.M{"active_provider": accountdata.LoginProvider(shared.Str(r["family"]), tokens)}
	accountdata.AttachLinkedBigModel(linked, identity, shared.Obj(data["bigmodel"]))
	for _, key := range []string{"linked_bigmodel", "business_token", "business_customer_id"} {
		if value := linked[key]; shared.Truth(value) && shared.Str(value) != tokens[key] {
			changes[key] = value
		}
	}
	if shared.Truth(changes["business_token"]) {
		fields["business_token_expires_at"] = accountdata.OptionalExpiry(accountdata.JwtExpiresAt(shared.Str(changes["business_token"])))
	}
	if len(changes) > 0 {
		if changed, err := a.updateLoginMaterialIfCurrent(r, changes, fields); err != nil || !changed {
			return nil
		}
	}
	return profile
}

func (a *App) platformName(ctx context.Context, r shared.M) any {
	id := shared.Str(r["id"])
	key := profileCacheKey(r)
	a.mu.Lock()
	if a.profiles == nil {
		a.profiles = map[string]*profileEntry{}
	}
	if cached := a.profiles[id]; cached != nil && cached.Key == key && cached.Until > shared.NowMS() {
		done := cached.Done
		a.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil
		case <-done:
			return cached.Name
		}
	}
	entry := &profileEntry{Key: key, Until: shared.NowMS() + 300000, Done: make(chan struct{})}
	a.profiles[id] = entry
	a.mu.Unlock()
	defer close(entry.Done)
	tokens := a.accountTokens(r)
	entry.Name = profileName(shared.Obj(shared.Parse(tokens["provider_user_info"], nil)))
	provider := accountdata.LoginProvider(shared.Str(r["family"]), tokens)
	if entry.Name == nil && (provider == "zai_cn" || provider == "zai") {
		if profile := a.refreshLoginProfile(ctx, r, tokens); profile != nil {
			if name := profileName(profile); name != nil {
				entry.Name = name
			}
			if current := a.account(id); a.sameLoginSession(r, current) {
				r, tokens = current, a.accountTokens(current)
				a.mu.Lock()
				entry.Key = profileCacheKey(current)
				a.mu.Unlock()
			}
		}
	}
	if entry.Name != nil {
		return entry.Name
	}
	for _, k := range []string{"business_token", "web_jwt", "zcode_jwt"} {
		if entry.Name = profileName(accountdata.JwtPayload(tokens[k])); entry.Name != nil {
			return entry.Name
		}
	}
	if tokens["zcode_jwt"] != "" {
		if res, err := a.businessCustomer(ctx, r); err == nil && res.OK {
			entry.Name = profileName(shared.Obj(res.JSON["data"]))
		}
	} else if token := tokens["web_jwt"]; token != "" {
		if res, e := a.customer(ctx, r, token); e == nil && res.OK {
			entry.Name = profileName(shared.Obj(res.JSON["data"]))
		}
	}
	return entry.Name
}

func (a *App) addPlatformNames(ctx context.Context, accounts []any) {
	shared.ParallelFor(len(accounts), shared.AccountOperationConcurrency, func(i int) {
		if ctx.Err() == nil {
			r := shared.Obj(accounts[i])
			r["platform_name"] = a.platformName(ctx, r)
		}
	})
}
