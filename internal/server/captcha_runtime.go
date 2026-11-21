package server

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	childproc "zcode2api/internal/server/process"
	"zcode2api/internal/server/shared"
)

func (a *App) captchaModulePath(configured string) string {
	if configured == "" {
		configured = "captcha_node"
	}
	if !filepath.IsAbs(configured) {
		configured = filepath.Join(a.root, configured)
	}
	return filepath.Clean(configured)
}

func (a *App) resolveCaptchaNode(configured string) (string, error) {
	if configured == "" {
		configured = "node"
	}
	if configured == "node" {
		return childproc.ResolveExecutable(a.root, configured, filepath.Join(a.root, "node"))
	}
	return childproc.ResolveExecutable(a.root, configured)
}

// Check the configured runtime without downloading the SDK or requesting tickets.
const captchaRuntimeProbe = `
const fs = require('node:fs'), path = require('node:path');
const input = JSON.parse(fs.readFileSync(0, 'utf8'));
const result = {node_version: process.versions.node, jsdom_available: false, jsdom_version: '', ...require(input.browser_helper).probe(input)};
if (Number(process.versions.node.split('.')[0]) >= 18) {
  try {
    const target = path.join(input.module_path, 'node_modules', 'jsdom');
    const library = require(target);
    result.jsdom_available = typeof library.JSDOM === 'function';
    try { result.jsdom_version = JSON.parse(fs.readFileSync(path.join(target, 'package.json'), 'utf8')).version || ''; } catch {}
  } catch {}
}
process.stdout.write(JSON.stringify(result));
`

func (a *App) captchaRuntimeStatus(parent context.Context, options shared.M) shared.M {
	modulePath := a.captchaModulePath(shared.Str(options["jsdom_path"]))
	jsdomPath := filepath.Join(modulePath, "node_modules", "jsdom")
	info, statErr := os.Stat(jsdomPath)
	jsdom := shared.M{"installed": statErr == nil && info.IsDir(), "available": false, "path": jsdomPath, "message": "未找到 jsdom"}
	if shared.Truth(jsdom["installed"]) {
		jsdom["message"] = "需先配置 Node.js"
	}
	node := shared.M{"installed": false, "available": false, "message": "未找到 Node.js"}
	result := shared.M{"ok": true, "ready": false, "node": node, "jsdom": jsdom, "playwright": shared.M{"available": false, "message": "需先配置 Node.js"}, "browser": shared.M{"installed": false, "message": "需先配置 Node.js 后检测"}, "browser_ready": false, "install_available": false}
	binary, err := a.resolveCaptchaNode(shared.Str(options["node_path"]))
	if err != nil {
		return result
	}
	node["installed"], node["path"], node["message"] = true, binary, "Node.js 无法运行"
	ctx, cancel := a.operationContext(parent, 5*time.Second)
	defer cancel()
	helper, err := a.browserHelper()
	if err != nil {
		node["message"] = "无法准备运行时检测脚本"
		return result
	}
	parameters := a.browserParameters(options)
	parameters["browser_helper"] = helper
	command, output := childproc.NewJSONCommand(ctx, binary, parameters, "--input-type=commonjs", "-e", captchaRuntimeProbe)
	command.Dir = a.root
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			node["message"] = "Node.js 检测超时或已取消"
		}
		return result
	}
	probe := shared.Obj(shared.Parse(string(output.Value), nil))
	version := shared.Str(probe["node_version"])
	major, err := strconv.Atoi(strings.SplitN(version, ".", 2)[0])
	if err != nil {
		return result
	}
	node["version"] = version
	if major < 18 {
		node["message"] = "需要 Node.js 18 或以上版本"
		return result
	}
	node["available"], node["message"] = true, "可用"
	result["playwright"], result["browser"] = probe["playwright"], probe["browser"]
	shared.CopyFields(result, probe, "browser_ready", "browser_backend", "browser_headless", "native_browser", "display")
	result["install_available"] = shared.Truth(shared.Obj(probe["playwright"])["available"]) || captchaNPM(binary) != ""
	if shared.Str(options["playwright_path"]) != "" && !shared.Truth(shared.Obj(probe["playwright"])["available"]) {
		result["install_available"] = false
	} else if !shared.Truth(result["install_available"]) {
		shared.Obj(result["playwright"])["message"] = "未找到 Playwright 和 npm，请指定已有 Playwright 依赖目录或配置完整 Node.js"
	}
	result["browser_cache"] = parameters["browser_cache"]
	jsdom["available"] = shared.Truth(probe["jsdom_available"])
	if shared.Truth(jsdom["available"]) {
		jsdom["installed"], jsdom["version"], jsdom["message"] = true, probe["jsdom_version"], "可用"
	} else if shared.Truth(jsdom["installed"]) {
		jsdom["message"] = "jsdom 无法加载，请检查依赖"
	}
	result["ready"] = shared.Truth(jsdom["available"])
	return result
}

