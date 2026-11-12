package server

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

const dailyClaimRecoveryWindow = 10 * time.Minute
const dailyClaimRecoveryDelay = time.Minute

type scheduledClaimContextKey struct{}

// Versioned checkpoints live in the existing setting. No tickets or proxy
// credentials belong here: the setting is also returned by the admin API.
type scheduledClaimRun struct {
	Version       int                      `json:"version"`
	Day           string                   `json:"day"`
	ScheduledAt   int64                    `json:"scheduled_at"`
	StartedAt     int64                    `json:"started_at"`
	FinishedAt    int64                    `json:"finished_at,omitempty"`
	RetryDeadline int64                    `json:"retry_deadline"`
	NextRetryAt   int64                    `json:"next_retry_at,omitempty"`
	Status        string                   `json:"status"`
	Checked       int                      `json:"checked"`
	Claimed       int                      `json:"claimed"`
	Failed        int                      `json:"failed"`
	PendingManual int                      `json:"pending_manual"`
	Pending       int                      `json:"pending"`
	Accounts      []*scheduledClaimAccount `json:"accounts"`
}

type scheduledClaimAccount struct {
	ID        string                `json:"account_id"`
	Attempts  int                   `json:"attempts"`
	StartedAt int64                 `json:"started_at,omitempty"`
	RetryAt   int64                 `json:"retry_at,omitempty"`
	Status    string                `json:"status"`
	ExitHash  string                `json:"exit_hash,omitempty"`
	SameExit  bool                  `json:"same_exit,omitempty"`
	Result    M                     `json:"result,omitempty"`
	Items     []*scheduledClaimItem `json:"items"`
}

type scheduledClaimItem struct {
	PlanID   string `json:"plan_id"`
	Attempts int    `json:"attempts"`
	Status   string `json:"status"`
	Result   M      `json:"result,omitempty"`
}

func scheduledClaimOpen(status string) bool {
	return status == "pending" || status == "running" || status == "retry_wait" || status == "interrupted"
}

// Keep one copy of results in the checkpoint; flatten only for API consumers.
func scheduledClaimReport(run M) M {
	if integer(run["version"]) != 2 {
		return run
	}
	results := []any{}
	for _, value := range arr(run["accounts"]) {
		account := obj(value)
		if account["result"] != nil {
			results = append(results, merge(obj(account["result"]), M{"account": account["account_id"], "status": account["status"]}))
		}
		for _, value := range arr(account["items"]) {
			item := obj(value)
			results = append(results, merge(obj(item["result"]), M{"account": account["account_id"], "plan_id": item["plan_id"], "status": item["status"], "attempts": item["attempts"]}))
		}
	}
	run["results"] = results
	return run
}

func scheduledClaimError(ctx context.Context, err error) M {
	var failure *Failure
	if !errors.As(err, &failure) {
		failure = transportFailure(context.WithoutCancel(ctx), err)
	}
	failure = classify(failure)
	return M{"ok": false, "code": failure.Code, "error": conciseFailureMessage(failure), "message": conciseFailureMessage(failure), "failure_kind": failure.Kind, "retryable": failure.Retryable, "retry_after_ms": failure.RetryAfter.Milliseconds(), "requires_verification": claimBrowserFallback(failure)}
}

func scheduledClaimRetryable(result M) bool {
	switch str(result["code"]) {
	case "captcha_risk_blocked", "F001":
		return false // An explicit risk refusal is not a transient transport error.
	case "captcha_timeout", "captcha_config_timeout", "captcha_expired", "captcha_invalid", "captcha_duplicate_result", "captcha_invalid_result", "captcha_config_changed", "F008", "F014", "F018", "F019", "F020", "CAPTCHA_VERIFY_FAILED", "coding_plan_security_verification_required", "claim_invalid_response", "claim_request_failed", "in_progress", "canceled":
		return true
	case "captcha_runtime_error":
		// SDK initialization failures can be transient; a missing/broken Node
		// runtime cannot be repaired by repeating the same command.
		stage := str(obj(result["verification"])["sdk_stage"])
		return stage == "sdk_load" || stage == "sdk_init" || stage == "sdk_ready" || stage == "sdk_verify"
	case "captcha_interaction_required", "captcha_jsdom_missing", "captcha_proxy_unsupported", "captcha_config_invalid", "captcha_scene_disabled", "captcha_session_invalid":
		return false
	}
	switch str(result["failure_kind"]) {
	case "auth", "account_risk", "permission", "invalid":
		return false
	}
	return truth(result["retryable"])
}

