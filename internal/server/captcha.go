package server

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"
)

//go:embed captcha.cjs
var captchaRuntime []byte

type captchaScene struct {
	Value      M
	Expires    time.Time
	Generation uint64
	Loading    chan struct{}
}

type captchaTicket struct {
	param     string
	region    string
	expiresAt time.Time
	scope     captchaTicketScope
}

type captchaManager struct {
	mu         sync.Mutex
	slot       chan struct{}
	available  chan struct{}
	waiters    int
	generation uint64
	scenes     map[string]captchaScene
	used       map[string]time.Time
	pools      map[string]*captchaTicketPool
	browser    map[string]*claimCaptchaSession
	refreshing map[string]bool
	cancels    map[string]context.CancelFunc
	run        func(context.Context, M) (M, error)
}

// Scene configuration is shared by exit. Tickets have a separate, narrower scope.
func (a *App) captchaSceneKey(ctx context.Context) string {
	return a.origin + "|" + str(a.configValue("app_version")) + "|" + selectedProxy(ctx)
}

func (a *App) captchaSlot() chan struct{} {
	a.captcha.mu.Lock()
	defer a.captcha.mu.Unlock()
	if a.captcha.slot == nil {
		a.captcha.slot = make(chan struct{}, 1)
		a.captcha.available = make(chan struct{})
	}
	return a.captcha.slot
}

func (a *App) captchaHeaders(parent context.Context, account M, purpose string, force bool) (captchaVerification, *Failure) {
	options := obj(a.configValue("http", "captcha"))
	mode := str(fallback(options["mode"], "off"))
	verification := captchaVerification{Headers: http.Header{}}
	if err := validateCaptchaOptions(options); err != nil {
		return verification, &Failure{Kind: "server", Code: "captcha_config_invalid", Message: err.Error()}
	}
	if mode == "off" && !force {
		return verification, nil
	}
	parent, release, err := a.pinOutboundProxy(parent, account)
	if err != nil {
		return verification, asProxyFailure(err)
	}
	defer release()
	ctx, cancel := a.operationContext(parent, ms(fallback(options["timeout_ms"], 30000))+3*time.Second)
	defer cancel()
	if ctx.Err() != nil {
		return verification, &Failure{Kind: "server", Code: "captcha_timeout", Message: "上游安全验证已取消或超时"}
	}
	for holdingSlot := false; ; holdingSlot = true {
		// Recheck the scene and pool after waiting: either may have changed.
		scene, failure := a.captchaScene(ctx, account)
		if failure != nil {
			return verification, failure
		}
		if force && !truth(scene["enabled"]) {
			return verification, &Failure{Kind: "risk", Code: "captcha_scene_disabled", Message: "上游要求安全验证，但未提供可用的验证场景"}
		}
		if !truth(scene["enabled"]) || (!force && mode == "auto" && truth(scene["skip_model_request"])) {
			return verification, nil
		}
		scope, failure := a.captchaScope(ctx, account, purpose, scene)
		if failure != nil {
			return verification, failure
		}
		pool := a.captchaPool(scope)
		ticket, ready := a.takeCaptchaTicket(pool)
		// Ready tickets bypass the SDK queue to preserve their short lifetime.
		if !ready && !holdingSlot {
			releaseSlot, acquired := a.acquireClaimCaptchaSlot(ctx, false)
			if !acquired {
				return verification, &Failure{Kind: "server", Code: "captcha_timeout", Message: "等待上游安全验证超时"}
			}
			defer releaseSlot()
			continue
		}
		if !ready {
			ticket, failure = a.mintCaptchaTicket(ctx, account, options, scene, scope)
			if failure != nil {
				return verification, failure
			}
		}
		failure = a.attachCaptchaTicket(ctx, &verification, pool, ticket)
		return verification, failure
	}
}

