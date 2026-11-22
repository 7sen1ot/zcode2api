package server

import (
	"context"
	"time"
	accountdata "zcode2api/internal/server/account"
	"zcode2api/internal/server/fault"
	"zcode2api/internal/server/shared"
	"zcode2api/internal/server/storage"
)

const claimConfirmationGrace = 30 * time.Second

type claimConfirmationOnlyKey struct{}

func pendingClaimKey(account shared.M, plan string) string {
	return "pending_claim:" + shared.Str(account["id"]) + ":" + shared.Hash(plan)
}

func (a *App) claimAccountScope(account shared.M) string {
	identity := accountdata.JwtPayload(a.accountTokens(account)["zcode_jwt"])
	return shared.Hash(shared.JSON([]any{a.origin, account["id"], account["customer_id"], identity["user_id"], identity["sub"]}))
}

// Persist before the POST so a crash cannot turn an unconfirmed submission into a new one.
func (a *App) markPendingClaim(account shared.M, plan string) error {
	return a.setSetting(pendingClaimKey(account, plan), shared.M{"scope": a.claimAccountScope(account), "started_at": shared.NowMS()})
}

func (a *App) clearPendingClaim(account shared.M, plan string) error {
	return a.exec("DELETE FROM settings WHERE k=?", pendingClaimKey(account, plan))
}

func claimResultUnknown(started int64) *fault.Failure {
	wait := max(time.Second, time.Until(time.UnixMilli(started).Add(claimConfirmationGrace)))
	return &fault.Failure{Kind: "network", Code: "claim_result_unknown", Message: "领取结果尚未确认，请稍后重试，系统会先查询持卡状态", Retryable: true, RetryAfter: wait, Detail: shared.M{"stage": "claim_confirmation", "result_unknown": true}}
}

// A successful, fresh ownership query is required before an uncertain POST can be repeated.
func (a *App) reconcilePendingClaim(parent context.Context, account shared.M, plan, trigger string) (shared.M, error) {
	key := pendingClaimKey(account, plan)
	rows, err := storage.Query(a.db, "SELECT v FROM settings WHERE k=?", key)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	pending := shared.Obj(shared.Parse(shared.Obj(rows[0])["v"], nil))
	started := shared.Integer(pending["started_at"])
	if started <= 0 || shared.Str(pending["scope"]) == "" {
		return nil, claimResultUnknown(shared.NowMS())
	}
	if pending["scope"] != a.claimAccountScope(account) {
		return nil, a.clearPendingClaim(account, plan)
	}
	if parent.Err() != nil {
		return nil, claimResultUnknown(started)
	}
	ctx, cancel := a.operationContext(parent, 10*time.Second)
	defer cancel()
	if diagnostic, ok := parent.Value(claimCaptchaDiagnosticKey{}).(shared.M); ok {
		diagnostic["stage"] = "claim_confirmation"
	}
	// Invalidate earlier flights as well as the cache: a pre-POST balance cannot resolve this claim.
	a.invalidateBalance(shared.Str(account["id"]))
	balance, err := a.fetchBalanceFresh(ctx, account)
	if err != nil || !shared.Truth(balance["ok"]) {
		return nil, claimResultUnknown(started)
	}
	data := shared.Obj(shared.Obj(balance["raw"])["data"])
	plans, valid := data["plans"].([]any)
	current := a.account(shared.Str(account["id"]))
	if !valid || current == nil || a.claimAccountScope(current) != pending["scope"] || !a.applyBalance(current, balance, true) {
		return nil, claimResultUnknown(started)
	}
	for _, value := range plans {
		owned := shared.Obj(value)
		if shared.Str(owned["plan_id"]) != plan || shared.Str(owned["user_plan_id"]) == "" || shared.Str(owned["status"]) != "active" {
			continue
		}
		end := accountdata.SecondsMS(owned["ends_at"])
		detail := shared.M{"stage": "claim_confirmation", "reconciled": true}
		tx, err := a.db.BeginTx(ctx, nil)
		if err != nil {
			return nil, claimResultUnknown(started)
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO claim_log(account_id,ts,plan_id,ok,code,message,user_plan_id,ends_at,trigger,error_detail) VALUES(?,?,?,?,?,?,?,?,?,?)", account["id"], shared.NowMS(), plan, 1, "claim_result_confirmed", "已向上游确认该账号持有此卡", owned["user_plan_id"], end, trigger, shared.JSON(detail))
		if err == nil {
			_, err = tx.ExecContext(ctx, "DELETE FROM settings WHERE k=?", key)
		}
		if err != nil {
			_ = tx.Rollback()
			return nil, claimResultUnknown(started)
		}
		if err = tx.Commit(); err != nil {
			return nil, claimResultUnknown(started)
		}
		return shared.M{"ok": true, "already_claimed": true, "external_claim": true, "reconciled": true, "code": "claim_result_confirmed", "message": "已向上游确认该账号持有此卡", "plan": owned, "endsAt": end, "requires_verification": false}, nil
	}
	if time.Now().Before(time.UnixMilli(started).Add(claimConfirmationGrace)) {
		return nil, claimResultUnknown(started)
	}
	return nil, a.clearPendingClaim(account, plan)
}
