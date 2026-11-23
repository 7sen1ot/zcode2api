package storage

import (
	"context"
	"database/sql"
	_ "embed"
	_ "modernc.org/sqlite"
	"path/filepath"
	"zcode2api/internal/server/shared"
)

//go:embed schema.sql
var Schema string

func Open(data string) (*sql.DB, error) {
	d, e := sql.Open("sqlite", filepath.Join(data, "zcode2api.db"))
	if e != nil {
		return nil, e
	}
	d.SetMaxOpenConns(1)
	if _, e = d.Exec("PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000; PRAGMA foreign_keys=ON;" + Schema); e != nil {
		d.Close()
		return nil, e
	}
	for table, cols := range map[string]map[string]string{"usage_log": {"timings": "TEXT", "trace_id": "TEXT", "error_detail": "TEXT"}, "claim_log": {"error_detail": "TEXT"}, "keys": {"default_level": "TEXT", "secret_enc": "TEXT", "default_instruction_mode": "TEXT"}, "oauth_flows": {"poll_interval_sec": "INTEGER NOT NULL DEFAULT 3", "result_json": "TEXT", "updated_at": "INTEGER", "noticed_at": "INTEGER"}, "accounts": {"proxy_url": "TEXT"}} {
		rows, e := Query(d, "PRAGMA table_info("+table+")")
		if e != nil {
			d.Close()
			return nil, e
		}
		existing := map[string]bool{}
		for _, r := range rows {
			existing[shared.Str(shared.Obj(r)["name"])] = true
		}
		for c, t := range cols {
			if !existing[c] {
				if _, e = d.Exec("ALTER TABLE " + table + " ADD COLUMN " + c + " " + t); e != nil {
					d.Close()
					return nil, e
				}
			}
		}
	}
	return d, nil
}

type Executor interface {
	Query(string, ...any) (*sql.Rows, error)
	Exec(string, ...any) (sql.Result, error)
}

func Query(d Executor, q string, args ...any) ([]any, error) {
	return scanRows(d.Query(q, args...))
}

func QueryContext(ctx context.Context, d *sql.DB, q string, args ...any) ([]any, error) {
	return scanRows(d.QueryContext(ctx, q, args...))
}

func scanRows(rows *sql.Rows, e error) ([]any, error) {
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	cols, e := rows.Columns()
	if e != nil {
		return nil, e
	}
	out := []any{}
	v := make([]any, len(cols))
	p := make([]any, len(cols))
	for i := range v {
		p[i] = &v[i]
	}
	for rows.Next() {
		if e = rows.Scan(p...); e != nil {
			return nil, e
		}
		m := make(shared.M, len(cols))
		for i, k := range cols {
			if b, ok := v[i].([]byte); ok {
				v[i] = string(b)
			}
			m[k] = v[i]
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
