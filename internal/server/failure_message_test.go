package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestConciseFailureMessages(t *testing.T) {
	for _, item := range []struct {
		failure Failure
		want    string
	}{
		{Failure{Code: "3012", Message: strings.Repeat("blocked ", 1000)}, "上游风控拦截"},
		{Failure{Status: 403, Message: "Request has been blocked due to unusual activity."}, "上游风控拦截"},
		{Failure{Code: "upstream_waf_blocked"}, "上游安全网关拦截"},
		{Failure{Code: "insufficient_quota"}, "账号额度不足"},
		{Failure{Kind: "network", Message: "Post https://private.invalid?token=secret: EOF"}, "上游连接失败"},
		{Failure{Code: "proxy_connection_failed", Message: "proxyconnect tcp: private endpoint"}, "代理连接失败"},
		{Failure{Code: "upstream_stream_error", Message: "Post https://private.invalid?token=secret: EOF"}, "上游响应中断"},
		{Failure{Code: "truncated_stream", Kind: "server"}, "上游响应不完整"},
		{Failure{Code: "3008"}, "账号并发已满"},
		{Failure{Code: "account_busy", Kind: "busy"}, "本地排队超时"},
		{Failure{Code: "queue_full", Kind: "busy"}, "本地等待队列已满"},
		{Failure{Code: "rate_limit_error"}, "请求频率受限"},
		{Failure{Code: "upstream_http_error", Status: 503}, "上游服务过载"},
		{Failure{Code: "authentication_error"}, "账号凭据失效"},
		{Failure{Code: "no_account", Kind: "exhausted"}, "无可用账号"},
		{Failure{Code: "captcha_network_error", Kind: "network"}, "SDK 连接失败"},
		{Failure{Code: "captcha_jsdom_missing", Kind: "server"}, "SDK 缺少 jsdom"},
		{Failure{Code: "captcha_timeout", Kind: "server"}, "SDK 验证超时"},
		{Failure{Code: "captcha_interaction_required", Kind: "risk"}, "SDK 需要人工验证"},
		{Failure{Code: "captcha_verification_failed", VerifyCode: "F001"}, "SDK 风控拒绝（F001）"},
		{Failure{Code: "captcha_verification_failed", Message: "上游安全验证未通过，需要按官方验证流程处理（F001）"}, "SDK 风控拒绝（F001）"},
		{Failure{Code: "captcha_verification_failed", VerifyCode: "F999"}, "SDK 验证失败（F999）"},
		{Failure{Code: "captcha_verification_failed", VerifyCode: "<script>private</script>"}, "SDK 验证失败"},
		{Failure{Code: "proxy_pool_exhausted", Kind: "server"}, "代理节点全部不可用"},
		{Failure{Code: "proxy_runtime_missing", Kind: "server"}, "未找到 Mihomo"},
		{Failure{Code: "proxy_auth_failed", Kind: "network"}, "代理认证失败"},
		{Failure{Code: "first_token_timeout", Kind: "timeout"}, "上游首字超时"},
		{Failure{Code: "stream_idle_timeout", Kind: "timeout"}, "上游流空闲超时"},
		{Failure{Code: "inference_timeout", Kind: "timeout"}, "上游推理超时"},
		{Failure{Code: "captcha_config_timeout", Kind: "timeout"}, "SDK 配置获取超时"},
		{Failure{Code: "throttle_window_exhausted", Kind: "rate_limit"}, "限流重试超时"},
		{Failure{Code: "timeout", Message: "HTTP 首字超时"}, "上游首字超时"},
		{Failure{Code: "canceled", Kind: "canceled"}, "请求已取消"},
		{Failure{Code: "invalid_request_error", Message: "messages[0].content is required"}, "messages[0].content is required"},
	} {
		t.Run(item.want, func(t *testing.T) {
			message := conciseFailureMessage(&item.failure)
			equal(t, message, item.want)
			// A historical row has the code and summary, but no in-memory kind.
			equal(t, conciseFailureMessage(&Failure{Code: item.failure.Code, Message: message}), message)
		})
	}
	failure := &Failure{Code: "unknown", Message: strings.Repeat("未知错误\n\t ", 1000)}
	message := conciseFailureMessage(failure)
	if !utf8.ValidString(message) || len([]rune(message)) > 80 || strings.ContainsAny(message, "\n\t") {
		t.Fatal("unknown error was not bounded safely", message)
	}
	response := &http.Response{StatusCode: 405, Header: http.Header{"Server": {strings.Repeat("diagnostic ", 1000)}}}
	failure = upstreamHTTPFailure(response, []byte("<html>errors.aliyun.com/ block_message</html>"))
	equal(t, failure.Message, "上游安全网关拦截")
}

