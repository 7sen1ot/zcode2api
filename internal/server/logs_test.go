package server

import (
	"fmt"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"testing/fstest"
)

func pagedLogResponse(t *testing.T, a *App, path string) M {
	t.Helper()
	value, status, err := a.adminRoute(httptest.NewRequest("GET", path, nil), nil)
	must(t, err)
	if status != 200 {
		t.Fatalf("log query failed: %d %v", status, value)
	}
	return obj(value)
}

func TestLogPaginationFindsFailuresBeyondLatestRecords(t *testing.T) {
	a := testApp(t)
	old := nowMS() - 100*86400000
	for i := range 125 {
		status, code, message := "success", "", ""
		if i == 0 {
			status, code, message = "failed", "3008", "busy"
		}
		if i == 1 {
			status, code, message = "error", "captcha_network_error", "sdk offline"
		}
		if i == 2 {
			status, code, message = "canceled", "canceled", "canceled"
		}
		must(t, a.exec("INSERT INTO usage_log(request_id,ts,status,error_code,error_message,model,account_id,key_id,tokens_in,tokens_out) VALUES(?,?,?,?,?,?,?,?,?,?)", fmt.Sprintf("request-%03d", i), old+int64(i), status, code, message, fmt.Sprintf("model-%d", i%2), "deleted-account", "deleted-key", 1, 2))
	}
	first := pagedLogResponse(t, a, "/api/usage/logs?page=1")
	equal(t, first["total"], 125)
	equal(t, first["page_size"], 20)
	equal(t, len(arr(first["items"])), 20)
	if obj(arr(first["items"])[0])["request_id"] != "request-124" {
		t.Fatal("logs were not ordered newest first")
	}
	failures := pagedLogResponse(t, a, "/api/usage/logs?page=1&status=failed")
	equal(t, failures["total"], 2)
	equal(t, pagedLogResponse(t, a, "/api/usage/logs?page=1&status=error")["total"], 2)
	equal(t, pagedLogResponse(t, a, "/api/usage/logs?page=1&status=non_success")["total"], 3)
	equal(t, pagedLogResponse(t, a, "/api/usage/logs?page=1&status=canceled")["total"], 1)
	search := pagedLogResponse(t, a, "/api/usage/logs?page=1&q="+url.QueryEscape("账号并发已满"))
	equal(t, search["total"], 1)
	equal(t, obj(arr(search["items"])[0])["request_id"], "request-000")
	equal(t, pagedLogResponse(t, a, "/api/usage/logs?page=1&error_code=captcha_network_error")["total"], 1)
	equal(t, pagedLogResponse(t, a, "/api/usage/logs?page=1&hours=720")["total"], 0)
	equal(t, pagedLogResponse(t, a, "/api/usage/logs?page=1&account=deleted-account&key=deleted-key&model=model-0")["total"], 63)
	last := pagedLogResponse(t, a, "/api/usage/logs?page=9999")
	equal(t, last["page"], 7)
	equal(t, len(arr(last["items"])), 5)
	if len(arr(obj(first["filters"])["errors"])) != 3 {
		t.Fatal("error choices were limited to the current page")
	}
	equal(t, a.row("SELECT COUNT(*) n FROM usage_log")["n"], 125)
}

func TestLogPageSnapshotAvoidsDuplicatesDuringNewCalls(t *testing.T) {
	a := testApp(t)
	ts := nowMS()
	for i := range 43 {
		must(t, a.exec("INSERT INTO usage_log(request_id,ts,status) VALUES(?,?,'success')", fmt.Sprint(i), ts))
	}
	first := pagedLogResponse(t, a, "/api/usage/logs?page=1")
	must(t, a.exec("INSERT INTO usage_log(request_id,ts,status) VALUES('new',?,'success')", ts+1))
	second := pagedLogResponse(t, a, fmt.Sprintf("/api/usage/logs?page=2&snapshot=%v&as_of=%v", first["snapshot"], first["as_of"]))
	equal(t, second["total"], 43)
	seen := map[string]bool{}
	for _, row := range arr(first["items"]) {
		seen[str(obj(row)["request_id"])] = true
	}
	for _, row := range arr(second["items"]) {
		if seen[str(obj(row)["request_id"])] {
			t.Fatal("a new call shifted a record into both pages")
		}
	}
	equal(t, pagedLogResponse(t, a, "/api/usage/logs?page=1")["total"], 44)
}

