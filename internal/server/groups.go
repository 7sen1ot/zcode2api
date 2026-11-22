package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strings"
	"zcode2api/internal/server/shared"
	"zcode2api/internal/server/storage"
)

const groupColumns = "name role family_filter policy urgent_window_min enabled"

func groupConfig(body shared.M, create bool) (shared.M, error) {
	fields := shared.M{}
	if create {
		fields = shared.M{"role": "primary", "policy": "balanced", "urgent_window_min": 180, "enabled": 1}
	}
	for _, field := range strings.Fields(groupColumns) {
		value, exists := body[field]
		if !exists {
			continue
		}
		var err error
		switch field {
		case "name":
			value, err = parseAdminName(value, "分组名称")
		case "role":
			switch value {
			case nil, "":
				value = "primary"
			case "primary", "backup":
			default:
				return nil, errors.New("分组角色无效")
			}
		case "family_filter":
			switch value {
			case nil, "":
				value = nil
			case "bigmodel", "zai":
			default:
				return nil, errors.New("账号类型无效")
			}
		case "policy":
			switch value {
			case nil, "":
				value = "balanced"
			case "balanced", "expiring_first", "remaining_desc":
			default:
				return nil, errors.New("调度策略无效")
			}
		case "urgent_window_min":
			if value == nil || value == "" {
				value = 180
			} else if !shared.IntegerInRange(value, 0, math.MaxInt64/60000) {
				return nil, errors.New("临期窗口必须为非负整数分钟")
			} else {
				value = shared.Integer(value)
			}
		case "enabled":
			value, err = parseEnabledFlag(value)
		}
		if err != nil {
			return nil, err
		}
		fields[field] = value
	}
	if create && fields["name"] == nil {
		return nil, errors.New("请输入分组名称")
	}
	return fields, nil
}

func (a *App) groups() []any { return a.rows("SELECT * FROM groups ORDER BY created_at,id") }

func defaultGroupChain(groups []any) []any {
	chain := []any{}
	for _, role := range []string{"primary", "backup"} {
		for _, row := range groups {
			group := shared.Obj(row)
			if shared.Num(group["enabled"]) == 1 && group["role"] == role {
				chain = append(chain, group["id"])
				break
			}
		}
	}
	return chain
}

func keyGroupReferences(db storage.Executor) (map[string]int, error) {
	rows, err := storage.Query(db, "SELECT group_order FROM keys WHERE group_order IS NOT NULL")
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	for _, row := range rows {
		seen := map[string]bool{}
		for _, group := range shared.Arr(shared.Parse(shared.Obj(row)["group_order"], nil)) {
			id := shared.Str(group)
			if !seen[id] {
				counts[id]++
				seen[id] = true
			}
		}
	}
	return counts, nil
}

func (a *App) groupDetails(ctx context.Context) (shared.M, error) {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	groups, err := storage.Query(tx, `SELECT g.*,
		(SELECT count(*) FROM accounts WHERE group_id=g.id) AS account_count,
		(SELECT count(*) FROM accounts WHERE group_id=g.id AND status='active'
			AND (coalesce(g.family_filter,'')='' OR family=g.family_filter)) AS active_account_count
		FROM groups g ORDER BY g.created_at,g.id`)
	if err != nil {
		return nil, err
	}
	references, err := keyGroupReferences(tx)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	chain := defaultGroupChain(groups)
	for i, row := range groups {
		group := shared.Obj(row)
		group["key_count"] = references[shared.Str(group["id"])]
		if slices.Contains(chain, group["id"]) {
			remaining := slices.Delete(slices.Clone(groups), i, i+1)
			group["default_chain_after_delete"] = defaultGroupChain(remaining)
		}
	}
	return shared.M{"groups": groups, "default_chain": chain}, nil
}

func (a *App) createGroup(fields shared.M) (shared.M, error) {
	id := "g_" + shared.Random(4)
	err := a.exec(`INSERT INTO groups(id,name,role,family_filter,policy,urgent_window_min,enabled,created_at)
		VALUES(?,?,?,?,?,?,?,?)`, id, fields["name"], fields["role"], fields["family_filter"], fields["policy"], fields["urgent_window_min"], fields["enabled"], shared.NowMS())
	if err != nil {
		return nil, err
	}
	a.notifyAvailable()
	return shared.M{"ok": true, "id": id}, nil
}

