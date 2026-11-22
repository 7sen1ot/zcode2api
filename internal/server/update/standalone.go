package update

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
	"zcode2api/internal/server/shared"
)

func StartStandaloneUpdater(ctx context.Context, helper, planFile string) error {
	log, err := os.OpenFile(filepath.Join(filepath.Dir(planFile), "helper.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer log.Close()
	cmd := detachedUpdateCommand(helper, "--apply-update", planFile)
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		return err
	}
	exit := make(chan error, 1)
	go func() { exit <- cmd.Wait() }()
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	ready := filepath.Join(filepath.Dir(planFile), "ready")
	for ctx.Err() == nil {
		select {
		case <-exit:
			return fmt.Errorf("独立更新器无法启动，请查看 %s", log.Name())
		default:
		}
		if info, err := os.Stat(ready); err == nil && info.Mode().IsRegular() {
			return nil
		}
		if err := shared.Pause(ctx, 100*time.Millisecond); err != nil {
			break
		}
	}
	_ = cmd.Process.Kill()
	return fmt.Errorf("独立更新器未就绪，请查看 %s", log.Name())
}

func startStandaloneVersion(plan UpdatePlan) (*exec.Cmd, <-chan struct{}, error) {
	stdout, err := os.OpenFile(filepath.Join(plan.Data, "service.stdout.log"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600)
	if err != nil {
		return nil, nil, err
	}
	defer stdout.Close()
	stderr, err := os.OpenFile(filepath.Join(plan.Data, "service.stderr.log"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600)
	if err != nil {
		return nil, nil, err
	}
	defer stderr.Close()
	cmd := detachedUpdateCommand(plan.Binary, plan.Args...)
	cmd.Dir, cmd.Stdout, cmd.Stderr = plan.Directory, stdout, stderr
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	return cmd, done, nil
}

func waitStandaloneHealthy(ctx context.Context, plan UpdatePlan, version string, pid int, done <-chan struct{}) error {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	go func() {
		select {
		case <-done:
			cancel()
		case <-ctx.Done():
		}
	}()
	err := fmt.Errorf("新进程未能在一分钟内完成启动")
	for ctx.Err() == nil {
		var running struct {
			PID         int `json:"pid"`
			WebPort     int `json:"web_port"`
			GatewayPort int `json:"gateway_port"`
		}
		if ReadUpdateJSON(filepath.Join(plan.Data, "ports.json"), &running) == nil && running.PID == pid &&
			running.WebPort > 0 && running.WebPort <= 65535 && running.GatewayPort > 0 && running.GatewayPort <= 65535 {
			plan.WebPort, plan.GatewayPort = running.WebPort, running.GatewayPort
			err = waitUpdateHealthy(ctx, plan, version)
			break
		}
		if shared.Pause(ctx, 250*time.Millisecond) != nil {
			break
		}
	}
	select {
	case <-done:
		return fmt.Errorf("程序启动后退出，请查看 service.stderr.log")
	default:
		return err
	}
}

func applyStandaloneUpdate(ctx context.Context, filename string, plan UpdatePlan, state UpdateState) (resultErr error) {
	var child *exec.Cmd
	var childDone <-chan struct{}
	stopped, replaced := false, false
	defer func() {
		if resultErr != nil {
			state.Phase, state.Message = "error", resultErr.Error()
			if stopped {
				recovery, cancel := context.WithTimeout(context.Background(), 90*time.Second)
				defer cancel()
				err := restoreStandaloneVersion(recovery, plan, replaced, child, childDone)
				if err == nil {
					state.Phase, state.Message = "rolled_back", "更新失败，已恢复原版本："+resultErr.Error()
				} else {
					state.Message = "更新失败，自动恢复未完成：" + err.Error() + "；备份位于 " + filepath.Dir(filename)
				}
			}
		}
		state.FinishedAt = shared.NowMS()
		if err := WriteUpdateJSON(plan.StateFile, state); resultErr == nil {
			resultErr = err
		}
	}()
	if err := validateUpdatePlan(filename, plan); err != nil {
		return err
	}
	if err := waitForUpdateParent(ctx, plan, func() error {
		return os.WriteFile(filepath.Join(filepath.Dir(filename), "ready"), []byte("ready"), 0600)
	}); err != nil {
		return err
	}
	stopped = true
	if err := replaceStandaloneBinary(ctx, plan.Candidate, plan.Binary); err != nil {
		return err
	}
	replaced = true
	state.Phase, state.Message = "restarting", "正在启动新版本"
	if err := WriteUpdateJSON(plan.StateFile, state); err != nil {
		return err
	}
	var err error
	child, childDone, err = startStandaloneVersion(plan)
	if err != nil {
		return err
	}
	if err := waitStandaloneHealthy(ctx, plan, plan.Version, child.Process.Pid, childDone); err != nil {
		return err
	}
	state.Phase, state.Message = "completed", "更新完成"
	return nil
}

func restoreStandaloneVersion(ctx context.Context, plan UpdatePlan, replaced bool, child *exec.Cmd, done <-chan struct{}) error {
	if child != nil {
		if err := child.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return err
		}
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if replaced {
		if err := replaceStandaloneBinary(ctx, plan.Backup, plan.Binary); err != nil {
			return err
		}
	}
	old, oldDone, err := startStandaloneVersion(plan)
	if err != nil {
		return err
	}
	return waitStandaloneHealthy(ctx, plan, plan.PreviousVersion, old.Process.Pid, oldDone)
}
