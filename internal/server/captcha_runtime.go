package server

import (
	"context"
	"os"
	"os/exec"
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
	binary := configured
	if !filepath.IsAbs(binary) && strings.ContainsAny(binary, "/\\") {
		binary = filepath.Join(a.root, binary)
	}
	resolved, err := exec.LookPath(binary)
	if err != nil && configured == "node" {
		resolved, err = exec.LookPath(filepath.Join(a.root, "node"))
	}
	if err != nil {
		return "", err
	}
	return filepath.Abs(resolved)
}

// Check the configured runtime without downloading the SDK or requesting tickets.
const captchaRuntimeProbe = `
const fs = require('node:fs'), path = require('node:path');
const input = JSON.parse(fs.readFileSync(0, 'utf8'));
const result = {node_version: process.versions.node, jsdom_available: false, jsdom_version: ''};
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
	result := M{"ok": true, "ready": false, "node": node, "jsdom": jsdom}
	binary, err := a.resolveCaptchaNode(str(options["node_path"]))
	if err != nil {
		return result
	}
	node["installed"], node["path"], node["message"] = true, binary, "Node.js 无法运行"
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	stop := context.AfterFunc(a.ctx, cancel)
	defer stop()
	command, output := newJSONCommand(ctx, binary, M{"module_path": modulePath}, "--input-type=commonjs", "-e", captchaRuntimeProbe)
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
	jsdom["available"] = truth(probe["jsdom_available"])
	if truth(jsdom["available"]) {
		jsdom["installed"], jsdom["version"], jsdom["message"] = true, probe["jsdom_version"], "可用"
	} else if truth(jsdom["installed"]) {
		jsdom["message"] = "jsdom 无法加载，请检查依赖"
	}
	result["ready"] = truth(jsdom["available"])
	return result
}
