package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
	"zcode2api/internal/server/shared"
)

type UpdateSupport struct {
	CanCheck, Supported           bool
	SourceBuild                   bool
	Reason, Mode, Service, Binary string
	SystemdRun                    string
}

var updateServicePattern = regexp.MustCompile(`^[A-Za-z0-9_@][A-Za-z0-9_.@:-]*\.service$`)

func DetectUpdateSupport(version string) UpdateSupport {
	_, validVersion := updateVersionNumbers(version)
	localVersion := strings.Contains(version, "-local.")
	support := UpdateSupport{
		Mode:        "manual",
		Reason:      "当前部署支持检查版本，请使用发行包手动更新",
		CanCheck:    validVersion && !localVersion,
		SourceBuild: version == "dev" || strings.HasPrefix(version, "src-") || localVersion,
	}
	if !support.CanCheck {
		support.Reason = "当前构建请通过原部署方式手动更新"
	}
	for _, marker := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := os.Stat(marker); err == nil {
			support.Mode, support.Reason = "container", "容器部署请在宿主机更新镜像并重建容器"
			return support
		}
	}
	var cgroups []byte
	var err error
	if runtime.GOOS == "linux" {
		cgroups, err = os.ReadFile("/proc/self/cgroup")
		if err != nil {
			return support
		}
		groups := string(cgroups)
		if strings.Contains(groups, "docker") || strings.Contains(groups, "kubepods") || strings.Contains(groups, "libpod") {
			support.Mode, support.Reason = "container", "容器部署请在宿主机更新镜像并重建容器"
			return support
		}
	}
	if !support.CanCheck {
		return support
	}
	switch runtime.GOOS {
	case "windows":
		if !windowsUpdateAvailable() {
			support.Reason = "Windows 服务请通过服务管理器更新"
			return support
		}
		return detectUpdateBinary(support, "windows")
	case "darwin":
		if service := os.Getenv("XPC_SERVICE_NAME"); os.Getppid() == 1 && service != "" && service != "0" {
			support.Reason = "launchd 托管的程序请通过服务管理器更新"
			return support
		}
		return detectUpdateBinary(support, "standalone")
	case "linux":
		if os.Getpid() == 1 {
			support.Reason = "当前程序是系统或容器的首进程，请通过部署工具更新"
			return support
		}
		support.Service = linuxUpdateService(string(cgroups))
		if support.Service == "" {
			return detectUpdateBinary(support, "standalone")
		}
	default:
		return support
	}
	if os.Geteuid() != 0 || !updateServicePattern.MatchString(support.Service) {
		support.Reason = "当前 systemd 服务请通过服务管理器更新，在线更新需要 root 系统服务"
		return support
	}
	systemctl, err := exec.LookPath("systemctl")
	if err != nil {
		return support
	}
	support.SystemdRun, err = exec.LookPath("systemd-run")
	if err != nil {
		support.Reason = "系统缺少 systemd-run，无法独立执行更新和回滚"
		return support
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, systemctl, "show", support.Service, "-p", "MainPID", "--value").Output()
	if err != nil || strings.TrimSpace(string(output)) != strconv.Itoa(os.Getpid()) {
		support.Reason = "当前程序不是 systemd 主进程，请使用发行包手动更新"
		return support
	}
	return detectUpdateBinary(support, "systemd")
}

// Use the systemd hierarchy (v1 or unified v2). A login session scope
// nested inside a service belongs to the session, not the enclosing service.
func linuxUpdateService(cgroups string) string {
	for _, line := range strings.Split(cgroups, "\n") {
		fields := strings.SplitN(line, ":", 3)
		if len(fields) != 3 || fields[1] != "" && !strings.Contains(","+fields[1]+",", ",name=systemd,") {
			continue
		}
		parts := strings.Split(fields[2], "/")
		for i := len(parts) - 1; i >= 0; i-- {
			if strings.HasSuffix(parts[i], ".scope") {
				break
			}
			if strings.HasSuffix(parts[i], ".service") {
				return parts[i]
			}
		}
	}
	return ""
}

func detectUpdateBinary(support UpdateSupport, mode string) UpdateSupport {
	binary, err := os.Executable()
	if err != nil {
		return support
	}
	support.Binary, err = filepath.EvalSymlinks(binary)
	if err != nil {
		return support
	}
	file, err := os.CreateTemp(filepath.Dir(support.Binary), ".update-permission-*")
	if err != nil {
		support.Reason = "程序目录不可写，无法在线替换发行版"
		return support
	}
	file.Close()
	os.Remove(file.Name())
	support.Supported, support.Mode, support.Reason = true, mode, ""
	return support
}

