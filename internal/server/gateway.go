package server

import (
	"context"
	"fmt"
	"maps"
	"math"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strings"
	settings "zcode2api/internal/server/config"
	"zcode2api/internal/server/fault"
	wire "zcode2api/internal/server/protocol"
	"zcode2api/internal/server/shared"
)

func (a *App) GatewayHandler() http.Handler {
	return a.guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "authorization, content-type, x-api-key, anthropic-version, anthropic-beta, x-session-id, x-zcode-instruction-mode, x-zcode-session-type, x-zcode-query-source, x-zcode-template-mode, x-zcode-reuse-tools")
		w.Header().Set("Access-Control-Expose-Headers", "X-Zcode-Instruction-Mode, X-Zcode-Compatibility, X-Zcode-Grammar-Validation, X-Zcode-Session-Type, X-Zcode-Query-Source, X-Zcode-Template-Mode, X-Zcode-Tools-Reused, X-Zcode-Token-Count, Retry-After, X-Retry-Attempts")
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
					if strings.EqualFold(a.resolveModel(shared.Str(shared.Obj(v)["id"])), a.resolveModel(id)) {
						w.Header().Set("X-Upstream-Model", shared.Str(shared.Obj(v)["id"]))
						writeJSON(w, 200, v)
						return
					}
				}
				writeJSON(w, 404, shared.M{"error": shared.M{"type": "not_found_error", "code": "model_not_found", "message": "模型不存在或当前密钥不可访问"}})
				return
			}
			var firstID, lastID any
			if len(models) > 0 {
				firstID = shared.Obj(models[0])["id"]
				lastID = shared.Obj(models[len(models)-1])["id"]
			}
			writeJSON(w, 200, shared.M{"object": "list", "data": models, "has_more": false, "first_id": firstID, "last_id": lastID})
			return
		case r.Method == "GET" && path == "/v1/usage":
			var remain any
			if k["quota_limit"] != nil {
				remain = math.Max(0, shared.Num(k["quota_limit"])-shared.Num(k["used_units"]))
			}
			writeJSON(w, 200, shared.M{"key": shared.M{"id": k["id"], "name": k["name"], "created_at": k["created_at"]}, "used_units": k["used_units"], "quota_limit": k["quota_limit"], "remaining": remain, "expires_at": k["expires_at"], "last_used_at": k["last_used_at"], "server_time": shared.NowMS()})
			return
		case strings.HasPrefix(path, "/v1/responses/") && (r.Method == "GET" || r.Method == "DELETE"):
			id := strings.TrimPrefix(path, "/v1/responses/")
			a.mu.Lock()
			entry, ok := a.responses[id]
			ok = ok && entry.Key == shared.Str(k["id"])
			if r.Method == "DELETE" {
				if ok {
					a.forgetResponse(id)
				}
				a.mu.Unlock()
				writeJSON(w, 200, shared.M{"id": id, "object": "response", "deleted": ok})
				return
			}
			if ok {
				a.touchResponse(id)
			}
			a.mu.Unlock()
			if !ok {
				writeJSON(w, 404, shared.M{"error": shared.M{"type": "invalid_request_error", "code": "response_not_found", "message": "响应不存在（仅保留最近 100 条，重启后失效）"}})
			} else {
				writeJSON(w, 200, entry.Response)
			}
			return
		case r.Method == "POST" && (path == "/v1/chat/completions" || path == "/v1/messages" || path == "/v1/responses" || path == "/v1/messages/count_tokens"):
			body, e := readBody(w, r)
			if e != nil {
				a.sendFailure(w, e.(*fault.Failure), protocol)
				return
			}
			model := a.resolveModel(shared.Str(body["model"]))
			w.Header().Set("X-Upstream-Model", model)
			if wire.ModelSuffix.MatchString(shared.Str(body["model"])) {
				w.Header().Set("X-Model-Context-Hint", "not-enforced")
			}
			if !a.modelAllowed(k, model) {
				a.sendFailure(w, &fault.Failure{Kind: "permission", Code: "model_not_allowed", Message: "模型不在该密钥白名单"}, protocol)
				return
			}
			if path == "/v1/messages/count_tokens" {
				a.handleCountTokens(w, r, body, model)
				return
			}
			a.handleInference(w, r, body, k, protocol, model)
			return
		}
		writeJSON(w, 404, shared.M{"error": shared.M{"type": "invalid_request_error", "code": "not_found", "message": "未知路径 " + r.Method + " " + path}})
	}))
}

