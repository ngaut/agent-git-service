package gitstore

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// FileMutation describes one path change inside a single commit.
type FileMutation struct {
	Path    string
	Content []byte
	Delete  bool
}

// CommitFiles applies a set of file mutations and records them in one commit.
func (s *Store) CommitFiles(ctx context.Context, fullName, branch, message string, changes []FileMutation) (string, error) {
	if len(changes) == 0 {
		return "", fmt.Errorf("no file changes supplied")
	}

	dir, err := s.repoPath(ctx, fullName)
	if err != nil {
		return "", err
	}

	ref, parentSHA, err := s.resolveBranchParent(ctx, dir, branch, true)
	if err != nil {
		return "", err
	}

	tmpIndex, err := os.CreateTemp("", "git-index-*")
	if err != nil {
		return "", fmt.Errorf("failed to create temp index: %w", err)
	}
	tmpIndexPath := tmpIndex.Name()
	tmpIndex.Close()
	_ = os.Remove(tmpIndexPath)
	defer os.Remove(tmpIndexPath)

	indexEnv := append(os.Environ(), "GIT_INDEX_FILE="+tmpIndexPath)
	runGit := func(args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "git", "--git-dir", dir)
		cmd.Env = indexEnv
		cmd.Args = append(cmd.Args, args...)
		return cmd.CombinedOutput()
	}

	if parentSHA != "" {
		if out, err := runGit("read-tree", parentSHA); err != nil {
			return "", fmt.Errorf("read-tree failed: %w, output: %s", err, out)
		}
	}

	for _, change := range changes {
		if change.Path == "" {
			return "", fmt.Errorf("file mutation path is required")
		}
		if change.Delete {
			if out, err := runGit("update-index", "--force-remove", change.Path); err != nil {
				return "", fmt.Errorf("update-index remove failed for %s: %w, output: %s", change.Path, err, out)
			}
			continue
		}
		blobSHA, err := s.hashBlob(ctx, dir, change.Content)
		if err != nil {
			return "", err
		}
		if out, err := runGit("update-index", "--add", "--cacheinfo", "100644", blobSHA, change.Path); err != nil {
			return "", fmt.Errorf("update-index add failed for %s: %w, output: %s", change.Path, err, out)
		}
	}

	treeOut, err := runGit("write-tree")
	if err != nil {
		return "", fmt.Errorf("write-tree failed: %w, output: %s", err, treeOut)
	}
	newTreeSHA := strings.TrimSpace(string(treeOut))

	commitSHA, err := s.commitTree(ctx, dir, newTreeSHA, parentSHA, message)
	if err != nil {
		return "", err
	}
	if err := s.updateRef(ctx, dir, ref, commitSHA); err != nil {
		return "", err
	}
	return commitSHA, nil
}
