package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	accountdata "zcode2api/internal/server/account"
	"zcode2api/internal/server/shared"
	"zcode2api/internal/server/storage"
)

const accountTransferFormat = "zcode2api-accounts"

type accountTransferFile struct {
	Format     string            `json:"format"`
	Version    int               `json:"version"`
	ExportedAt string            `json:"exported_at"`
	Accounts   []transferAccount `json:"accounts"`
}

// Only portable account data belongs here. Pools, proxies, runtime state and
// usage history are local to each installation.
type transferAccount struct {
	Label                  string            `json:"label"`
	Family                 string            `json:"family"`
	CustomerID             string            `json:"customer_id,omitempty"`
	DeviceMid              string            `json:"device_mid"`
	Tokens                 map[string]string `json:"tokens"`
	WebCookieExpiresAt     *int64            `json:"web_cookie_expires_at,omitempty"`
	BusinessTokenExpiresAt *int64            `json:"business_token_expires_at,omitempty"`
	Disabled               bool              `json:"disabled,omitempty"`
}

func (a *App) exportAccounts(b shared.M) (any, int, error) {
	selected := map[string]bool{}
	if raw, present := b["account_ids"]; present {
		ids, valid := raw.([]any)
		if !valid || len(ids) == 0 || len(ids) > 10000 {
			return adminError(400, "请选择 1–10000 个账号")
		}
		for _, value := range ids {
			id, valid := value.(string)
			if !valid || id == "" {
				return adminError(400, "账号 ID 无效")
			}
			selected[id] = true
		}
	}
	rows, err := storage.Query(a.db, "SELECT * FROM accounts ORDER BY created_at,id")
	if err != nil {
		return nil, 500, err
	}
	file := accountTransferFile{Format: accountTransferFormat, Version: 1, ExportedAt: time.Now().UTC().Format(time.RFC3339), Accounts: []transferAccount{}}
	for _, value := range rows {
		r := shared.Obj(value)
		if len(selected) > 0 && !selected[shared.Str(r["id"])] {
			continue
		}
		entry := transferAccount{Label: shared.Str(r["label"]), Family: shared.Str(r["family"]), CustomerID: shared.Str(r["customer_id"]), DeviceMid: shared.Str(r["device_mid"]), Tokens: map[string]string{}, Disabled: r["status"] == "disabled"}
		var encrypted map[string]string
		if err := json.Unmarshal([]byte(shared.Str(r["enc_tokens"])), &encrypted); err != nil {
			return adminError(409, "账号 "+shared.Str(r["id"])+" 的凭据损坏，未导出")
		}
		for key, value := range encrypted {
			plain, err := accountdata.Decrypt(value, a.secret)
			if err != nil {
				return adminError(409, "账号 "+shared.Str(r["id"])+" 的凭据无法解密，未导出")
			}
			entry.Tokens[key] = plain
		}
		if r["web_cookie_expires_at"] != nil {
			v := shared.Integer(r["web_cookie_expires_at"])
			entry.WebCookieExpiresAt = &v
		}
		if r["business_token_expires_at"] != nil {
			v := shared.Integer(r["business_token_expires_at"])
			entry.BusinessTokenExpiresAt = &v
		}
		if err := entry.validate(); err != nil {
			return adminError(409, "账号 "+shared.Str(r["id"])+" 无法导出："+err.Error())
		}
		file.Accounts = append(file.Accounts, entry)
	}
	if len(selected) > 0 && len(file.Accounts) != len(selected) {
		return adminError(409, "部分账号已删除，请刷新后重新导出")
	}
	if len(file.Accounts) == 0 || len(file.Accounts) > 10000 {
		return adminError(400, "每次可导出 1–10000 个账号，请调整筛选范围")
	}
	// Leave room for the import request envelope under readBody's 32 MiB limit.
	if len(shared.JSON(file)) > 30<<20 {
		return adminError(400, "导出文件过大，请缩小筛选范围后分批导出")
	}
	return file, 200, nil
}

