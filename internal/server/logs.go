package server

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

const logPageSize = 20

func publicUsageLogs(rows []any) []any {
	for _, v := range rows {
		m := obj(v)
		m["timings"] = parse(m["timings"], nil)
		m["error_detail"] = parse(m["error_detail"], nil)
		if str(m["error_message"]) != "" {
			if m["error_detail"] == nil {
				m["error_detail"] = M{"message": m["error_message"]}
			}
			m["error_message"] = conciseFailureMessage(&Failure{Code: str(m["error_code"]), Message: str(m["error_message"])})
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
		r := obj(v)
		if str(r[value]) != "" {
			text := str(r[label])
			if text == "" {
				text = str(r[value])
			}
			choices = append(choices, M{"value": str(r[value]), "label": text})
		}
	}
	return choices
}

func (a *App) pagedLogs(r *http.Request, kind string) (any, int, error) {
	table, alias := "usage_log", "u"
	from := "usage_log u LEFT JOIN keys k ON k.id=u.key_id LEFT JOIN accounts a ON a.id=u.account_id"
	columns := "u.*,k.name key_name,a.label account_label"
	if kind == "sched" {
		table, alias, from, columns = "schedule_log", "s", "schedule_log s LEFT JOIN accounts a ON a.id=s.picked", "s.*,a.label account_label"
	}
	if kind == "audit" {
		table, alias, from, columns = "audit_log", "d", "audit_log d", "d.*"
	}
	q := r.URL.Query()
	bad := func(err error) (any, int, error) { return adminError(400, err.Error()) }
	snapshot, err := logInteger(r, "snapshot", 0)
	if err != nil {
		return bad(err)
	}
	if q.Get("snapshot") == "" {
		snapshot = integer(a.row("SELECT COALESCE(MAX(id),0) n FROM " + table)["n"])
	}
	asOf, err := logInteger(r, "as_of", nowMS())
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
	s.add(alias+".id<=?", snapshot)
	if since > 0 {
		s.add(alias+".ts>=?", since)
	}
	if until > 0 {
		s.add(alias+".ts<=?", until)
	}
	exact := func(name, column string) {
		if value := q.Get(name); value != "" && value != "all" {
			s.add(column+"=?", value)
		}
	}
	choices := M{}
	if kind == "logs" {
		switch status := q.Get("status"); status {
		case "", "all":
		case "failed", "error":
			s.add("COALESCE(u.status,'') NOT IN ('success','busy','canceled')")
		case "non_success":
			s.add("COALESCE(u.status,'')<>'success'")
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
		choices["models"] = logChoice(a.rows("SELECT DISTINCT model FROM usage_log WHERE id<=? AND COALESCE(model,'')<>'' ORDER BY model", snapshot), "model", "model")
		choices["accounts"] = logChoice(a.rows("SELECT DISTINCT u.account_id,a.label FROM usage_log u LEFT JOIN accounts a ON a.id=u.account_id WHERE u.id<=? ORDER BY COALESCE(a.label,u.account_id)", snapshot), "account_id", "label")
		choices["keys"] = logChoice(a.rows("SELECT DISTINCT u.key_id,k.name FROM usage_log u LEFT JOIN keys k ON k.id=u.key_id WHERE u.id<=? ORDER BY COALESCE(k.name,u.key_id)", snapshot), "key_id", "name")
		errors := []any{}
		for _, v := range a.rows("SELECT COALESCE(error_code,'') code,MAX(error_message) message FROM usage_log WHERE id<=? AND COALESCE(status,'')<>'success' GROUP BY COALESCE(error_code,'') ORDER BY code", snapshot) {
			r := obj(v)
			code := str(r["code"])
			label := conciseFailureMessage(&Failure{Code: code, Message: str(r["message"])})
			value := code
			if value == "" {
				value, label = "__uncoded__", "未提供错误码"
			} else {
				label += "（" + code + "）"
			}
			errors = append(errors, M{"value": value, "label": label})
		}
		choices["errors"] = errors
	} else if kind == "sched" {
		exact("policy", "s.policy")
		exact("account", "s.picked")
		switch q.Get("outcome") {
		case "", "all":
		case "picked":
			s.add("COALESCE(s.picked,'')<>''")
		case "unpicked":
			s.add("COALESCE(s.picked,'')=''")
		default:
			return bad(fmt.Errorf("调度结果无效"))
		}
		choices["policies"] = logChoice(a.rows("SELECT DISTINCT policy FROM schedule_log WHERE id<=? ORDER BY policy", snapshot), "policy", "policy")
		choices["accounts"] = logChoice(a.rows("SELECT DISTINCT s.picked,a.label FROM schedule_log s LEFT JOIN accounts a ON a.id=s.picked WHERE s.id<=? ORDER BY COALESCE(a.label,s.picked)", snapshot), "picked", "label")
	} else {
		exact("actor", "d.actor")
		exact("action", "d.action")
		exact("ip", "d.ip")
		choices["actors"] = logChoice(a.rows("SELECT DISTINCT actor FROM audit_log WHERE id<=? ORDER BY actor", snapshot), "actor", "actor")
		choices["actions"] = logChoice(a.rows("SELECT DISTINCT action FROM audit_log WHERE id<=? ORDER BY action", snapshot), "action", "action")
		choices["ips"] = logChoice(a.rows("SELECT DISTINCT ip FROM audit_log WHERE id<=? ORDER BY ip", snapshot), "ip", "ip")
	}
	if term := strings.TrimSpace(q.Get("q")); term != "" {
		if len([]rune(term)) > 200 {
			return bad(fmt.Errorf("搜索内容最多 200 个字符"))
		}
		fields := []string{"u.request_id", "u.trace_id", "u.model", "u.account_id", "a.label", "u.key_id", "k.name", "u.client_ip", "u.error_code", "u.error_message", "u.error_detail", "u.status"}
		if kind == "sched" {
			fields = []string{"s.request_id", "s.picked", "a.label", "s.policy", "s.candidates", "s.chain"}
		}
		if kind == "audit" {
			fields = []string{"d.actor", "d.action", "d.detail", "d.ip"}
		}
		parts := []string{}
		args := []any{}
		for _, field := range fields {
			parts = append(parts, "instr(lower(COALESCE("+field+",'')),lower(?))>0")
			args = append(args, term)
		}
		// Search current short summaries as well as the raw text of old records.
		if kind == "logs" {
			for _, choice := range arr(choices["errors"]) {
				c := obj(choice)
				if strings.Contains(strings.ToLower(str(c["label"])), strings.ToLower(term)) {
					if c["value"] == "__uncoded__" {
						continue
					}
					parts = append(parts, "u.error_code=?")
					args = append(args, c["value"])
				}
			}
		}
		s.add("("+strings.Join(parts, " OR ")+")", args...)
	}
	where := s.where()
	aggregate := "COUNT(*) total"
	if kind == "logs" {
		aggregate += ",COALESCE(SUM(u.status='success'),0) success,COALESCE(SUM(u.status='busy'),0) busy,COALESCE(SUM(u.status='canceled'),0) canceled,COALESCE(SUM(COALESCE(u.status,'') NOT IN ('success','busy','canceled')),0) failed,COALESCE(SUM(COALESCE(u.tokens_in,0)+COALESCE(u.tokens_out,0)),0) tokens,COUNT(u.ttft_ms) ttft_count"
	}
	summary := a.row("SELECT "+aggregate+" FROM "+from+where, s.args...)
	total := integer(summary["total"])
	page, err := logInteger(r, "page", 1)
	if err != nil {
		return bad(err)
	}
	pages := max(int64(1), (total+logPageSize-1)/logPageSize)
	page = min(max(int64(1), page), pages)
	args := append(append([]any{}, s.args...), logPageSize, (page-1)*logPageSize)
	items := a.rows("SELECT "+columns+" FROM "+from+where+" ORDER BY "+alias+".ts DESC,"+alias+".id DESC LIMIT ? OFFSET ?", args...)
	if items == nil {
		items = []any{}
	}
	if kind == "logs" {
		items = publicUsageLogs(items)
		n := integer(summary["ttft_count"])
		if n > 0 {
			args := append(append([]any{}, s.args...), n/2)
			summary["p50"] = a.row("SELECT u.ttft_ms value FROM "+from+where+" AND u.ttft_ms IS NOT NULL ORDER BY u.ttft_ms LIMIT 1 OFFSET ?", args...)["value"]
		}
	}
	return M{"items": items, "total": total, "page": page, "pages": pages, "page_size": logPageSize, "snapshot": snapshot, "as_of": asOf, "summary": summary, "filters": choices}, 200, nil
}