func TestUsageLogFailureCategoriesSurviveHistoricalRead(t *testing.T) {
	a := testApp(t)
	for _, item := range []struct {
		id      string
		failure Failure
		want    string
	}{
		{"busy", Failure{Kind: "busy", Code: "3008"}, "账号并发已满"},
		{"queue", Failure{Kind: "busy", Code: "account_busy"}, "本地排队超时"},
		{"rate", Failure{Kind: "rate_limit", Code: "1302"}, "请求频率受限"},
		{"overload", Failure{Kind: "overload", Status: 503, Code: "upstream_http_error"}, "上游服务过载"},
		{"sdk", Failure{Kind: "network", Code: "captcha_network_error"}, "SDK 连接失败"},
		{"proxy", Failure{Kind: "network", Code: "proxy_connection_failed"}, "代理连接失败"},
		{"sdk-risk", Failure{Kind: "risk", Code: "captcha_verification_failed", VerifyCode: "F001"}, "SDK 风控拒绝（F001）"},
	} {
		a.logUsage(inferOptions{ID: item.id, Key: M{}, Body: M{}, Timing: newTiming()}, nil, &item.failure, M{}, 2, nil)
		value, status, err := a.adminRoute(httptest.NewRequest("GET", "/api/usage/logs", nil), nil)
		must(t, err)
		equal(t, status, 200)
		found := false
		for _, entry := range arr(value) {
			row := obj(entry)
			if row["request_id"] == item.id {
				found = true
				equal(t, row["error_message"], item.want)
				equal(t, row["error_code"], item.failure.Code)
				equal(t, row["attempt_count"], 2)
			}
		}
		if !found {
			t.Fatal("request log missing", item.id)
		}
	}
}

func TestTransportFailuresIdentifyProxyAndDirectConnection(t *testing.T) {
	for _, viaProxy := range []bool{false, true} {
		a := testApp(t)
		closed := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		closed.Close()
		a.origin = closed.URL
		wantCode, wantMessage := "network", "上游连接失败"
		if viaProxy {
			setTestProxy(a, M{"enabled": true, "urls": []any{closed.URL}})
			wantCode, wantMessage = "proxy_connection_failed", "代理连接失败"
		}
		_, failure := a.inferHTTP(t.Context(), M{}, inferOptions{Upstream: M{}})
		if failure == nil {
			t.Fatal("closed endpoint unexpectedly succeeded")
		}
		equal(t, failure.Code, wantCode)
		equal(t, failure.Message, wantMessage)
	}
}

func TestUsageLogsSummarizeNewAndHistoricalErrors(t *testing.T) {
	a := testApp(t)
	raw := "request has been blocked due to unusual activity. " + strings.Repeat("details ", 1000)
	a.logUsage(inferOptions{ID: "new-request", Key: M{}, Body: M{}, Timing: newTiming()}, nil, &Failure{Code: "3012", Message: raw}, M{}, 1, nil)
	equal(t, a.row("SELECT error_message FROM usage_log WHERE request_id=?", "new-request")["error_message"], "上游风控拦截")
	must(t, a.exec("INSERT INTO usage_log(request_id,ts,status,error_code,error_message,trace_id) VALUES(?,?,?,?,?,?)", "old-request", nowMS(), "failed", "3012", raw, "original-trace"))
	value, status, err := a.adminRoute(httptest.NewRequest("GET", "/api/usage/logs", nil), nil)
	must(t, err)
	equal(t, status, 200)
	for _, value := range arr(value) {
		row := obj(value)
		equal(t, row["error_message"], "上游风控拦截")
		equal(t, row["error_code"], "3012")
		if row["request_id"] == "old-request" {
			equal(t, row["trace_id"], "original-trace")
		}
	}
	equal(t, a.row("SELECT error_message FROM usage_log WHERE request_id=?", "old-request")["error_message"], raw)
}
