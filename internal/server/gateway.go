package server

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"
)

func (a *App) GatewayHandler() http.Handler {
	return a.guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "authorization, content-type, x-api-key, anthropic-version, anthropic-beta, x-session-id, x-zcode-instruction-mode")
		w.Header().Set("Access-Control-Expose-Headers", "X-Zcode-Instruction-Mode, X-Zcode-Compatibility, X-Zcode-Grammar-Validation")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, PUT, DELETE, OPTIONS")
		if r.Method == "OPTIONS" {
			w.WriteHeader(204)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/v1/v1/") || r.URL.Path == "/v1/v1" {
			r.URL.Path = strings.TrimPrefix(r.URL.Path, "/v1")
		}
		if a.health(w, r, "gateway") {
			return
		}
		protocol := "chat"
		if strings.HasPrefix(r.URL.Path, "/v1/messages") {
			protocol = "anthropic"
		} else if strings.HasPrefix(r.URL.Path, "/v1/responses") {
			protocol = "responses"
		}
		k, f := a.authenticate(r)
		if f != nil {
			a.sendFailure(w, f, protocol)
			return
		}
		path := r.URL.Path
		switch {
		case r.Method == "GET" && (path == "/v1/models" || strings.HasPrefix(path, "/v1/models/")):
			models := a.models(k)
			if path != "/v1/models" {
				id := strings.TrimPrefix(path, "/v1/models/")
				for _, v := range models {
					if strings.EqualFold(a.resolveModel(str(obj(v)["id"])), a.resolveModel(id)) {
						w.Header().Set("X-Upstream-Model", str(obj(v)["id"]))
						writeJSON(w, 200, v)
						return
					}
				}
				writeJSON(w, 404, M{"error": M{"type": "not_found_error", "code": "model_not_found", "message": "模型不存在或当前密钥不可访问"}})
				return
			}
			var firstID, lastID any
			if len(models) > 0 {
				firstID = obj(models[0])["id"]
				lastID = obj(models[len(models)-1])["id"]
			}
			writeJSON(w, 200, M{"object": "list", "data": models, "has_more": false, "first_id": firstID, "last_id": lastID})
			return
		case r.Method == "GET" && path == "/v1/usage":
			var remain any
			if k["quota_limit"] != nil {
				remain = math.Max(0, num(k["quota_limit"])-num(k["used_units"]))
			}
			writeJSON(w, 200, M{"key": M{"id": k["id"], "name": k["name"], "created_at": k["created_at"]}, "used_units": k["used_units"], "quota_limit": k["quota_limit"], "remaining": remain, "expires_at": k["expires_at"], "last_used_at": k["last_used_at"], "server_time": nowMS()})
			return
		case strings.HasPrefix(path, "/v1/responses/") && (r.Method == "GET" || r.Method == "DELETE"):
			id := strings.TrimPrefix(path, "/v1/responses/")
			a.mu.Lock()
			entry, ok := a.responses[id]
			ok = ok && entry.Key == str(k["id"])
			if r.Method == "DELETE" {
				if ok {
					a.forgetResponse(id)
				}
				a.mu.Unlock()
				writeJSON(w, 200, M{"id": id, "object": "response", "deleted": ok})
				return
			}
			if ok {
				a.touchResponse(id)
			}
			a.mu.Unlock()
			if !ok {
				writeJSON(w, 404, M{"error": M{"type": "invalid_request_error", "code": "response_not_found", "message": "响应不存在（仅保留最近 100 条，重启后失效）"}})
			} else {
				writeJSON(w, 200, entry.Response)
			}
			return
		case r.Method == "POST" && (path == "/v1/chat/completions" || path == "/v1/messages" || path == "/v1/responses" || path == "/v1/messages/count_tokens"):
			body, e := readBody(w, r)
			if e != nil {
				a.sendFailure(w, e.(*Failure), protocol)
				return
			}
			if path == "/v1/messages/count_tokens" {
				writeJSON(w, 200, M{"input_tokens": countTokens(body)})
				return
			}
			model := a.resolveModel(str(body["model"]))
			w.Header().Set("X-Upstream-Model", model)
			if modelSuffix.MatchString(str(body["model"])) {
				w.Header().Set("X-Model-Context-Hint", "not-enforced")
			}
			if !a.modelAllowed(k, model) {
				a.sendFailure(w, &Failure{Kind: "permission", Code: "model_not_allowed", Message: "模型不在该密钥白名单"}, protocol)
				return
			}
			a.handleInference(w, r, body, k, protocol, model)
			return
		}
		writeJSON(w, 404, M{"error": M{"type": "invalid_request_error", "code": "not_found", "message": "未知路径 " + r.Method + " " + path}})
	}))
}
func (a *App) models(k M) []any {
	ids := map[string]string{}
	accounts := a.rows("SELECT capabilities FROM accounts WHERE status='active'")
	for _, v := range accounts {
		for _, id := range arr(parse(obj(v)["capabilities"], nil)) {
			ids[lower(id)] = str(id)
		}
	}
	aliases := obj(a.configValue("aliases"))
	if len(accounts) > 0 && len(ids) == 0 {
		for _, id := range aliases {
			ids[lower(id)] = str(id)
		}
	}
	names := []string{}
	for _, id := range ids {
		if a.modelAllowed(k, a.resolveModel(id)) {
			names = append(names, id)
		}
	}
	sort.Strings(names)
	out := []any{}
	for _, id := range names {
		matches := []any{}
		for alias, target := range aliases {
			if strings.EqualFold(str(target), id) {
				matches = append(matches, alias)
			}
		}
		sort.Slice(matches, func(i, j int) bool { return str(matches[i]) < str(matches[j]) })
		out = append(out, M{"id": id, "object": "model", "type": "model", "created": 0, "created_at": "1970-01-01T00:00:00Z", "display_name": id, "owned_by": "zcode-start-plan", "aliases": matches})
	}
	return out
}
func countTokens(b M) int {
	n := 0
	count := func(v any) {
		if s, ok := v.(string); ok {
			n += utf8.RuneCountInString(s)
		} else {
			for _, v := range arr(v) {
				c := obj(v)
				switch str(c["type"]) {
				case "text":
					n += utf8.RuneCountInString(str(c["text"]))
				case "tool_result":
					n += utf8.RuneCountInString(js(c["content"]))
				case "tool_use":
					n += utf8.RuneCountInString(js(c["input"]))
				}
			}
		}
	}
	count(b["system"])
	for _, m := range arr(b["messages"]) {
		count(obj(m)["content"])
	}
	return max(1, int(math.Ceil(float64(n)/2.5)))
}
func (a *App) sendFailure(w http.ResponseWriter, f *Failure, protocol string) {
	status, shape := errorShape(f, protocol)
	if f.RetryAfter > 0 || status == 429 || status == 503 || status == 529 {
		d := f.RetryAfter
		if d <= 0 {
			d = busyWait(a.retryParams())
		}
		w.Header().Set("Retry-After", fmt.Sprint(max(1, int(math.Ceil(d.Seconds())))))
	}
	w.Header().Set("X-Retry-Attempts", fmt.Sprint(f.Attempts))
	body := M{"error": shape}
	if protocol == "anthropic" {
		body["type"] = "error"
	}
	writeJSON(w, status, body)
}

