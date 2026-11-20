package server

import (
	"context"
	"errors"
	"io"
	"maps"
	"net/http"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	accountdata "zcode2api/internal/server/account"
	captchasdk "zcode2api/internal/server/captcha"
	settings "zcode2api/internal/server/config"
	"zcode2api/internal/server/fault"
	childproc "zcode2api/internal/server/process"
	wire "zcode2api/internal/server/protocol"
	routing "zcode2api/internal/server/proxy"
	"zcode2api/internal/server/shared"
)

type contentBuffer struct{ text, thinking, signature, partial strings.Builder }

type inferenceSource struct {
	SessionType string
	QuerySource string
	Template    wire.SystemTemplate
}

type inferOptions struct {
	Key, Body, Upstream                      shared.M
	Tools                                    *wire.ToolCatalog
	Source                                   inferenceSource
	Protocol, Model, Level, ID, IP, Affinity string
	Sticky                                   float64
	Stream                                   bool
	OnEvent                                  func(wire.Event) error
	OnAdmitted                               func()
	OnAttempt                                func()
	Timing                                   *requestTiming
	Admission                                time.Duration
	RecheckAt                                time.Time
	Recovery                                 *poolRecoveryLease
}

type requestTiming struct {
	start                     time.Time
	queue, retry              time.Duration
	first, reason, text, last *int64
}

func newTiming() *requestTiming { return &requestTiming{start: time.Now()} }

