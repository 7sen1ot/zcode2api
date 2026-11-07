package server

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"runtime"
	"sort"
	"strings"
	"time"
)

type Frame struct{ Data, Raw string }

func readSSE(body io.Reader, consume func(Frame) error) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 4096), 32<<20)
	lines := []string{}
	size := 0
	flush := func() error {
		if len(lines) == 0 {
			return nil
		}
		f := Frame{Raw: strings.Join(lines, "\n") + "\n\n"}
		data := []string{}
		for _, line := range lines {
			if strings.HasPrefix(line, "data:") {
				data = append(data, strings.TrimPrefix(line[5:], " "))
			}
		}
		f.Data = strings.Join(data, "\n")
		lines = nil
		size = 0
		return consume(f)
	}
	for scanner.Scan() {
		line := scanner.Text()
		size += len(line) + 1
		if size > 32<<20 {
			return fmt.Errorf("上游 SSE 事件超过 32MB")
		}
		if line == "" {
			if e := flush(); e != nil {
				return e
			}
		} else {
			lines = append(lines, line)
		}
	}
	if e := scanner.Err(); e != nil {
		return e
	}
	return flush()
}

type Event struct {
	Type, Text, Delta, ID, Name string
	Index                       int
	Frame                       Frame
}
type Result struct {
	Text, Reasoning, FinishReason, SessionID string
	Native, Usage                            M
	Calls                                    []any
	Attempts                                 int
}
type contentBuffer struct{ text, thinking, signature, partial strings.Builder }
type inferOptions struct {
	Key, Body, Upstream                      M
	Tools                                    *toolCatalog
	Protocol, Model, Level, ID, IP, Affinity string
	Sticky                                   float64
	Stream                                   bool
	OnEvent                                  func(Event) error
	OnAdmitted                               func()
	Timing                                   *requestTiming
	Admission                                time.Duration
}
type requestTiming struct {
	start                     time.Time
	queue, retry              time.Duration
	first, reason, text, last *int64
}

func newTiming() *requestTiming { return &requestTiming{start: time.Now()} }
func (t *requestTiming) observe(e Event) {
	if !((e.Type == "text_delta" || e.Type == "reasoning_delta") && e.Text != "") && e.Type != "tool_start" {
		return
	}
	v := time.Since(t.start).Milliseconds()
	if t.first == nil {
		t.first = &v
	}
	t.last = &v
	if e.Type == "reasoning_delta" && t.reason == nil {
		t.reason = &v
	}
	if e.Type == "text_delta" && t.text == nil {
		t.text = &v
	}
}
func (t *requestTiming) snapshot() M {
	value := func(p *int64) any {
		if p == nil {
			return nil
		}
		return *p
	}
	var generation any
	if t.first != nil {
		generation = *t.last - *t.first
	}
	return M{"queue_ms": t.queue.Milliseconds(), "retry_ms": t.retry.Milliseconds(), "ttft_ms": value(t.first), "first_reasoning_ms": value(t.reason), "first_text_ms": value(t.text), "generation_ms": generation, "total_ms": time.Since(t.start).Milliseconds()}
}
func (a *App) upstreamHeaders(token, session, id string) http.Header {
	cfg := obj(a.config()["http"])
	version := str(fallback(cfg["client_version"], defaultHTTPClientVersion))
	osCategory := runtime.GOOS
	if osCategory == "darwin" {
		osCategory = "macos"
	}
	tz := os.Getenv("TZ")
	if tz == "" {
		tz = time.Now().Location().String()
		if tz == "Local" {
			_, offset := time.Now().Zone()
			if offset == 8*3600 {
				tz = "Asia/Shanghai"
			}
		}
	}
	h := http.Header{}
	for k, v := range map[string]string{"authorization": "Bearer " + token, "x-api-key": token, "content-type": "application/json", "anthropic-version": "2023-06-01", "user-agent": "ZCode/" + version, "x-zcode-app-version": version, "x-platform": platform(), "x-client-language": "zh-CN", "x-client-timezone": tz, "x-os-category": osCategory, "x-os-version": osVersion(), "x-release-channel": "production", "http-referer": "https://zcode.z.ai", "x-title": "Z Code@electron", "x-zcode-agent": "glm", "x-session-id": session, "x-request-id": id, "x-query-id": uuidv7(), "x-zcode-trace-id": uuid(), "x-zcode-session-type": "main", "x-zcode-query-source": "main_turn"} {
		h.Set(k, v)
	}
	return h
}
func providerFailure(v M, status int, retry string) *Failure {
	e := obj(v["error"])
	return &Failure{Status: status, Code: str(first(e["code"], v["code"], e["type"], "upstream_http_error")), Message: str(first(e["message"], v["msg"], v["message"], fmt.Sprintf("上游 HTTP %d", status))), RetryAfter: retryAfter(retry, time.Now())}
}

