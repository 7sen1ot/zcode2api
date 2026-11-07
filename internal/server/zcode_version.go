package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"runtime"
	"strings"
	"time"
)

const (
	defaultZCodeVersionCheckIntervalSec = 5 * 24 * 60 * 60
	minZCodeVersionCheckIntervalSec     = 60
	maxZCodeVersionCheckIntervalSec     = 365 * 24 * 60 * 60
)

var zcodeManifestVersion = regexp.MustCompile(`(?m)^version:[ \t]*([^\r\n]+)`)

func zcodeVersionInterval(cfg M) time.Duration {
	seconds := integer(cfg["zcode_version_check_interval_sec"])
	if seconds < minZCodeVersionCheckIntervalSec || seconds > maxZCodeVersionCheckIntervalSec {
		seconds = defaultZCodeVersionCheckIntervalSec
	}
	return time.Duration(seconds) * time.Second
}

func (a *App) rescheduleZCodeVersionChecks() {
	select {
	case a.zcodeVersionReschedule <- struct{}{}:
	default:
	}
}

func zcodeReleasePlatform() string {
	arch := runtime.GOARCH
	switch arch {
	case "amd64":
		arch = "x86_64"
	case "arm64":
		arch = "aarch64"
	case "386":
		arch = "x86"
	}
	return runtime.GOOS + "-" + arch
}

func (a *App) fetchZCodeVersion(parent context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	ctx, release, err := a.pinOutboundProxy(ctx, nil)
	if err != nil {
		return "", err
	}
	defer release()
	client, err := a.client(ctx)
	if err != nil {
		return "", err
	}
	platform := zcodeReleasePlatform()
	address := a.origin + "/api/v1/releases/electron/manifest?" + url.Values{"platform": {platform}, "channel": {"1"}}.Encode()
	request, err := http.NewRequestWithContext(ctx, "GET", address, nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Accept", "application/x-yaml,text/yaml,text/plain,*/*")
	request.Header.Set("X-Platform", platform)
	request.Header.Set("X-Release-Channel", "1")
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("无法读取 ZCode 稳定版清单：%w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ZCode 版本清单返回 HTTP %d", response.StatusCode)
	}
	const limit = 256 << 10
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return "", err
	}
	if len(body) > limit {
		return "", fmt.Errorf("ZCode 版本清单过大")
	}
	// Read only the root version scalar; the remaining YAML describes installers.
	matches := zcodeManifestVersion.FindAllStringSubmatch(strings.TrimPrefix(string(body), "\uFEFF"), 2)
	if len(matches) != 1 {
		return "", fmt.Errorf("ZCode 版本清单缺少唯一的版本号")
	}
	version := strings.TrimSpace(matches[0][1])
	if len(version) > 1 && (version[0] == '"' || version[0] == '\'') && version[len(version)-1] == version[0] {
		version = version[1 : len(version)-1]
	}
	if !versionPattern.MatchString(version) {
		return "", fmt.Errorf("ZCode 清单未返回有效的稳定版本号")
	}
	return version, nil
}

func (a *App) syncZCodeVersion(parent context.Context) (err error) {
	if !a.zcodeVersionMu.TryLock() {
		return fmt.Errorf("正在同步 ZCode 版本")
	}
	defer a.zcodeVersionMu.Unlock()
	if !truth(a.config()["zcode_version_auto_update"]) {
		return fmt.Errorf("请先启用自动跟随 ZCode 稳定版")
	}
	state := obj(a.setting("zcode_version_status", M{}))
	defer func() {
		state["checked_at"] = nowMS()
		state["error"] = ""
		if err != nil {
			state["error"] = err.Error()
		}
		if saveErr := a.setSetting("zcode_version_status", state); err == nil {
			err = saveErr
		}
		a.rescheduleZCodeVersionChecks()
	}()
	version, err := a.fetchZCodeVersion(parent)
	if err != nil {
		return err
	}
	if previous := str(state["version"]); previous != "" && newerUpdate(version, previous) {
		return fmt.Errorf("ZCode 清单低于已同步版本，保留当前版本")
	}
	state["version"] = version
	a.configMu.Lock()
	if !truth(a.cfg["zcode_version_auto_update"]) {
		a.configMu.Unlock()
		return nil
	}
	if newerUpdate(version, str(a.cfg["app_version"])) || newerUpdate(version, str(obj(a.cfg["http"])["client_version"])) {
		a.configMu.Unlock()
		return fmt.Errorf("ZCode 清单低于当前配置，保留当前版本")
	}
	changed := str(a.cfg["app_version"]) != version || str(obj(a.cfg["http"])["client_version"]) != version
	if !changed {
		a.configMu.Unlock()
		return nil
	}
	overrides := obj(a.setting("config_overrides", M{}))
	httpOverrides := obj(overrides["http"])
	httpOverrides["client_version"] = version
	overrides["http"], overrides["app_version"] = httpOverrides, version
	if err = a.setSetting("config_overrides", overrides); err != nil {
		a.configMu.Unlock()
		return err
	}
	a.cfg["app_version"] = version
	obj(a.cfg["http"])["client_version"] = version
	a.configMu.Unlock()
	a.resetCaptchaCache()
	a.log.Printf("ZCode 请求版本已同步至 %s", version)
	return nil
}

func (a *App) startZCodeVersionUpdates() {
	a.background(func() {
		var lastAttempt int64
		for {
			cfg := a.config()
			var timer *time.Timer
			var due <-chan time.Time
			if truth(cfg["zcode_version_auto_update"]) {
				state := obj(a.setting("zcode_version_status", M{}))
				checkedAt := max(integer(state["checked_at"]), lastAttempt)
				wait := time.Duration(0)
				if checkedAt > 0 {
					wait = max(0, time.Until(time.UnixMilli(checkedAt).Add(zcodeVersionInterval(cfg))))
				}
				timer = time.NewTimer(wait)
				due = timer.C
			}
			select {
			case <-a.ctx.Done():
				if timer != nil {
					timer.Stop()
				}
				return
			case <-a.zcodeVersionReschedule:
				if timer != nil {
					timer.Stop()
				}
			case <-due:
				lastAttempt = nowMS()
				if err := a.syncZCodeVersion(a.ctx); err != nil && a.ctx.Err() == nil {
					a.log.Printf("ZCode 版本同步失败，沿用当前版本：%v", err)
				}
			}
		}
	})
}
