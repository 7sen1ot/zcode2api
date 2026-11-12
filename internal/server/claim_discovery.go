package server

import (
	"sort"
	"time"
)

// Marketing touch returns deliveries for this visit, not an inventory of all
// unclaimed cards. Keep an account's actionable deliveries until expiry or a
// confirmed claim, so refreshing and scheduled recovery cannot lose them.
func (a *App) pendingDailyClaimPlans(account M, fresh []any) ([]any, error) {
	id := str(account["id"])
	now := nowMS()
	cutoff := now - (24 * time.Hour).Milliseconds()
	identity := jwtPayload(a.accountTokens(account)["zcode_jwt"])
	scope := hash(js([]any{a.origin, id, account["customer_id"], account["device_mid"], identity["user_id"]}))
	key := "daily_claim_candidates:" + id
	// Serialize read/merge/write against simultaneous page refreshes and the
	// scheduler. No network request is made while holding this lock.
	a.mu.Lock()
	defer a.mu.Unlock()
	saved := obj(a.setting(key, nil))
	candidates := map[string]M{}
	if saved["scope"] == scope {
		for _, value := range arr(saved["plans"]) {
			entry := obj(value)
			if integer(entry["expires_at"]) > now {
				candidates[str(obj(entry["plan"])["plan_id"])] = entry
			}
		}
	}
	add := func(plan M, seen int64) {
		end := integer(secondsMS(plan["ends_at"]))
		if end <= now {
			return // Never retain a delivery with unknown or expired validity.
		}
		candidates[str(plan["plan_id"])] = M{"plan": plan, "expires_at": min(end, seen+(24*time.Hour).Milliseconds())}
	}
	for _, value := range fresh {
		add(obj(value), now)
	}
	if saved["scope"] == nil {
		// Upgrade recovery: old versions discarded delivery metadata, but a
		// scheduled verification failure proves this account had that entry.
		// Recover only those IDs; another account's plan alone is insufficient.
		attempts, err := query(a.db, "SELECT plan_id,MAX(ts) seen_at FROM claim_log WHERE account_id=? AND trigger='scheduled' AND ok=0 AND ts>=? AND code IN ('captcha_risk_blocked','captcha_runtime_error','captcha_timeout','captcha_expired','claim_request_failed') GROUP BY plan_id", id, cutoff)
		if err != nil {
			return nil, err
		}
		if len(attempts) > 0 {
			rows, err := query(a.db, "SELECT last_balance FROM accounts WHERE last_balance IS NOT NULL")
			if err != nil {
				return nil, err
			}
			catalog := map[string]M{}
			for _, row := range rows {
				for _, value := range dailyFreePlans(arr(balancePayload(parse(obj(row)["last_balance"], nil))["plans"])) {
					plan := obj(value)
					// Only public campaign metadata; user_plan_id and account
					// balances must never be copied to another account.
					catalog[str(plan["plan_id"])] = M{"plan_id": plan["plan_id"], "name": plan["name"], "ends_at": plan["ends_at"], "entitlements": plan["entitlements"]}
				}
			}
			for _, value := range attempts {
				attempt := obj(value)
				planID := str(attempt["plan_id"])
				if candidates[planID] == nil && catalog[planID] != nil {
					add(catalog[planID], integer(attempt["seen_at"]))
				}
			}
		}
	}
	claimed := map[string]bool{}
	rows, err := query(a.db, "SELECT DISTINCT plan_id FROM claim_log WHERE account_id=? AND ok=1 AND (ts>=? OR ends_at>?)", id, cutoff, now)
	if err != nil {
		return nil, err
	}
	for _, value := range rows {
		claimed[str(obj(value)["plan_id"])] = true
	}
	// Re-read after discovery: a simultaneous manual claim or balance poll
	// may already have confirmed ownership while the upstream query ran.
	current := a.account(id)
	for _, value := range arr(balanceData(current)["plans"]) {
		plan := obj(value)
		if integer(secondsMS(plan["ends_at"])) > now {
			claimed[str(plan["plan_id"])] = true
		}
	}
	ids := make([]string, 0, len(candidates))
	for planID := range candidates {
		if !claimed[planID] {
			ids = append(ids, planID)
		}
	}
	sort.Strings(ids)
	retained, result := []any{}, []any{}
	for _, planID := range ids {
		retained = append(retained, candidates[planID])
		result = append(result, candidates[planID]["plan"])
	}
	if err := a.setSetting(key, M{"scope": scope, "plans": retained}); err != nil {
		return nil, err
	}
	// A fresh actionable entry can be shown even without an expiry, but it
	// cannot be carried forward into later discovery responses.
	for _, value := range fresh {
		plan := obj(value)
		planID := str(plan["plan_id"])
		if candidates[planID] == nil && !claimed[planID] && integer(plan["ends_at"]) == 0 {
			result = append(result, plan)
		}
	}
	return result, nil
}
