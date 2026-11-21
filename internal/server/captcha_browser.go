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
	"zcode2api/internal/server/shared"
	"zcode2api/internal/server/storage"
)

const claimBrowserWarmup = 3 * time.Minute

const claimBrowserIdle = 30 * time.Second

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
}

type scheduledClaimWorkerKey struct{}

func (a *App) claimBrowserForContext(ctx context.Context) *claimBrowserManager {
	if worker, ok := ctx.Value(scheduledClaimWorkerKey{}).(int); ok && worker >= 0 && worker < dailyClaimConcurrency {
		return &a.scheduledClaimBrowsers[worker]
	}
	if worker, ok := ctx.Value(manualClaimWorkerKey{}).(int); ok && worker >= 0 && worker < manualClaimConcurrency {
		return &a.manualClaimBrowsers[worker]
	}
	return &a.claimBrowser
}

// Each claim worker owns one serial verification channel. Inference and
// background prefetch retain their shared slot and foreground priority.
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
	return shared.M{"root": a.root, "module_path": a.captchaModulePath(shared.Str(options["jsdom_path"])), "playwright_path": shared.Str(options["playwright_path"]), "driver_path": filepath.Join(a.data, "tools", "playwright"), "browser_cache": filepath.Join(a.data, "tools", "chromium"), "browser_path": shared.Str(options["browser_path"]), "browser_backend": shared.Str(options["browser_backend"]), "browser_headless": options["browser_headless"], "browser_display": shared.Str(options["browser_display"])}
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
	managers := []*claimBrowserManager{&a.claimBrowser}
	for i := range a.manualClaimBrowsers {
		managers = append(managers, &a.manualClaimBrowsers[i])
	}
	for i := range a.scheduledClaimBrowsers {
		managers = append(managers, &a.scheduledClaimBrowsers[i])
	}
	for _, manager := range managers {
		manager.mu.Lock()
		manager.window = time.Time{}
		a.stopClaimBrowserLocked(manager)
		manager.mu.Unlock()
	}
}

func (a *App) claimBrowserWindow(until time.Time) {
	for i := range a.scheduledClaimBrowsers {
		manager := &a.scheduledClaimBrowsers[i]
		manager.mu.Lock()
		manager.window = until
		if until.IsZero() && manager.process != nil && !manager.process.active {
			a.stopClaimBrowserLocked(manager)
		} else {
			a.armClaimBrowserLocked(manager)
		}
		manager.mu.Unlock()
	}
}

func (a *App) warmClaimBrowser(ctx context.Context, options shared.M, due time.Time) error {
	a.claimBrowserWindow(due.Add(dailyClaimRecoveryWindow))
	errors := make([]error, dailyClaimConcurrency)
	shared.ParallelFor(dailyClaimConcurrency, dailyClaimConcurrency, func(worker int) {
		workerCtx := context.WithValue(ctx, scheduledClaimWorkerKey{}, worker)
		errors[worker] = a.warmClaimBrowserWorker(workerCtx, options)
	})
	for _, err := range errors {
		if err != nil {
			return err
		}
	}
	return nil
}

func (a *App) warmClaimBrowserWorker(ctx context.Context, options shared.M) error {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
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
		return &fault.Failure{Kind: "server", Code: code, Message: fault.ConciseFailureMessage(&fault.Failure{Code: code})}
	}
	return nil
}

// Prepare account-isolated pages and SDK instances before the scheduled time.
// No verification ticket or billing request is created during this phase.
func (a *App) prepareScheduledClaimBrowsers(parent context.Context, due time.Time) error {
	ctx, cancel := context.WithDeadline(parent, due)
	defer cancel()
	accounts, err := storage.Query(a.db, "SELECT * FROM accounts WHERE status IN ('active','unverified','cooldown') ORDER BY created_at LIMIT 16")
	if err != nil {
		return err
	}
	options := shared.Obj(a.configValue("http", "captcha"))
	for index, value := range accounts {
		// Do not let the preparation deadline kill the shared browser just as
		// the prepared accounts are about to claim.
		if ctx.Err() != nil || !a.scheduledClaimEnabled(due) || time.Until(due) <= shared.Ms(shared.Fallback(options["timeout_ms"], 30000))+5*time.Second {
			break
		}
		account := shared.Obj(value)
		if a.accountTokens(account)["zcode_jwt"] == "" {
			continue
		}
		workerCtx := context.WithValue(ctx, scheduledClaimWorkerKey{}, index%dailyClaimConcurrency)
		if err := a.prepareClaimBrowser(workerCtx, options, account); err != nil && ctx.Err() == nil {
			a.log.Printf("自动领卡账号 %s 验证页面预热失败: %v", shared.Str(account["id"]), err)
		}
	}
	return nil
}

func (a *App) prepareClaimBrowser(parent context.Context, options, account shared.M) error {
	ctx, cancel := context.WithTimeout(parent, shared.Ms(shared.Fallback(options["timeout_ms"], 30000))+3*time.Second)
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
	return nil
}
