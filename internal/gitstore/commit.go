package gitstore

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// CommitDetails holds metadata and file stats for a single commit.
type CommitDetails struct {
	Commit SearchCommitInfo
	Files  []FileInfo
}

// CommitDetails returns metadata and file stats for a specific commit.
func (s *Store) CommitDetails(ctx context.Context, fullName, sha string) (CommitDetails, error) {
	var details CommitDetails
	if !IsValidRev(sha) {
		return details, fmt.Errorf("invalid revision")
	}
	dir, err := s.repoPath(ctx, fullName)
	if err != nil {
		return details, err
	}
	commit, err := commitInfo(ctx, dir, sha)
	if err != nil {
		return details, err
	}
	files, err := commitFiles(ctx, dir, sha, commit.ParentSHAs)
	if err != nil {
		return details, err
	}
	details.Commit = commit
	details.Files = files
	return details, nil
}

// CommitForPathAtRef returns the most recent commit that touched path from the
// content snapshot resolved by ref.
func (s *Store) CommitForPathAtRef(ctx context.Context, fullName, ref, path string) (GitCommitObject, error) {
	dir, err := s.repoPath(ctx, fullName)
	if err != nil {
		return GitCommitObject{}, err
	}
	commit, err := s.resolveContentCommit(ctx, dir, ref)
	if err != nil {
		return GitCommitObject{}, err
	}

	cmd := exec.CommandContext(ctx, "git", "-C", dir, "log", "-1", "--format=%H", commit, "--", path)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return GitCommitObject{}, fmt.Errorf("git log path %s at %s failed: %v\n%s", path, commit, err, out)
	}
	sha := strings.TrimSpace(string(out))
	if sha == "" {
		return GitCommitObject{}, ErrCommitNotFound
	}
	return s.GetGitCommitObject(ctx, fullName, sha)
}

func commitInfo(ctx context.Context, dir, sha string) (SearchCommitInfo, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "log", "-1", "--format=%H|%an|%ae|%aI|%s|%P", sha)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return SearchCommitInfo{}, fmt.Errorf("git log failed: %v\n%s", err, out)
	}
	line := strings.TrimSpace(string(out))
	if line == "" {
		return SearchCommitInfo{}, fmt.Errorf("git log returned empty output")
	}
	commit, ok := parseCommitLogLine(line)
	if !ok {
		return SearchCommitInfo{}, fmt.Errorf("unable to parse git log output")
	}
	return commit, nil
}

func parseCommitLogLine(line string) (SearchCommitInfo, bool) {
	parts := strings.SplitN(line, "|", 5)
	if len(parts) < 5 {
		return SearchCommitInfo{}, false
	}
	messageAndParents := parts[4]
	message := messageAndParents
	parentField := ""
	if idx := strings.LastIndex(messageAndParents, "|"); idx >= 0 {
		message = messageAndParents[:idx]
		parentField = messageAndParents[idx+1:]
	}
	commit := SearchCommitInfo{
		SHA: parts[0], Author: parts[1], Email: parts[2], Date: parts[3], Message: message,
	}
	if parentField != "" {
		commit.ParentSHAs = strings.Fields(parentField)
	}
	return commit, true
}

func commitFiles(ctx context.Context, dir, sha string, parents []string) ([]FileInfo, error) {
	var out []byte
	var err error
	if len(parents) == 0 {
		cmd := exec.CommandContext(ctx, "git", "-C", dir, "diff-tree", "--root", "--no-commit-id", "--numstat", "-r", sha)
		out, err = cmd.CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("git diff-tree --numstat failed: %v\n%s", err, out)
		}
	} else {
		base := parents[0]
		cmd := exec.CommandContext(ctx, "git", "-C", dir, "diff", "--numstat", base+"..."+sha)
		out, err = cmd.CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("git diff --numstat failed: %v\n%s", err, out)
		}
	}
	return parseNumStatFiles(string(out)), nil
}

func parseNumStatFiles(out string) []FileInfo {
	var files []FileInfo
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) < 3 {
			continue
		}
		additions := parseNumStatCount(parts[0])
		deletions := parseNumStatCount(parts[1])
		filename := strings.TrimSpace(parts[2])
		files = append(files, FileInfo{
			Filename:  filename,
			Status:    "modified",
			Additions: additions,
			Deletions: deletions,
		})
	}
	return files
}

func parseNumStatCount(value string) int {
	if value == "-" {
		return 0
	}
	var n int
	fmt.Sscanf(value, "%d", &n)
	return n
}