// Waiting for a shared cooldown is not an SDK execution or a claim attempt.
func scheduledClaimWaiting(result M) bool {
	if result["code"] == "in_progress" {
		return true
	}
	diagnostic := obj(result["verification"])
	return integer(result["retry_after_ms"]) > 0 && !truth(diagnostic["sdk_executed"]) && !truth(diagnostic["claim_request_started"]) &&
		(diagnostic["stage"] == "admission" || diagnostic["sdk_stage"] == "admission")
}

func (run *scheduledClaimRun) summarize() {
	run.Checked, run.Claimed, run.Failed, run.PendingManual, run.Pending = 0, 0, 0, 0, 0
	run.NextRetryAt = 0
	running := false
	for _, account := range run.Accounts {
		if account.StartedAt > 0 || account.Attempts > 0 {
			run.Checked++
		}
		if scheduledClaimOpen(account.Status) {
			running = running || account.Status == "running"
			run.Pending++
			if run.Pending == 1 || account.RetryAt < run.NextRetryAt {
				run.NextRetryAt = account.RetryAt
			}
		}
		if account.Result != nil {
			if account.Status == "failed" && len(account.Items) == 0 {
				run.Failed++
			}
			if account.Status == "needs_verification" && len(account.Items) == 0 {
				run.PendingManual++
			}
		}
		for _, item := range account.Items {
			switch item.Status {
			case "claimed":
				if !truth(item.Result["external_claim"]) {
					run.Claimed++
				}
			case "needs_verification":
				run.PendingManual++
			case "failed":
				run.Failed++
			}
		}
	}
	if run.Pending > 0 {
		run.Status = "retry_wait"
		if running {
			run.Status = "running"
		}
		run.FinishedAt = 0
		return
	}
	run.Status = "completed"
	if run.PendingManual > 0 {
		run.Status = "needs_verification"
	}
	if run.Failed > 0 {
		run.Status = "failed"
	}
	if run.Claimed > 0 && (run.Failed > 0 || run.PendingManual > 0) {
		run.Status = "partial"
	}
	if run.FinishedAt == 0 {
		run.FinishedAt = nowMS()
	}
}

func (a *App) saveScheduledClaims(run *scheduledClaimRun) error {
	run.summarize()
	return a.setSetting("daily_claim_last_run", run)
}

func finishScheduledAccount(account *scheduledClaimAccount) {
	account.Status, account.RetryAt = "completed", 0
	if len(account.Items) == 0 && account.Result == nil {
		account.Result = M{"ok": false, "message": "本次自动领取未完成，等待下次计划"}
	}
	for _, item := range account.Items {
		if scheduledClaimOpen(item.Status) {
			item.Status = "failed"
			if truth(item.Result["requires_verification"]) {
				item.Status = "needs_verification"
			}
			if item.Result == nil {
				item.Result = M{"ok": false, "message": "本次自动领取已结束，等待下次计划"}
			}
		}
		if item.Status == "failed" {
			account.Status = "failed"
		} else if item.Status == "needs_verification" && account.Status != "failed" {
			account.Status = "needs_verification"
		}
	}
	if account.Result != nil && !truth(account.Result["no_cards"]) {
		account.Status = "failed"
		if truth(account.Result["requires_verification"]) {
			account.Status = "needs_verification"
		}
	}
}

func (run *scheduledClaimRun) recoverInterrupted(now time.Time) {
	for _, account := range run.Accounts {
		if !scheduledClaimOpen(account.Status) {
			continue
		}
		if now.UnixMilli() >= run.RetryDeadline || account.Attempts >= 2 {
			finishScheduledAccount(account)
			continue
		}
		if account.Status == "running" {
			account.Status = "retry_wait"
			account.RetryAt = max(account.RetryAt, account.StartedAt+dailyClaimRecoveryDelay.Milliseconds())
		}
	}
}

