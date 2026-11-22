package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	accountdata "zcode2api/internal/server/account"
	settings "zcode2api/internal/server/config"
	"zcode2api/internal/server/fault"
	"zcode2api/internal/server/shared"
	"zcode2api/internal/server/storage"
)

const apiKeyColumns = "id,name,key_tail,quota_limit,used_units,expires_at,ip_whitelist,model_whitelist,group_order,sticky_minutes,default_level,enabled,created_at,last_used_at"
const apiKeyConfigColumns = "name quota_limit expires_at ip_whitelist model_whitelist group_order sticky_minutes default_level enabled"

func apiKeyConfig(body shared.M, create bool) (shared.M, error) {
	fields := shared.M{}
	if create {
		fields = shared.M{"ip_whitelist": "[]", "model_whitelist": "[]", "sticky_minutes": 0, "enabled": 1}
	}
	for _, field := range []struct{ column, alias, label string }{
		{"name", "", "密钥名称"},
		{"quota_limit", "quotaLimit", "配额（Token）"},
		{"expires_at", "expiresAt", "过期时间"},
		{"ip_whitelist", "ipWhitelist", "IP 白名单"},
		{"model_whitelist", "modelWhitelist", "模型白名单"},
		{"group_order", "groupOrder", "分组链"},
		{"sticky_minutes", "stickyMinutes", "黏住分钟"},
		{"default_level", "defaultLevel", "默认思考档位"},
		{"enabled", "", "启用状态"},
	} {
		value, exists := body[field.column]
		if alias, present := body[field.alias]; field.alias != "" && present && (alias != nil || !exists) {
			value, exists = alias, true
		}
		if !exists {
			continue
		}
		var err error
		switch field.column {
		case "name":
			value, err = parseAdminName(value, field.label)
		case "quota_limit", "expires_at":
			if value == nil || value == "" {
				value = nil
				break
			}
			number, err := strconv.ParseInt(shared.Str(value), 10, 64)
			if err != nil || number < 0 {
				return nil, fmt.Errorf("%s必须为非负整数", field.label)
			}
			value = number
		case "ip_whitelist", "model_whitelist", "group_order":
			if value == nil || value == "" {
				value = nil
				if field.column != "group_order" {
					value = "[]"
				}
				break
			}
			if raw, ok := value.(string); ok {
				value = shared.Parse(raw, nil)
			}
			items, ok := value.([]any)
			if !ok {
				return nil, fmt.Errorf("%s必须为字符串列表", field.label)
			}
			list := make([]string, 0, len(items))
			for _, item := range items {
				entry, _ := item.(string)
				entry = strings.TrimSpace(entry)
				if entry == "" {
					return nil, fmt.Errorf("%s包含无效条目", field.label)
				}
				list = append(list, entry)
			}
			value = shared.JSON(list)
		case "sticky_minutes":
			if value == nil || value == "" {
				value = 0
				break
			}
			minutes, err := strconv.ParseFloat(shared.Str(value), 64)
			if err != nil || minutes < 0 || math.IsNaN(minutes) || math.IsInf(minutes, 0) {
				return nil, errors.New("黏住分钟必须为非负数")
			}
			value = minutes
		case "default_level":
			if value == nil || value == "" {
				value = nil
			} else if !settings.ValidLevel(shared.Str(value)) {
				return nil, errors.New("默认思考档位无效")
			}
		case "enabled":
			value, err = parseEnabledFlag(value)
		}
		if err != nil {
			return nil, err
		}
		fields[field.column] = value
	}
	if create && fields["name"] == nil {
		return nil, errors.New("请输入密钥名称")
	}
	return fields, nil
}

func (a *App) listKeys() ([]any, error) {
	rows, err := storage.Query(a.db, "SELECT "+apiKeyColumns+" FROM keys ORDER BY created_at")
	if err != nil {
		return nil, fmt.Errorf("读取 API 密钥列表失败: %w", err)
	}
	for _, row := range rows {
		key := shared.Obj(row)
		key["ip_whitelist"] = shared.Arr(shared.Parse(key["ip_whitelist"], nil))
		key["model_whitelist"] = shared.Arr(shared.Parse(key["model_whitelist"], nil))
		chain, explicit := shared.Parse(key["group_order"], nil).([]any)
		key["group_order"] = nil
		if explicit {
			key["group_order"] = chain
		}
	}
	return rows, nil
}

func (a *App) apiKeyEncryptionKey() string {
	mac := hmac.New(sha256.New, []byte(a.secret))
	_, _ = mac.Write([]byte("zcode2api/api-key-secret/v1"))
	return string(mac.Sum(nil))
}