func (a *App) attachCaptchaTicket(ctx context.Context, verification *captchaVerification, pool *captchaTicketPool, ticket captchaTicket) *Failure {
	if ticket.scope != pool.scope || !a.captchaScopeCurrent(ctx, ticket.scope) {
		return captchaScopeChanged()
	}
	a.captcha.mu.Lock()
	current := a.captcha.scenes[ticket.scope.sceneKey]
	valid := current.Generation == ticket.scope.generation && a.captcha.pools[ticket.scope.key()] == pool
	a.captcha.mu.Unlock()
	if !valid {
		return captchaScopeChanged()
	}
	if !time.Now().Add(captchaTicketSafetyMargin).Before(ticket.expiresAt) {
		return &Failure{Kind: "risk", Code: "captcha_expired", Message: "验证票据已过期，请重新验证"}
	}
	if failure := a.reserveCaptchaTicket(ticket.param); failure != nil {
		return failure
	}
	verification.Headers.Set("X-Aliyun-Captcha-Verify-Param", ticket.param)
	verification.Headers.Set("X-Aliyun-Captcha-Verify-Region", ticket.region)
	verification.pool, verification.proxyURL = pool, selectedProxy(ctx)
	return nil
}

func (a *App) captchaRuntimeParameters(ctx context.Context, options, scene M, scope captchaTicketScope) (M, *captchaTicketPool) {
	modulePath := a.captchaModulePath(str(options["jsdom_path"]))
	parameters := M{"origin": a.origin, "scene_id": scene["sceneId"], "region": scene["region"], "prefix": scene["prefix"], "simulate_canvas_webgl": first(options["simulate_canvas_webgl"], true), "module_path": modulePath, "timeout_ms": options["timeout_ms"], "proxy_url": selectedProxy(ctx)}
	if captchaUsesBrowser(scope.purpose, options) {
		parameters["session_key"] = scope.key()
	}
	pool := a.captchaPool(scope)
	a.captcha.mu.Lock()
	parameters["state"] = clone(pool.runtimeState)
	a.captcha.mu.Unlock()
	parameters["cookies"] = a.jar(scope.accountID).header()
	return parameters, pool
}