func upstreamHTTPFailure(res *http.Response, body []byte) *Failure {
	if res.StatusCode == http.StatusProxyAuthRequired {
		return &Failure{Status: res.StatusCode, Kind: "network", Code: "proxy_auth_failed", Message: "代理鉴权失败", Retryable: true}
	}
	v := obj(parse(string(body), nil))
	f := providerFailure(v, res.StatusCode, res.Header.Get("Retry-After"))
	if len(v) > 0 {
		return f
	}
	page := strings.ToLower(string(body))
	switch {
	case (res.StatusCode == 403 || res.StatusCode == 405) && strings.Contains(page, "errors.aliyun.com/") && strings.Contains(page, "block_message"):
		f.Kind, f.Code = "risk", "upstream_waf_blocked"
		f.Message = "上游安全网关拦截"
	case riskBlockMessage(page):
		f.Kind, f.Code, f.Message = "account_risk", "upstream_risk_blocked", "账号风控拒绝"
	case strings.TrimSpace(string(body)) == "":
		f.Message += "：上游返回空响应体"
	default:
		f.Message += "：上游返回非 JSON 错误响应"
	}
	return f
}

var errStopSSE = errors.New("message_stop")

func (a *App) inferHTTP(parent context.Context, account M, o inferOptions) (result *Result, failure *Failure) {
	if o.Timing == nil {
		o.Timing = newTiming()
	}
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(nil)
	params := obj(a.config()["http"])
	timers := []*time.Timer{}
	timer := func(code, label string, d time.Duration) *time.Timer {
		if d <= 0 {
			return nil
		}
		t := time.AfterFunc(d, func() { cancel(&Failure{Kind: "timeout", Code: code, Message: label}) })
		timers = append(timers, t)
		return t
	}
	timer("inference_timeout", "上游推理超时", ms(params["total_timeout_ms"]))
	firstTimer := timer("first_token_timeout", "上游首字超时", ms(params["first_token_timeout_ms"]))
	idle := timer("stream_idle_timeout", "上游流空闲超时", ms(params["idle_timeout_ms"]))
	var admission *time.Timer
	if o.Admission > 0 {
		admission = time.AfterFunc(o.Admission, func() {
			cancel(&Failure{Kind: "rate_limit", Code: "throttle_window_exhausted", Message: "限流重试超时"})
		})
		timers = append(timers, admission)
	}
	defer func() {
		for _, t := range timers {
			t.Stop()
		}
	}()
	resetIdle := func() {
		if idle != nil {
			idle.Reset(ms(params["idle_timeout_ms"]))
		}
	}
	admitted := false
	admit := func() {
		if !admitted && o.OnAdmitted != nil {
			o.OnAdmitted()
		}
		admitted = true
		if firstTimer != nil {
			firstTimer.Stop()
		}
		if admission != nil {
			admission.Stop()
		}
	}
	committed := false
	defer func() {
		if failure == nil {
			return
		}
		defer func() { failure.Message = conciseFailureMessage(failure) }()
		failure.Committed = committed
		if parent.Err() != nil {
			failure.Kind, failure.Code, failure.Message = "canceled", "canceled", "客户端已断开"
			return
		}
		if cause, ok := context.Cause(ctx).(*Failure); ok {
			failure.Kind, failure.Code, failure.Message = cause.Kind, cause.Code, cause.Message
		}
	}()
	r := &Result{Calls: []any{}, Usage: M{}}
	session := uuid()
	if o.Affinity != "" {
		session = hash(o.Affinity)[:32]
	}
	r.SessionID = session
	upstreamBody := clone(o.Upstream)
	metadata := obj(upstreamBody["metadata"])
	metadata["user_id"] = js(M{"device_id": str(account["device_mid"]), "account_uuid": "", "session_id": session})
	upstreamBody["metadata"] = metadata
	requestID := uuid()
	ctx, release, e := a.pinOutboundProxy(ctx, account)
	if e != nil {
		return nil, asProxyFailure(e)
	}
	defer release()
	defer func() { a.prepareProxyFailover(ctx, failure) }()
	ctx, finish, gateFailure := a.beginUpstreamRequest(ctx, a.upstreamScope(ctx, account, a.origin, true))
	if gateFailure != nil {
		return nil, gateFailure
	}
	defer func() { finish(failure) }()
	var verification captchaVerification
	defer func() {
		if failure != nil {
			a.rejectCaptchaVerification(verification)
		}
	}()
	client, e := a.client(ctx)
	if e != nil {
		return nil, &Failure{Kind: "invalid", Message: e.Error()}
	}
	var res *http.Response
	for verificationAttempt := 0; ; verificationAttempt++ {
		var f *Failure
		verification, f = a.captchaHeaders(ctx, account, captchaPurposeInference, verificationAttempt > 0)
		if f != nil {
			return nil, f
		}
		req, err := http.NewRequestWithContext(ctx, "POST", a.origin+"/api/v1/zcode-plan/anthropic/v1/messages", strings.NewReader(js(upstreamBody)))
		if err != nil {
			return nil, &Failure{Message: err.Error()}
		}
		token := str(a.tokens(str(account["id"]))["zcode_jwt"])
		if failure := validateCaptchaRequestToken(verification, account, token); failure != nil {
			return nil, failure
		}
		req.Header = a.upstreamHeaders(token, session, requestID)
		for key, values := range verification.Headers {
			req.Header[key] = values
		}
		res, e = client.Do(req)
		if e != nil {
			return nil, transportFailure(ctx, e)
		}
		resetIdle()
		if res.StatusCode >= 200 && res.StatusCode < 300 {
			break
		}
		b, readError := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		if readError != nil {
			if res.StatusCode == http.StatusTooManyRequests {
				return nil, &Failure{Kind: "rate_limit", Code: "rate_limit_error", Status: res.StatusCode, Message: "请求频率受限", Retryable: true, RetryAfter: retryAfter(res.Header.Get("Retry-After"), time.Now())}
			}
			return nil, transportFailure(ctx, readError)
		}
		failure := upstreamHTTPFailure(res, b)
		captchaMode := str(fallback(obj(obj(a.config()["http"])["captcha"])["mode"], "off"))
		if captchaExpired(failure) {
			failure.Kind, failure.Retryable = "risk", false
			if verification.Headers.Get("X-Aliyun-Captcha-Verify-Param") != "" {
				a.rejectCaptchaVerification(verification)
				if failure.Code == "F014" || failure.Code == "F020" {
					a.invalidateCaptchaScene(a.captchaSceneKey(ctx))
				}
			} else {
				a.prepareCaptchaChallenge(ctx)
			}
			if verificationAttempt == 0 && captchaMode != "off" {
				continue
			}
		}
		return nil, failure
	}
	defer res.Body.Close()
	if !strings.Contains(res.Header.Get("Content-Type"), "text/event-stream") {
		return nil, &Failure{Status: 502, Message: "上游未返回 SSE 响应"}
	}
	content := map[int]M{}
	buffers := map[int]*contentBuffer{}
	var text, reasoning strings.Builder
	open := map[int]bool{}
	usage := M{}
	nativePending := []Frame{}
	pendingBytes := 0
	nativeStarted, stopped := false, false
	emit := func(ev Event) error {
		o.Timing.observe(ev)
		if ((ev.Type == "text_delta" || ev.Type == "reasoning_delta") && ev.Text != "") || ev.Type == "tool_start" {
			admit()
		}
		if o.OnEvent == nil {
			return nil
		}
		if (o.Protocol == "anthropic" && ev.Type == "native_event") || (o.Protocol != "anthropic" && (ev.Type == "tool_start" || ev.Type == "tool_delta" || (ev.Type == "text_delta" || ev.Type == "reasoning_delta") && ev.Text != "")) {
			committed = true
		}
		return o.OnEvent(ev)
	}
	e = readSSE(res.Body, func(f Frame) error {
		resetIdle()
		if f.Data == "" {
			return nil
		}
		v := parse(f.Data, nil)
		event, ok := v.(map[string]any)
		if !ok {
			return providerFailure(M{"message": "上游 SSE JSON 无效"}, 502, "")
		}
		typ := str(event["type"])
		idx := int(integer(event["index"]))
		if typ == "error" {
			return providerFailure(event, 502, "")
		}
		if typ == "message_start" {
			r.Native = clone(obj(event["message"]))
			usage = clone(obj(r.Native["usage"]))
			if len(r.Native) > 0 {
				a.acceptCaptchaVerification(ctx, &verification)
			}
		}
		if typ == "content_block_start" {
			if open[idx] {
				return invalid("上游内容块重复开始")
			}
			open[idx] = true
			b := clone(obj(event["content_block"]))
			content[idx] = b
			buffer := &contentBuffer{}
			buffer.text.WriteString(str(b["text"]))
			buffer.thinking.WriteString(str(b["thinking"]))
			buffer.signature.WriteString(str(b["signature"]))
			buffers[idx] = buffer
			if str(b["type"]) == "tool_use" {
				if e := emit(Event{Type: "tool_start", Index: idx, ID: str(b["id"]), Name: str(b["name"])}); e != nil {
					return e
				}
			}
			if truth(b["text"]) {
				s := str(b["text"])
				text.WriteString(s)
				if e := emit(Event{Type: "text_delta", Text: s}); e != nil {
					return e
				}
			}
			if truth(b["thinking"]) {
				s := str(b["thinking"])
				reasoning.WriteString(s)
				if e := emit(Event{Type: "reasoning_delta", Text: s}); e != nil {
					return e
				}
			}
		}
		if typ == "content_block_delta" {
			b := content[idx]
			if b == nil || !open[idx] {
				return providerFailure(M{"message": "上游内容块缺少 start 事件"}, 502, "")
			}
			d := obj(event["delta"])
			buffer := buffers[idx]
			switch str(d["type"]) {
			case "text_delta":
				s := str(d["text"])
				buffer.text.WriteString(s)
				text.WriteString(s)
				if e := emit(Event{Type: "text_delta", Text: s}); e != nil {
					return e
				}
			case "thinking_delta":
				s := str(d["thinking"])
				buffer.thinking.WriteString(s)
				reasoning.WriteString(s)
				if e := emit(Event{Type: "reasoning_delta", Text: s}); e != nil {
					return e
				}
			case "signature_delta":
				buffer.signature.WriteString(str(d["signature"]))
			case "citations_delta":
				b["citations"] = append(arr(b["citations"]), d["citation"])
			case "input_json_delta":
				s := str(d["partial_json"])
				buffer.partial.WriteString(s)
				if e := emit(Event{Type: "tool_delta", Index: idx, Delta: s}); e != nil {
					return e
				}
			}
		}
		if typ == "content_block_stop" {
			delete(open, idx)
			b := content[idx]
			buffer := buffers[idx]
			if buffer != nil {
				for field, value := range map[string]*strings.Builder{"text": &buffer.text, "thinking": &buffer.thinking, "signature": &buffer.signature} {
					if value.Len() > 0 || b[field] != nil {
						b[field] = value.String()
					}
				}
			}
			if str(b["type"]) == "tool_use" {
				partial := buffer.partial.String()
				if partial == "" {
					if e := emit(Event{Type: "tool_delta", Index: idx, Delta: js(first(b["input"], M{}))}); e != nil {
						return e
					}
				} else {
					input := parse(partial, nil)
					if input == nil {
						return providerFailure(M{"message": "上游工具参数 JSON 不完整"}, 502, "")
					}
					b["input"] = input
				}
				arguments := first(b["input"], M{})
				if o.Tools != nil {
					if e := o.Tools.validateCall(ctx, str(b["name"]), arguments); e != nil {
						return e
					}
				}
				r.Calls = append(r.Calls, M{"id": b["id"], "name": b["name"], "arguments": arguments})
				if e := emit(Event{Type: "tool_end", Index: idx}); e != nil {
					return e
				}
			}
		}
		if typ == "message_delta" {
			if r.Native == nil {
				return providerFailure(M{"message": "上游缺少 message_start"}, 502, "")
			}
			maps.Copy(r.Native, obj(event["delta"]))
			maps.Copy(usage, obj(event["usage"]))
		}
		if typ == "message_stop" {
			if len(open) > 0 {
				return providerFailure(M{"message": "上游事件流提前结束，内容块未关闭"}, 502, "")
			}
			stopped = true
			admit()
		}
		if o.Stream && o.Protocol == "anthropic" {
			b, d := obj(event["content_block"]), obj(event["delta"])
			visible := stopped || str(b["type"]) == "tool_use" || truth(b["text"]) || truth(b["thinking"]) || truth(d["text"]) || truth(d["thinking"])
			nativeStarted = nativeStarted || visible
			nativePending = append(nativePending, f)
			pendingBytes += len(f.Raw)
			if !nativeStarted && pendingBytes > 1<<20 {
				return providerFailure(M{"message": "上游 SSE 前导事件超过 1MB"}, 502, "")
			}
			if nativeStarted {
				for _, p := range nativePending {
					if e := emit(Event{Type: "native_event", Frame: p}); e != nil {
						return e
					}
				}
				nativePending = nil
				pendingBytes = 0
			}
		}
		if stopped {
			return errStopSSE
		}
		return nil
	})
	if e != nil && e != errStopSSE {
		if f, ok := e.(*Failure); ok {
			return nil, f
		}
		return nil, &Failure{Kind: "network", Code: "upstream_stream_error", Message: e.Error(), Retryable: true}
	}
	if !stopped || r.Native == nil {
		return nil, &Failure{Status: 502, Code: "truncated_stream", Kind: "server", Message: "上游事件流提前结束，缺少 message_stop", Retryable: true}
	}
	indices := []int{}
	for i := range content {
		indices = append(indices, i)
	}
	sort.Ints(indices)
	blocks := []any{}
	for _, i := range indices {
		blocks = append(blocks, content[i])
	}
	r.Native["content"], r.Native["usage"] = blocks, usage
	r.Text, r.Reasoning = text.String(), reasoning.String()
	r.FinishReason = str(r.Native["stop_reason"])
	r.Usage = normalizeUsage(merge(usage, M{"input_tokens": num(usage["input_tokens"]) + num(usage["cache_read_input_tokens"]) + num(usage["cache_creation_input_tokens"])}))
	return r, nil
}
