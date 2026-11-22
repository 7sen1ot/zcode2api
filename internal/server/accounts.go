package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
	accountdata "zcode2api/internal/server/account"
	settings "zcode2api/internal/server/config"
	"zcode2api/internal/server/fault"
	"zcode2api/internal/server/shared"
	"zcode2api/internal/server/storage"
)

const accountFields = "label family customer_id group_id status enc_tokens device_mid app_version card_started_at card_expires_at web_cookie_expires_at business_token_expires_at zcode_jwt_last_ok_at fail_streak cooldown_until capabilities last_balance last_error proxy_url last_ok_at last_used_at"
const automaticAccountGroup = "SELECT id FROM groups WHERE enabled=1 AND role='primary' AND (family_filter IS NULL OR family_filter='' OR family_filter=?) ORDER BY created_at LIMIT 1"

func (a *App) persistAccountFields(id string, fields shared.M) error {
	result, err := updateFields(a.db, "accounts", id, fields, accountFields)
	if err != nil {
		a.log.Printf("database write: %v", err)
	} else if result != nil {
		a.notifyAvailable()
	}
	return err
}

func (a *App) updateAccount(id string, m shared.M) error {
	return a.writeAccountUpdate(id, m, func() error { return a.persistAccountFields(id, m) })
}

func (a *App) writeAccountUpdate(id string, m shared.M, write func() error) error {
	invalidate := m["status"] == "disabled" || m["status"] == "needs_relogin" || m["status"] == "web_only"
	for _, key := range []string{"enc_tokens", "device_mid", "proxy_url"} {
		if _, changed := m[key]; changed {
			invalidate = true
		}
	}
	if invalidate {
		a.balances.applyMu.Lock()
		defer a.balances.applyMu.Unlock()
	}
	err := write()
	if err == nil && invalidate {
		a.invalidateBalanceLocked(id)
	}
	return err
}

func (a *App) updateAccountPlacement(ctx context.Context, id string, fields, body shared.M) error {
	group, valid := fields["group_id"].(string)
	if fields["group_id"] != nil && !valid {
		return fault.Invalid("目标分组无效")
	}
	expectedValue, compare := body["expected_group_id"]
	expected, valid := expectedValue.(string)
	if compare && expectedValue != nil && !valid {
		return fault.Invalid("原分组无效")
	}
	return a.writeAccountUpdate(id, fields, func() error {
		tx, err := a.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		var current sql.NullString
		var family string
		err = tx.QueryRowContext(ctx, "SELECT group_id,family FROM accounts WHERE id=?", id).Scan(&current, &family)
		if errors.Is(err, sql.ErrNoRows) {
			return &fault.Failure{Status: http.StatusNotFound, Message: "账号不存在"}
		}
		if err != nil {
			return err
		}
		if compare && current.String != expected {
			return &fault.Failure{Status: http.StatusConflict, Message: "账号归属已变更，请刷新后重试"}
		}
		if group != "" && group != current.String {
			var filter sql.NullString
			err := tx.QueryRowContext(ctx, "SELECT family_filter FROM groups WHERE id=?", group).Scan(&filter)
			if errors.Is(err, sql.ErrNoRows) {
				return fault.Invalid("目标分组不存在")
			}
			if err != nil {
				return err
			}
			if filter.String != "" && filter.String != family {
				return fault.Invalid("账号类型与目标分组不匹配")
			}
		}
		fields["group_id"] = shared.Nullable(group)
		if _, err := updateFields(tx, "accounts", id, fields, accountFields); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		a.notifyAvailable()
		return nil
	})
}

func (a *App) tokens(id string) map[string]string { return a.accountTokens(a.account(id)) }

func (a *App) accountTokens(r shared.M) map[string]string {
	out := map[string]string{}
	for k, v := range shared.Obj(shared.Parse(r["enc_tokens"], nil)) {
		s, e := accountdata.Decrypt(shared.Str(v), a.secret)
		if e == nil {
			out[k] = s
		}
	}
	return out
}