func (a *App) scheduledClaimEnabled(due time.Time) bool {
	return a.ctx.Err() == nil && truth(a.configValue("daily_claim_enabled")) && a.dailyClaimTime() == due.In(claimTimezone).Format("15:04")
}

func (a *App) runScheduledClaims(ctx context.Context, due time.Time) (M, error) {
	ctx, cancel := context.WithDeadline(ctx, due.Add(dailyClaimRecoveryWindow))
	defer cancel()
	ctx = context.WithValue(ctx, scheduledClaimContextKey{}, due.UnixMilli())
	last := obj(a.setting("daily_claim_last_run", nil))
	var run scheduledClaimRun
	if claimRunRecorded(last, due) {
		if integer(last["version"]) != 2 || integer(last["scheduled_at"]) != due.UnixMilli() || !scheduledClaimOpen(str(last["status"])) {
			return M{"skipped": true}, nil
		}
		if err := json.Unmarshal([]byte(js(last)), &run); err != nil {
			return nil, err
		}
		if err := a.reconcileScheduledClaims(&run); err != nil {
			return nil, err
		}
		run.recoverInterrupted(time.Now())
	} else {
		if !time.Now().Before(due.Add(dailyClaimRecoveryWindow)) {
			return M{"skipped": true}, nil
		}
		run = scheduledClaimRun{Version: 2, Day: due.In(claimTimezone).Format("2006-01-02"), ScheduledAt: due.UnixMilli(), StartedAt: nowMS(), RetryDeadline: due.Add(dailyClaimRecoveryWindow).UnixMilli(), Accounts: []*scheduledClaimAccount{}}
		accounts, err := query(a.db, "SELECT * FROM accounts WHERE status IN ('active','unverified','cooldown') ORDER BY created_at")
		if err != nil {
			return nil, err
		}
		for _, value := range accounts {
			r := obj(value)
			if a.accountTokens(r)["zcode_jwt"] != "" {
				run.Accounts = append(run.Accounts, &scheduledClaimAccount{ID: str(r["id"]), Status: "pending", Items: []*scheduledClaimItem{}})
			}
		}
	}
	if err := a.saveScheduledClaims(&run); err != nil {
		return nil, err
	}
	// Require a running browser before discovery or any claim, including when
	// the upstream verification scene is disabled. Never fall back to jsdom.
	if run.Pending > 0 && ctx.Err() == nil && a.scheduledClaimEnabled(due) {
		if err := a.warmClaimBrowser(ctx, obj(a.configValue("http", "captcha")), due); err != nil && ctx.Err() == nil && a.scheduledClaimEnabled(due) {
			for _, account := range run.Accounts {
				if !scheduledClaimOpen(account.Status) {
					continue
				}
				account.Result = scheduledClaimError(ctx, err)
				account.Result["requires_verification"] = false
				if err := a.deferScheduledClaimAccount(&run, account, scheduledClaimRetryable(account.Result)); err != nil {
					return nil, err
				}
			}
			if err := a.saveScheduledClaims(&run); err != nil {
				return nil, err
			}
			return scheduledClaimReport(obj(parse(js(run), nil))), nil
		}
	}
	for _, account := range run.Accounts {
		if ctx.Err() != nil || !a.scheduledClaimEnabled(due) {
			run.Status = "interrupted"
			break
		}
		if !scheduledClaimOpen(account.Status) || nowMS() < account.RetryAt {
			continue
		}
		if nowMS() >= run.RetryDeadline || account.Attempts >= 2 {
			finishScheduledAccount(account)
		} else if err := a.runScheduledClaimAccount(ctx, due, &run, account); err != nil {
			return nil, err
		}
		if err := a.saveScheduledClaims(&run); err != nil {
			return nil, err
		}
	}
	if run.Status == "interrupted" || ctx.Err() != nil || !a.scheduledClaimEnabled(due) {
		run.Status = "interrupted"
		if err := a.setSetting("daily_claim_last_run", &run); err != nil {
			return nil, err
		}
	}
	return scheduledClaimReport(obj(parse(js(run), nil))), nil
}