// The caller holds the captcha slot and pins the exit for this SDK execution.
func (a *App) mintCaptchaTicket(ctx context.Context, account M, options, scene M, scope captchaTicketScope) (ticket captchaTicket, failure *Failure) {
	diagnostic, _ := ctx.Value(claimCaptchaDiagnosticKey{}).(M)
	if diagnostic != nil {
		diagnostic["sdk_stage"] = "admission"
	}
	ctx, finish, gateFailure := a.beginUpstreamRequest(ctx, a.captchaRequestScope(ctx, account, scene))
	if gateFailure != nil {
		return captchaTicket{}, gateFailure
	}
	defer func() { finish(failure) }()
	parameters, pool := a.captchaRuntimeParameters(ctx, options, scene, scope)
	run := a.captcha.run
	if run == nil {
		run = func(ctx context.Context, parameters M) (M, error) { return a.runCaptcha(ctx, options, parameters) }
		if captchaUsesBrowser(scope.purpose, options) {
			run = func(ctx context.Context, parameters M) (M, error) {
				return a.runBrowserCaptcha(ctx, options, parameters)
			}
		}
	}
	started := time.Now()
	if diagnostic != nil {
		diagnostic["sdk_stage"] = "runtime"
		diagnostic["sdk_executed"], diagnostic["sdk_initialized"] = nil, nil
		diagnostic["runtime"] = "jsdom"
		if captchaUsesBrowser(scope.purpose, options) {
			diagnostic["runtime"] = "browser"
		}
	}
	result, err := run(ctx, parameters)
	// Only bounded SDK session data is reusable; the scoped pool discards it on invalidation.
	if state := obj(result["state"]); ctx.Err() == nil && len(state) > 0 && len(js(state)) <= 16<<10 && a.captchaScopeCurrent(ctx, scope) {
		a.captcha.mu.Lock()
		if a.captcha.pools[scope.key()] == pool {
			pool.runtimeState = clone(state)
		}
		a.captcha.mu.Unlock()
	}
	if diagnostic != nil {
		diagnostic["sdk_executed"] = result["sdk_executed"]
		diagnostic["sdk_initialized"] = result["sdk_initialized"]
		if stage := str(result["stage"]); stage != "" {
			diagnostic["sdk_stage"] = stage
		}
		if code := str(result["verify_code"]); code != "" {
			diagnostic["verify_code"] = code
		}
		if result["runtime"] == "native_browser" {
			diagnostic["runtime"] = "native_browser"
		}
		copyFields(diagnostic, result, "sdk_error_code", "sdk_load_ms", "sdk_init_ms", "sdk_ready_wait_ms", "sdk_prepared_age_ms", "sdk_verify_ms", "request_id", "browser_version", "browser_visibility", "session_reused")
	}
	if ctx.Err() != nil {
		return captchaTicket{}, &Failure{Kind: "server", Code: "captcha_timeout", Message: "SDK 验证已取消或超时"}
	}
	if err != nil {
		return captchaTicket{}, &Failure{Kind: "server", Code: "captcha_runtime_error", Message: "上游安全验证运行失败，请检查 Node、验证运行时和网络配置"}
	}
	if !truth(result["ok"]) {
		switch str(result["code"]) {
		case "captcha_playwright_missing", "captcha_browser_missing", "captcha_browser_launch_failed":
			return captchaTicket{}, &Failure{Kind: "server", Code: str(result["code"]), Message: conciseFailureMessage(&Failure{Code: str(result["code"])})}
		case "captcha_jsdom_missing":
			return captchaTicket{}, &Failure{Kind: "server", Code: "captcha_jsdom_missing", Message: "未找到 jsdom，请在程序根目录运行 npm --prefix captcha_node ci --omit=dev --ignore-scripts，或配置 http.captcha.jsdom_path"}
		case "captcha_timeout":
			return captchaTicket{}, &Failure{Kind: "server", Code: "captcha_timeout", Message: "上游安全验证超时或需要人工交互"}
		case "captcha_proxy_unsupported":
			return captchaTicket{}, &Failure{Kind: "server", Code: "captcha_proxy_unsupported", Message: "SDK 验证需要有效的 HTTP/HTTPS 代理"}
		case "proxy_auth_failed":
			return captchaTicket{}, &Failure{Kind: "network", Code: "proxy_auth_failed", Message: "代理认证失败", Retryable: true}
		case "proxy_connection_failed":
			return captchaTicket{}, &Failure{Kind: "network", Code: "proxy_connection_failed", Message: "SDK 代理连接失败", Retryable: true}
		case "captcha_resource_http_error":
			if integer(result["status"]) == http.StatusTooManyRequests {
				return captchaTicket{}, &Failure{Kind: "rate_limit", Code: "captcha_resource_http_error", Status: http.StatusTooManyRequests, Message: "验证码 SDK 资源请求受限", Retryable: true, RetryAfter: retryAfter(str(result["retry_after"]), time.Now())}
			}
			return captchaTicket{}, &Failure{Kind: "server", Code: "captcha_resource_http_error", Status: int(integer(result["status"])), Message: "验证码 SDK 资源返回 HTTP 错误", Retryable: integer(result["status"]) >= 500}
		case "captcha_network_error":
			return captchaTicket{}, &Failure{Kind: "network", Code: "captcha_network_error", Message: "验证码 SDK 资源连接失败", Retryable: true}
		case "captcha_interaction_required":
			return captchaTicket{}, &Failure{Kind: "risk", Code: "captcha_interaction_required", Message: "上游要求人工安全验证，请先在官方客户端完成验证", VerifyCode: str(result["verify_code"])}
		case "captcha_runtime_error", "captcha_sdk_error", "captcha_origin_invalid", "captcha_canceled":
			return captchaTicket{}, &Failure{Kind: "server", Code: "captcha_runtime_error", Message: "验证码运行环境、页面或网络配置不可用"}
		}
		code := str(result["verify_code"])
		message := "上游安全验证未通过，需要按官方验证流程处理"
		if len(code) == 4 && code[0] == 'F' && code[1] >= '0' && code[1] <= '9' && code[2] >= '0' && code[2] <= '9' && code[3] >= '0' && code[3] <= '9' {
			message += "（" + code + "）"
		}
		return captchaTicket{}, classify(&Failure{Kind: "risk", Code: "captcha_verification_failed", Message: message, VerifyCode: code})
	}
	param := str(result["param"])
	if !validCaptchaParam(param) {
		return captchaTicket{}, &Failure{Kind: "server", Code: "captcha_invalid_result", Message: "验证码运行时返回了无效票据"}
	}
	// Count from the SDK verification, never from the child process's exit.
	issued := started
	if timestamp := integer(result["issued_at_ms"]); timestamp >= started.UnixMilli() && timestamp <= time.Now().UnixMilli() {
		issued = started.Add(time.Duration(timestamp-started.UnixMilli()) * time.Millisecond)
	}
	ticket = captchaTicket{param: param, region: str(scene["region"]), expiresAt: issued.Add(maxCaptchaTicketTTL), scope: scope}
	if !time.Now().Add(captchaTicketSafetyMargin).Before(ticket.expiresAt) {
		return captchaTicket{}, &Failure{Kind: "risk", Code: "captcha_expired", Message: "验证票据已过期，请重新验证"}
	}
	return ticket, nil
}

