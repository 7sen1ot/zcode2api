package server

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// This release supports the project's Node 18 runtime. Core never downloads a
// browser on its own; the separate CLI install below requires an explicit choice.
const claimPlaywrightVersion = "1.58.2"

func captchaNPM(node string) string {
	directories := []string{filepath.Dir(node)}
	if npm, err := exec.LookPath("npm"); err == nil {
		if target, err := filepath.EvalSymlinks(npm); err == nil && filepath.Base(target) == "npm-cli.js" {
			return target
		}
		directories = append(directories, filepath.Dir(npm))
	}
	for _, directory := range directories {
		for _, relative := range []string{"node_modules/npm/bin/npm-cli.js", "../lib/node_modules/npm/bin/npm-cli.js", "../share/nodejs/npm/bin/npm-cli.js"} {
			file := filepath.Clean(filepath.Join(directory, relative))
			if info, err := os.Stat(file); err == nil && !info.IsDir() {
				return file
			}
		}
	}
	return ""
}

func (a *App) installClaimBrowser(parent context.Context, options M, downloadBrowser bool) (M, error) {
	if !a.claimBrowser.installMu.TryLock() {
		return nil, fmt.Errorf("浏览器运行时正在安装")
	}
	defer a.claimBrowser.installMu.Unlock()
	ctx, cancel := context.WithTimeout(parent, 10*time.Minute)
	defer cancel()
	stop := context.AfterFunc(a.ctx, cancel)
	defer stop()
	status := a.captchaRuntimeStatus(ctx, options)
	if !truth(obj(status["node"])["available"]) {
		return nil, fmt.Errorf("请先配置可用的 Node.js 18 或以上版本")
	}
	if str(options["playwright_path"]) != "" && !truth(obj(status["playwright"])["available"]) {
		return nil, fmt.Errorf("请修正 Playwright 依赖目录，或清空后安装托管驱动")
	}
	if str(options["browser_path"]) != "" && !truth(obj(status["browser"])["installed"]) {
		return nil, fmt.Errorf("请修正浏览器路径，或清空路径后使用自动检测")
	}
	node := str(obj(status["node"])["path"])
	parameters := a.browserParameters(options)
	run := func(args ...string) error {
		command := exec.CommandContext(ctx, node, args...)
		prepareBrowserProcess(command)
		command.Env = append(os.Environ(), "PLAYWRIGHT_BROWSERS_PATH="+str(parameters["browser_cache"]), "PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1")
		command.Cancel = func() error { killBrowserProcess(command); return nil }
		command.WaitDelay = 2 * time.Second
		return command.Run()
	}
	if !truth(obj(status["playwright"])["available"]) {
		npm := captchaNPM(node)
		if npm == "" {
			return nil, fmt.Errorf("未找到 npm，请安装 Node.js 完整发行版，或手动提供 playwright-core")
		}
		directory := str(parameters["driver_path"])
		if err := os.MkdirAll(directory, 0700); err != nil {
			return nil, err
		}
		if err := run(npm, "--prefix", directory, "install", "--ignore-scripts", "--no-audit", "--no-fund", "--save-exact", "playwright-core@"+claimPlaywrightVersion); err != nil {
			return nil, fmt.Errorf("Playwright 驱动安装失败，请检查网络、磁盘空间与 Node.js/npm：%w", err)
		}
		status = a.captchaRuntimeStatus(ctx, options)
	}
	driver := obj(status["playwright"])
	if !truth(driver["available"]) {
		return nil, fmt.Errorf("Playwright 驱动无法加载")
	}
	// Recheck after installing the driver; its existing browser cache may suffice.
	if downloadBrowser && !truth(obj(status["browser"])["installed"]) {
		if err := run(filepath.Join(str(driver["path"]), "cli.js"), "install", "chromium", "--only-shell"); err != nil {
			return nil, fmt.Errorf("Chromium 安装失败，请检查网络、磁盘空间与系统运行依赖：%w", err)
		}
		status = a.captchaRuntimeStatus(ctx, options)
		if !truth(obj(status["browser"])["installed"]) {
			return nil, fmt.Errorf("下载完成后未找到 Chromium，请重新检测运行时")
		}
	}
	return status, nil
}