func (a *App) models(k shared.M) []any {
	ids := map[string]string{}
	accounts := a.rows("SELECT capabilities FROM accounts WHERE status='active'")
	for _, v := range accounts {
		for _, id := range shared.Arr(shared.Parse(shared.Obj(v)["capabilities"], nil)) {
			ids[shared.Lower(id)] = shared.Str(id)
		}
	}
	aliases := shared.Obj(a.configValue("aliases"))
	if len(accounts) > 0 && len(ids) == 0 {
		for _, id := range aliases {
			ids[shared.Lower(id)] = shared.Str(id)
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
			if strings.EqualFold(shared.Str(target), id) {
				matches = append(matches, alias)
			}
		}
		sort.Slice(matches, func(i, j int) bool { return shared.Str(matches[i]) < shared.Str(matches[j]) })
		out = append(out, shared.M{"id": id, "object": "model", "type": "model", "created": 0, "created_at": "1970-01-01T00:00:00Z", "display_name": id, "owned_by": "zcode-start-plan", "aliases": matches})
	}
	return out
}

func (a *App) handleCountTokens(w http.ResponseWriter, r *http.Request, body shared.M, model string) {
	const protocol = "anthropic"
	source, err := a.parseInferenceSource(r)
	if err != nil {
		a.sendFailure(w, err.(*fault.Failure), protocol)
		return
	}
	tools, err := wire.NewToolCatalog(r.Context(), a.root, body["tools"], protocol)
	if err != nil {
		a.sendFailure(w, err.(*fault.Failure), protocol)
		return
	}
	upstream, err := wire.BuildRequest(body, protocol, model, "", tools, &wire.InstructionContext{}, source.Template)
	if err != nil {
		a.sendFailure(w, err.(*fault.Failure), protocol)
		return
	}
	w.Header().Set("X-Zcode-Token-Count", "estimated")
	writeJSON(w, 200, shared.M{"input_tokens": wire.EstimateInputTokens(upstream)})
}

func (a *App) sendFailure(w http.ResponseWriter, f *fault.Failure, protocol string) {
	status, shape := wire.ErrorShape(f, protocol)
	if f.RetryAfter > 0 || status == 429 || status == 503 || status == 529 {
		d := f.RetryAfter
		if d <= 0 {
			d = busyWait(a.retryParams())
		}
		w.Header().Set("Retry-After", fmt.Sprint(max(1, int(math.Ceil(d.Seconds())))))
	}
	w.Header().Set("X-Retry-Attempts", fmt.Sprint(f.Attempts))
	body := shared.M{"error": shape}
	if protocol == "anthropic" {
		body["type"] = "error"
	}
	writeJSON(w, status, body)
}

// Claude Code composes user_id as user_<id>_session_<sid>_account_<acct>, so
// the session id must be extracted from the middle of the string, not its end.
// The quantifier stays lazy so the capture cannot swallow a following segment.
var sessionPattern = regexp.MustCompile(`(?:^|_)session_([a-zA-Z0-9-]{8,128}?)(?:_|$)`)

func (a *App) affinity(r *http.Request, b, k shared.M) (string, float64) {
	session := r.Header.Get("X-Session-Id")
	if session == "" {
		u := shared.Str(shared.Obj(b["metadata"])["user_id"])
		m := shared.Obj(shared.Parse(u, nil))
		session = shared.Str(m["session_id"])
		if session == "" {
			if p := sessionPattern.FindStringSubmatch(u); len(p) > 1 {
				session = p[1]
			}
		}
	}
	if session != "" && len(session) <= 1024 {
		return shared.Str(k["id"]) + ":" + shared.Hash(session)[:24], math.Max(0, shared.Num(a.configValue("conversation_affinity_minutes")))
	}
	return shared.Str(k["id"]), shared.Num(k["sticky_minutes"])
}

func inputItems(v any) []any {
	if s, ok := v.(string); ok {
		return []any{shared.M{"role": "user", "content": s}}
	}
	return shared.Arr(v)
}

