//go:build !windows

package update

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"time"
	"zcode2api/internal/server/shared"

	"golang.org/x/sys/unix"
)

func windowsUpdateAvailable() bool { return false }

func SecureUpdateJob(string) error { return nil }

func tryLockUpdateFile(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}

func replaceUpdateState(rename func() error) error { return rename() }

func detachedUpdateCommand(binary string, args ...string) *exec.Cmd {
	cmd := exec.Command(binary, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if runtime.GOOS == "darwin" {
		// A terminal can pass its own launchd job name to shell children.
		// The detached gateway is not that job when launchd adopts it.
		cmd.Env = []string{}
		for _, v := range os.Environ() {
			if !strings.HasPrefix(v, "XPC_SERVICE_NAME=") {
				cmd.Env = append(cmd.Env, v)
			}
		}
	}
	return cmd
}

func waitForUpdateParent(ctx context.Context, plan UpdatePlan, signalReady func() error) error {
	// The server starts this helper itself. Reparenting identifies its exit
	// without signalling a PID that the OS may already have reused.
	if os.Getppid() != plan.PID {
		return fmt.Errorf("当前网关进程与更新计划不一致")
	}
	if err := signalReady(); err != nil {
		return err
	}
	wait, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	for os.Getppid() == plan.PID {
		if shared.Pause(wait, 100*time.Millisecond) != nil {
			return fmt.Errorf("等待当前网关正常退出超时")
		}
	}
	return nil
}

func replaceStandaloneBinary(_ context.Context, source, target string) error {
	return replaceUpdateBinary(source, target)
}