func (a *App) updateGroup(ctx context.Context, id string, fields shared.M) (any, int, error) {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, 500, err
	}
	defer tx.Rollback()
	var filter sql.NullString
	err = tx.QueryRowContext(ctx, "SELECT family_filter FROM groups WHERE id=?", id).Scan(&filter)
	if errors.Is(err, sql.ErrNoRows) {
		return adminError(http.StatusNotFound, "分组不存在")
	}
	if err != nil {
		return nil, 500, err
	}
	if family := shared.Str(fields["family_filter"]); family != "" && family != filter.String {
		var conflicts int
		err := tx.QueryRowContext(ctx, "SELECT count(*) FROM accounts WHERE group_id=? AND family<>?", id, family).Scan(&conflicts)
		if err != nil {
			return nil, 500, err
		}
		if conflicts > 0 {
			return adminError(http.StatusConflict, fmt.Sprintf("有 %d 个关联账号与新类型不匹配，请先移出这些账号", conflicts))
		}
	}
	if _, err := updateFields(tx, "groups", id, fields, groupColumns); err != nil {
		return nil, 500, err
	}
	if err := tx.Commit(); err != nil {
		return nil, 500, err
	}
	a.notifyAvailable()
	return shared.M{"ok": true}, http.StatusOK, nil
}

func (a *App) deleteGroup(ctx context.Context, id string) (any, int, error) {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, 500, err
	}
	defer tx.Rollback()
	var accountCount int
	err = tx.QueryRowContext(ctx, "SELECT (SELECT count(*) FROM accounts WHERE group_id=groups.id) FROM groups WHERE id=?", id).Scan(&accountCount)
	if errors.Is(err, sql.ErrNoRows) {
		return adminError(http.StatusNotFound, "分组不存在")
	}
	if err != nil {
		return nil, 500, err
	}
	if accountCount > 0 {
		return adminError(http.StatusConflict, fmt.Sprintf("分组下还有 %d 个账号，请先移出这些账号", accountCount))
	}
	references, err := keyGroupReferences(tx)
	if err != nil {
		return nil, 500, err
	}
	if references[id] > 0 {
		return adminError(http.StatusConflict, fmt.Sprintf("有 %d 个 API 密钥引用此分组，请先调整这些密钥的分组链", references[id]))
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM groups WHERE id=?", id); err != nil {
		return nil, 500, err
	}
	if err := tx.Commit(); err != nil {
		return nil, 500, err
	}
	a.notifyAvailable()
	return shared.M{"ok": true}, http.StatusOK, nil
}

func (a *App) ensureGroups() error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	groups, err := storage.Query(tx, "SELECT id FROM groups")
	if err != nil {
		return err
	}
	var reviewed bool
	if err := tx.QueryRow("SELECT EXISTS(SELECT 1 FROM settings WHERE k='legacy_group_layout_reviewed')").Scan(&reviewed); err != nil {
		return err
	}
	legacy := false
	hasMain := false
	for _, row := range groups {
		switch shared.Obj(row)["id"] {
		case "g_main":
			hasMain = true
		case "g_domestic", "g_intl", "g_backup":
			legacy = true
		}
	}
	migrate := !reviewed && !hasMain && legacy
	if migrate {
		references, err := keyGroupReferences(tx)
		if err != nil {
			return err
		}
		migrate = references["g_domestic"]+references["g_intl"]+references["g_backup"] == 0
	}
	if len(groups) == 0 || migrate {
		if _, err := tx.Exec("INSERT INTO groups(id,name,created_at) VALUES('g_main','默认池',?)", shared.NowMS()); err != nil {
			return err
		}
		if migrate {
			if _, err := tx.Exec("UPDATE accounts SET group_id='g_main' WHERE group_id IN ('g_domestic','g_intl','g_backup')"); err != nil {
				return err
			}
			if _, err := tx.Exec("DELETE FROM groups WHERE id IN ('g_domestic','g_intl','g_backup')"); err != nil {
				return err
			}
		}
	}
	if !reviewed {
		// A layout kept for existing key references remains user-managed on future starts.
		if _, err := tx.Exec("INSERT INTO settings(k,v) VALUES('legacy_group_layout_reviewed','true')"); err != nil {
			return err
		}
	}
	return tx.Commit()
}