type UpdatePlan struct {
	Service         string   `json:"service"`
	Binary          string   `json:"binary"`
	Candidate       string   `json:"candidate"`
	Backup          string   `json:"backup"`
	Data            string   `json:"data"`
	StateFile       string   `json:"state_file"`
	Version         string   `json:"version"`
	PreviousVersion string   `json:"previous_version"`
	OldSHA256       string   `json:"old_sha256"`
	NewSHA256       string   `json:"new_sha256"`
	CreatedAt       int64    `json:"created_at"`
	PID             int      `json:"pid"`
	WebPort         int      `json:"web_port"`
	GatewayPort     int      `json:"gateway_port"`
	WebHost         string   `json:"web_host,omitempty"`
	GatewayHost     string   `json:"gateway_host,omitempty"`
	Directory       string   `json:"directory,omitempty"`
	Args            []string `json:"args,omitempty"`
}

func UpdateBinaryFilename(base string) string {
	if runtime.GOOS == "windows" {
		return base + ".exe"
	}
	return base
}

func CopyUpdateFile(source, target string, mode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, err = io.Copy(output, input)
	if err == nil {
		err = output.Sync()
	}
	closeErr := output.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func UpdateFileHash(filename string) (string, error) {
	file, err := os.Open(filename)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	size, err := io.Copy(digest, io.LimitReader(file, updateBinaryLimit+1))
	if err != nil || size > updateBinaryLimit {
		return "", fmt.Errorf("程序读取失败或文件过大")
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func runUpdateSystemctl(parent context.Context, service, action string) error {
	ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "systemctl", action, service)
	if err := command.Run(); err != nil {
		return fmt.Errorf("systemd %s 失败：%w", action, err)
	}
	return nil
}

// The temporary file lives beside the destination so rename is atomic even
// when the data and executable directories are on different filesystems.
func replaceUpdateBinary(source, target string) error {
	info, err := os.Stat(target)
	if err != nil {
		return err
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.CreateTemp(filepath.Dir(target), ".zcode2api-update-*")
	if err != nil {
		return err
	}
	defer os.Remove(output.Name())
	_, err = io.Copy(output, input)
	if err == nil {
		err = output.Chmod(info.Mode().Perm())
	}
	if err == nil {
		err = output.Sync()
	}
	closeErr := output.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(output.Name(), target)
}

func waitUpdateHealthy(ctx context.Context, plan UpdatePlan, version string) error {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second}
	for ctx.Err() == nil {
		healthy := true
		for _, endpoint := range []struct {
			host string
			port int
		}{{plan.WebHost, plan.WebPort}, {plan.GatewayHost, plan.GatewayPort}} {
			host := endpoint.host
			if host == "" {
				host = "127.0.0.1"
			}
			address := url.URL{Scheme: "http", Host: net.JoinHostPort(host, strconv.Itoa(endpoint.port)), Path: "/healthz"}
			request, err := http.NewRequestWithContext(ctx, "GET", address.String(), nil)
			if err != nil {
				return err
			}
			response, err := client.Do(request)
			if err != nil {
				healthy = false
				break
			}
			var health struct {
				OK      bool   `json:"ok"`
				Version string `json:"version"`
			}
			err = json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&health)
			response.Body.Close()
			if err != nil || response.StatusCode != 200 || !health.OK ||
				health.Version != version && (health.Version != "" || plan.Service == "") {
				healthy = false
				break
			}
		}
		if healthy {
			return nil
		}
		if shared.Pause(ctx, time.Second) != nil {
			break
		}
	}
	return fmt.Errorf("新程序未能在一分钟内通过健康检查")
}

func validateUpdatePlanPaths(filename string, plan UpdatePlan) error {
	job := filepath.Dir(filename)
	validPlatform := runtime.GOOS == "linux" && os.Geteuid() == 0 && updateServicePattern.MatchString(plan.Service)
	if plan.Service == "" && filepath.IsAbs(plan.Directory) {
		validPlatform = runtime.GOOS == "linux" || runtime.GOOS == "darwin" ||
			runtime.GOOS == "windows" && strings.EqualFold(filepath.Ext(plan.Binary), ".exe")
	}
	if !validPlatform ||
		!filepath.IsAbs(plan.Binary) || !filepath.IsAbs(plan.Data) ||
		filepath.Dir(job) != filepath.Join(plan.Data, "updates") || !strings.HasPrefix(filepath.Base(job), "job-") ||
		plan.StateFile != filepath.Join(plan.Data, "updates", "state.json") ||
		plan.Candidate != filepath.Join(job, UpdateBinaryFilename("new-binary")) || plan.Backup != filepath.Join(job, UpdateBinaryFilename("previous-binary")) ||
		plan.CreatedAt < shared.NowMS()-int64((30*time.Minute)/time.Millisecond) || plan.CreatedAt > shared.NowMS()+60000 ||
		!NewerUpdate(plan.PreviousVersion, plan.Version) || plan.PID < 1 || plan.WebPort < 1 || plan.WebPort > 65535 || plan.GatewayPort < 1 || plan.GatewayPort > 65535 {
		return fmt.Errorf("更新计划无效或已过期")
	}
	for _, host := range []string{plan.WebHost, plan.GatewayHost} {
		if host != "" {
			if _, err := netip.ParseAddr(host); err != nil {
				return fmt.Errorf("更新健康检查地址无效")
			}
		}
	}
	return nil
}

func validateUpdatePlan(filename string, plan UpdatePlan) error {
	if err := validateUpdatePlanPaths(filename, plan); err != nil {
		return err
	}
	for name, expected := range map[string]string{plan.Binary: plan.OldSHA256, plan.Backup: plan.OldSHA256, plan.Candidate: plan.NewSHA256} {
		actual, err := UpdateFileHash(name)
		if err != nil || len(expected) != 64 || actual != expected {
			return fmt.Errorf("更新文件已变化，拒绝替换程序")
		}
	}
	return nil
}

// Apply runs outside the server process so it can finish the restart and recovery.
func Apply(filename string) (resultErr error) {
	filename, err := filepath.Abs(filename)
	if err != nil {
		return err
	}
	info, err := os.Lstat(filename)
	if err != nil || !info.Mode().IsRegular() || runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("更新计划文件不存在或权限无效")
	}
	var plan UpdatePlan
	if err = ReadUpdateJSON(filename, &plan); err != nil {
		return fmt.Errorf("无法读取更新计划")
	}
	if err = validateUpdatePlanPaths(filename, plan); err != nil {
		return err
	}
	var state UpdateState
	if err = ReadUpdateJSON(plan.StateFile, &state); err != nil || state.Version != plan.Version || state.Phase != "applying" {
		return fmt.Errorf("更新任务状态无效")
	}
	lock, err := os.OpenFile(filepath.Join(filepath.Dir(filename), "apply.lock"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("该更新计划已经执行")
	}
	if err := tryLockUpdateFile(lock); err != nil {
		lock.Close()
		return err
	}
	defer lock.Close()
	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(signalCtx, 5*time.Minute)
	defer cancel()
	if plan.Service == "" {
		return applyStandaloneUpdate(ctx, filename, plan, state)
	}
	stopped, replaced := false, false
	defer func() {
		if recovered := recover(); recovered != nil {
			resultErr = fmt.Errorf("更新进程异常：%v", recovered)
		}
		if resultErr != nil {
			state.Phase, state.Message = "error", resultErr.Error()
			if stopped {
				// Recovery has its own context: a canceled update must still be
				// able to restore and start the previous executable.
				recovery, recoveryCancel := context.WithTimeout(context.Background(), 4*time.Minute)
				defer recoveryCancel()
				stopErr := runUpdateSystemctl(recovery, plan.Service, "stop")
				var restoreErr error
				if replaced && stopErr == nil {
					restoreErr = replaceUpdateBinary(plan.Backup, plan.Binary)
				}
				_ = runUpdateSystemctl(recovery, plan.Service, "reset-failed")
				startErr := runUpdateSystemctl(recovery, plan.Service, "start")
				if stopErr == nil && restoreErr == nil && startErr == nil && waitUpdateHealthy(recovery, plan, plan.PreviousVersion) == nil {
					state.Phase, state.Message = "rolled_back", "更新失败，已恢复原版本："+resultErr.Error()
				} else {
					state.Message = "更新失败且自动恢复未完成，请通过 SSH 检查服务；备份位于 " + filepath.Dir(filename)
				}
			}
		}
		state.FinishedAt = shared.NowMS()
		if writeErr := WriteUpdateJSON(plan.StateFile, state); writeErr != nil && resultErr == nil {
			resultErr = fmt.Errorf("程序已更新，但无法写入完成状态：%w", writeErr)
		}
	}()
	if err = shared.Pause(ctx, 2*time.Second); err != nil {
		return err
	}
	// Ensure the original process is still the one that authorized this plan.
	if err = validateUpdatePlan(filename, plan); err != nil {
		return err
	}
	pidOutput, err := exec.CommandContext(ctx, "systemctl", "show", plan.Service, "-p", "MainPID", "--value").Output()
	if err != nil || strings.TrimSpace(string(pidOutput)) != strconv.Itoa(plan.PID) {
		return fmt.Errorf("服务进程已变化，请重新检查后更新")
	}
	stopped = true
	if err = runUpdateSystemctl(ctx, plan.Service, "stop"); err != nil {
		return err
	}
	if err = replaceUpdateBinary(plan.Candidate, plan.Binary); err != nil {
		return err
	}
	replaced = true
	state.Phase, state.Message = "restarting", "正在启动新版本"
	if err = WriteUpdateJSON(plan.StateFile, state); err != nil {
		return err
	}
	if err = runUpdateSystemctl(ctx, plan.Service, "start"); err != nil {
		return err
	}
	if err = waitUpdateHealthy(ctx, plan, plan.Version); err != nil {
		return err
	}
	state.Phase, state.Message = "completed", "更新完成"
	return nil
}
