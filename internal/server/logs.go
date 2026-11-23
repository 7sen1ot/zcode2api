package server

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"zcode2api/internal/server/fault"
	"zcode2api/internal/server/shared"
)

const (
	logPageSize        = 20
	usageLogTable      = "usage_log u"
	usageLogWithLabels = usageLogTable + " LEFT JOIN keys k ON k.id=u.key_id LEFT JOIN accounts a ON a.id=u.account_id"
	usageLogColumns    = "u.*,k.name key_name,a.label account_label"
)

func (a *App) startUsageLogCleanup() {
	a.background(func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			a.pruneUsageLogs()
			select {
			case <-a.ctx.Done():
				return
			case <-ticker.C:
			case <-a.usageLogCleanupWake:
			}
		}
	})
}

func (a *App) pruneUsageLogs() {
	const batchSize = 1000
	for a.ctx.Err() == nil {
		// Keep each batch consistent with any concurrent retention change.
		a.configMu.RLock()
		days := shared.Integer(a.cfg["usage_log_retention_days"])
		cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour).UnixMilli()
		result, err := a.db.ExecContext(a.ctx, `DELETE FROM usage_log WHERE id IN (
			SELECT id FROM usage_log WHERE ts<? ORDER BY ts LIMIT ?
		)`, cutoff, batchSize)
		a.configMu.RUnlock()
		if err != nil {
			if a.ctx.Err() == nil {
				a.log.Printf("调用记录清理失败: %v", err)
			}
			return
		}
		deleted, err := result.RowsAffected()
		if err != nil || deleted < batchSize {
			return
		}
	}
}

func loggedFailure(code, message string, rawDetail any) *fault.Failure {
	detail := shared.Obj(rawDetail)
	if value, ok := rawDetail.(string); ok {
		detail = shared.Obj(shared.Parse(value, nil))
	}
	if original := shared.Str(detail["message"]); original != "" {
		message = original
	}
	return &fault.Failure{
		Code:       code,
		Message:    message,
		Kind:       shared.Str(detail["kind"]),
		Status:     int(shared.Integer(shared.First(detail["http_status"], detail["status"]))),
		VerifyCode: shared.Str(detail["verify_code"]),
		Detail:     detail,
	}
}

func publicUsageLogs(rows []any) []any {
	for _, v := range rows {
		m := shared.Obj(v)
		m["timings"] = shared.Parse(m["timings"], nil)
		m["error_detail"] = shared.Parse(m["error_detail"], nil)
		if m["status"] == "success" && shared.Str(m["error_code"]) == "" && shared.Str(m["error_message"]) == "" &&
			m["ttft_ms"] != nil && m["tokens_in"] != nil && m["tokens_out"] != nil && shared.Num(m["tokens_in"]) == 0 && shared.Num(m["tokens_out"]) == 0 {
			m["error_message"] = "已收到内容，但日志记录的 Token 用量为 0"
		}
		if shared.Str(m["error_message"]) != "" || shared.Str(m["error_code"]) != "" {
			if m["error_detail"] == nil {
				m["error_detail"] = shared.M{"message": m["error_message"]}
			}
			detail := shared.Obj(m["error_detail"])
			body := shared.Obj(shared.Parse(detail["body"], nil))
			upstreamError := shared.Obj(body["error"])
			if code := shared.Str(shared.First(upstreamError["code"], body["code"], upstreamError["type"])); code != "" {
				detail["upstream_code"] = code
			}
			failure := loggedFailure(shared.Str(m["error_code"]), shared.Str(m["error_message"]), detail)
			m["error_message"] = fault.ConciseFailureMessage(failure)
			m["error_reason"] = fault.FailureReason(failure)
		}
	}
	return rows
}

type logSelection struct {
	conditions []string
	args       []any
}

func (s *logSelection) add(condition string, args ...any) {
	s.conditions = append(s.conditions, condition)
	s.args = append(s.args, args...)
}

func (s logSelection) where() string { return " WHERE " + strings.Join(s.conditions, " AND ") }

func logInteger(r *http.Request, name string, fallback int64) (int64, error) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%s 参数无效", name)
	}
	return n, nil
}

