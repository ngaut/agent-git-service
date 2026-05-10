package gitstore

import (
	"context"
	"fmt"
	"io"
	"os/exec"
)

// Archive streams a compressed archive of the specified reference to w.
// format must be "zip" or "tar.gz".
// prefix is the directory name to wrap the contents in (e.g. "repo-1.0.0/").
func (s *Store) Archive(ctx context.Context, fullName string, format string, ref string, prefix string, w io.Writer) error {
	repoDir, err := s.repoPath(ctx, fullName)
	if err != nil {
		return err
	}

	gitFormat := format
	if format == "tar.gz" {
		gitFormat = "tgz"
	} else if format != "zip" && format != "tar" {
		return fmt.Errorf("unsupported archive format: %s", format)
	}

	args := []string{"-C", repoDir, "archive", "--format=" + gitFormat}
	if prefix != "" {
		if prefix[len(prefix)-1] != '/' {
			prefix += "/"
		}
		args = append(args, "--prefix="+prefix)
	}
	args = append(args, ref)

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Stdout = w

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git archive failed: %w", err)
	}
	return nil
}
