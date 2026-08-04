//go:build linux

package gitstore

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func publishLooseObjectNoReplace(tempPath, targetPath string) error {
	err := unix.Renameat2(
		unix.AT_FDCWD,
		tempPath,
		unix.AT_FDCWD,
		targetPath,
		unix.RENAME_NOREPLACE,
	)
	if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL) || errors.Is(err, unix.EOPNOTSUPP) {
		return os.Link(tempPath, targetPath)
	}
	return err
}
