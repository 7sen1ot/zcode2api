package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
)

type updateSupport struct {
	Supported                     bool
	Reason, Mode, Service, Binary string
	Systemctl, SystemdRun         string
}

var updateServicePattern = regexp.MustCompile(`^[A-Za-z0-9_@][A-Za-z0-9_.@:-]*\.service$`)

func detectUpdateSupport(version string) updateSupport {
	support := updateSupport{Mode: "manual", Reason: "当前部署支持检查版本，请使用发行包手动更新"}
	for _, marker := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := os.Stat(marker); err == nil {
			support.Mode, support.Reason = "container", "容器部署请在宿主机更新镜像并重建容器"
			return support
		}
	}
	if runtime.GOOS != "linux" {
		return support
	}
	if _, ok := updateVersionNumbers(version); !ok {
		support.Reason = "开发构建请通过源码更新；安装发行版后可使用在线更新"
		return support
	}
	if os.Geteuid() != 0 {
		support.Reason = "在线更新需要由 root 运行的 systemd 服务"
		return support
	}
	cgroups, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return support
	}
	if strings.Contains(string(cgroups), "docker") || strings.Contains(string(cgroups), "kubepods") || strings.Contains(string(cgroups), "libpod") {
		support.Mode, support.Reason = "container", "容器部署请在宿主机更新镜像并重建容器"
		return support
	}
	for _, line := range strings.Split(string(cgroups), "\n") {
		fields := strings.SplitN(line, ":", 3)
		if len(fields) != 3 {
			continue
		}
		unit := filepath.Base(fields[2])
		if strings.HasPrefix(fields[2], "/system.slice/") && updateServicePattern.MatchString(unit) {
			support.Service = unit
			break
		}
	}
	if support.Service == "" {
		support.Reason = "在线更新需要使用 systemd 托管当前程序"
		return support
	}
	support.Systemctl, err = exec.LookPath("systemctl")
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
	output, err := exec.CommandContext(ctx, support.Systemctl, "show", support.Service, "-p", "MainPID", "--value").Output()
	if err != nil || strings.TrimSpace(string(output)) != strconv.Itoa(os.Getpid()) {
		support.Reason = "当前程序不是 systemd 主进程，请使用发行包手动更新"
		return support
	}
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
	support.Supported, support.Mode, support.Reason = true, "systemd", ""
	return support
}

type updatePlan struct {
	Service         string `json:"service"`
	Binary          string `json:"binary"`
	Candidate       string `json:"candidate"`
	Backup          string `json:"backup"`
	Data            string `json:"data"`
	StateFile       string `json:"state_file"`
	Version         string `json:"version"`
	PreviousVersion string `json:"previous_version"`
	OldSHA256       string `json:"old_sha256"`
	NewSHA256       string `json:"new_sha256"`
	CreatedAt       int64  `json:"created_at"`
	PID             int    `json:"pid"`
	WebPort         int    `json:"web_port"`
	GatewayPort     int    `json:"gateway_port"`
}

func copyUpdateFile(source, target string, mode os.FileMode) error {
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

func updateFileHash(filename string) (string, error) {
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

func (a *App) launchUpdateHelper(ctx context.Context, job, binary string, state *updateState) error {
	support := detectUpdateSupport(a.version)
	if !support.Supported {
		return fmt.Errorf("%s", support.Reason)
	}
	data, err := filepath.Abs(a.data)
	if err != nil {
		return err
	}
	plan := updatePlan{Service: support.Service, Binary: support.Binary, Candidate: binary,
		Backup: filepath.Join(job, "previous-binary"), Data: data, StateFile: filepath.Join(a.updateDirectory(), "state.json"),
		Version: state.Version, PreviousVersion: a.version, CreatedAt: nowMS(), PID: os.Getpid(), WebPort: a.webPort, GatewayPort: a.gatewayPort}
	if err = copyUpdateFile(support.Binary, plan.Backup, 0700); err != nil {
		return fmt.Errorf("无法备份当前程序：%w", err)
	}
	plan.OldSHA256, err = updateFileHash(plan.Backup)
	if err != nil {
		return err
	}
	plan.NewSHA256, err = updateFileHash(binary)
	if err != nil {
		return err
	}
	// SQLite creates a consistent backup while existing requests can finish.
	if _, err = a.db.ExecContext(ctx, "VACUUM INTO ?", filepath.Join(job, "database-before.db")); err != nil {
		return fmt.Errorf("数据库备份失败，未替换当前程序")
	}
	if err = copyUpdateFile(filepath.Join(a.root, "config.json"), filepath.Join(job, "config-before.json"), 0600); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("配置文件备份失败，未替换当前程序")
	}
	helper := filepath.Join(job, "update-helper")
	if err = copyUpdateFile(plan.Backup, helper, 0700); err != nil {
		return err
	}
	planFile := filepath.Join(job, "plan.json")
	if err = writeUpdateJSON(planFile, plan); err != nil {
		return err
	}
	state.Phase, state.Message = "applying", "备份完成，即将重启服务"
	if err = writeUpdateJSON(plan.StateFile, state); err != nil {
		return err
	}
	command := exec.CommandContext(ctx, support.SystemdRun, "--quiet", "--collect", "--unit=zcode2api-update-"+random(6),
		"--property=Type=exec", "--property=RuntimeMaxSec=10min", "--property=TimeoutStopSec=15s", "--", helper, "--apply-update", planFile)
	if err = command.Run(); err != nil {
		return fmt.Errorf("无法启动独立更新进程，当前程序未替换")
	}
	a.audit("system", "update.started", M{"version": state.Version, "backup": job}, "")
	return nil
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

func waitUpdateHealthy(ctx context.Context, plan updatePlan, version string) error {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second}
	for ctx.Err() == nil {
		healthy := true
		for _, port := range []int{plan.WebPort, plan.GatewayPort} {
			request, _ := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("http://127.0.0.1:%d/healthz", port), nil)
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
			if err != nil || response.StatusCode != 200 || !health.OK || (health.Version != "" && health.Version != version) {
				healthy = false
				break
			}
		}
		if healthy {
			return nil
		}
		if pause(ctx, time.Second) != nil {
			break
		}
	}
	return fmt.Errorf("新程序未能在一分钟内通过健康检查")
}

