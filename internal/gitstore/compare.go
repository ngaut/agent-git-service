package gitstore

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
)

// DiffResult holds the result of a git compare operation.
type DiffResult struct {
	AheadBy      int
	BehindBy     int
	Commits      []CommitInfo
	Files        []FileInfo
	MergeBaseSHA string
}

// CommitInfo holds basic commit information.
type CommitInfo struct {
	SHA     string
	Message string
	Author  string
	Email   string
	Date    string
}

// FileInfo holds file change information.
type FileInfo struct {
	Filename  string
	Status    string
	Additions int
	Deletions int
}

// Compare returns diff stats between base and head refs.
// Independent git commands run concurrently to reduce latency.
func (s *Store) Compare(ctx context.Context, fullName, base, head string) (DiffResult, error) {
	var result DiffResult
	if !IsValidRev(base) || !IsValidRev(head) {
		return result, fmt.Errorf("invalid base or head revision")
	}

	dir, err := s.repoPath(ctx, fullName)
	if err != nil {
		return result, err
	}
	var wg sync.WaitGroup

	wg.Add(5)

	var aheadBy, behindBy int
	var commits []CommitInfo
	var files []FileInfo
	var mergeBaseSHA string

	go func() {
		defer wg.Done()
		cmd := exec.CommandContext(ctx, "git", "-C", dir, "rev-list", "--count", base+".."+head)
		if out, err := cmd.Output(); err == nil {
			fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &aheadBy)
		}
	}()

	go func() {
		defer wg.Done()
		cmd := exec.CommandContext(ctx, "git", "-C", dir, "rev-list", "--count", head+".."+base)
		if out, err := cmd.Output(); err == nil {
			fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &behindBy)
		}
	}()

	go func() {
		defer wg.Done()
		cmd := exec.CommandContext(ctx, "git", "-C", dir, "log", "--format=%H|%an|%ae|%aI|%s", base+".."+head)
		if out, err := cmd.Output(); err == nil {
			for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
				if line == "" {
					continue
				}
				parts := strings.SplitN(line, "|", 5)
				if len(parts) == 5 {
					commits = append(commits, CommitInfo{
						SHA: parts[0], Author: parts[1], Email: parts[2], Date: parts[3], Message: parts[4],
					})
				}
			}
		}
	}()

	go func() {
		defer wg.Done()
		files, _ = diffNumStat(ctx, dir, base, head)
	}()

	go func() {
		defer wg.Done()
		// merge-base exits 1 with no output when histories are disjoint;
		// leave mergeBaseSHA == "" in that case.
		cmd := exec.CommandContext(ctx, "git", "-C", dir, "merge-base", base, head)
		if out, err := cmd.Output(); err == nil {
			mergeBaseSHA = strings.TrimSpace(string(out))
		}
	}()

	wg.Wait()

	result.AheadBy = aheadBy
	result.BehindBy = behindBy
	result.Commits = commits
	result.Files = files
	result.MergeBaseSHA = mergeBaseSHA

	return result, nil
}

// DiffNumStats returns only the per-file additions/deletions for base...head,
// running a single `git diff --numstat` subprocess. Callers that need just
// the file-level stats should prefer this over Compare, which additionally
// runs ahead/behind counts and a full commit log.
func (s *Store) DiffNumStats(ctx context.Context, fullName, base, head string) ([]FileInfo, error) {
	if !IsValidRev(base) || !IsValidRev(head) {
		return nil, fmt.Errorf("invalid base or head revision")
	}
	dir, err := s.repoPath(ctx, fullName)
	if err != nil {
		return nil, err
	}
	return diffNumStat(ctx, dir, base, head)
}

// diffNumStat runs `git diff --numstat base...head` in dir and parses the
// output via parseNumStatFiles. Tab-split parsing preserves filenames with
// spaces (unlike strings.Fields).
func diffNumStat(ctx context.Context, dir, base, head string) ([]FileInfo, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "diff", "--numstat", base+"..."+head)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return parseNumStatFiles(string(out)), nil
}

// Contributors returns unique authors from the git log, sorted by commit count.
func (s *Store) Contributors(ctx context.Context, fullName string) ([]string, error) {
	dir, err := s.repoPath(ctx, fullName)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "shortlog", "-sne", "--all")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var contributors []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		// Format: "  123\tName <email>"
		line = strings.TrimSpace(line)
		if idx := strings.IndexByte(line, '\t'); idx >= 0 {
			contributors = append(contributors, strings.TrimSpace(line[idx+1:]))
		}
	}
	return contributors, nil
}

// LogBetweenTags returns the git log between two tags (or refs).
func (s *Store) LogBetweenTags(ctx context.Context, fullName, from, to string) (string, error) {
	dir, err := s.repoPath(ctx, fullName)
	if err != nil {
		return "", err
	}
	var args []string
	if from != "" {
		args = []string{"-C", dir, "log", "--format=* %s (%h)", from + ".." + to}
	} else {
		args = []string{"-C", dir, "log", "--format=* %s (%h)", to}
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// PRCommitsLog performs a git log to fetch commits between base and head.
func (s *Store) PRCommitsLog(ctx context.Context, fullName, base, head string) (string, error) {
	if !IsValidRev(base) || !IsValidRev(head) {
		return "", fmt.Errorf("invalid base or head revision")
	}
	dir, err := s.repoPath(ctx, fullName)
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, "git", "log", "--format=%H|%an|%ae|%aI|%s", base+".."+head)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git log failed: %v\n%s", err, out)
	}
	return string(out), nil
}
