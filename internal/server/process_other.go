//go:build !windows

package server

import (
	"os/exec"
	"syscall"
)

func hideChildWindow(command *exec.Cmd) {}

func prepareBrowserProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killBrowserProcess(command *exec.Cmd) { _ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL) }
