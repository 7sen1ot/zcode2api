package server

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

//go:embed captcha.cjs
var captchaRuntime []byte

type captchaScene struct {
	Value   M
	Expires time.Time
}

type captchaManager struct {
	mu     sync.Mutex
	slot   chan struct{}
	scenes map[string]captchaScene
	used   map[string]time.Time
	run    func(context.Context, M) (M, error)
	reset  bool
}

func (a *App) captchaHeaders(parent context.Context, account M, force bool) (http.Header, *Failure) {
	options := obj(obj(a.config()["http"])["captcha"])
	mode := str(fallback(options["mode"], "off"))
	headers := http.Header{}
	if err := validateCaptchaOptions(options); err != nil {
		return nil, &Failure{Kind: "server", Code: "captcha_config_invalid", Message: err.Error()}
	}
	if mode == "off" {
		return headers, nil
	}
	parent, release, err := a.pinOutboundProxy(parent, account)
	if err != nil {
		return nil, asProxyFailure(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(parent, ms(fallback(options["timeout_ms"], 30000))+3*time.Second)
	defer cancel()
	stop := context.AfterFunc(a.ctx, cancel)
	defer stop()
	a.captcha.mu.Lock()
	if a.captcha.slot == nil {
		a.captcha.slot = make(chan struct{}, 1)
		a.captcha.scenes = map[string]captchaScene{}
		a.captcha.used = map[string]time.Time{}
	}
	slot := a.captcha.slot
	a.captcha.mu.Unlock()
	select {
	case slot <- struct{}{}:
		defer func() { <-slot }()
	case <-ctx.Done():
		return nil, &Failure{Kind: "server", Code: "captcha_timeout", Message: "等待上游安全验证超时"}
	}
	if ctx.Err() != nil {
		return nil, &Failure{Kind: "server", Code: "captcha_timeout", Message: "上游安全验证已取消或超时"}
	}
	a.captcha.mu.Lock()
	if a.captcha.reset {
		clear(a.captcha.scenes)
		a.captcha.reset = false
	}
	a.captcha.mu.Unlock()
	scene, failure := a.captchaScene(ctx, account)
	if failure != nil {
		return nil, failure
	}
	if force && !truth(scene["enabled"]) {
		return nil, &Failure{Kind: "risk", Code: "captcha_scene_disabled", Message: "上游要求安全验证，但未提供可用的验证场景"}
	}
	if !truth(scene["enabled"]) || (!force && mode == "auto" && truth(scene["skip_model_request"])) {
		return headers, nil
	}
	modulePath := a.captchaModulePath(str(options["jsdom_path"]))
	parameters := M{"origin": a.origin, "scene_id": scene["sceneId"], "region": scene["region"], "prefix": scene["prefix"], "simulate_canvas_webgl": first(options["simulate_canvas_webgl"], true), "module_path": modulePath, "timeout_ms": options["timeout_ms"], "proxy_url": selectedProxy(ctx)}
	run := a.captcha.run
	if run == nil {
		run = func(ctx context.Context, parameters M) (M, error) { return a.runCaptcha(ctx, options, parameters) }
	}
	result, err := run(ctx, parameters)
	if err != nil {
		return nil, &Failure{Kind: "server", Code: "captcha_runtime_error", Message: "上游安全验证运行失败，请检查 Node、jsdom 和网络配置"}
	}
	if !truth(result["ok"]) {
		switch str(result["code"]) {
		case "captcha_jsdom_missing":
			return nil, &Failure{Kind: "server", Code: "captcha_jsdom_missing", Message: "未找到 jsdom，请在程序根目录运行 npm --prefix captcha_node ci --omit=dev --ignore-scripts，或配置 http.captcha.jsdom_path"}
		case "captcha_timeout":
			return nil, &Failure{Kind: "server", Code: "captcha_timeout", Message: "上游安全验证超时或需要人工交互"}
		case "captcha_proxy_unsupported":
			return nil, &Failure{Kind: "server", Code: "captcha_proxy_unsupported", Message: "SDK 验证仅支持无认证 HTTP/HTTPS 代理，请使用本机 Mihomo 入口或无认证代理"}
		case "captcha_network_error":
			return nil, &Failure{Kind: "network", Code: "captcha_network_error", Message: "验证码 SDK 资源连接失败", Retryable: true}
		case "captcha_interaction_required":
			return nil, &Failure{Kind: "risk", Code: "captcha_interaction_required", Message: "上游要求人工安全验证，请先在官方客户端完成验证"}
		case "captcha_runtime_error", "captcha_sdk_error", "captcha_origin_invalid", "captcha_canceled":
			return nil, &Failure{Kind: "server", Code: "captcha_runtime_error", Message: "验证码运行环境、页面或网络配置不可用"}
		}
		code := str(result["verify_code"])
		message := "上游安全验证未通过，需要按官方验证流程处理"
		if len(code) == 4 && code[0] == 'F' && code[1] >= '0' && code[1] <= '9' && code[2] >= '0' && code[2] <= '9' && code[3] >= '0' && code[3] <= '9' {
			message += "（" + code + "）"
		}
		return nil, &Failure{Kind: "risk", Code: "captcha_verification_failed", Message: message, VerifyCode: code}
	}
	param := str(result["param"])
	invalid := strings.TrimSpace(param) == "" || len(param) > 32<<10
	for _, character := range param {
		invalid = invalid || character < 0x20 || character == 0x7f
	}
	if invalid {
		return nil, &Failure{Kind: "server", Code: "captcha_invalid_result", Message: "验证码运行时返回了无效票据"}
	}
	// CaptchaVerifyParam is single-use. Cache configuration, never the ticket itself.
	for key, expires := range a.captcha.used {
		if time.Now().After(expires) {
			delete(a.captcha.used, key)
		}
	}
	id := hash(param)
	if _, exists := a.captcha.used[id]; exists {
		return nil, &Failure{Kind: "risk", Code: "captcha_duplicate_result", Message: "验证码运行时返回了已使用的票据，请重新验证"}
	}
	if len(a.captcha.used) >= 2048 {
		return nil, &Failure{Kind: "server", Code: "captcha_capacity_exceeded", Message: "安全验证票据保护缓存已满，请稍后重试"}
	}
	a.captcha.used[id] = time.Now().Add(20 * time.Minute)
	headers.Set("X-Aliyun-Captcha-Verify-Param", param)
	headers.Set("X-Aliyun-Captcha-Verify-Region", str(scene["region"]))
	return headers, nil
}

func (a *App) captchaScene(ctx context.Context, account M) (M, *Failure) {
	version := str(a.config()["app_version"])
	proxy := selectedProxy(ctx)
	key := a.origin + "|" + version + "|" + proxy
	if entry, ok := a.captcha.scenes[key]; ok && time.Now().Before(entry.Expires) {
		return entry.Value, nil
	}
	configCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	endpoint := "/api/v1/client/configs?app_version=" + url.QueryEscape(version) + "&platform=" + url.QueryEscape(platform())
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
		failure := classify(upstreamHTTPFailure(&http.Response{StatusCode: response.Status, Header: response.Header}, response.errorBody))
		if failure.Kind == "risk" || failure.Code == "proxy_auth_failed" {
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
	if len(a.captcha.scenes) >= 32 {
		clear(a.captcha.scenes)
	}
	a.captcha.scenes[key] = captchaScene{Value: scene, Expires: time.Now().Add(10 * time.Minute)}
	return scene, nil
}

type limitedCaptchaOutput struct{ value []byte }

func (w *limitedCaptchaOutput) Write(p []byte) (int, error) {
	if len(w.value)+len(p) > 64<<10 {
		return 0, fmt.Errorf("captcha runtime output limit exceeded")
	}
	w.value = append(w.value, p...)
	return len(p), nil
}

func (a *App) runCaptcha(ctx context.Context, options, parameters M) (M, error) {
	directory := filepath.Join(a.data, "runtime")
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	helper := filepath.Join(directory, "captcha.cjs")
	if err := os.WriteFile(helper, captchaRuntime, 0600); err != nil {
		return nil, err
	}
	node, err := a.resolveCaptchaNode(str(options["node_path"]))
	if err != nil {
		return nil, err
	}
	command := exec.CommandContext(ctx, node, helper)
	hideChildWindow(command)
	command.Stdin = strings.NewReader(js(parameters))
	output := &limitedCaptchaOutput{}
	command.Stdout, command.Stderr = output, io.Discard
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
	if f.Status != 401 && f.Status != 403 {
		return false
	}
	switch f.Code {
	case "CAPTCHA_VERIFY_FAILED", "coding_plan_security_verification_required", "captcha_expired", "captcha_invalid", "F008", "F018", "F019":
		return true
	}
	return false
}

func (a *App) invalidateCaptchaScene() {
	a.captcha.mu.Lock()
	a.captcha.reset = true
	a.captcha.mu.Unlock()
}

func validateCaptchaOptions(value any) error {
	options, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("http.captcha 必须为配置对象")
	}
	for key, value := range options {
		switch key {
		case "mode":
			if value != "off" && value != "auto" && value != "always" {
				return fmt.Errorf("http.captcha.mode 须为 off/auto/always")
			}
		case "simulate_canvas_webgl":
			if _, ok := value.(bool); !ok {
				return fmt.Errorf("http.captcha.simulate_canvas_webgl 必须为布尔值")
			}
		case "node_path", "jsdom_path":
			text, ok := value.(string)
			if !ok || strings.ContainsAny(text, "\r\n\x00") || (key == "node_path" && strings.TrimSpace(text) == "") {
				return fmt.Errorf("http.captcha.%s 路径无效", key)
			}
		case "timeout_ms":
			switch value.(type) {
			case int, int64, float64, json.Number:
			default:
				return fmt.Errorf("http.captcha.timeout_ms 须为 5000 到 120000 的整数")
			}
			n := num(value)
			if n < 5000 || n > 120000 || n != math.Trunc(n) {
				return fmt.Errorf("http.captcha.timeout_ms 须为 5000 到 120000 的整数")
			}
		default:
			return fmt.Errorf("未知安全验证设置 http.captcha.%s", key)
		}
	}
	return nil
}