const (
	captchaSceneTTL         = 10 * time.Minute
	captchaSceneRefreshLead = 2 * time.Minute
)

func (a *App) captchaScene(ctx context.Context, account M) (M, *Failure) {
	key := a.captchaSceneKey(ctx)
	for {
		a.captcha.mu.Lock()
		if a.captcha.scenes == nil {
			a.captcha.scenes = map[string]captchaScene{}
		}
		entry, cached := a.captcha.scenes[key]
		if entry.Loading != nil {
			a.captcha.mu.Unlock()
			select {
			case <-entry.Loading:
				continue
			case <-ctx.Done():
				return nil, &Failure{Kind: "timeout", Code: "captcha_config_timeout", Message: "等待 SDK 配置已取消或超时", Retryable: true}
			}
		}
		if cached && time.Now().Before(entry.Expires) {
			a.captcha.mu.Unlock()
			if time.Until(entry.Expires) < captchaSceneRefreshLead {
				a.refreshCaptchaScene(key, selectedProxy(ctx))
			}
			return entry.Value, nil
		}
		// Bound both scene and ticket caches without flushing unrelated exits.
		if !cached && len(a.captcha.scenes) >= 32 {
			oldestKey := ""
			var oldest time.Time
			for candidate, value := range a.captcha.scenes {
				if value.Loading == nil && (oldestKey == "" || value.Expires.Before(oldest)) {
					oldestKey, oldest = candidate, value.Expires
				}
			}
			if oldestKey == "" {
				a.captcha.mu.Unlock()
				return nil, &Failure{Kind: "server", Code: "captcha_capacity_exceeded", Message: "安全验证配置请求过多，请稍后重试"}
			}
			a.invalidateCaptchaSceneLocked(oldestKey)
		}
		if cached {
			a.invalidateCaptchaSceneLocked(key)
		}
		a.captcha.generation++
		entry = captchaScene{Generation: a.captcha.generation, Loading: make(chan struct{})}
		a.captcha.scenes[key] = entry
		a.dropCaptchaScenePoolsLocked(key)
		a.captcha.mu.Unlock()
		scene, failure := a.fetchCaptchaScene(ctx, account)
		a.captcha.mu.Lock()
		current := a.captcha.scenes[key]
		if current.Generation != entry.Generation {
			failure = &Failure{Kind: "server", Code: "captcha_config_changed", Message: "安全验证配置已更新，请重试", Retryable: true}
		} else if failure != nil {
			delete(a.captcha.scenes, key)
		} else {
			a.captcha.scenes[key] = captchaScene{Value: scene, Expires: time.Now().Add(captchaSceneTTL), Generation: entry.Generation}
		}
		close(entry.Loading)
		a.captcha.mu.Unlock()
		return scene, failure
	}
}