// Matches ZCode runner-attribution.ts at fec975905a2abd4c07e35c40d51fa097a2258ec9.
var querySourcePattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)

func (a *App) parseInferenceSource(r *http.Request) (inferenceSource, error) {
	cfg := a.config(
		"zcode_template_mode", "zcode_subagent_template",
		"zcode_subagent_insert_enabled", "zcode_subagent_harness_enabled", "zcode_subagent_files_enabled", "zcode_subagent_comments_enabled",
		"zcode_subagent_finishing_work_enabled", "zcode_subagent_memory_notes_enabled",
	)
	mode := strings.ToLower(strings.TrimSpace(r.Header.Get("X-Zcode-Template-Mode")))
	explicitMode := mode != ""
	source := inferenceSource{
		SessionType: strings.ToLower(strings.TrimSpace(r.Header.Get("X-Zcode-Session-Type"))),
		QuerySource: strings.TrimSpace(r.Header.Get("X-Zcode-Query-Source")),
	}
	switch source.SessionType {
	case "", "main", "subagent":
	default:
		return source, fault.Invalid("X-Zcode-Session-Type 仅支持 main 或 subagent")
	}
	if !explicitMode {
		mode = shared.Str(shared.Fallback(cfg["zcode_template_mode"], wire.TemplateMain))
		if source.SessionType == "main" {
			mode = wire.TemplateMain
		} else if source.SessionType == "subagent" {
			mode = wire.TemplateSubagent
		}
	}
	custom := shared.Str(cfg["zcode_subagent_template"])
	if mode == wire.TemplateSubagent && shared.Truth(cfg["zcode_subagent_insert_enabled"]) && strings.TrimSpace(custom) == "" {
		custom = defaultSubagentInsert
	}
	template, err := wire.NewSystemTemplate(mode, custom)
	if err != nil {
		return source, err
	}
	template.InsertEnabled = shared.Truth(cfg["zcode_subagent_insert_enabled"])
	template.HarnessEnabled = shared.Truth(cfg["zcode_subagent_harness_enabled"])
	template.FilesEnabled = shared.Truth(cfg["zcode_subagent_files_enabled"])
	template.CommentsEnabled = shared.Truth(cfg["zcode_subagent_comments_enabled"])
	template.FinishingWorkEnabled = shared.Truth(cfg["zcode_subagent_finishing_work_enabled"])
	template.MemoryNotesEnabled = shared.Truth(cfg["zcode_subagent_memory_notes_enabled"])
	if explicitMode && source.SessionType != "" && source.SessionType != template.SessionType() {
		return source, fault.Invalid("X-Zcode-Session-Type 与 X-Zcode-Template-Mode 不一致")
	}
	source.Template, source.SessionType = template, template.SessionType()
	if source.QuerySource == "" {
		source.QuerySource = template.QuerySource()
	} else if !querySourcePattern.MatchString(source.QuerySource) {
		// Official attribution omits invalid query sources instead of sending them.
		source.QuerySource = ""
	}
	return source, nil
}