// Claude Code composes user_id as user_<id>_session_<sid>_account_<acct>, so
// the session id must be extracted from the middle of the string, not its end.
// The quantifier stays lazy so the capture cannot swallow a following segment.
var sessionPattern = regexp.MustCompile(`(?:^|_)session_([a-zA-Z0-9-]{8,128}?)(?:_|$)`)

func (a *App) affinity(r *http.Request, b, k M) (string, float64) {
	session := r.Header.Get("X-Session-Id")
	if session == "" {
		u := str(obj(b["metadata"])["user_id"])
		m := obj(parse(u, nil))
		session = str(m["session_id"])
		if session == "" {
			if p := sessionPattern.FindStringSubmatch(u); len(p) > 1 {
				session = p[1]
			}
		}
	}
	if session != "" && len(session) <= 1024 {
		return str(k["id"]) + ":" + hash(session)[:24], math.Max(0, num(a.configValue("conversation_affinity_minutes")))
	}
	return str(k["id"]), num(k["sticky_minutes"])
}
func inputItems(v any) []any {
	if s, ok := v.(string); ok {
		return []any{M{"role": "user", "content": s}}
	}
	return arr(v)
}
func (a *App) handleInference(w http.ResponseWriter, r *http.Request, body, k M, protocol, model string) {
	payload := clone(body)
	var inheritedAffinity string
	if protocol == "responses" && truth(body["previous_response_id"]) {
		a.mu.Lock()
		entry, ok := a.responses[str(body["previous_response_id"])]
		a.mu.Unlock()
		if !ok || entry.Key != str(k["id"]) {
			writeJSON(w, 404, M{"error": M{"type": "invalid_request_error", "code": "response_not_found", "message": "previous_response_id 不存在或不属于当前密钥"}})
			return
		}
		inheritedAffinity = entry.Affinity
		items := append([]any{}, entry.Input...)
		items = append(items, arr(entry.Response["output"])...)
		payload["input"] = append(items, inputItems(body["input"])...)
	}
	level := a.effectiveEffort(body, k)
	mode := str(fallback(r.Header.Get("X-Zcode-Instruction-Mode"), payload["instruction_mode"]))
	instructions, e := newInstructionContext(mode)
	if e != nil {
		a.sendFailure(w, e.(*Failure), protocol)
		return
	}
	tools, e := newToolCatalog(r.Context(), a.root, payload["tools"], protocol)
	if e != nil {
		a.sendFailure(w, e.(*Failure), protocol)
		return
	}
	upstream, e := buildRequest(payload, protocol, model, level, tools, instructions)
	if e != nil {
		a.sendFailure(w, e.(*Failure), protocol)
		return
	}
	if protocol != "anthropic" && instructions.Mode == "history" {
		w.Header().Set("X-Zcode-Instruction-Mode", "history")
	} else if instructions.Used {
		w.Header().Set("X-Zcode-Instruction-Mode", "scoped-system")
	}
	if instructions.Used {
		w.Header().Set("X-Zcode-Compatibility", "instruction-roles-and-scope-approximated")
	}
	for _, definition := range tools.byName {
		if definition.Grammar != nil {
			w.Header().Set("X-Zcode-Grammar-Validation", "llguidance-post-generation")
			break
		}
	}
	prefix := "chatcmpl-"
	if protocol == "responses" {
		prefix = "resp_"
	}
	if protocol == "anthropic" {
		prefix = "msg_"
	}
	id := prefix + uuid()
	affinity, sticky := a.affinity(r, body, k)
	if protocol == "responses" && affinity == str(k["id"]) {
		// The bare key means no explicit session was recognized by affinity().
		affinity = inheritedAffinity
		if affinity == "" {
			affinity = str(k["id"]) + ":responses:" + hash(id)[:24]
		}
		sticky = math.Max(0, num(a.configValue("conversation_affinity_minutes")))
	}
	o := inferOptions{Key: k, Body: payload, Upstream: upstream, Tools: tools, Protocol: protocol, Model: model, Level: level, ID: id, IP: a.ip(r), Affinity: affinity, Sticky: sticky, Stream: truth(body["stream"]), Timing: newTiming()}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	stop := context.AfterFunc(a.ctx, cancel)
	defer stop()
	w.Header().Set("X-Inference-Driver", "http")
	var stream *protocolStream
	if o.Stream {
		stream = newStream(w, r, protocol, id, model, truth(obj(body["stream_options"])["include_usage"]), tools)
		defer stream.end()
		o.OnEvent = stream.onEvent
		stopStream := context.AfterFunc(stream.ctx, cancel)
		defer stopStream()
	}
	result, f := a.infer(ctx, o)
	if f != nil {
		if stream != nil {
			stream.fail(f)
		} else {
			a.sendFailure(w, f, protocol)
		}
		return
	}
	response, e := buildResponse(protocol, result, model, id, tools)
	if e != nil {
		if stream != nil {
			stream.fail(e.(*Failure))
		} else {
			a.sendFailure(w, e.(*Failure), protocol)
		}
		return
	}
	if stream != nil {
		response = stream.complete(result, response)
	}
	if protocol == "responses" && body["store"] != false {
		a.rememberResponse(str(k["id"]), affinity, inputItems(payload["input"]), response)
	}
	if stream == nil {
		w.Header().Set("X-Retry-Attempts", fmt.Sprint(result.Attempts))
		writeJSON(w, 200, response)
	}
}
func (a *App) forgetResponse(id string) {
	if e, ok := a.responses[id]; ok {
		a.responseBytes -= e.Bytes
		delete(a.responses, id)
	}
	if i := slices.Index(a.responseOrder, id); i >= 0 {
		a.responseOrder = slices.Delete(a.responseOrder, i, i+1)
	}
}
func (a *App) touchResponse(id string) {
	if i := slices.Index(a.responseOrder, id); i >= 0 {
		a.responseOrder = slices.Delete(a.responseOrder, i, i+1)
	}
	a.responseOrder = append(a.responseOrder, id)
}
func (a *App) rememberResponse(key, affinity string, input []any, response M) {
	size := len(js(input)) + len(js(response)) + len(affinity)
	if size > 64<<20 {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	id := str(response["id"])
	a.forgetResponse(id)
	a.responses[id] = responseEntry{Key: key, Affinity: affinity, Input: input, Response: response, Bytes: size}
	a.responseOrder = append(a.responseOrder, id)
	a.responseBytes += size
	for len(a.responses) > 100 || a.responseBytes > 64<<20 {
		a.forgetResponse(a.responseOrder[0])
	}
}