func (a *App) newKeySecret() (secret, encrypted string, err error) {
	if a.secret == "" {
		return "", "", errors.New("服务端主密钥不可用")
	}
	secret = "zk2-" + shared.Random(20)
	encrypted, err = accountdata.Encrypt(secret, a.apiKeyEncryptionKey())
	if err != nil {
		return "", "", errors.New("无法加密 API 密钥")
	}
	return secret, encrypted, nil
}

// Keep validation and writes in the same transaction as group deletion checks.
func (a *App) writeKeyConfig(ctx context.Context, fields shared.M, write func(storage.Executor) error) error {
	chain := shared.Arr(shared.Parse(fields["group_order"], nil))
	if len(chain) == 0 {
		return write(a.db)
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	groups, err := storage.Query(tx, "SELECT id FROM groups")
	if err != nil {
		return err
	}
	valid := map[string]bool{}
	for _, row := range groups {
		valid[shared.Str(shared.Obj(row)["id"])] = true
	}
	seen := map[string]bool{}
	for _, item := range chain {
		id := shared.Str(item)
		if !valid[id] {
			return fault.Invalid("分组链包含不存在的分组")
		}
		if seen[id] {
			return fault.Invalid("分组链中每个分组只能出现一次")
		}
		seen[id] = true
	}
	if err := write(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (a *App) createKey(ctx context.Context, fields shared.M) (shared.M, error) {
	secret, encrypted, err := a.newKeySecret()
	if err != nil {
		return nil, err
	}
	id := "key_" + shared.Random(4)
	err = a.writeKeyConfig(ctx, fields, func(db storage.Executor) error {
		_, err := db.Exec(`INSERT INTO keys(id,name,key_hash,key_tail,secret_enc,quota_limit,expires_at,ip_whitelist,model_whitelist,group_order,sticky_minutes,default_level,enabled,created_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, fields["name"], shared.Hash(secret), secret[len(secret)-6:], encrypted, fields["quota_limit"], fields["expires_at"], fields["ip_whitelist"], fields["model_whitelist"], fields["group_order"], fields["sticky_minutes"], fields["default_level"], fields["enabled"], shared.NowMS())
		if err != nil {
			a.log.Printf("insert API key: %v", err)
			return errors.New("无法保存 API 密钥")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return shared.M{"ok": true, "id": id, "secret": secret}, nil
}

func (a *App) patchKey(ctx context.Context, id string, fields shared.M) error {
	return a.writeKeyConfig(ctx, fields, func(db storage.Executor) error {
		result, err := updateFields(db, "keys", id, fields, apiKeyConfigColumns)
		if err != nil || result == nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err == nil && changed == 0 {
			return &fault.Failure{Status: http.StatusNotFound, Message: "密钥不存在"}
		}
		return err
	})
}

func (a *App) revealKey(ctx context.Context, id string) (any, int, error) {
	var encrypted sql.NullString
	var hash string
	err := a.db.QueryRowContext(ctx, "SELECT secret_enc,key_hash FROM keys WHERE id=?", id).Scan(&encrypted, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return adminError(http.StatusNotFound, "密钥不存在")
	}
	if err != nil {
		return nil, 0, errors.New("无法读取 API 密钥")
	}
	if !encrypted.Valid {
		return shared.M{"ok": false, "code": "key_not_recoverable", "error": "这把历史密钥仅保存了哈希，无法复制。"}, http.StatusConflict, nil
	}
	secret, err := accountdata.Decrypt(encrypted.String, a.apiKeyEncryptionKey())
	// The credential helper also accepts legacy plaintext. Require ciphertext and a matching gateway hash.
	if a.secret == "" || err != nil || secret == encrypted.String || shared.Hash(secret) != hash {
		return adminError(http.StatusConflict, "无法解密此密钥，请检查服务端主密钥")
	}
	return shared.M{"ok": true, "secret": secret}, http.StatusOK, nil
}

func (a *App) rotateKey(ctx context.Context, id, currentTail string) (any, int, error) {
	if len(currentTail) != 6 {
		return adminError(http.StatusBadRequest, "缺少当前密钥尾号")
	}
	secret, encrypted, err := a.newKeySecret()
	if err != nil {
		return nil, 0, err
	}
	result, err := a.db.ExecContext(ctx, "UPDATE keys SET key_hash=?,key_tail=?,secret_enc=? WHERE id=? AND key_tail=? AND secret_enc IS NULL", shared.Hash(secret), secret[len(secret)-6:], encrypted, id, currentTail)
	if err != nil {
		return nil, 0, errors.New("无法轮换 API 密钥")
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return nil, 0, err
	}
	if changed == 0 {
		return adminError(http.StatusConflict, "密钥已变更或删除，请刷新列表")
	}
	return shared.M{"ok": true, "id": id, "secret": secret}, http.StatusOK, nil
}