func (entry *transferAccount) validate() error {
	if entry.Family != "bigmodel" && entry.Family != "zai" {
		return fmt.Errorf("账号平台无效")
	}
	provider := accountdata.LoginProvider(entry.Family, entry.Tokens)
	if !accountdata.ValidLoginProvider(provider) || accountdata.LoginFamily(provider) != entry.Family {
		return fmt.Errorf("登录渠道与账号平台不匹配")
	}
	if len([]rune(entry.Label)) > 200 || len(entry.CustomerID) > 512 || len(entry.DeviceMid) > 512 || strings.ContainsAny(entry.DeviceMid, "\r\n\x00") {
		return fmt.Errorf("账号信息格式无效")
	}
	if len(entry.Tokens) == 0 || len(entry.Tokens) > 32 {
		return fmt.Errorf("账号凭据无效")
	}
	for key, value := range entry.Tokens {
		if key == "" || len(key) > 100 || len(value) > 128<<10 || strings.ContainsAny(value, "\r\n\x00") || strings.HasPrefix(value, "enc:") {
			return fmt.Errorf("账号凭据格式无效，请使用导出的 JSON 文件")
		}
	}
	if entry.Tokens["zcode_jwt"] == "" && entry.Tokens["web_jwt"] == "" {
		return fmt.Errorf("缺少客户端或网页凭据")
	}
	identity := ""
	if token := entry.Tokens["zcode_jwt"]; token != "" {
		identity = shared.Str(accountdata.JwtPayload(token)["user_id"])
		if entry.Tokens["active_provider"] != "" && (identity == "" || len(strings.Split(token, ".")) != 3) {
			return fmt.Errorf("缺少有效的 ZCode 登录身份")
		}
	} else {
		payload := accountdata.JwtPayload(entry.Tokens["web_jwt"])
		identity = shared.Str(shared.First(payload["customer_id"], payload["user_id"]))
	}
	if identity != "" {
		if entry.CustomerID != "" && entry.CustomerID != identity {
			return fmt.Errorf("账号 ID 与凭据不一致")
		}
		entry.CustomerID = identity
	}
	if raw := entry.Tokens["provider_user_info"]; raw != "" {
		var profile shared.M
		if json.Unmarshal([]byte(raw), &profile) != nil || profile == nil || shared.Truth(profile["user_id"]) && shared.Str(profile["user_id"]) != identity {
			return fmt.Errorf("用户资料与登录身份不一致")
		}
		entry.Tokens["provider_user_info"] = shared.JSON(accountdata.LoginProfile(profile))
	}
	if raw := entry.Tokens["linked_bigmodel"]; raw != "" {
		var linked shared.M
		if json.Unmarshal([]byte(raw), &linked) != nil || identity == "" || shared.Str(linked["userId"]) != identity || shared.Str(linked["customerId"]) == "" || shared.Str(linked["accessToken"]) == "" {
			return fmt.Errorf("BigModel 关联凭据与登录身份不一致")
		}
		if provider == "zai_cn" && (entry.Tokens["business_token"] != "" && entry.Tokens["business_token"] != shared.Str(linked["accessToken"]) || entry.Tokens["business_customer_id"] != "" && entry.Tokens["business_customer_id"] != shared.Str(linked["customerId"])) {
			return fmt.Errorf("BigModel 关联与业务凭据不一致")
		}
	}
	if kind := entry.Tokens["provider_access_kind"]; kind != "" && kind != "business" && kind != "oauth" {
		return fmt.Errorf("渠道凭据类型无效")
	}
	if provider == "zai" && entry.Tokens["provider_access_kind"] == "oauth" && entry.Tokens["oauth_access"] != "" && entry.Tokens["oauth_access"] != entry.Tokens["provider_access_token"] {
		return fmt.Errorf("Z.ai OAuth 凭据不一致")
	}
	for _, expiry := range []*int64{entry.WebCookieExpiresAt, entry.BusinessTokenExpiresAt} {
		if expiry != nil && (*expiry < 0 || *expiry > 9007199254740991) {
			return fmt.Errorf("凭据到期时间无效")
		}
	}
	return nil
}

func (entry transferAccount) identityKeys() []string {
	keys := []string{}
	provider := accountdata.LoginProvider(entry.Family, entry.Tokens)
	if entry.CustomerID != "" {
		keys = append(keys, provider+":id:"+entry.CustomerID)
	}
	for _, kind := range []string{"zcode_jwt", "web_jwt", "business_token"} {
		if token := entry.Tokens[kind]; token != "" {
			keys = append(keys, provider+":"+kind+":"+shared.Hash(token))
		}
	}
	return keys
}

