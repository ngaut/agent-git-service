package gitstore

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

const maxPatchBytes = 1 << 20 // ~1MB, matches GitHub-style patch truncation.

// CommitFilePatches returns a map of file path -> unified diff patch for a commit.
// For merge commits, the diff is against the first parent (GitHub default).
func (s *Store) CommitFilePatches(ctx context.Context, fullName, sha string, parents []string) (map[string]string, error) {
	if !IsValidRev(sha) {
		return nil, fmt.Errorf("invalid revision")
	}
	dir, err := s.repoPath(ctx, fullName)
	if err != nil {
		return nil, err
	}

	var cmd *exec.Cmd
	if len(parents) == 0 {
		cmd = exec.CommandContext(ctx, "git", "-C", dir, "diff-tree", "--root", "-p", "--no-commit-id", "-r", sha)
	} else {
		base := parents[0]
		if !IsValidRev(base) {
			return nil, fmt.Errorf("invalid base revision")
		}
		cmd = exec.CommandContext(ctx, "git", "-C", dir, "diff", base+"..."+sha)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git diff failed: %v\n%s", err, out)
	}
	return parseFilePatches(string(out)), nil
}

// CompareFilePatches returns a map of file path -> unified diff patch for base...head.
func (s *Store) CompareFilePatches(ctx context.Context, fullName, base, head string) (map[string]string, error) {
	if !IsValidRev(base) || !IsValidRev(head) {
		return nil, fmt.Errorf("invalid base or head revision")
	}
	dir, err := s.repoPath(ctx, fullName)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "diff", base+"..."+head)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git diff failed: %v\n%s", err, out)
	}
	return parseFilePatches(string(out)), nil
}

// BlobSHAs returns blob SHAs for the given paths at ref.
func (s *Store) BlobSHAs(ctx context.Context, fullName, ref string, paths []string) (map[string]string, error) {
	if !IsValidRev(ref) {
		return nil, fmt.Errorf("invalid revision")
	}
	result := make(map[string]string)
	if len(paths) == 0 {
		return result, nil
	}
	dir, err := s.repoPath(ctx, fullName)
	if err != nil {
		return nil, err
	}
	args := []string{"-C", dir, "ls-tree", "-r", ref, "--"}
	args = append(args, paths...)
	cmd := exec.CommandContext(ctx, "git", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git ls-tree failed: %v\n%s", err, out)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		meta := strings.Fields(parts[0])
		if len(meta) < 3 {
			continue
		}
		sha := meta[2]
		path := parts[1]
		result[path] = sha
	}
	return result, nil
}

func parseFilePatches(diff string) map[string]string {
	patches := make(map[string]string)
	if strings.TrimSpace(diff) == "" {
		return patches
	}

	lines := strings.Split(diff, "\n")
	var (
		builder  strings.Builder
		oldPath  string
		newPath  string
		isBinary bool
	)

	flush := func() {
		if builder.Len() == 0 {
			oldPath = ""
			newPath = ""
			isBinary = false
			return
		}
		patch := strings.TrimSuffix(builder.String(), "\n")
		builder.Reset()

		if isBinary || len(patch) > maxPatchBytes {
			oldPath = ""
			newPath = ""
			isBinary = false
			return
		}

		if newPath != "" {
			patches[newPath] = patch
		}
		if oldPath != "" && oldPath != newPath {
			patches[oldPath] = patch
		}
		oldPath = ""
		newPath = ""
		isBinary = false
	}

	for _, line := range lines {
		if strings.HasPrefix(line, "diff --git ") {
			flush()
			oldPath, newPath = parseDiffGitLine(line)
			if newPath != "" || oldPath != "" {
				builder.WriteString(line)
				builder.WriteString("\n")
			}
			continue
		}
		if strings.HasPrefix(line, "diff --cc ") || strings.HasPrefix(line, "diff --combined ") {
			flush()
			oldPath, newPath = parseCombinedDiffLine(line)
			if newPath != "" || oldPath != "" {
				builder.WriteString(line)
				builder.WriteString("\n")
			}
			continue
		}
		if newPath == "" && oldPath == "" {
			continue
		}
		builder.WriteString(line)
		builder.WriteString("\n")
		if strings.HasPrefix(line, "Binary files ") || strings.HasPrefix(line, "GIT binary patch") {
			isBinary = true
		}
	}
	flush()
	return patches
}

func parseDiffGitLine(line string) (string, string) {
	fields := strings.Fields(line)
	if len(fields) < 4 {
		return "", ""
	}
	oldPath := strings.TrimPrefix(fields[2], "a/")
	newPath := strings.TrimPrefix(fields[3], "b/")
	return oldPath, newPath
}

func parseCombinedDiffLine(line string) (string, string) {
	fields := strings.Fields(line)
	if len(fields) < 3 {
		return "", ""
	}
	path := fields[2]
	return path, path
}