// Keep the driver compatible with Node.js 18.
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

func (a *App) installClaimBrowser(parent context.Context, options shared.M, downloadBrowser bool) (shared.M, error) {
	if !a.claimBrowser.installMu.TryLock() {
		return nil, fmt.Errorf("浏览器运行时正在安装")
	}
	defer a.claimBrowser.installMu.Unlock()
	ctx, cancel := a.operationContext(parent, 10*time.Minute)
	defer cancel()
	status := a.captchaRuntimeStatus(ctx, options)
	if !shared.Truth(shared.Obj(status["node"])["available"]) {
		return nil, fmt.Errorf("请先配置可用的 Node.js 18 或以上版本")
	}
	if shared.Str(options["playwright_path"]) != "" && !shared.Truth(shared.Obj(status["playwright"])["available"]) {
		return nil, fmt.Errorf("请修正 Playwright 依赖目录，或清空后安装托管驱动")
	}
	if shared.Str(options["browser_path"]) != "" && !shared.Truth(shared.Obj(status["browser"])["installed"]) {
		return nil, fmt.Errorf("请修正浏览器路径，或清空路径后使用自动检测")
	}
	node := shared.Str(shared.Obj(status["node"])["path"])
	parameters := a.browserParameters(options)
	run := func(args ...string) error {
		command := exec.CommandContext(ctx, node, args...)
		childproc.PrepareBrowserProcess(command)
		command.Env = append(os.Environ(), "PLAYWRIGHT_BROWSERS_PATH="+shared.Str(parameters["browser_cache"]), "PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1")
		command.Cancel = func() error { childproc.KillBrowserProcess(command); return nil }
		command.WaitDelay = 2 * time.Second
		return command.Run()
	}
	if !shared.Truth(shared.Obj(status["playwright"])["available"]) {
		npm := captchaNPM(node)
		if npm == "" {
			return nil, fmt.Errorf("未找到 npm，请安装 Node.js 完整发行版，或手动提供 playwright-core")
		}
		directory := shared.Str(parameters["driver_path"])
		if err := os.MkdirAll(directory, 0700); err != nil {
			return nil, err
		}
		if err := run(npm, "--prefix", directory, "install", "--ignore-scripts", "--no-audit", "--no-fund", "--save-exact", "playwright-core@"+claimPlaywrightVersion); err != nil {
			return nil, fmt.Errorf("Playwright 驱动安装失败，请检查网络、磁盘空间与 Node.js/npm：%w", err)
		}
		status = a.captchaRuntimeStatus(ctx, options)
	}
	driver := shared.Obj(status["playwright"])
	if !shared.Truth(driver["available"]) {
		return nil, fmt.Errorf("Playwright 驱动无法加载")
	}
	// Recheck after installing the driver; its existing browser cache may suffice.
	if downloadBrowser && !shared.Truth(shared.Obj(status["browser"])["installed"]) {
		args := []string{filepath.Join(shared.Str(driver["path"]), "cli.js"), "install", "chromium"}
		if options["browser_backend"] == "playwright" && options["browser_headless"] != false {
			args = append(args, "--only-shell")
		}
		if err := run(args...); err != nil {
			return nil, fmt.Errorf("Chromium 安装失败，请检查网络、磁盘空间与系统运行依赖：%w", err)
		}
		status = a.captchaRuntimeStatus(ctx, options)
		if !shared.Truth(shared.Obj(status["browser"])["installed"]) {
			return nil, fmt.Errorf("下载完成后未找到 Chromium，请重新检测运行时")
		}
	}
	return status, nil
}