func (a *App) refreshCaptchaScene(key, proxyURL string) {
	a.captcha.mu.Lock()
	entry, exists := a.captcha.scenes[key]
	if a.captcha.refreshing[key] || !exists || entry.Loading != nil || a.ctx.Err() != nil {
		a.captcha.mu.Unlock()
		return
	}
	if a.captcha.refreshing == nil {
		a.captcha.refreshing = map[string]bool{}
	}
	a.captcha.refreshing[key] = true
	ctx, cancel := context.WithTimeout(pinnedProxyContext(a.ctx, proxyURL), 10*time.Second)
	a.registerCaptchaCancelLocked("refresh|"+key, cancel)
	a.captcha.mu.Unlock()
	release, retained := a.retainCaptchaProxy(proxyURL)
	a.background(func() {
		defer func() {
			cancel()
			release()
			a.captcha.mu.Lock()
			delete(a.captcha.refreshing, key)
			delete(a.captcha.cancels, "refresh|"+key)
			a.captcha.mu.Unlock()
		}()
		if !retained || ctx.Err() != nil || key != a.captchaSceneKey(ctx) {
			return
		}
		scene, failure := a.fetchCaptchaScene(ctx, nil)
		if failure != nil {
			return
		}
		a.captcha.mu.Lock()
		// Identity prevents a stale refresh from overwriting a replacement after invalidation.
		if current := a.captcha.scenes[key]; ctx.Err() == nil && current.Generation == entry.Generation {
			if js(scene) != js(current.Value) {
				a.captcha.generation++
				current.Generation = a.captcha.generation
				a.dropCaptchaScenePoolsLocked(key)
			}
			current.Value, current.Expires = scene, time.Now().Add(captchaSceneTTL)
			a.captcha.scenes[key] = current
		}
		a.captcha.mu.Unlock()
	})
}

func (a *App) fetchCaptchaScene(ctx context.Context, account M) (M, *Failure) {
	configCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	endpoint := "/api/v1/client/configs?app_version=" + url.QueryEscape(str(a.configValue("app_version"))) + "&platform=" + url.QueryEscape(platform())
	response, err := a.api(configCtx, account, "GET", endpoint, "", nil, nil, nil)
	if err != nil {
		var failure *Failure
		if errors.As(err, &failure) {
			return nil, failure
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, &Failure{Kind: "timeout", Code: "captcha_config_timeout", Message: "SDK 配置获取超时", Retryable: true}
		}
		return nil, &Failure{Kind: "network", Code: "captcha_config_unavailable", Message: "读取上游安全验证配置的网络请求失败", Retryable: true}
	}
	if !response.OK {
		failure := apiResponseFailure(response)
		// Anonymous config refusals cannot establish account risk, even with business error codes.
		if failure.Kind == "account_risk" {
			failure.Kind, failure.Code, failure.Message = "risk", "captcha_config_risk_blocked", "SDK 配置请求被风控拦截"
		}
		if failure.Kind == "risk" || failure.Kind == "account_risk" || failure.Kind == "rate_limit" || failure.Code == "proxy_auth_failed" {
			return nil, failure
		}
		return nil, &Failure{Kind: "server", Code: "captcha_config_unavailable", Message: fmt.Sprintf("读取上游安全验证配置失败（HTTP %d）", response.Status), Retryable: response.Status >= 500}
	}
	scene := obj(obj(obj(response.JSON["data"])["configs"])["captcha"])
	if _, ok := scene["enabled"].(bool); !ok {
		return nil, &Failure{Kind: "server", Code: "captcha_config_invalid", Message: "上游没有返回有效的安全验证配置"}
	}
	if value, present := scene["skip_model_request"]; present {
		if _, ok := value.(bool); !ok {
			return nil, &Failure{Kind: "server", Code: "captcha_config_invalid", Message: "上游安全验证跳过策略无效"}
		}
	}
	if truth(scene["enabled"]) {
		region := str(scene["region"])
		if (region != "cn" && region != "sgp") || strings.TrimSpace(str(scene["sceneId"])) == "" || strings.TrimSpace(str(scene["prefix"])) == "" {
			return nil, &Failure{Kind: "server", Code: "captcha_config_invalid", Message: "上游安全验证场景或地域配置不完整"}
		}
	}
	return scene, nil
}