func (a *App) handleInference(w http.ResponseWriter, r *http.Request, body, k shared.M, protocol, model string) {
	source, failure := a.parseInferenceSource(r)
	if failure != nil {
		a.sendFailure(w, failure.(*fault.Failure), protocol)
		return
	}
	w.Header().Set("X-Zcode-Session-Type", source.SessionType)
	w.Header().Set("X-Zcode-Template-Mode", source.Template.Mode)
	if source.QuerySource != "" {
		w.Header().Set("X-Zcode-Query-Source", source.QuerySource)
	}
	// Continuation overrides replace top-level fields. The original input is
	// kept separately for storing just this turn in the response history.
	payload := maps.Clone(body)
	var previous responseEntry
	if protocol == "responses" && shared.Truth(body["previous_response_id"]) {
		switch body["input"].(type) {
		case nil, string, []any:
		default:
			a.sendFailure(w, fault.Invalid("input 必须为字符串或数组").(*fault.Failure), protocol)
			return
		}
		a.mu.Lock()
		entry, ok := a.responses[shared.Str(body["previous_response_id"])]
		a.mu.Unlock()
		if !ok || entry.Key != shared.Str(k["id"]) {
			writeJSON(w, 404, shared.M{"error": shared.M{"type": "invalid_request_error", "code": "response_not_found", "message": "previous_response_id 不存在或不属于当前密钥"}})
			return
		}
		previous = entry
		payload["input"] = previous.History.withInput(inputItems(body["input"]))
	}
	var keepTools bool
	var reusedTools *responseToolSet
	if protocol == "responses" {
		switch strings.ToLower(strings.TrimSpace(r.Header.Get("X-Zcode-Reuse-Tools"))) {
		case "":
			keepTools = previous.Tools != nil
		case "true":
			keepTools = true
		case "false":
		default:
			a.sendFailure(w, &fault.Failure{Kind: "invalid", Code: "unsupported_request", Message: "X-Zcode-Reuse-Tools 只能为 true 或 false"}, protocol)
			return
		}
		if keepTools {
			reusedTools = previous.reuseTools(payload)
		}
	}
	level := a.effectiveEffort(body, k)
	mode := shared.Str(shared.Fallback(r.Header.Get("X-Zcode-Instruction-Mode"), payload["instruction_mode"]))
	if mode == "" && protocol != "anthropic" {
		mode = shared.Str(shared.Fallback(previous.InstructionMode, k["default_instruction_mode"]))
	}
	instructions, e := wire.NewInstructionContext(mode)
	if e != nil {
		a.sendFailure(w, e.(*fault.Failure), protocol)
		return
	}
	tools, e := wire.NewToolCatalog(r.Context(), a.root, payload["tools"], protocol)
	if e != nil {
		a.sendFailure(w, e.(*fault.Failure), protocol)
		return
	}
	upstream, e := wire.BuildRequest(payload, protocol, model, level, tools, instructions, source.Template)
	if e != nil {
		a.sendFailure(w, e.(*fault.Failure), protocol)
		return
	}
	if reusedTools != nil {
		w.Header().Set("X-Zcode-Tools-Reused", "true")
	}
	if protocol != "anthropic" && instructions.Mode == "history" {
		w.Header().Set("X-Zcode-Instruction-Mode", "history")
	} else if instructions.Used {
		w.Header().Set("X-Zcode-Instruction-Mode", "scoped-system")
	}
	if instructions.Used {
		w.Header().Set("X-Zcode-Compatibility", "instruction-roles-and-scope-approximated")
	}
	for _, definition := range tools.ByName {
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
	id := prefix + shared.UUID()
	affinity, sticky := a.affinity(r, body, k)
	if protocol == "responses" && affinity == shared.Str(k["id"]) {
		// The bare key means no explicit session was recognized by affinity().
		affinity = previous.Affinity
		if affinity == "" {
			affinity = shared.Str(k["id"]) + ":responses:" + shared.Hash(id)[:24]
		}
		sticky = math.Max(0, shared.Num(a.configValue("conversation_affinity_minutes")))
	}
	o := inferOptions{Key: k, Body: payload, Upstream: upstream, Tools: tools, Source: source, Protocol: protocol, Model: model, Level: level, ID: id, IP: a.ip(r), Affinity: affinity, Sticky: sticky, Stream: shared.Truth(body["stream"]), Timing: newTiming()}
	ctx, cancel := a.operationContext(r.Context(), 0)
	defer cancel()
	w.Header().Set("X-Inference-Driver", settings.InferenceDriver)
	var stream *wire.Stream
	if o.Stream {
		stream = wire.NewStream(w, r, protocol, id, model, shared.Truth(shared.Obj(body["stream_options"])["include_usage"]), tools)
		defer stream.End()
		o.OnEvent = stream.OnEvent
		stopStream := context.AfterFunc(stream.Ctx, cancel)
		defer stopStream()
	}
	result, f := a.infer(ctx, o)
	if f != nil {
		if stream == nil || !stream.Fail(f) {
			a.sendFailure(w, f, protocol)
		}
		return
	}
	var response shared.M
	if stream == nil {
		response, e = wire.BuildResponse(protocol, result, model, id, tools)
		if e != nil {
			a.sendFailure(w, e.(*fault.Failure), protocol)
			return
		}
	} else if protocol == "responses" {
		response = stream.FinalResponse(result)
	}
	if protocol == "responses" && body["store"] != false {
		entry := responseEntry{Key: shared.Str(k["id"]), Affinity: affinity, Response: response}
		// Only the cache-friendly opt-in carries forward when the next turn omits a mode.
		if instructions.Mode == "history" {
			entry.InstructionMode = instructions.Mode
		}
		if keepTools {
			entry.Tools = reusedTools
			if definitions := shared.Arr(payload["tools"]); entry.Tools == nil && len(definitions) > 0 {
				entry.Tools = &responseToolSet{definitions: definitions, bytes: len(shared.JSON(definitions))}
			}
			if entry.Tools != nil {
				entry.ToolOptions = shared.M{}
				shared.CopyFields(entry.ToolOptions, payload, "tool_choice", "parallel_tool_calls")
			}
		}
		a.rememberResponse(entry, previous.History, inputItems(body["input"]), result.ContinuationItem())
	}
	if stream != nil {
		stream.Complete(result, response)
		return
	}
	w.Header().Set("X-Retry-Attempts", fmt.Sprint(result.Attempts))
	writeJSON(w, 200, response)
}

func (e responseEntry) reuseTools(payload shared.M) *responseToolSet {
	// An explicit tools field, including an empty list, replaces the previous set.
	if _, explicit := payload["tools"]; explicit || e.Tools == nil {
		return nil
	}
	payload["tools"] = e.Tools.definitions
	for field, value := range e.ToolOptions {
		if _, explicit := payload[field]; !explicit {
			payload[field] = value
		}
	}
	return e.Tools
}

func (a *App) forgetResponse(id string) {
	if e, ok := a.responses[id]; ok {
		a.responseBytes -= e.Bytes
		delete(a.responses, id)
		if e.Tools != nil {
			e.Tools.refs--
			if e.Tools.refs == 0 {
				a.responseBytes -= e.Tools.bytes
			}
		}
		for h := e.History; h != nil; h = h.parent {
			h.refs--
			if h.refs > 0 {
				break
			}
			a.responseBytes -= h.bytes
		}
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

func (h *responseHistory) withInput(input []any) []any {
	items := make([]any, h.count+len(input))
	copy(items[h.count:], input)
	for node := h; node != nil; node = node.parent {
		copy(items[node.count-len(node.items):node.count], node.items)
	}
	return items
}

func (a *App) rememberResponse(entry responseEntry, parent *responseHistory, input []any, assistant any) {
	const maxResponseBytes = 64 << 20
	items := make([]any, 0, len(input)+1)
	items = append(items, input...)
	if assistant != nil {
		items = append(items, assistant)
	}
	// Share history across continuations and branches, including when a parent
	// response is later evicted or explicitly deleted.
	itemBytes := len(shared.JSON(items))
	history := &responseHistory{parent: parent, items: items, count: len(items), bytes: itemBytes, totalBytes: itemBytes}
	if parent != nil {
		history.count += parent.count
		history.totalBytes += parent.totalBytes
	}
	entry.Bytes = len(shared.JSON(entry.Response)) + len(entry.Affinity) + len(entry.InstructionMode)
	toolBytes := 0
	if entry.Tools != nil {
		entry.Bytes += len(shared.JSON(entry.ToolOptions))
		toolBytes = entry.Tools.bytes
	}
	if entry.Bytes+history.totalBytes+toolBytes > maxResponseBytes {
		return
	}
	entry.History = history
	a.mu.Lock()
	defer a.mu.Unlock()
	id := shared.Str(entry.Response["id"])
	a.forgetResponse(id)
	a.responses[id] = entry
	a.responseOrder = append(a.responseOrder, id)
	a.responseBytes += entry.Bytes
	if entry.Tools != nil {
		entry.Tools.refs++
		if entry.Tools.refs == 1 {
			a.responseBytes += toolBytes
		}
	}
	for h := history; h != nil; h = h.parent {
		h.refs++
		if h.refs > 1 {
			break
		}
		a.responseBytes += h.bytes
	}
	for len(a.responses) > 100 || a.responseBytes > maxResponseBytes {
		a.forgetResponse(a.responseOrder[0])
	}
}
