package server

import (
	"database/sql"
	"strings"
	"zcode2api/internal/server/shared"
	"zcode2api/internal/server/storage"
)

func (a *App) rows(q string, args ...any) []any {
	v, e := storage.Query(a.db, q, args...)
	if e != nil {
		a.log.Printf("database query: %v", e)
	}
	return v
}

func (a *App) row(q string, args ...any) shared.M {
	v := a.rows(q, args...)
	if len(v) > 0 {
		return shared.Obj(v[0])
	}
	return nil
}

func (a *App) exec(q string, args ...any) error {
	_, e := a.db.Exec(q, args...)
	if e != nil {
		a.log.Printf("database write: %v", e)
	}
	return e
}

func (a *App) setting(k string, d any) any {
	r := a.row("SELECT v FROM settings WHERE k=?", k)
	if r == nil {
		return d
	}
	return shared.Parse(r["v"], d)
}

func (a *App) setSetting(k string, v any) error {
	return a.exec("INSERT INTO settings(k,v) VALUES(?,?) ON CONFLICT(k) DO UPDATE SET v=excluded.v WHERE settings.v IS NOT excluded.v", k, shared.JSON(v))
}

func updateFields(db storage.Executor, table, id string, fields shared.M, allowed string) (sql.Result, error) {
	sets := []string{}
	args := []any{}
	for k := range strings.FieldsSeq(allowed) {
		v, present := fields[k]
		if !present {
			continue
		}
		switch v.(type) {
		case map[string]any, []any:
			v = shared.JSON(v)
		}
		sets = append(sets, k+"=?")
		args = append(args, v)
	}
	if len(sets) == 0 {
		return nil, nil
	}
	args = append(args, id)
	return db.Exec("UPDATE "+table+" SET "+strings.Join(sets, ",")+" WHERE id=?", args...)
}