// Reconcile successes even if the process exited after receiving the claim but
// before updating its checkpoint, including on the final allowed attempt.
func (a *App) reconcileScheduledClaims(run *scheduledClaimRun) error {
	rows, err := query(a.db, "SELECT account_id,plan_id,trigger FROM claim_log WHERE ok=1 AND ts>=? ORDER BY ts", run.ScheduledAt)
	if err != nil {
		return err
	}
	successes := map[string]M{}
	for _, value := range rows {
		r := obj(value)
		successes[str(r["account_id"])+"|"+str(r["plan_id"])] = r
	}
	for _, account := range run.Accounts {
		for _, item := range account.Items {
			if item.Status == "claimed" {
				continue
			}
			if success := successes[account.ID+"|"+item.PlanID]; success != nil {
				item.Status, item.Result = "claimed", M{"ok": true, "external_claim": success["trigger"] != "scheduled", "message": "已确认领取成功"}
			}
		}
	}
	return nil
}

func (a *App) runScheduledClaimAccount(ctx context.Context, due time.Time, run *scheduledClaimRun, account *scheduledClaimAccount) error {
	account.Attempts++
	account.StartedAt, account.Status = nowMS(), "running"
	if err := a.saveScheduledClaims(run); err != nil {
		return err
	}
	r := a.account(account.ID)
	if r == nil || r["status"] == "disabled" || r["status"] == "needs_relogin" || a.accountTokens(r)["zcode_jwt"] == "" {
		account.Result = M{"ok": false, "message": "账号不存在、已停用或需要重新登录"}
		finishScheduledAccount(account)
		return nil
	}
	if ctx.Err() != nil || !a.scheduledClaimEnabled(due) {
		return nil
	}
	// Scheduled recovery keeps the ordinary sticky exit, rather than advancing
	// the manual per-card cursor after a verification refusal.
	pinned, release, err := a.pinOutboundProxy(ctx, r)
	if err != nil {
		account.Result = scheduledClaimError(ctx, err)
		return a.deferScheduledClaimAccount(run, account, scheduledClaimRetryable(account.Result))
	}
	defer release()
	if account.SameExit && account.ExitHash != hash(selectedProxy(pinned)) {
		// A different request may have moved the shared sticky selection. Reuse
		// the original healthy endpoint if it is still in the configured pool.
		if _, pooled := pinned.Value(proxySelectionKey{}).(proxySelection); pooled {
			a.proxies.mu.Lock()
			for _, endpoint := range a.proxies.endpoints {
				if hash(endpoint.URL) == account.ExitHash && !a.proxies.unavailable[endpoint.identity()] && a.proxies.blocked[endpoint.identity()].IsZero() {
					pinned = pinnedProxyContext(pinned, endpoint.URL)
					pinned = context.WithValue(pinned, proxySelectionKey{}, proxySelection{a.proxies.generation, endpoint.identity()})
					break
				}
			}
			a.proxies.mu.Unlock()
		}
	}
	exit := hash(selectedProxy(pinned))
	if account.SameExit && account.ExitHash != "" && account.ExitHash != exit {
		account.Result = M{"ok": false, "message": "验证出口已变更，本次不再自动重试"}
		finishScheduledAccount(account)
		return nil
	}
	account.ExitHash = exit
	if ctx.Err() != nil || !a.scheduledClaimEnabled(due) {
		return nil
	}
	p, err := a.preview(pinned, r, true)
	if err != nil {
		account.Result = scheduledClaimError(ctx, err)
		if integer(account.Result["retry_after_ms"]) > 0 {
			account.Attempts--
		}
		return a.deferScheduledClaimAccount(run, account, scheduledClaimRetryable(account.Result))
	}
	account.Result = nil
	available := map[string]bool{}
	for _, value := range arr(p["plans"]) {
		plan := str(obj(value)["plan_id"])
		available[plan] = true
		found := false
		for _, item := range account.Items {
			found = found || item.PlanID == plan
		}
		if !found {
			account.Items = append(account.Items, &scheduledClaimItem{PlanID: plan, Status: "pending"})
		}
	}
	if len(account.Items) == 0 {
		// Campaign delivery may lag the configured minute. Keep discovering
		// within the recovery window without spending actual claim attempts.
		account.Attempts--
		account.Result = M{"no_cards": true, "message": "暂未查询到可领取的每日免费卡"}
		return a.deferScheduledClaimAccount(run, account, true)
	}
	attempted := false
	for _, item := range account.Items {
		if !scheduledClaimOpen(item.Status) {
			continue
		}
		if ctx.Err() != nil || !a.scheduledClaimEnabled(due) {
			return nil // Leave the checkpoint recoverable without another POST.
		}
		if nowMS() >= run.RetryDeadline {
			finishScheduledAccount(account)
			return nil
		}
		recorded, err := a.recordedClaim(account.ID, item.PlanID, due.UnixMilli())
		if err != nil {
			return err
		}
		if recorded != nil {
			item.Status, item.Result = "claimed", recorded
		} else if !available[item.PlanID] {
			item.Status, item.Result = "unavailable", M{"ok": false, "message": "该卡片已不在可领取清单中"}
		} else if item.Attempts >= 2 {
			item.Status = "failed"
		} else {
			item.Attempts++
			item.Status = "running"
			account.SameExit = true
			if err := a.saveScheduledClaims(run); err != nil {
				return err
			}
			result, claimErr := a.claim(pinned, account.ID, item.PlanID, "scheduled", nil)
			if claimErr != nil {
				result = merge(result, scheduledClaimError(ctx, claimErr))
			}
			item.Result = result
			if scheduledClaimWaiting(result) {
				item.Attempts--
				if !attempted {
					account.Attempts--
				}
				item.Status = "retry_wait"
				account.RetryAt = nowMS() + max(dailyClaimRecoveryDelay.Milliseconds(), integer(result["retry_after_ms"]))
				return a.deferScheduledClaimAccount(run, account, true)
			}
			attempted = true
			item.Status = "failed"
			if truth(result["ok"]) {
				item.Status = "claimed"
			} else if truth(result["requires_verification"]) {
				item.Status = "needs_verification"
			}
			if !truth(result["ok"]) && scheduledClaimRetryable(result) && account.Attempts < 2 {
				item.Status = "retry_wait"
				account.RetryAt = max(account.RetryAt, nowMS()+max(dailyClaimRecoveryDelay.Milliseconds(), integer(result["retry_after_ms"])))
			}
			if result["failure_kind"] == "network" {
				account.SameExit = false
			}
		}
		if err := a.saveScheduledClaims(run); err != nil {
			return err
		}
		if item.Status == "needs_verification" || item.Result["failure_kind"] == "auth" || item.Result["failure_kind"] == "account_risk" {
			// Do not submit further cards for an account needing intervention.
			for _, remaining := range account.Items {
				if scheduledClaimOpen(remaining.Status) {
					remaining.Status, remaining.Result = item.Status, item.Result
				}
			}
			break
		}
	}
	return a.deferScheduledClaimAccount(run, account, false)
}

func (a *App) deferScheduledClaimAccount(run *scheduledClaimRun, account *scheduledClaimAccount, retry bool) error {
	retryAt := max(account.RetryAt, nowMS()+dailyClaimRecoveryDelay.Milliseconds())
	if account.Result != nil {
		retryAt = max(retryAt, nowMS()+integer(account.Result["retry_after_ms"]))
	}
	for _, item := range account.Items {
		// Relative item delays were already converted to account.RetryAt.
		if account.Result == nil && scheduledClaimOpen(item.Status) {
			retry = true
			break
		}
	}
	if retry && account.Attempts < 2 && retryAt < run.RetryDeadline {
		account.Status, account.RetryAt = "retry_wait", retryAt
	} else {
		finishScheduledAccount(account)
	}
	return nil
}
