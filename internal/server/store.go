package server

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/base64"
	"errors"
	"fmt"
	_ "modernc.org/sqlite"
	"os"
	"path/filepath"
	"strings"
)

//go:embed schema.sql
var schema string

func openDB(data string) (*sql.DB, error) {
	d, e := sql.Open("sqlite", filepath.Join(data, "zcode2api.db"))
	if e != nil {
		return nil, e
	}
	d.SetMaxOpenConns(1)
	if _, e = d.Exec("PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000; PRAGMA foreign_keys=ON;" + schema); e != nil {
		d.Close()
		return nil, e
	}
	for table, cols := range map[string]map[string]string{"usage_log": {"timings": "TEXT", "trace_id": "TEXT"}, "keys": {"default_level": "TEXT"}, "oauth_flows": {"poll_interval_sec": "INTEGER NOT NULL DEFAULT 3", "result_json": "TEXT", "updated_at": "INTEGER", "noticed_at": "INTEGER"}, "accounts": {"proxy_url": "TEXT"}} {
		rows, e := query(d, "PRAGMA table_info("+table+")")
		if e != nil {
			d.Close()
			return nil, e
		}
		existing := map[string]bool{}
		for _, r := range rows {
			existing[str(obj(r)["name"])] = true
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

func query(d *sql.DB, q string, args ...any) ([]any, error) {
	rows, e := d.Query(q, args...)
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
		m := make(M, len(cols))
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
func (a *App) rows(q string, args ...any) []any {
	v, e := query(a.db, q, args...)
	if e != nil {
		a.log.Printf("database query: %v", e)
	}
	return v
}
func (a *App) row(q string, args ...any) M {
	v := a.rows(q, args...)
	if len(v) > 0 {
		return obj(v[0])
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
	return parse(r["v"], d)
}
func (a *App) setSetting(k string, v any) error {
	return a.exec("INSERT INTO settings(k,v) VALUES(?,?) ON CONFLICT(k) DO UPDATE SET v=excluded.v", k, js(v))
}
func (a *App) audit(actor, action string, detail any, ip string) {
	_ = a.exec("INSERT INTO audit_log(ts,actor,action,detail,ip) VALUES(?,?,?,?,?)", nowMS(), actor, action, js(detail), ip)
}
func (a *App) update(table, idcol, id string, fields M, allowed string) error {
	sets := []string{}
	args := []any{}
	for k, v := range fields {
		if !strings.Contains(" "+allowed+" ", " "+k+" ") {
			continue
		}
		switch v.(type) {
		case map[string]any, []any:
			v = js(v)
		}
		if b, ok := v.(bool); ok {
			v = 0
			if b {
				v = 1
			}
		}
		sets = append(sets, k+"=?")
		args = append(args, v)
	}
	if len(sets) == 0 {
		return nil
	}
	args = append(args, id)
	e := a.exec("UPDATE "+table+" SET "+strings.Join(sets, ",")+" WHERE "+idcol+"=?", args...)
	if e == nil && (table == "accounts" || table == "groups") {
		a.notifyAvailable()
	}
	return e
}
func loadSecret(data string) (string, error) {
	p := filepath.Join(data, "secret.key")
	b, e := os.ReadFile(p)
	if e == nil {
		return strings.TrimSpace(string(b)), nil
	}
	if !os.IsNotExist(e) {
		return "", e
	}
	s := random(32)
	return s, os.WriteFile(p, []byte(s), 0600)
}
func encrypt(s, secret string) (string, error) {
	k := sha256.Sum256([]byte(secret))
	b, e := aes.NewCipher(k[:])
	if e != nil {
		return "", e
	}
	g, e := cipher.NewGCM(b)
	if e != nil {
		return "", e
	}
	iv := make([]byte, 12)
	if _, e = rand.Read(iv); e != nil {
		return "", e
	}
	v := g.Seal(nil, iv, []byte(s), nil)
	enc := base64.RawURLEncoding.EncodeToString
	return "enc:v1:" + enc(iv) + "." + enc(v[len(v)-16:]) + "." + enc(v[:len(v)-16]), nil
}
func decrypt(s, secret string) (string, error) {
	if !strings.HasPrefix(s, "enc:v1:") {
		return s, nil
	}
	parts := strings.Split(strings.TrimPrefix(s, "enc:v1:"), ".")
	if len(parts) != 3 {
		return "", errors.New("凭据格式无效")
	}
	decode := base64.RawURLEncoding.DecodeString
	iv, e := decode(parts[0])
	if e != nil {
		return "", e
	}
	tag, e := decode(parts[1])
	if e != nil {
		return "", e
	}
	ct, e := decode(parts[2])
	if e != nil {
		return "", e
	}
	if len(iv) != 12 || len(tag) != 16 {
		return "", fmt.Errorf("凭据长度无效")
	}
	k := sha256.Sum256([]byte(secret))
	b, _ := aes.NewCipher(k[:])
	g, _ := cipher.NewGCM(b)
	v, e := g.Open(nil, iv, append(ct, tag...), nil)
	return string(v), e
}