func logChoice(rows []any, value, label string) []any {
	choices := []any{}
	for _, v := range rows {
		r := shared.Obj(v)
		if shared.Str(r[value]) != "" {
			text := shared.Str(r[label])
			if text == "" {
				text = shared.Str(r[value])
			}
			choices = append(choices, shared.M{"value": shared.Str(r[value]), "label": text})
		}
	}
	return choices
}

func (a *App) pagedUsageLogs(r *http.Request) (any, int, error) {
	ctx := r.Context()
	readRows := func(q string, args ...any) []any { return a.rowsContext(ctx, q, args...) }
	q := r.URL.Query()
	bad := func(err error) (any, int, error) { return adminError(400, err.Error()) }
	snapshot, err := logInteger(r, "snapshot", 0)
	if err != nil {
		return bad(err)
	}
	if q.Get("snapshot") == "" {
		if err := a.db.QueryRowContext(ctx, "SELECT COALESCE(MAX(id),0) FROM usage_log").Scan(&snapshot); err != nil {
			return nil, 0, err
		}
	}
	asOf, err := logInteger(r, "as_of", shared.NowMS())
	if err != nil {
		return bad(err)
	}
	since, err := logInteger(r, "since", 0)
	if err != nil {
		return bad(err)
	}
	until, err := logInteger(r, "until", 0)
	if err != nil {
		return bad(err)
	}
	if until != 0 && until < since {
		return bad(fmt.Errorf("结束时间不能早于开始时间"))
	}
	if hours := q.Get("hours"); hours != "" && hours != "all" {
		n, err := strconv.ParseInt(hours, 10, 64)
		if err != nil || (n != 24 && n != 168 && n != 720) {
			return bad(fmt.Errorf("时间范围无效"))
		}
		since = max(since, asOf-n*3600000)
	}
	s := logSelection{}
	s.add("u.id<=?", snapshot)
	if since > 0 {
		s.add("u.ts>=?", since)
	}
	if until > 0 {
		s.add("u.ts<=?", until)
	}
	exact := func(name, column string) {
		if value := q.Get(name); value != "" && value != "all" {
			s.add(column+"=?", value)
		}
	}
	switch status := q.Get("status"); status {
	case "", "all":
	case "failed", "error":
		s.add("COALESCE(u.status,'') NOT IN ('success','busy','canceled')")
	case "non_success":
		s.add("(COALESCE(u.status,'')<>'success' OR COALESCE(u.error_code,'')<>'')")
	case "success", "busy", "canceled":
		s.add("u.status=?", status)
	default:
		return bad(fmt.Errorf("调用状态无效"))
	}
	exact("model", "u.model")
	exact("account", "u.account_id")
	exact("key", "u.key_id")
	if code := q.Get("error_code"); code != "" && code != "all" {
		if code == "__uncoded__" {
			s.add("COALESCE(u.error_code,'')='' AND COALESCE(u.status,'')<>'success'")
		} else {
			s.add("u.error_code=?", code)
		}
	}
	term := strings.TrimSpace(q.Get("q"))
	if len([]rune(term)) > 200 {
		return bad(fmt.Errorf("搜索内容最多 200 个字符"))
	}
	page, err := logInteger(r, "page", 1)
	if err != nil {
		return bad(err)
	}
	knownSnapshot, err := logInteger(r, "known_snapshot", -1)
	if err != nil {
		return bad(err)
	}
	// The live client periodically requests a full refresh so renames,
	// retention and rolling time filters still update without new log IDs.
	if page == 1 && q.Get("snapshot") == "" && knownSnapshot == snapshot {
		return shared.M{"unchanged": true}, 200, nil
	}
	choices := shared.M{
		"models":   logChoice(readRows("SELECT DISTINCT model FROM usage_log WHERE id<=? AND model<>'' ORDER BY model", snapshot), "model", "model"),
		"accounts": logChoice(readRows("SELECT u.account_id,a.label FROM (SELECT DISTINCT account_id FROM usage_log WHERE id<=?) u LEFT JOIN accounts a ON a.id=u.account_id ORDER BY COALESCE(a.label,u.account_id)", snapshot), "account_id", "label"),
		"keys":     logChoice(readRows("SELECT u.key_id,k.name FROM (SELECT DISTINCT key_id FROM usage_log WHERE id<=?) u LEFT JOIN keys k ON k.id=u.key_id ORDER BY COALESCE(k.name,u.key_id)", snapshot), "key_id", "name"),
	}
	errors := []any{}
	for _, v := range readRows("SELECT COALESCE(error_code,'') code,error_message message,error_detail FROM usage_log WHERE id IN (SELECT MAX(id) FROM usage_log WHERE id<=? AND (COALESCE(status,'')<>'success' OR COALESCE(error_code,'')<>'') GROUP BY COALESCE(error_code,'')) ORDER BY code", snapshot) {
		r := shared.Obj(v)
		code := shared.Str(r["code"])
		label := fault.ConciseFailureMessage(loggedFailure(code, shared.Str(r["message"]), r["error_detail"]))
		value := code
		if value == "" {
			value, label = "__uncoded__", "未提供错误码"
		} else if !strings.HasSuffix(label, "（"+code+"）") {
			label += "（" + code + "）"
		}
		errors = append(errors, shared.M{"value": value, "label": label})
	}
	choices["errors"] = errors
	summaryFrom := usageLogTable
	if term != "" {
		summaryFrom = usageLogWithLabels
		fields := []string{"u.request_id", "u.trace_id", "u.model", "u.account_id", "a.label", "u.key_id", "k.name", "u.client_ip", "u.error_code", "u.error_message", "u.error_detail", "u.status"}
		parts := []string{}
		args := []any{}
		for _, field := range fields {
			parts = append(parts, "instr(lower(COALESCE("+field+",'')),lower(?))>0")
			args = append(args, term)
		}
		// Search current short summaries as well as the raw text of old records.
		for _, choice := range errors {
			c := shared.Obj(choice)
			if c["value"] != "__uncoded__" && strings.Contains(strings.ToLower(shared.Str(c["label"])), strings.ToLower(term)) {
				parts = append(parts, "u.error_code=?")
				args = append(args, c["value"])
			}
		}
		s.add("("+strings.Join(parts, " OR ")+")", args...)
	}
	where := s.where()
	const aggregate = "COUNT(*) total,COALESCE(SUM(u.status='success'),0) success,COALESCE(SUM(u.status='busy'),0) busy,COALESCE(SUM(u.status='canceled'),0) canceled,COALESCE(SUM(COALESCE(u.status,'') NOT IN ('success','busy','canceled')),0) failed,COALESCE(SUM(COALESCE(u.tokens_in,0)+COALESCE(u.tokens_out,0)),0) tokens,COUNT(u.ttft_ms) ttft_count"
	summary := firstRow(readRows("SELECT "+aggregate+" FROM "+summaryFrom+where, s.args...))
	total := shared.Integer(summary["total"])
	pages := max(int64(1), (total+logPageSize-1)/logPageSize)
	page = min(max(int64(1), page), pages)
	args := append(append([]any{}, s.args...), logPageSize, (page-1)*logPageSize)
	items := readRows("SELECT "+usageLogColumns+" FROM "+usageLogWithLabels+where+" ORDER BY u.ts DESC,u.id DESC LIMIT ? OFFSET ?", args...)
	if items == nil {
		items = []any{}
	}
	items = publicUsageLogs(items)
	if n := shared.Integer(summary["ttft_count"]); n > 0 {
		args := append(append([]any{}, s.args...), n/2)
		summary["p50"] = firstRow(readRows("SELECT u.ttft_ms value FROM "+summaryFrom+where+" AND u.ttft_ms IS NOT NULL ORDER BY u.ttft_ms LIMIT 1 OFFSET ?", args...))["value"]
	}
	return shared.M{"items": items, "total": total, "page": page, "pages": pages, "page_size": logPageSize, "snapshot": snapshot, "as_of": asOf, "summary": summary, "filters": choices}, 200, nil
}