func (t *requestTiming) observe(e wire.Event) {
	if !e.HasText() && e.Type != "tool_start" {
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

func (t *requestTiming) snapshot() shared.M {
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
	return shared.M{"queue_ms": t.queue.Milliseconds(), "retry_ms": t.retry.Milliseconds(), "ttft_ms": value(t.first), "first_reasoning_ms": value(t.reason), "first_text_ms": value(t.text), "generation_ms": generation, "total_ms": time.Since(t.start).Milliseconds()}
}

func (a *App) upstreamHeaders(token, session, id string, source inferenceSource) http.Header {
	// ZCode 3.17.2 sends the desktop app version, not the inference API version.
	version := shared.Str(shared.Fallback(a.configValue("app_version"), settings.DefaultHTTPClientVersion))
	clientPlatform := platform()
	osCategory := runtime.GOOS
	if osCategory == "darwin" {
		osCategory = "macos"
	}
	clientOSVersion := childproc.OSVersion()
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
	for k, v := range map[string]string{"authorization": "Bearer " + token, "x-api-key": token, "content-type": "application/json", "anthropic-version": "2023-06-01", "user-agent": "ZCode/" + version, "x-zcode-app-version": version, "x-platform": clientPlatform, "x-client-language": "zh-CN", "x-client-timezone": tz, "x-os-category": osCategory, "x-os-version": clientOSVersion, "x-release-channel": "production", "http-referer": "https://zcode.z.ai", "x-title": "Z Code@electron", "x-zcode-agent": "glm", "x-session-id": session, "x-request-id": id, "x-query-id": shared.UUIDv7(), "x-zcode-trace-id": shared.UUID(), "x-zcode-session-type": source.SessionType} {
		h.Set(k, v)
	}
	if source.QuerySource != "" {
		h.Set("X-Zcode-Query-Source", source.QuerySource)
	}
	return h
}

func requestMetadataUserID(deviceMid, session string) string {
	return shared.JSON(shared.M{
		"device_id":    deviceMid,
		"account_uuid": "",
		"session_id":   session,
	})
}

var errStopSSE = errors.New("message_stop")

func watchStreamIdle(timeout time.Duration, cancel context.CancelCauseFunc) (func(), func()) {
	if timeout <= 0 {
		noop := func() {}
		return noop, noop
	}
	var mu sync.Mutex
	var timer *time.Timer
	deadline := time.Now().Add(timeout)
	mu.Lock()
	timer = time.AfterFunc(timeout, func() {
		mu.Lock()
		defer mu.Unlock()
		if deadline.IsZero() {
			return
		}
		// A timer callback can run after fresh output has moved the deadline.
		if remaining := time.Until(deadline); remaining > 0 {
			timer.Reset(remaining)
			return
		}
		deadline = time.Time{}
		cancel(&fault.Failure{Kind: "timeout", Code: "stream_idle_timeout", Message: "上游长时间未返回有效内容"})
	})
	mu.Unlock()
	progress := func() {
		mu.Lock()
		if now := time.Now(); now.Before(deadline) {
			deadline = now.Add(timeout)
		}
		mu.Unlock()
	}
	stop := func() {
		mu.Lock()
		deadline = time.Time{}
		timer.Stop()
		mu.Unlock()
	}
	return progress, stop
}

func (a *App) inferHTTP(parent context.Context, account shared.M, o inferOptions) (result *wire.Result, failure *fault.Failure) {
	if o.Timing == nil {
		o.Timing = newTiming()
	}
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(nil)
	params := shared.Obj(a.configValue("http"))
	timers := []*time.Timer{}
	timer := func(code, label string, d time.Duration) *time.Timer {
		if d <= 0 {
			return nil
		}
		t := time.AfterFunc(d, func() { cancel(&fault.Failure{Kind: "timeout", Code: code, Message: label}) })
		timers = append(timers, t)
		return t
	}
	timer("inference_timeout", "上游推理超时", shared.Ms(params["total_timeout_ms"]))
	firstTimer := timer("first_token_timeout", "上游首字超时", shared.Ms(params["first_token_timeout_ms"]))
	refreshIdle, stopIdle := watchStreamIdle(shared.Ms(params["idle_timeout_ms"]), cancel)
	defer stopIdle()
	var admission *time.Timer
	if o.Admission > 0 {
		admission = time.AfterFunc(o.Admission, func() {
			cancel(&fault.Failure{Kind: "timeout", Code: "retry_budget_exhausted", Message: "故障重试超时"})
		})
		timers = append(timers, admission)
	}
	defer func() {
		for _, t := range timers {
			t.Stop()
		}
	}()
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
	var res *http.Response
	upstreamAttempted := false
	requestDetail := shared.M{}
	var requestSecrets []string
	defer func() {
		if failure == nil {
			return
		}
		failure.Detail = fault.FailureDetail(failure)
		for key, value := range requestDetail {
			failure.Detail[key] = value
		}
		if !failure.LocalOnly && !shared.Truth(failure.Detail["local_cooldown"]) {
			failure.Detail["upstream_attempted"] = upstreamAttempted
			failure.Detail["upstream_response_received"] = res != nil
		}
		if res != nil {
			failure.Detail["http_status"] = res.StatusCode
			failure.Detail["response_headers"] = fault.FailureResponseHeaders(res.Header)
		}
		// Upstream errors occasionally echo a credential in free-form text.
		for _, key := range []string{"message", "body"} {
			if value, ok := failure.Detail[key].(string); ok {
				for _, secret := range requestSecrets {
					if secret != "" {
						value = strings.ReplaceAll(value, secret, "[REDACTED]")
					}
				}
				failure.Detail[key] = value
			}
		}
		defer func() { failure.Message = fault.ConciseFailureMessage(failure) }()
		failure.Committed = committed
		if parent.Err() != nil {
			failure.Kind, failure.Code, failure.Message = "canceled", "canceled", "客户端已断开"
			return
		}
		if cause, ok := context.Cause(ctx).(*fault.Failure); ok {
			failure.Kind, failure.Code, failure.Message = cause.Kind, cause.Code, cause.Message
			failure.Retryable = true
			failure.Detail["kind"], failure.Detail["code"], failure.Detail["message"] = cause.Kind, cause.Code, cause.Message
		}
	}()
	r := &wire.Result{Calls: []any{}, Usage: shared.M{}}
	session := shared.UUID()
	if o.Affinity != "" {
		session = shared.Hash(o.Affinity)[:32]
	}
	metadata := maps.Clone(shared.Obj(o.Upstream["metadata"]))
	metadata["user_id"] = requestMetadataUserID(
		shared.Str(account["device_mid"]),
		session,
	)
	upstreamBody := shared.Merge(o.Upstream, shared.M{"metadata": metadata})
	requestID := shared.UUID()
	ctx, release, e := a.pinOutboundProxy(ctx, account)
	if e != nil {
		return nil, routing.AsProxyFailure(e)
	}
	defer release()
	scope := a.upstreamScope(ctx, account, a.origin, true)
	scope.inference = true
	ctx, finish, gateFailure := a.beginUpstreamRequest(ctx, scope)
	if gateFailure != nil {
		return nil, gateFailure
	}
	defer func() { a.prepareProxyFailover(ctx, failure) }()
	defer func() { finish(failure) }()
	var verification captchaVerification
	defer func() {
		if failure != nil {
			a.rejectCaptchaVerification(verification)
		}
	}()
	client, e := a.client(ctx)
	if e != nil {
		return nil, &fault.Failure{Kind: "invalid", Message: e.Error()}
	}
	for verificationAttempt := 0; ; verificationAttempt++ {
		var f *fault.Failure
		verification, f = a.captchaHeaders(ctx, account, captchasdk.CaptchaPurposeInference, verificationAttempt > 0)
		if f != nil {
			return nil, f
		}
		req, err := http.NewRequestWithContext(ctx, "POST", a.origin+"/api/v1/zcode-plan/anthropic/v1/messages", strings.NewReader(shared.JSON(upstreamBody)))
		if err != nil {
			return nil, &fault.Failure{Message: err.Error()}
		}
		tokens := a.tokens(shared.Str(account["id"]))
		if accountdata.LoginJWTExpired(tokens) {
			return nil, &fault.Failure{Kind: "auth", Code: "login_expired", Status: 401, Message: "ZCode 登录已过期，请重新授权"}
		}
		token := tokens["zcode_jwt"]
		if failure := validateCaptchaRequestToken(verification, account, token); failure != nil {
			return nil, failure
		}
		req.Header = a.upstreamHeaders(token, session, requestID, o.Source)
		// Keep the source header and metadata tied to the account's existing
		// device identity, as the official 3.18.2 client does.
		if device := shared.Str(account["device_mid"]); device != "" {
			req.Header.Set("X-Device-Mid", device)
		}
		for key, values := range verification.Headers {
			req.Header[key] = values
		}
		requestDetail = shared.M{"request_id": req.Header.Get("X-Request-Id"), "trace_id": req.Header.Get("X-Zcode-Trace-Id"), "query_id": req.Header.Get("X-Query-Id"), "session_id": session, "session_type": o.Source.SessionType, "query_source": o.Source.QuerySource, "template_mode": o.Source.Template.Mode}
		requestSecrets = append(requestSecrets, token, req.Header.Get("X-Aliyun-Captcha-Verify-Param"))
		r.TraceID = req.Header.Get("X-Zcode-Trace-Id")
		if o.OnAttempt != nil {
			o.OnAttempt()
		}
		upstreamAttempted = true
		res, e = client.Do(req)
		if e != nil {
			return nil, routing.TransportFailure(ctx, e)
		}
		refreshIdle()
		if res.StatusCode >= 200 && res.StatusCode < 300 {
			// Accepted requests use the first-token timeout. Error responses and
			// verification retries must still fit within the admission budget.
			if admission != nil {
				admission.Stop()
			}
			break
		}
		b, readError := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		if readError != nil {
			failure := routing.TransportFailure(ctx, readError)
			failure.Detail = fault.FailureDetail(failure)
			failure.Detail["body"] = fault.FailureBody(string(b))
			if res.StatusCode == http.StatusTooManyRequests {
				failure.Kind, failure.Code, failure.Status, failure.Message = "rate_limit", "rate_limit_error", res.StatusCode, "请求频率受限"
				failure.RetryAfter = fault.RetryAfter(res.Header.Get("Retry-After"), time.Now())
			}
			return nil, failure
		}
		failure := fault.UpstreamHTTPFailure(res, b)
		captchaMode := shared.Str(shared.Fallback(shared.Obj(a.configValue("http", "captcha"))["mode"], "off"))
		if captchasdk.CaptchaExpired(failure) {
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
	r.HTTPStatus = res.StatusCode
	defer res.Body.Close()
	if !strings.Contains(res.Header.Get("Content-Type"), "text/event-stream") {
		body, readError := io.ReadAll(io.LimitReader(res.Body, fault.FailureBodyLimit+1))
		if len(body) == 0 && readError == nil {
			return nil, &fault.Failure{Status: http.StatusBadGateway, Code: "upstream_empty_response", Message: "上游结束响应，但未返回任何内容"}
		}
		if readError == nil {
			payload := shared.Obj(shared.Parse(string(body), nil))
			code := payload["code"]
			if len(shared.Obj(payload["error"])) > 0 || shared.Str(payload["type"]) == "error" || code != nil && !shared.CodeIs(code, 0) && !shared.CodeIs(code, 200) {
				// A successful HTTP status can still carry a business refusal.
				return nil, fault.UpstreamHTTPFailure(res, body)
			}
		}
		detail := shared.M{"body": fault.FailureBody(string(body))}
		if readError != nil {
			detail["read_error"] = fault.BoundedFailureText(readError.Error())
		}
		return nil, &fault.Failure{Status: 502, Code: "upstream_invalid_content_type", Message: "上游未返回 SSE 响应", Detail: detail}
	}
	content := map[int]shared.M{}
	buffers := map[int]*contentBuffer{}
	var text, reasoning strings.Builder
	open := map[int]bool{}
	usage := shared.M{}
	nativePending := []wire.Frame{}
	pendingBytes := 0
	nativeStarted, stopped := false, false
	hasOutput := false
	emit := func(ev wire.Event) error {
		o.Timing.observe(ev)
		visibleOutput := ev.HasText() || ev.Type == "tool_start"
		if visibleOutput || ev.Type == "tool_delta" && ev.Delta != "" || ev.Type == "tool_end" {
			refreshIdle()
		}
		if visibleOutput {
			admit()
		}
		if o.OnEvent == nil {
			return nil
		}
		if ev.StartsStream(o.Protocol) {
			committed = true
		}
		return o.OnEvent(ev)
	}
	e = wire.ReadSSE(res.Body, func(f wire.Frame) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if f.Data == "" {
			return nil
		}
		v := shared.Parse(f.Data, nil)
		event, ok := v.(map[string]any)
		if !ok {
			return &fault.Failure{Status: 502, Code: "upstream_invalid_sse_json", Message: "上游 SSE JSON 无效", Detail: shared.M{"body": fault.FailureBody(f.Data)}}
		}
		typ := shared.Str(event["type"])
		idx := int(shared.Integer(event["index"]))
		if typ == "error" {
			failure := fault.ProviderFailure(event, 502, "")
			failure.Detail["body"] = fault.FailureBody(f.Data)
			return failure
		}
		if typ == "message_start" {
			r.Native = shared.Clone(shared.Obj(event["message"]))
			usage = shared.Clone(shared.Obj(r.Native["usage"]))
			if len(r.Native) > 0 {
				a.acceptCaptchaVerification(ctx, &verification)
			}
		}
		if typ == "content_block_start" {
			if open[idx] {
				return fault.Invalid("上游内容块重复开始")
			}
			open[idx] = true
			b := shared.Clone(shared.Obj(event["content_block"]))
			content[idx] = b
			buffer := &contentBuffer{}
			buffer.text.WriteString(shared.Str(b["text"]))
			buffer.thinking.WriteString(shared.Str(b["thinking"]))
			buffer.signature.WriteString(shared.Str(b["signature"]))
			buffers[idx] = buffer
			if shared.Str(b["type"]) == "tool_use" {
				if e := emit(wire.Event{Type: "tool_start", Index: idx, ID: shared.Str(b["id"]), Name: shared.Str(b["name"])}); e != nil {
					return e
				}
			}
			if shared.Truth(b["text"]) {
				s := shared.Str(b["text"])
				text.WriteString(s)
				if e := emit(wire.Event{Type: "text_delta", Text: s}); e != nil {
					return e
				}
			}
			if shared.Truth(b["thinking"]) {
				s := shared.Str(b["thinking"])
				reasoning.WriteString(s)
				if e := emit(wire.Event{Type: "reasoning_delta", Text: s}); e != nil {
					return e
				}
			}
		}
		if typ == "content_block_delta" {
			b := content[idx]
			if b == nil || !open[idx] {
				return fault.ProviderFailure(shared.M{"message": "上游内容块缺少 start 事件"}, 502, "")
			}
			d := shared.Obj(event["delta"])
			buffer := buffers[idx]
			switch shared.Str(d["type"]) {
			case "text_delta":
				s := shared.Str(d["text"])
				buffer.text.WriteString(s)
				text.WriteString(s)
				if e := emit(wire.Event{Type: "text_delta", Text: s}); e != nil {
					return e
				}
			case "thinking_delta":
				s := shared.Str(d["thinking"])
				buffer.thinking.WriteString(s)
				reasoning.WriteString(s)
				if e := emit(wire.Event{Type: "reasoning_delta", Text: s}); e != nil {
					return e
				}
			case "signature_delta":
				buffer.signature.WriteString(shared.Str(d["signature"]))
			case "citations_delta":
				b["citations"] = append(shared.Arr(b["citations"]), d["citation"])
			case "input_json_delta":
				s := shared.Str(d["partial_json"])
				buffer.partial.WriteString(s)
				if e := emit(wire.Event{Type: "tool_delta", Index: idx, Delta: s}); e != nil {
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
			if shared.Str(b["type"]) == "tool_use" {
				partial := buffer.partial.String()
				if partial == "" {
					if e := emit(wire.Event{Type: "tool_delta", Index: idx, Delta: shared.JSON(shared.First(b["input"], shared.M{}))}); e != nil {
						return e
					}
				} else {
					input := shared.Parse(partial, nil)
					if input == nil {
						return fault.ProviderFailure(shared.M{"message": "上游工具参数 JSON 不完整"}, 502, "")
					}
					b["input"] = input
				}
				arguments := shared.First(b["input"], shared.M{})
				if o.Tools != nil {
					if e := o.Tools.ValidateCall(ctx, shared.Str(b["name"]), arguments); e != nil {
						return e
					}
				}
				r.Calls = append(r.Calls, shared.M{"id": b["id"], "name": b["name"], "arguments": arguments})
				if e := emit(wire.Event{Type: "tool_end", Index: idx}); e != nil {
					return e
				}
			}
		}
		if typ == "message_delta" {
			if r.Native == nil {
				return fault.ProviderFailure(shared.M{"message": "上游缺少 message_start"}, 502, "")
			}
			maps.Copy(r.Native, shared.Obj(event["delta"]))
			maps.Copy(usage, shared.Obj(event["usage"]))
		}
		if typ == "message_stop" {
			if r.Native == nil {
				return &fault.Failure{Status: http.StatusBadGateway, Code: "truncated_stream", Message: "上游事件流缺少 message_start"}
			}
			if len(open) > 0 {
				return fault.ProviderFailure(shared.M{"message": "上游事件流提前结束，内容块未关闭"}, 502, "")
			}
			hasOutput = text.Len() > 0 || reasoning.Len() > 0 || len(r.Calls) > 0
			if !hasOutput && o.Protocol == "anthropic" {
				for _, block := range content {
					switch shared.Str(block["type"]) {
					case "", "text", "thinking", "tool_use":
						continue
					}
					if len(block) > 1 {
						hasOutput = true
						break
					}
				}
			}
			if !hasOutput {
				switch reason := shared.Str(r.Native["stop_reason"]); reason {
				case "max_tokens", "stop_sequence", "refusal", "pause_turn":
					// An explicit stop can legitimately end a turn without content.
				default:
					return &fault.Failure{Status: http.StatusBadGateway, Code: "upstream_empty_response", Message: "上游结束响应，但未返回任何内容", Detail: shared.M{"stop_reason": reason}}
				}
			}
			stopped = true
			admit()
		}
		if o.Stream && o.Protocol == "anthropic" {
			b, d := shared.Obj(event["content_block"]), shared.Obj(event["delta"])
			visible := stopped || shared.Str(b["type"]) == "tool_use" || shared.Truth(b["text"]) || shared.Truth(b["thinking"]) || shared.Truth(d["text"]) || shared.Truth(d["thinking"])
			nativeStarted = nativeStarted || visible
			nativePending = append(nativePending, f)
			pendingBytes += len(f.Raw)
			if !nativeStarted && pendingBytes > 1<<20 {
				return fault.ProviderFailure(shared.M{"message": "上游 SSE 前导事件超过 1MB"}, 502, "")
			}
			if nativeStarted {
				for _, p := range nativePending {
					if e := emit(wire.Event{Type: "native_event", Frame: p}); e != nil {
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
		if f, ok := e.(*fault.Failure); ok {
			return nil, f
		}
		return nil, &fault.Failure{Kind: "network", Code: "upstream_stream_error", Message: e.Error(), Retryable: true}
	}
	if !stopped {
		return nil, &fault.Failure{Status: 502, Code: "truncated_stream", Kind: "server", Message: "上游事件流提前结束，缺少 message_stop", Retryable: true}
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
	r.FinishReason = shared.Str(r.Native["stop_reason"])
	inputUsage := shared.First(usage["inputTokens"], usage["input_tokens"], usage["prompt_tokens"])
	outputUsage := shared.First(usage["outputTokens"], usage["output_tokens"], usage["completion_tokens"])
	if usage["input_tokens"] != nil || inputUsage == nil {
		usage = shared.Merge(usage, shared.M{"input_tokens": shared.Num(usage["input_tokens"]) + shared.Num(usage["cache_read_input_tokens"]) + shared.Num(usage["cache_creation_input_tokens"])})
	}
	r.Usage = wire.NormalizeUsage(usage)
	if hasOutput {
		switch {
		case inputUsage == nil || outputUsage == nil:
			r.UsageWarning = "upstream_usage_missing"
		case shared.Num(r.Usage["outputTokens"]) == 0:
			r.UsageWarning = "upstream_usage_zero"
		}
	}
	return r, nil
}
