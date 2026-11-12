package server

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

//go:embed captcha_browser.cjs
var captchaBrowserRuntime []byte

const claimBrowserWarmup = 3 * time.Minute
const claimBrowserIdle = 30 * time.Second

// The configured runtime applies only to manual claims. Scheduled claims
// always use Chromium and have their own ticket scope.
func captchaUsesBrowser(purpose string, options M) bool {
	return purpose == captchaPurposeScheduledClaim || (purpose == captchaPurposeClaim && options["claim_runtime"] == "browser")
}

type claimBrowserProcess struct {
	key     string
	input   io.WriteCloser
	replies chan M
	done    chan struct{}
	cancel  context.CancelFunc
	active  bool
}

type claimBrowserManager struct {
	mu        sync.Mutex
	process   *claimBrowserProcess
	window    time.Time
	timer     *time.Timer
	epoch     uint64
	installMu sync.Mutex
	filesMu   sync.Mutex
}

func (a *App) browserParameters(options M) M {
	return M{"root": a.root, "module_path": a.captchaModulePath(str(options["jsdom_path"])), "playwright_path": str(options["playwright_path"]), "driver_path": filepath.Join(a.data, "tools", "playwright"), "browser_cache": filepath.Join(a.data, "tools", "chromium"), "browser_path": str(options["browser_path"])}
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
	return a.writeCaptchaRuntime("captcha_browser.cjs", captchaBrowserRuntime)
}

// Requests are serialized by the existing captcha slot. Only the lifecycle
// mutex is shared with settings, shutdown and the idle timer.
func (a *App) runBrowserCaptcha(ctx context.Context, options, parameters M) (result M, err error) {
	manager := &a.claimBrowser
	manager.mu.Lock()
	if ctx.Err() != nil || a.ctx.Err() != nil {
		manager.mu.Unlock()
		return nil, context.Canceled
	}
	key := js([]any{options["node_path"], options["playwright_path"], options["browser_path"]})
	if manager.process != nil && manager.process.key != key {
		a.stopClaimBrowserLocked()
	}
	if manager.process == nil {
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
		if err != nil || ctx.Err() != nil || result["code"] == "captcha_playwright_missing" || result["code"] == "captcha_browser_missing" || result["code"] == "captcha_browser_launch_failed" {
			a.stopClaimBrowserLocked()
		} else {
			a.armClaimBrowserLocked()
		}
	}()
	if _, err = io.WriteString(process.input, js(merge(a.browserParameters(options), parameters))+"\n"); err != nil {
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

func (a *App) startClaimBrowser(options M, key string) (*claimBrowserProcess, error) {
	node, err := a.resolveCaptchaNode(str(options["node_path"]))
	if err != nil {
		return nil, err
	}
	helper, err := a.browserHelper()
	if err != nil {
		return nil, err
	}
	if _, err = a.writeCaptchaRuntime("captcha.cjs", captchaRuntime); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(a.ctx)
	command := exec.CommandContext(ctx, node, helper)
	prepareBrowserProcess(command)
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
	process := &claimBrowserProcess{key: key, input: input, replies: make(chan M, 1), done: make(chan struct{}), cancel: cancel}
	command.Cancel = func() error {
		_ = input.Close() // EOF lets Playwright close Chromium and its children.
		time.AfterFunc(3*time.Second, func() {
			select {
			case <-process.done:
			default:
				killBrowserProcess(command)
			}
		})
		return nil
	}
	command.WaitDelay = 5 * time.Second
	if err = command.Start(); err != nil {
		cancel()
		input.Close()
		output.Close()
		return nil, err
	}
	a.background(func() {
		defer cancel()
		defer close(process.done)
		defer close(process.replies)
		scanner := bufio.NewScanner(output)
		scanner.Buffer(make([]byte, 4096), 64<<10)
		for scanner.Scan() {
			result, valid := parse(scanner.Text(), nil).(map[string]any)
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
	})
	return process, nil
}

func (a *App) armClaimBrowserLocked() {
	manager := &a.claimBrowser
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
			a.stopClaimBrowserLocked()
		}
	})
}

func (a *App) stopClaimBrowserLocked() {
	a.claimBrowser.epoch++
	if a.claimBrowser.timer != nil {
		a.claimBrowser.timer.Stop()
	}
	if process := a.claimBrowser.process; process != nil {
		process.cancel()
		a.claimBrowser.process = nil
	}
}

func (a *App) closeClaimBrowser() {
	a.claimBrowser.mu.Lock()
	defer a.claimBrowser.mu.Unlock()
	a.claimBrowser.window = time.Time{}
	a.stopClaimBrowserLocked()
}

func (a *App) claimBrowserWindow(until time.Time) {
	a.claimBrowser.mu.Lock()
	defer a.claimBrowser.mu.Unlock()
	a.claimBrowser.window = until
	if until.IsZero() && a.claimBrowser.process != nil && !a.claimBrowser.process.active {
		a.stopClaimBrowserLocked()
	} else {
		a.armClaimBrowserLocked()
	}
}

func (a *App) warmClaimBrowser(ctx context.Context, options M, due time.Time) error {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	if !a.acquireCaptchaSlot(ctx, true) {
		return &Failure{Kind: "server", Code: "captcha_browser_timeout", Message: "等待浏览器准备超时", Retryable: true}
	}
	defer a.releaseCaptchaSlot()
	a.claimBrowserWindow(due.Add(dailyClaimRecoveryWindow))
	result, err := a.runBrowserCaptcha(ctx, options, M{"action": "warm"})
	if ctx.Err() != nil {
		return &Failure{Kind: "server", Code: "captcha_browser_timeout", Message: "等待浏览器准备超时", Retryable: true}
	}
	code := str(fallback(result["code"], "captcha_browser_launch_failed"))
	if err != nil {
		code = "captcha_browser_launch_failed"
	}
	if err != nil || !truth(result["ok"]) {
		return &Failure{Kind: "server", Code: code, Message: conciseFailureMessage(&Failure{Code: code})}
	}
	return nil
}