func validateUpdatePlanPaths(filename string, plan updatePlan) error {
	job := filepath.Dir(filename)
	if runtime.GOOS != "linux" || os.Geteuid() != 0 || !updateServicePattern.MatchString(plan.Service) ||
		!filepath.IsAbs(plan.Binary) || !filepath.IsAbs(plan.Data) ||
		filepath.Dir(job) != filepath.Join(plan.Data, "updates") || !strings.HasPrefix(filepath.Base(job), "job-") ||
		plan.StateFile != filepath.Join(plan.Data, "updates", "state.json") ||
		plan.Candidate != filepath.Join(job, "new-binary") || plan.Backup != filepath.Join(job, "previous-binary") ||
		plan.CreatedAt < nowMS()-int64((30*time.Minute)/time.Millisecond) || plan.CreatedAt > nowMS()+60000 ||
		!newerUpdate(plan.PreviousVersion, plan.Version) || plan.PID < 1 || plan.WebPort < 1 || plan.WebPort > 65535 || plan.GatewayPort < 1 || plan.GatewayPort > 65535 {
		return fmt.Errorf("更新计划无效或已过期")
	}
	return nil
}

func validateUpdatePlan(filename string, plan updatePlan) error {
	if err := validateUpdatePlanPaths(filename, plan); err != nil {
		return err
	}
	for name, expected := range map[string]string{plan.Binary: plan.OldSHA256, plan.Backup: plan.OldSHA256, plan.Candidate: plan.NewSHA256} {
		actual, err := updateFileHash(name)
		if err != nil || len(expected) != 64 || actual != expected {
			return fmt.Errorf("更新文件已变化，拒绝替换程序")
		}
	}
	return nil
}

// ApplyUpdate runs in a separate transient systemd unit, so stopping the main
// service cannot kill the updater. It never overwrites configuration or data.
func ApplyUpdate(filename string) (resultErr error) {
	filename, err := filepath.Abs(filename)
	if err != nil {
		return err
	}
	info, err := os.Lstat(filename)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("更新计划文件不存在或权限无效")
	}
	var plan updatePlan
	if err = readUpdateJSON(filename, &plan); err != nil {
		return fmt.Errorf("无法读取更新计划")
	}
	if err = validateUpdatePlanPaths(filename, plan); err != nil {
		return err
	}
	var state updateState
	if err = readUpdateJSON(plan.StateFile, &state); err != nil || state.Version != plan.Version || state.Phase != "applying" {
		return fmt.Errorf("更新任务状态无效")
	}
	lock, err := os.OpenFile(filepath.Join(filepath.Dir(filename), "apply.lock"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("该更新计划已经执行")
	}
	lock.Close()
	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(signalCtx, 5*time.Minute)
	defer cancel()
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
		state.FinishedAt = nowMS()
		if writeErr := writeUpdateJSON(plan.StateFile, state); writeErr != nil && resultErr == nil {
			resultErr = fmt.Errorf("程序已更新，但无法写入完成状态：%w", writeErr)
		}
	}()
	if err = pause(ctx, 2*time.Second); err != nil {
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
	if err = writeUpdateJSON(plan.StateFile, state); err != nil {
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