func (a *App) importAccounts(ctx context.Context, b shared.M) (any, int, error) {
	var file accountTransferFile
	if err := json.Unmarshal([]byte(shared.JSON(b["data"])), &file); err != nil || file.Format != accountTransferFormat || file.Version != 1 {
		return adminError(400, "文件格式或版本不支持，请选择本程序导出的账号 JSON 文件")
	}
	if len(file.Accounts) == 0 || len(file.Accounts) > 10000 {
		return adminError(400, "每次可导入 1–10000 个账号")
	}
	for i := range file.Accounts {
		if err := file.Accounts[i].validate(); err != nil {
			return adminError(400, fmt.Sprintf("第 %d 个账号：%s；未导入任何账号", i+1, err))
		}
	}
	group, validGroup := b["group_id"].(string)
	if b["group_id"] != nil && !validGroup {
		return adminError(400, "目标分组无效")
	}
	a.credentialMu.Lock()
	defer a.credentialMu.Unlock()
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, 500, err
	}
	defer tx.Rollback()
	if group != "" {
		var filter sql.NullString
		if err := tx.QueryRowContext(ctx, "SELECT family_filter FROM groups WHERE id=?", group).Scan(&filter); err == sql.ErrNoRows {
			return adminError(400, "目标分组不存在")
		} else if err != nil {
			return nil, 500, err
		}
		for _, entry := range file.Accounts {
			if filter.String != "" && filter.String != entry.Family {
				return adminError(400, "文件中的账号平台与目标分组不匹配")
			}
		}
	}
	seen := map[string]bool{}
	rows, err := storage.Query(tx, "SELECT family,customer_id,enc_tokens FROM accounts")
	if err != nil {
		return nil, 500, err
	}
	for _, value := range rows {
		r := shared.Obj(value)
		entry := transferAccount{Family: shared.Str(r["family"]), CustomerID: shared.Str(r["customer_id"]), Tokens: a.accountTokens(r)}
		for _, key := range entry.identityKeys() {
			seen[key] = true
		}
	}
	defaultGroups := map[string]string{}
	pollIDs := []string{}
	imported, skipped := 0, 0
	for _, entry := range file.Accounts {
		keys := entry.identityKeys()
		duplicate := false
		for _, key := range keys {
			duplicate = duplicate || seen[key]
		}
		if duplicate {
			skipped++
			continue
		}
		targetGroup := group
		if targetGroup == "" {
			var loaded bool
			if targetGroup, loaded = defaultGroups[entry.Family]; !loaded {
				err := tx.QueryRowContext(ctx, automaticAccountGroup, entry.Family).Scan(&targetGroup)
				if err != nil && err != sql.ErrNoRows {
					return nil, 500, err
				}
				defaultGroups[entry.Family] = targetGroup
			}
		}
		status := "unverified"
		if entry.Tokens["zcode_jwt"] == "" {
			status = "web_only"
		}
		if entry.Disabled {
			status = "disabled"
		}
		id, err := a.insertAccountRow(tx, shared.M{"label": shared.Fallback(entry.Label, "导入账号"), "family": entry.Family, "customer_id": shared.Nullable(entry.CustomerID), "group_id": shared.Nullable(targetGroup), "status": status, "device_mid": entry.DeviceMid, "web_cookie_expires_at": entry.WebCookieExpiresAt, "business_token_expires_at": entry.BusinessTokenExpiresAt, "source": "file"}, entry.Tokens)
		if err != nil {
			return nil, 500, err
		}
		imported++
		if !entry.Disabled {
			pollIDs = append(pollIDs, id)
		}
		for _, key := range keys {
			seen[key] = true
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, 500, err
	}
	for i, id := range pollIDs {
		a.scheduleNext(id, time.Second+time.Duration(i)*300*time.Millisecond)
	}
	a.notifyAvailable()
	return shared.M{"ok": true, "imported": imported, "skipped": skipped, "total": len(file.Accounts)}, 200, nil
}

func (a *App) importLocal(ctx context.Context, b shared.M) (shared.M, error) {
	if !a.localImportMu.TryLock() {
		return shared.M{"ok": false, "error": "正在导入客户端账号，请稍候"}, nil
	}
	defer a.localImportMu.Unlock()
	path, err := accountdata.ClientCredentialsPath(shared.Str(b["credentials_path"]))
	if err != nil {
		return shared.M{"ok": false, "error": err.Error()}, nil
	}
	clientType := shared.Str(shared.Fallback(b["client_type"], "desktop"))
	if clientType != "desktop" && clientType != "cli" {
		return shared.M{"ok": false, "error": "客户端类型须为 desktop 或 cli"}, nil
	}
	tokens, identity, err := accountdata.LoadClientAccount(ctx, path, clientType)
	if err != nil {
		return shared.M{"ok": false, "error": err.Error()}, nil
	}
	device := ""
	if data, err := accountdata.ReadClientCredentialFile(filepath.Join(filepath.Dir(path), "telemetry-state.json"), 1<<20); err == nil {
		if mid := shared.Str(shared.Obj(shared.Parse(string(data), nil))["deviceMid"]); mid != "" && len(mid) <= 512 && !strings.ContainsAny(mid, "\r\n\x00") {
			device = mid
		}
	}
	account, updated, err := a.saveLoginAccount("", identity, shared.M{"label": shared.Fallback(b["label"], "客户端-"+shared.Str(tokens["active_provider"])), "source": "local", "device_mid": device}, tokens)
	if err != nil {
		return nil, err
	}
	id := shared.Str(account["id"])
	result := a.verifyImportedAccount(ctx, account)
	imported := 1
	if updated {
		imported = 0
	}
	// Import success means credentials were saved; upstream verification is separate.
	return shared.M{"ok": true, "id": id, "login_provider": tokens["active_provider"], "imported": imported, "updated": updated, "verified": shared.Truth(result["ok"]), "status": result["status"], "reason": result["reason"], "results": []any{result}}, nil
}
