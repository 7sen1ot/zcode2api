package update

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
	"zcode2api/internal/server/shared"

	"golang.org/x/sys/windows"
)

func windowsUpdateAvailable() bool {
	var session uint32
	return windows.ProcessIdToSessionId(uint32(os.Getpid()), &session) == nil && session != 0
}

func SecureUpdateJob(job string) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FA;;;" + user.User.Sid.String() + ")")
	if err != nil {
		return err
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(job, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}

func tryLockUpdateFile(file *os.File) error {
	return windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &windows.Overlapped{})
}

func replaceUpdateState(rename func() error) error {
	deadline := time.Now().Add(time.Second)
	for {
		err := rename()
		if !errors.Is(err, windows.ERROR_SHARING_VIOLATION) && !errors.Is(err, windows.ERROR_ACCESS_DENIED) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func detachedUpdateCommand(binary string, args ...string) *exec.Cmd {
	cmd := exec.Command(binary, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.DETACHED_PROCESS, HideWindow: true}
	return cmd
}

func waitWindowsProcess(ctx context.Context, process windows.Handle) error {
	for ctx.Err() == nil {
		status, err := windows.WaitForSingleObject(process, 250)
		if err != nil {
			return err
		}
		if status == windows.WAIT_OBJECT_0 {
			return nil
		}
		if status != uint32(windows.WAIT_TIMEOUT) {
			return fmt.Errorf("等待原进程退出失败")
		}
	}
	return ctx.Err()
}

func waitForUpdateParent(ctx context.Context, plan UpdatePlan, signalReady func() error) error {
	parent, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(plan.PID))
	if err != nil {
		return fmt.Errorf("无法确认当前网关进程，请重新检查后更新")
	}
	defer windows.CloseHandle(parent)
	name := make([]uint16, 32768)
	size := uint32(len(name))
	if err := windows.QueryFullProcessImageName(parent, 0, &name[0], &size); err != nil {
		return err
	}
	original, err := os.Stat(windows.UTF16ToString(name[:size]))
	target, targetErr := os.Stat(plan.Binary)
	if err != nil || targetErr != nil || !os.SameFile(original, target) {
		return fmt.Errorf("当前网关进程与更新计划不一致")
	}
	if err := signalReady(); err != nil {
		return err
	}
	wait, cancel := context.WithTimeout(ctx, 60*time.Second)
	err = waitWindowsProcess(wait, parent)
	cancel()
	if err != nil {
		return fmt.Errorf("等待当前网关正常退出超时")
	}
	return nil
}

func replaceStandaloneBinary(ctx context.Context, source, target string) error {
	deadline := time.Now().Add(8 * time.Second)
	for {
		err := replaceUpdateBinary(source, target)
		if !errors.Is(err, windows.ERROR_SHARING_VIOLATION) && !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return err
		}
		if time.Now().After(deadline) || shared.Pause(ctx, 500*time.Millisecond) != nil {
			return err
		}
	}
}
