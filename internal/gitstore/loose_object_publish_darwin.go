//go:build darwin

package gitstore

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func publishLooseObjectNoReplace(tempPath, targetPath string) error {
	err := unix.RenamexNp(tempPath, targetPath, unix.RENAME_EXCL)
	if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL) || errors.Is(err, unix.EOPNOTSUPP) {
		return os.Link(tempPath, targetPath)
	}
	return err
}