func (a *App) runCaptcha(ctx context.Context, options, parameters M) (M, error) {
	helper, err := a.writeCaptchaRuntime("captcha.cjs", captchaRuntime)
	if err != nil {
		return nil, err
	}
	node, err := a.resolveCaptchaNode(str(options["node_path"]))
	if err != nil {
		return nil, err
	}
	command, output := newJSONCommand(ctx, node, parameters, helper)
	command.WaitDelay = 2 * time.Second
	command.Cancel = func() error {
		if runtime.GOOS != "windows" {
			if err := command.Process.Signal(os.Interrupt); err == nil {
				return nil
			}
		}
		return command.Process.Kill()
	}
	err = command.Run()
	result := obj(parse(string(output.value), nil))
	if err != nil && truth(result["ok"]) {
		return nil, err
	}
	if len(result) == 0 {
		if err == nil {
			err = fmt.Errorf("captcha runtime returned no result")
		}
		return nil, err
	}
	return result, nil
}

func captchaExpired(f *Failure) bool {
	if f == nil || (f.Status != 401 && f.Status != 403) {
		return false
	}
	return captchaChallenge(f)
}

func explicitCaptchaChallenge(message string) bool {
	message = strings.ToLower(message)
	return strings.Contains(message, "captcha") || strings.Contains(message, "security verification") || strings.Contains(message, "安全验证")
}

// An initial challenge is not evidence that a still-unused ticket is invalid.
// Only refresh a disabled scene here; rejected submitted tickets use invalidation.
func (a *App) prepareCaptchaChallenge(ctx context.Context) {
	key := a.captchaSceneKey(ctx)
	a.captcha.mu.Lock()
	defer a.captcha.mu.Unlock()
	if entry, exists := a.captcha.scenes[key]; exists && entry.Loading == nil && !truth(entry.Value["enabled"]) {
		a.invalidateCaptchaSceneLocked(key)
	}
}

func (a *App) invalidateCaptchaScene(key string) {
	a.captcha.mu.Lock()
	defer a.captcha.mu.Unlock()
	a.invalidateCaptchaSceneLocked(key)
}

func (a *App) registerCaptchaCancelLocked(key string, cancel context.CancelFunc) {
	if a.captcha.cancels == nil {
		a.captcha.cancels = map[string]context.CancelFunc{}
	}
	a.captcha.cancels[key] = cancel
}

func (a *App) invalidateCaptchaSceneLocked(key string) {
	delete(a.captcha.scenes, key)
	a.dropCaptchaScenePoolsLocked(key)
	if cancel := a.captcha.cancels["refresh|"+key]; cancel != nil {
		cancel()
	}
}

func (a *App) resetCaptchaCache() {
	a.closeClaimBrowser()
	a.captcha.mu.Lock()
	defer a.captcha.mu.Unlock()
	for _, cancel := range a.captcha.cancels {
		cancel()
	}
	for key := range a.captcha.pools {
		a.dropCaptchaPoolLocked(key)
	}
	clear(a.captcha.scenes)
}

func validateCaptchaOptions(value any) error {
	options, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("http.captcha 必须为配置对象")
	}
	for key, value := range options {
		switch key {
		case "claim_runtime":
			if value != "jsdom" && value != "browser" {
				return fmt.Errorf("http.captcha.claim_runtime 须为 jsdom/browser")
			}
		case "mode":
			if value != "off" && value != "auto" && value != "always" {
				return fmt.Errorf("http.captcha.mode 须为 off/auto/always")
			}
		case "simulate_canvas_webgl":
			if _, ok := value.(bool); !ok {
				return fmt.Errorf("http.captcha.simulate_canvas_webgl 必须为布尔值")
			}
		case "node_path", "jsdom_path", "playwright_path", "browser_path":
			text, ok := value.(string)
			if !ok || strings.ContainsAny(text, "\r\n\x00") || (key == "node_path" && strings.TrimSpace(text) == "") {
				return fmt.Errorf("http.captcha.%s 路径无效", key)
			}
		case "timeout_ms":
			if !integerInRange(value, 5000, 120000) {
				return fmt.Errorf("http.captcha.timeout_ms 须为 5000 到 120000 的整数")
			}
		case "ticket_ttl_ms":
			if !integerInRange(value, 0, 0) && !integerInRange(value, 30000, float64(maxCaptchaTicketTTL.Milliseconds())) {
				return fmt.Errorf("http.captcha.ticket_ttl_ms 须为 0（关闭）或 30000 到 90000 的整数")
			}
		default:
			return fmt.Errorf("未知安全验证设置 http.captcha.%s", key)
		}
	}
	return nil
}
