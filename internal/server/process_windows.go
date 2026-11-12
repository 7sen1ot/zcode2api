package server

import (
	"os/exec"
	"strconv"
	"syscall"
)

func hideChildWindow(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}

func prepareBrowserProcess(command *exec.Cmd) { hideChildWindow(command) }

func killBrowserProcess(command *exec.Cmd) {
	kill := exec.Command("taskkill", "/PID", strconv.Itoa(command.Process.Pid), "/T", "/F")
	hideChildWindow(kill)
	_ = kill.Run()
}
