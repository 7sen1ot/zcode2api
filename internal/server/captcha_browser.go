package server

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
	captchasdk "zcode2api/internal/server/captcha"
	"zcode2api/internal/server/fault"
	childproc "zcode2api/internal/server/process"
	routing "zcode2api/internal/server/proxy"
	"zcode2api/internal/server/shared"
	"zcode2api/internal/server/storage"
)

const claimBrowserWarmup = 3 * time.Minute

const claimBrowserIdle = 30 * time.Second

const claimBrowserLaunchTimeout = time.Minute

// A cold browser can try native, then Playwright, before SDK verification starts.
const claimBrowserStartupBudget = 2*claimBrowserLaunchTimeout + 10*time.Second

func captchaOperationTimeout(options shared.M, purpose string) time.Duration {
	timeout := shared.Ms(shared.Fallback(options["timeout_ms"], 30000)) + 3*time.Second
	if captchasdk.CaptchaUsesBrowser(purpose, options) {
		timeout += claimBrowserStartupBudget
	}
	return timeout
}

type claimBrowserProcess struct {
	key        string
	profileDir string
	input      io.WriteCloser
	replies    chan shared.M
	done       chan struct{}
	cancel     context.CancelFunc
	active     bool
}

type claimBrowserManager struct {
	mu        sync.Mutex
	process   *claimBrowserProcess
	stopping  <-chan struct{}
	slot      chan struct{}
	window    time.Time
	timer     *time.Timer
	epoch     uint64
	installMu sync.Mutex
	filesMu   sync.Mutex
	prepared  *preparedClaimRoute
}

type preparedClaimRoute struct {
	scope    captchaTicketScope
	proxyURL string
	expires  time.Time
	release  func()
}

type claimBrowserContextKey struct{}

func (a *App) claimBrowserForContext(ctx context.Context) *claimBrowserManager {
	if manager, ok := ctx.Value(claimBrowserContextKey{}).(*claimBrowserManager); ok {
		return manager
	}
	return &a.claimBrowser
}

// Claim browsers use their own slots; inference and prefetch share foreground priority.
func (a *App) acquireClaimCaptchaSlot(ctx context.Context, background bool) (func(), bool) {
	manager := a.claimBrowserForContext(ctx)
	if manager == &a.claimBrowser {
		if !a.acquireCaptchaSlot(ctx, background) {
			return nil, false
		}
		return a.releaseCaptchaSlot, true
	}
	manager.mu.Lock()
	if manager.slot == nil {
		manager.slot = make(chan struct{}, 1)
	}
	slot := manager.slot
	manager.mu.Unlock()
	select {
	case slot <- struct{}{}:
		return func() { <-slot }, true
	case <-ctx.Done():
		return nil, false
	}
}

func (a *App) browserParameters(options shared.M) shared.M {
	return shared.M{"root": a.root, "module_path": a.captchaModulePath(shared.Str(options["jsdom_path"])), "playwright_path": shared.Str(options["playwright_path"]), "driver_path": filepath.Join(a.data, "tools", "playwright"), "browser_cache": filepath.Join(a.data, "tools", "chromium"), "browser_path": shared.Str(options["browser_path"]), "browser_backend": shared.Str(options["browser_backend"]), "browser_headless": options["browser_headless"], "browser_display": shared.Str(options["browser_display"]), "browser_launch_timeout_ms": claimBrowserLaunchTimeout.Milliseconds()}
}

func (a *App) writeCaptchaRuntime(name string, source []byte) (string, error) {
	a.claimBrowser.filesMu.Lock()
	defer a.claimBrowser.filesMu.Unlock()
	directory := filepath.Join(a.data, "runtime")
	file := filepath.Join(directory, name)
	if existing, err := os.ReadFile(file); err == nil && bytes.Equal(existing, source) {
		return file, nil
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return "", err
	}
	return file, os.WriteFile(file, source, 0600)
}

func (a *App) browserHelper() (string, error) {
	return a.writeCaptchaRuntime("captcha_browser.cjs", captchasdk.CaptchaBrowserRuntime)
}

