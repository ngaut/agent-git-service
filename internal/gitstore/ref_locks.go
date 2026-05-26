package gitstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ErrRefLockActive is returned when a ref lock file exists but is still fresh
// enough that automatic cleanup should not remove it.
var ErrRefLockActive = errors.New("ref lock still active")

type RefLockRepairResult struct {
	Ref        string
	LockPath   string
	Present    bool
	Cleared    bool
	Force      bool
	AgeSeconds int64
}

func refLockPath(repoDir, ref string) (string, error) {
	if !IsValidRefName(ref) {
		return "", ErrInvalidRefName
	}
	lockRel := filepath.FromSlash(ref + ".lock")
	lockPath := filepath.Join(repoDir, lockRel)
	repoClean := filepath.Clean(repoDir)
	lockClean := filepath.Clean(lockPath)
	prefix := repoClean + string(os.PathSeparator)
	if lockClean != repoClean && !strings.HasPrefix(lockClean, prefix) {
		return "", fmt.Errorf("ref lock path escaped repo root: %s", ref)
	}
	return lockClean, nil
}

// RepairRefLock removes a stale git ref lock when it is old enough, or when
// force is true. Fresh locks are left in place and return ErrRefLockActive.
func (s *Store) RepairRefLock(ctx context.Context, fullName, ref string, staleAfter time.Duration, force bool) (RefLockRepairResult, error) {
	dir, err := s.repoPath(ctx, fullName)
	if err != nil {
		return RefLockRepairResult{}, err
	}
	lockPath, err := refLockPath(dir, ref)
	if err != nil {
		return RefLockRepairResult{}, err
	}
	info, err := os.Stat(lockPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return RefLockRepairResult{Ref: ref, LockPath: lockPath, Force: force}, nil
		}
		return RefLockRepairResult{}, fmt.Errorf("stat ref lock %s: %w", ref, err)
	}
	if info.IsDir() {
		return RefLockRepairResult{}, fmt.Errorf("ref lock path is directory: %s", lockPath)
	}
	age := time.Since(info.ModTime())
	result := RefLockRepairResult{
		Ref:        ref,
		LockPath:   lockPath,
		Present:    true,
		Force:      force,
		AgeSeconds: int64(age / time.Second),
	}
	if !force && staleAfter > 0 && age < staleAfter {
		return result, fmt.Errorf("%w: %s age=%s", ErrRefLockActive, ref, age.Truncate(time.Second))
	}
	if err := os.Remove(lockPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return result, nil
		}
		return result, fmt.Errorf("remove ref lock %s: %w", ref, err)
	}
	result.Cleared = true
	return result, nil
}
