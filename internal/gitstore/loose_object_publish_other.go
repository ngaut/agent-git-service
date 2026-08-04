//go:build !linux && !darwin

package gitstore

import "os"

func publishLooseObjectNoReplace(tempPath, targetPath string) error {
	return os.Link(tempPath, targetPath)
}
