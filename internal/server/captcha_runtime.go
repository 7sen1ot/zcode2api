package server

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
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
		return resolveExecutable(a.root, configured, filepath.Join(a.root, "node"))
	}
	return resolveExecutable(a.root, configured)
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

func (a *App) captchaRuntimeStatus(parent context.Context, options M) M {
	modulePath := a.captchaModulePath(str(options["jsdom_path"]))
	jsdomPath := filepath.Join(modulePath, "node_modules", "jsdom")
	info, statErr := os.Stat(jsdomPath)
	jsdom := M{"installed": statErr == nil && info.IsDir(), "available": false, "path": jsdomPath, "message": "未找到 jsdom"}
	if truth(jsdom["installed"]) {
		jsdom["message"] = "需先配置 Node.js"
	}
	node := M{"installed": false, "available": false, "message": "未找到 Node.js"}
	result := M{"ok": true, "ready": false, "node": node, "jsdom": jsdom, "playwright": M{"available": false, "message": "需先配置 Node.js"}, "browser": M{"installed": false, "message": "需先配置 Node.js 后检测"}, "browser_ready": false, "install_available": false}
	binary, err := a.resolveCaptchaNode(str(options["node_path"]))
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
	command, output := newJSONCommand(ctx, binary, parameters, "--input-type=commonjs", "-e", captchaRuntimeProbe)
	command.Dir = a.root
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			node["message"] = "Node.js 检测超时或已取消"
		}
		return result
	}
	probe := obj(parse(string(output.value), nil))
	version := str(probe["node_version"])
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
	result["browser_ready"] = truth(obj(probe["playwright"])["available"]) && truth(obj(probe["browser"])["installed"])
	result["install_available"] = truth(obj(probe["playwright"])["available"]) || captchaNPM(binary) != ""
	if str(options["playwright_path"]) != "" && !truth(obj(probe["playwright"])["available"]) {
		result["install_available"] = false
	} else if !truth(result["install_available"]) {
		obj(result["playwright"])["message"] = "未找到 Playwright 和 npm，请指定已有 Playwright 依赖目录或配置完整 Node.js"
	}
	result["browser_cache"] = parameters["browser_cache"]
	jsdom["available"] = truth(probe["jsdom_available"])
	if truth(jsdom["available"]) {
		jsdom["installed"], jsdom["version"], jsdom["message"] = true, probe["jsdom_version"], "可用"
	} else if truth(jsdom["installed"]) {
		jsdom["message"] = "jsdom 无法加载，请检查依赖"
	}
	result["ready"] = truth(jsdom["available"])
	return result
}
