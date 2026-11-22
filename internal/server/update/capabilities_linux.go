package update

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

func checkUpdateCapabilities(binary string) error {
	// Replacing the inode drops its file capabilities, including on rollback.
	_, err := unix.Getxattr(binary, "security.capability", nil)
	switch err {
	case nil:
		return errors.New("当前程序带有 Linux 文件能力，请手动更新并保留这些权限")
	case unix.ENODATA, unix.EOPNOTSUPP:
		return nil
	default:
		return fmt.Errorf("无法检查程序的 Linux 文件能力，请手动更新：%w", err)
	}
}