func (a *App) insertAccount(fields, tokens shared.M) (shared.M, error) {
	plain := map[string]string{}
	for k, v := range tokens {
		if shared.Truth(v) {
			plain[k] = shared.Str(v)
		}
	}
	tx, err := a.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	id, err := a.insertAccountRow(tx, fields, plain)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return a.account(id), nil
}

func (a *App) insertAccountRow(db storage.Executor, fields shared.M, tokens map[string]string) (string, error) {
	enc := map[string]string{}
	for key, value := range tokens {
		ciphertext, err := accountdata.Encrypt(value, a.secret)
		if err != nil {
			return "", err
		}
		enc[key] = ciphertext
	}
	group := fields["group_id"]
	if _, ok := fields["group_id"]; !ok {
		groups, err := storage.Query(db, automaticAccountGroup, shared.Fallback(fields["family"], "bigmodel"))
		if err != nil {
			return "", err
		}
		if len(groups) > 0 {
			group = shared.Obj(groups[0])["id"]
		}
	}
	id := "acc_" + shared.Random(8)
	_, err := db.Exec("INSERT INTO accounts(id,label,family,customer_id,group_id,status,enc_tokens,device_mid,app_version,web_cookie_expires_at,business_token_expires_at,source,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)", id, shared.Fallback(fields["label"], "账号"), shared.Fallback(fields["family"], "bigmodel"), fields["customer_id"], group, shared.Fallback(fields["status"], "unverified"), shared.JSON(enc), shared.Fallback(fields["device_mid"], shared.UUID()), settings.ZCodeFingerprintVersion, fields["web_cookie_expires_at"], fields["business_token_expires_at"], shared.Fallback(fields["source"], "local"), shared.NowMS())
	return id, err
}

func (a *App) publicAccount(r shared.M) shared.M {
	m := shared.Clone(r)
	tokens := shared.Obj(shared.Parse(m["enc_tokens"], nil))
	delete(m, "enc_tokens")
	kinds := []any{}
	for k := range tokens {
		kinds = append(kinds, k)
	}
	m["token_kinds"] = kinds
	plain := a.accountTokens(r)
	m["login_provider"] = accountdata.LoginProvider(shared.Str(r["family"]), plain)
	m["business_customer_id"] = shared.Nullable(plain["business_customer_id"])
	m["zcode_jwt_expires_at"] = accountdata.OptionalExpiry(accountdata.JwtExpiresAt(plain["zcode_jwt"]))
	for _, k := range []string{"zcode_jwt", "web_jwt", "business_token"} {
		m["has_"+k] = shared.Truth(tokens[k])
	}
	m["capabilities"] = shared.Parse(m["capabilities"], []any{})
	m["last_balance"] = shared.Parse(m["last_balance"], nil)
	m["platform_name"] = r["platform_name"]
	return m
}