func TestScheduleAndAuditFilteringSearchesAllHistory(t *testing.T) {
	a := testApp(t)
	ts := nowMS() - 70*86400000
	for i := range 65 {
		var picked any
		if i%2 == 0 {
			picked = "chosen-account"
		}
		must(t, a.exec("INSERT INTO schedule_log(ts,request_id,policy,picked,candidates) VALUES(?,?,?,?,?)", ts+int64(i), fmt.Sprintf("schedule-%d", i), "round_robin", picked, js([]any{M{"account": "candidate-needle", "skip": "upstream_busy"}})))
		must(t, a.exec("INSERT INTO audit_log(ts,actor,action,detail,ip) VALUES(?,?,?,?,?)", ts+int64(i), "admin", fmt.Sprintf("action-%d", i%2), fmt.Sprint("detail-", i), "127.0.0.1"))
	}
	equal(t, pagedLogResponse(t, a, "/api/schedule-logs?page=1&outcome=unpicked")["total"], 32)
	equal(t, pagedLogResponse(t, a, "/api/schedule-logs?page=1&account=chosen-account&policy=round_robin")["total"], 33)
	equal(t, pagedLogResponse(t, a, "/api/schedule-logs?page=1&q=schedule-0")["total"], 1)
	equal(t, pagedLogResponse(t, a, "/api/schedule-logs?page=1&q=candidate-needle")["total"], 65)
	equal(t, len(arr(pagedLogResponse(t, a, "/api/schedule-logs?page=3")["items"])), 20)
	audit := pagedLogResponse(t, a, "/api/audit?page=1&actor=admin&action=action-0&ip=127.0.0.1")
	equal(t, audit["total"], 33)
	equal(t, pagedLogResponse(t, a, "/api/audit?page=1&q=detail-0")["total"], 1)
	equal(t, pagedLogResponse(t, a, "/api/audit?page=1&since="+strconv.FormatInt(ts+10, 10)+"&until="+strconv.FormatInt(ts+20, 10))["total"], 11)
	equal(t, len(arr(pagedLogResponse(t, a, "/api/audit?page=4")["items"])), 5)
	root, data := a.root, a.data
	must(t, a.exec("INSERT INTO usage_log(request_id,ts,status) VALUES('retained-old-call',?,'failed')", ts))
	a.Close()
	reopened, err := New(root, data, fstest.MapFS{})
	must(t, err)
	defer reopened.Close()
	equal(t, pagedLogResponse(t, reopened, "/api/usage/logs?page=1")["total"], 1)
	equal(t, pagedLogResponse(t, reopened, "/api/schedule-logs?page=1")["total"], 65)
	equal(t, pagedLogResponse(t, reopened, "/api/audit?page=1")["total"], 65)
}

func TestPagedLogFiltersRejectInvalidRangesAndTreatSearchLiterally(t *testing.T) {
	a := testApp(t)
	must(t, a.exec("INSERT INTO audit_log(ts,actor,action,detail) VALUES(?,'admin','test','100%_literal')", nowMS()))
	equal(t, pagedLogResponse(t, a, "/api/audit?page=1&q="+url.QueryEscape("%_"))["total"], 1)
	equal(t, pagedLogResponse(t, a, "/api/audit?page=1&q="+url.QueryEscape("' OR 1=1 --"))["total"], 0)
	for _, path := range []string{"/api/audit?page=-1", "/api/audit?page=1&since=bad", "/api/audit?page=1&since=20&until=10", "/api/usage/logs?page=1&status=anything", "/api/schedule-logs?page=1&outcome=anything"} {
		_, status, err := a.adminRoute(httptest.NewRequest("GET", path, nil), nil)
		must(t, err)
		equal(t, status, 400)
	}
}