func (a *App) runBrowserCaptcha(ctx context.Context, options, parameters shared.M) (result shared.M, err error) {
	manager := a.claimBrowserForContext(ctx)
	manager.mu.Lock()
	if ctx.Err() != nil || a.ctx.Err() != nil {
		manager.mu.Unlock()
		return nil, context.Canceled
	}
	key := shared.JSON([]any{options["node_path"], options["playwright_path"], options["browser_path"], options["browser_backend"], options["browser_headless"], options["browser_display"]})
	if manager.process != nil && manager.process.key != key {
		a.stopClaimBrowserLocked(manager)
	}
	if manager.process == nil {
		if manager.stopping != nil {
			select {
			case <-manager.stopping:
				manager.stopping = nil
			case <-ctx.Done():
				manager.mu.Unlock()
				return nil, ctx.Err()
			}
		}
		manager.process, err = a.startClaimBrowser(options, key)
		if err != nil {
			manager.mu.Unlock()
			return nil, err
		}
	}
	process := manager.process
	process.active = true
	if manager.timer != nil {
		manager.timer.Stop()
	}
	manager.mu.Unlock()
	stop := context.AfterFunc(ctx, process.cancel)
	defer stop()
	defer func() {
		manager.mu.Lock()
		defer manager.mu.Unlock()
		process.active = false
		if manager.process != process {
			return
		}
		if err != nil || ctx.Err() != nil || result["code"] == "captcha_playwright_missing" || result["code"] == "captcha_browser_missing" || result["code"] == "captcha_browser_launch_failed" || result["code"] == "captcha_display_unavailable" {
			a.stopClaimBrowserLocked(manager)
		} else {
			a.armClaimBrowserLocked(manager)
		}
	}()
	parameters = shared.Merge(a.browserParameters(options), parameters)
	parameters["browser_profiles"] = process.profileDir
	if _, err = io.WriteString(process.input, shared.JSON(parameters)+"\n"); err != nil {
		return nil, err
	}
	select {
	case result, open := <-process.replies:
		if !open {
			return nil, fmt.Errorf("浏览器验证进程已退出")
		}
		return result, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (a *App) startClaimBrowser(options shared.M, key string) (*claimBrowserProcess, error) {
	node, err := a.resolveCaptchaNode(shared.Str(options["node_path"]))
	if err != nil {
		return nil, err
	}
	helper, err := a.browserHelper()
	if err != nil {
		return nil, err
	}
	if _, err = a.writeCaptchaRuntime("captcha.cjs", captchasdk.CaptchaRuntime); err != nil {
		return nil, err
	}
	if _, err = a.writeCaptchaRuntime("captcha_native.cjs", captchasdk.CaptchaNativeRuntime); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(a.ctx)
	command := exec.CommandContext(ctx, node, helper)
	childproc.PrepareBrowserProcess(command)
	command.Stderr = io.Discard
	input, err := command.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	output, err := command.StdoutPipe()
	if err != nil {
		cancel()
		input.Close()
		return nil, err
	}
	process := &claimBrowserProcess{key: key, input: input, replies: make(chan shared.M, 1), done: make(chan struct{}), cancel: cancel}
	profiles := filepath.Join(a.data, "browser-profiles")
	if err = os.MkdirAll(profiles, 0700); err == nil {
		process.profileDir, err = os.MkdirTemp(profiles, "worker-")
	}
	if err != nil {
		cancel()
		input.Close()
		output.Close()
		return nil, err
	}
	exited := make(chan struct{})
	command.Cancel = func() error {
		_ = input.Close() // EOF lets the worker close the browser and clean up its profile.
		time.AfterFunc(3*time.Second, func() {
			select {
			case <-exited:
			default:
				childproc.KillBrowserProcess(command)
			}
		})
		return nil
	}
	command.WaitDelay = 5 * time.Second
	if err = command.Start(); err != nil {
		cancel()
		input.Close()
		output.Close()
		_ = os.Remove(process.profileDir)
		return nil, err
	}
	a.background(func() {
		defer cancel()
		defer close(process.done)
		defer close(process.replies)
		scanner := bufio.NewScanner(output)
		scanner.Buffer(make([]byte, 4096), 64<<10)
		for scanner.Scan() {
			result, valid := shared.Parse(scanner.Text(), nil).(map[string]any)
			if !valid {
				break
			}
			select {
			case process.replies <- result:
			case <-ctx.Done():
			}
		}
		cancel()
		_ = command.Wait()
		close(exited)
		// Reap the worker's private directory even if it was stopped forcibly.
		for attempt := range 3 {
			err := os.RemoveAll(process.profileDir)
			if err == nil {
				break
			}
			if attempt == 2 {
				a.log.Printf("清理浏览器配置目录失败: %v", err)
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
	})
	return process, nil
}

func (a *App) armClaimBrowserLocked(manager *claimBrowserManager) {
	manager.epoch++
	epoch := manager.epoch
	if manager.timer != nil {
		manager.timer.Stop()
	}
	process := manager.process
	if process == nil || process.active {
		return
	}
	delay := claimBrowserIdle
	if manager.window.After(time.Now()) {
		delay = max(0, time.Until(manager.window))
	}
	manager.timer = time.AfterFunc(delay, func() {
		manager.mu.Lock()
		defer manager.mu.Unlock()
		if manager.epoch == epoch && manager.process == process && !process.active {
			a.stopClaimBrowserLocked(manager)
		}
	})
}

func (a *App) stopClaimBrowserLocked(manager *claimBrowserManager) {
	if manager.prepared != nil {
		manager.prepared.release()
		manager.prepared = nil
	}
	manager.epoch++
	if manager.timer != nil {
		manager.timer.Stop()
	}
	if process := manager.process; process != nil {
		process.cancel()
		manager.stopping = process.done
		manager.process = nil
	}
}

func (a *App) closeClaimBrowser() {
	for _, manager := range []*claimBrowserManager{&a.claimBrowser, &a.manualClaimBrowser, &a.scheduledClaimBrowser} {
		manager.mu.Lock()
		manager.window = time.Time{}
		a.stopClaimBrowserLocked(manager)
		manager.mu.Unlock()
	}
}

func (a *App) claimBrowserWindow(until time.Time) {
	manager := &a.scheduledClaimBrowser
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.window = until
	if until.IsZero() && manager.process != nil && !manager.process.active {
		a.stopClaimBrowserLocked(manager)
	} else {
		a.armClaimBrowserLocked(manager)
	}
}

func (a *App) warmClaimBrowser(ctx context.Context, options shared.M, due time.Time) error {
	a.claimBrowserWindow(due.Add(dailyClaimRecoveryWindow))
	ctx, cancel := context.WithTimeout(ctx, claimBrowserStartupBudget)
	defer cancel()
	release, acquired := a.acquireClaimCaptchaSlot(ctx, true)
	if !acquired {
		return &fault.Failure{Kind: "server", Code: "captcha_browser_timeout", Message: "等待浏览器准备超时", Retryable: true}
	}
	defer release()
	result, err := a.runBrowserCaptcha(ctx, options, shared.M{"action": "warm"})
	if ctx.Err() != nil {
		return &fault.Failure{Kind: "server", Code: "captcha_browser_timeout", Message: "等待浏览器准备超时", Retryable: true}
	}
	code := shared.Str(shared.Fallback(result["code"], "captcha_browser_launch_failed"))
	if err != nil {
		code = "captcha_browser_launch_failed"
	}
	if err != nil || !shared.Truth(result["ok"]) {
		reason := shared.Str(result["browser_launch_reason"])
		retryable := code == "captcha_browser_launch_failed" && reason != "sandbox" && reason != "system_library" && reason != "executable_missing"
		return &fault.Failure{Kind: "server", Code: code, Message: fault.ConciseFailureMessage(&fault.Failure{Code: code}), Retryable: retryable, Detail: shared.M{"stage": "browser_launch", "browser_launch_reason": reason, "fallback_code": result["fallback_code"]}}
	}
	return nil
}

// Prepare the first account without requesting a verification ticket or card.
func (a *App) prepareScheduledClaimBrowser(parent context.Context, due time.Time) error {
	ctx, cancel := context.WithDeadline(parent, due.Add(-5*time.Second))
	defer cancel()
	release, err := a.acquireClaimSlot(ctx)
	if err != nil {
		return err
	}
	defer release()
	ctx = context.WithValue(ctx, claimBrowserContextKey{}, &a.scheduledClaimBrowser)
	a.claimBrowserWindow(due.Add(dailyClaimRecoveryWindow))
	accounts, err := storage.Query(a.db, "SELECT * FROM accounts WHERE status IN ('active','unverified','cooldown') ORDER BY created_at,id")
	if err != nil {
		return err
	}
	options := shared.Obj(a.configValue("http", "captcha"))
	for _, value := range accounts {
		// Leave enough time to prepare without canceling the browser at claim time.
		if ctx.Err() != nil || !a.scheduledClaimEnabled(due) || time.Until(due) <= shared.Ms(shared.Fallback(options["timeout_ms"], 30000))+5*time.Second {
			break
		}
		account := shared.Obj(value)
		if a.accountTokens(account)["zcode_jwt"] == "" {
			continue
		}
		return a.prepareClaimBrowser(ctx, options, account)
	}
	return nil
}

func (a *App) prepareClaimBrowser(parent context.Context, options, account shared.M) error {
	ctx, cancel := context.WithTimeout(parent, captchaOperationTimeout(options, captchasdk.CaptchaPurposeScheduledClaim))
	defer cancel()
	ctx, release, err := a.pinOutboundProxy(ctx, account)
	if err != nil {
		return err
	}
	defer release()
	scene, failure := a.captchaScene(ctx, account)
	if failure != nil {
		return failure
	}
	if !shared.Truth(scene["enabled"]) {
		return nil
	}
	scope, failure := a.captchaScope(ctx, account, captchasdk.CaptchaPurposeScheduledClaim, scene)
	if failure != nil {
		return failure
	}
	if a.captchaPrefetchBlocked(scope) {
		return nil
	}
	releaseSlot, acquired := a.acquireClaimCaptchaSlot(ctx, true)
	if !acquired {
		return ctx.Err()
	}
	defer releaseSlot()
	if !a.captchaScopeCurrent(ctx, scope) || a.captchaPrefetchBlocked(scope) {
		return nil
	}
	parameters, _ := a.captchaRuntimeParameters(ctx, options, scene, scope)
	parameters["action"] = "prepare"
	result, err := a.runBrowserCaptcha(ctx, options, parameters)
	if err != nil {
		return err
	}
	if !shared.Truth(result["ok"]) {
		code := shared.Str(shared.Fallback(result["code"], "captcha_runtime_error"))
		return &fault.Failure{Kind: "server", Code: code, Message: fault.ConciseFailureMessage(&fault.Failure{Code: code})}
	}
	retain, retained := a.retainCaptchaProxy(routing.SelectedProxy(ctx))
	if !retained {
		return captchasdk.CaptchaScopeChanged()
	}
	manager := a.claimBrowserForContext(ctx)
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if ctx.Err() != nil || manager.process == nil || !manager.window.After(time.Now()) || !a.captchaScopeCurrent(ctx, scope) {
		retain()
		return captchasdk.CaptchaScopeChanged()
	}
	if manager.prepared != nil {
		manager.prepared.release()
	}
	manager.prepared = &preparedClaimRoute{scope: scope, proxyURL: routing.SelectedProxy(ctx), expires: manager.window, release: retain}
	return nil
}

func (a *App) pinScheduledClaimProxy(ctx context.Context, account shared.M) (context.Context, func(), error) {
	manager := a.claimBrowserForContext(ctx)
	manager.mu.Lock()
	prepared := manager.prepared
	manager.prepared = nil
	manager.mu.Unlock()
	if prepared != nil {
		pinned := routing.PinnedProxyContext(ctx, prepared.proxyURL)
		if prepared.scope.pooled {
			pinned = context.WithValue(pinned, routing.ProxySelectionKey{}, prepared.scope.route)
		}
		if prepared.scope.accountID == shared.Str(account["id"]) && time.Now().Before(prepared.expires) && a.captchaScopeCurrent(pinned, prepared.scope) && !a.captchaPrefetchBlocked(prepared.scope) {
			return pinned, prepared.release, nil
		}
		prepared.release()
	}
	return a.pinOutboundProxy(ctx, account)
}