func (a *App) fetchBalanceUpstream(ctx context.Context, r shared.M) (shared.M, error) {
	select {
	case a.balances.slots <- struct{}{}:
		defer func() { <-a.balances.slots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	t := a.accountTokens(r)
	if accountdata.LoginJWTExpired(t) {
		return shared.M{"ok": false, "authFailed": true, "httpStatus": 401, "failure_kind": "auth", "error_code": "login_expired", "reason": "ZCode 登录已过期，请重新授权"}, nil
	}
	res, e := a.api(ctx, r, "GET", "/api/v1/zcode-plan/billing/balance?app_version="+settings.ZCodeFingerprintVersion, shared.Str(t["zcode_jwt"]), nil, shared.M{"X-Device-Mid": r["device_mid"], "X-Request-Id": shared.UUID()}, a.jar(shared.Str(r["id"])))
	if e != nil {
		return nil, e
	}
	server := shared.NowMS()
	if d, e := time.Parse(time.RFC1123, res.Header.Get("Date")); e == nil {
		server = d.UnixMilli()
	}
	data := shared.Obj(res.JSON["data"])
	if shared.Truth(data["server_time"]) {
		server = shared.Integer(data["server_time"]) * 1000
	}
	out := shared.M{"ok": res.OK && shared.CodeIs(res.JSON["code"], 0), "authFailed": false, "httpStatus": res.Status, "plans": []any{}, "balances": []any{}, "capabilities": []any{}, "serverNowMs": server, "card": nil, "raw": res.JSON}
	if !shared.Truth(out["ok"]) {
		failure := fault.ApiResponseFailure(res)
		out["authFailed"] = failure.Kind == "auth"
		out["failure_kind"], out["error_code"] = failure.Kind, failure.Code
		out["retry_after_ms"] = failure.RetryAfter.Milliseconds()
		return out, nil
	}
	plans := shared.Arr(data["plans"])
	active := map[string]bool{}
	for _, v := range plans {
		p := shared.Obj(v)
		if shared.Str(p["status"]) != "active" {
			continue
		}
		if shared.Truth(p["ends_at"]) && shared.Integer(p["ends_at"])*1000 <= server {
			p["_staleExpired"] = true
			continue
		}
		active[shared.Str(shared.First(p["user_plan_id"], p["plan_id"]))] = true
		if out["card"] == nil && (strings.Contains(shared.Lower(p["plan_id"]), "start-plan") || strings.Contains(shared.Lower(p["name"]), "start plan") || strings.Contains(shared.Lower(p["name"]), "start-plan")) {
			out["card"] = shared.M{"userPlanId": p["user_plan_id"], "planId": p["plan_id"], "name": p["name"], "status": p["status"], "startedAt": accountdata.SecondsMS(p["starts_at"]), "endsAt": accountdata.SecondsMS(p["ends_at"])}
		}
	}
	balances, caps := []any{}, []any{}
	for _, v := range shared.Arr(data["balances"]) {
		b := shared.Obj(v)
		if shared.Truth(b["user_plan_id"]) && !active[shared.Str(b["user_plan_id"])] {
			continue
		}
		balances = append(balances, b)
		added := false
		for _, c := range shared.Arr(b["capabilities"]) {
			if s, ok := strings.CutPrefix(shared.Str(c), "model:"); ok {
				if !shared.Contains(caps, s) {
					caps = append(caps, s)
				}
				added = true
			}
		}
		if !added && shared.Truth(b["show_name"]) && !shared.Contains(caps, shared.Str(b["show_name"])) {
			caps = append(caps, b["show_name"])
		}
	}
	out["plans"], out["balances"], out["capabilities"] = plans, balances, caps
	return out, nil
}

func (a *App) applyBalance(r, b shared.M, snapshot bool) bool {
	if !shared.Truth(b["ok"]) {
		return false
	}
	id := shared.Str(r["id"])
	a.balances.applyMu.Lock()
	defer a.balances.applyMu.Unlock()
	r = a.account(id)
	if r == nil || r["status"] == "disabled" || !a.balanceResultCurrent(r, b) {
		return false
	}
	sequence := uint64(shared.Integer(b["_balance_sequence"]))
	if sequence > 0 && sequence < a.balances.applied[id] {
		return false
	}
	fields := shared.M{"status": "active", "zcode_jwt_last_ok_at": shared.NowMS(), "last_ok_at": shared.NowMS(), "fail_streak": 0, "last_error": nil, "capabilities": b["capabilities"], "last_balance": b["raw"], "card_expires_at": nil}
	if c := shared.Obj(b["card"]); len(c) > 0 {
		fields["card_started_at"], fields["card_expires_at"] = c["startedAt"], c["endsAt"]
	}
	changed := balancePayloadFingerprint(shared.Parse(r["last_balance"], nil)) != balancePayloadFingerprint(b["raw"])
	for key, value := range fields {
		if key == "last_balance" || key == "last_ok_at" || key == "zcode_jwt_last_ok_at" {
			continue
		}
		previous := r[key]
		if key == "capabilities" {
			previous = shared.Parse(previous, []any{})
		}
		changed = changed || shared.JSON(previous) != shared.JSON(value)
	}
	if changed || shared.NowMS()-shared.Integer(r["last_ok_at"]) >= balanceSnapshotBaseline.Milliseconds() {
		if err := a.updateAccount(id, fields); err != nil {
			return false
		}
	}
	if sequence > 0 {
		if a.balances.applied == nil {
			a.balances.applied = map[string]uint64{}
		}
		a.balances.applied[id] = sequence
	}
	a.mu.Lock()
	if s := a.states[id]; s != nil {
		if accountdata.RemainingBalance(accountdata.BalancePayload(b["raw"]), shared.NowMS()) > 0 {
			s.QuotaExhausted = false
		}
	}
	a.notifyAvailableLocked()
	a.mu.Unlock()
	if snapshot {
		if err := a.saveBalanceSnapshot(id, shared.Arr(b["balances"])); err != nil {
			a.log.Printf("余额快照保存失败: %v", err)
		}
	}
	return true
}

func (a *App) verify(ctx context.Context, r shared.M) (shared.M, error) {
	id := shared.Str(r["id"])
	if r["status"] == "disabled" {
		return shared.M{"ok": false, "status": "disabled", "reason": "账号已停用"}, nil
	}
	t := a.accountTokens(r)
	if !shared.Truth(t["zcode_jwt"]) {
		if shared.Truth(t["web_jwt"]) {
			res, e := a.customer(ctx, r, shared.Str(t["web_jwt"]))
			if e != nil {
				return nil, e
			}
			if res.OK {
				a.updateAccountStatusIfCurrent(r, nil, shared.M{"status": "web_only", "last_ok_at": shared.NowMS(), "last_error": nil})
			}
			return shared.M{"ok": res.OK, "status": "web_only", "reason": "网页会话不可用于推理"}, nil
		}
		return shared.M{"ok": false, "status": "unverified", "reason": "缺少可用凭据"}, nil
	}
	bal, e := a.fetchBalanceFresh(ctx, r)
	if e != nil {
		return nil, e
	}
	if shared.Truth(bal["ok"]) {
		if !a.applyBalance(r, bal, true) {
			return shared.M{"ok": false, "status": a.account(id)["status"], "reason": "账号已更新，请重新验证"}, nil
		}
		publicBalance := shared.Clone(bal)
		delete(publicBalance, "_balance_scope")
		delete(publicBalance, "_balance_generation")
		delete(publicBalance, "_balance_sequence")
		return shared.M{"ok": true, "status": "active", "balance": publicBalance}, nil
	}
	status := "cooldown"
	if shared.Str(r["status"]) == "unverified" {
		status = "unverified"
	}
	if shared.Truth(bal["authFailed"]) {
		status = "needs_relogin"
	}
	reason := shared.Str(shared.Fallback(bal["reason"], fmt.Sprintf("balance HTTP %v", bal["httpStatus"])))
	if !a.updateAccountStatusIfCurrent(r, bal, shared.M{"status": status, "last_error": reason, "fail_streak": shared.Integer(r["fail_streak"]) + 1}) {
		return shared.M{"ok": false, "status": a.account(id)["status"], "reason": "账号已更新，请重新验证"}, nil
	}
	return shared.M{"ok": false, "status": status, "reason": reason}, nil
}

func (a *App) summary(ctx context.Context) shared.M {
	out := shared.M{"accounts": []any{}, "totalRemaining": 0, "totalUsed": 0, "totalGranted": 0, "totalPending": 0, "activeCount": 0, "count": 0}
	accounts := []any{}
	now := shared.NowMS()
	deltas := map[string]any{}
	for _, v := range a.rows("SELECT account_id,SUM(delta) n FROM (SELECT account_id,MAX(0,MAX(used_units)-MIN(used_units)) delta FROM quota_snapshots WHERE ts>=? GROUP BY account_id,bucket_id) GROUP BY account_id", now-86400000) {
		r := shared.Obj(v)
		deltas[shared.Str(r["account_id"])] = r["n"]
	}
	rows := a.rows("SELECT * FROM accounts ORDER BY created_at")
	a.addPlatformNames(ctx, rows)
	for _, v := range rows {
		r := shared.Obj(v)
		m := a.publicAccount(r)
		d := accountdata.BalancePayload(m["last_balance"])
		buckets, pending := []any{}, []any{}
		remaining, used, total, pendingTotal := float64(0), float64(0), float64(0), float64(0)
		ents := map[string]bool{}
		for _, x := range accountdata.LiveBalances(d, now) {
			b := shared.Obj(x)
			remaining += shared.Num(b["remaining_units"])
			used += shared.Num(b["used_units"])
			total += shared.Num(b["total_units"])
			buckets = append(buckets, shared.M{"name": shared.First(b["show_name"], b["entitlement_id"], b["meter"], "额度"), "total": shared.Num(b["total_units"]), "used": shared.Num(b["used_units"]), "remaining": shared.Num(b["remaining_units"]), "unit": shared.Fallback(b["unit_type"], ""), "expires_at": accountdata.SecondsMS(shared.First(b["expires_at"], b["period_end"])), "plan_id": b["plan_id"]})
			ents[shared.Str(b["entitlement_id"])] = true
			ents[shared.Str(b["plan_id"])+":"+shared.Str(shared.First(b["show_name"], b["meter"]))] = true
		}
		for _, v := range shared.Arr(d["plans"]) {
			p := shared.Obj(v)
			if shared.Str(p["status"]) != "active" || (shared.Truth(p["ends_at"]) && shared.Integer(p["ends_at"])*1000 <= now) {
				continue
			}
			for _, v := range shared.Arr(p["entitlements"]) {
				e := shared.Obj(v)
				if shared.Integer(e["effective_at"])*1000 <= now || ents[shared.Str(e["entitlement_id"])] || ents[shared.Str(p["plan_id"])+":"+shared.Str(shared.First(e["show_name"], e["meter"]))] {
					continue
				}
				pendingTotal += shared.Num(e["grant_units"])
				pending = append(pending, shared.M{"name": shared.First(e["show_name"], e["entitlement_id"], e["meter"], "额度"), "total": shared.Num(e["grant_units"]), "unit": shared.Fallback(e["unit_type"], ""), "effective_at": accountdata.SecondsMS(e["effective_at"]), "plan_id": p["plan_id"], "plan_name": p["name"]})
			}
		}
		sort.Slice(pending, func(i, j int) bool {
			return shared.Integer(shared.Obj(pending[i])["effective_at"]) < shared.Integer(shared.Obj(pending[j])["effective_at"])
		})
		m["remaining"], m["used"], m["total"] = remaining, used, total
		if len(d) == 0 {
			m["remaining"], m["used"], m["total"] = nil, nil, nil
		}
		m["pending_buckets"], m["pending_total"], m["quota_buckets"] = pending, pendingTotal, buckets
		m["balance_checked_at"] = r["zcode_jwt_last_ok_at"]
		m["expiring_in_min"] = nil
		if shared.Truth(r["card_expires_at"]) {
			m["expiring_in_min"] = (shared.Integer(r["card_expires_at"]) - now) / 60000
		}
		m["delta24h"] = shared.Num(deltas[shared.Str(r["id"])])
		accounts = append(accounts, m)
		out["totalRemaining"] = shared.Num(out["totalRemaining"]) + remaining
		out["totalUsed"] = shared.Num(out["totalUsed"]) + used
		out["totalGranted"] = shared.Num(out["totalGranted"]) + total
		out["totalPending"] = shared.Num(out["totalPending"]) + pendingTotal
		if shared.Str(r["status"]) == "active" {
			out["activeCount"] = shared.Num(out["activeCount"]) + 1
		}
	}
	out["accounts"], out["count"] = accounts, len(accounts)
	return out
}
