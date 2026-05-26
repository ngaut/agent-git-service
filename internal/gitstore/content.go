package gitstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
)

var ErrRefChanged = errors.New("ref changed")

// FileMove describes one path rename within a single commit.
type FileMove struct {
	OldPath string
	NewPath string
}

// ReadFile reads a file content from the bare git repo at HEAD.
// If HEAD points to a non-existent branch (e.g. repo created with default
// "main" but client pushed "master"), it falls back to the first available branch.
func (s *Store) ReadFile(ctx context.Context, fullName, path string) ([]byte, error) {
	return s.ReadFileAtRef(ctx, fullName, path, "")
}

// ResolveContentCommit resolves ref to the exact commit used for content reads.
// When ref is empty, it resolves HEAD and falls back to the first available
// branch only if HEAD itself is dangling.
func (s *Store) ResolveContentCommit(ctx context.Context, fullName, ref string) (string, error) {
	dir, err := s.repoPath(ctx, fullName)
	if err != nil {
		return "", err
	}
	return s.resolveContentCommit(ctx, dir, ref)
}

// ReadFileAtRef reads a file content from the bare git repo at the given ref.
// When ref is empty, it resolves HEAD and falls back to the first available
// branch only if HEAD itself is dangling.
func (s *Store) ReadFileAtRef(ctx context.Context, fullName, path, ref string) ([]byte, error) {
	dir, err := s.repoPath(ctx, fullName)
	if err != nil {
		return nil, err
	}
	commit, err := s.resolveContentCommit(ctx, dir, ref)
	if err != nil {
		return nil, err
	}
	return exec.CommandContext(ctx, "git", "-C", dir, "show", commit+":"+path).Output()
}

// FileExistsAtRef reports whether path exists in the tree for the given ref.
// When ref is empty, it resolves HEAD and applies the same dangling-HEAD
// fallback behavior as ReadFileAtRef.
func (s *Store) FileExistsAtRef(ctx context.Context, fullName, path, ref string) (bool, error) {
	dir, err := s.repoPath(ctx, fullName)
	if err != nil {
		return false, err
	}
	commit, err := s.resolveContentCommit(ctx, dir, ref)
	if err != nil {
		return false, err
	}
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "ls-tree", "--name-only", commit, "--", path).Output()
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(out)) == path, nil
}

// ReadFileWithSHAAtRef resolves ref once, then reads both file contents and the
// corresponding blob SHA from that exact commit snapshot.
func (s *Store) ReadFileWithSHAAtRef(ctx context.Context, fullName, path, ref string) ([]byte, string, error) {
	dir, err := s.repoPath(ctx, fullName)
	if err != nil {
		return nil, "", err
	}
	commit, err := s.resolveContentCommit(ctx, dir, ref)
	if err != nil {
		return nil, "", err
	}

	body, err := exec.CommandContext(ctx, "git", "-C", dir, "show", commit+":"+path).Output()
	if err != nil {
		return nil, "", err
	}

	cmd := exec.CommandContext(ctx, "git", "-C", dir, "ls-tree", "-z", commit, "--", path)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, "", fmt.Errorf("ls-tree failed: %w, output: %s", err, out)
	}
	raw := strings.TrimRight(string(out), "\x00")
	if raw == "" {
		return nil, "", fmt.Errorf("path not found")
	}
	parts := strings.SplitN(raw, "\t", 2)
	if len(parts) != 2 {
		return nil, "", fmt.Errorf("invalid ls-tree entry")
	}
	meta := strings.Fields(parts[0])
	if len(meta) < 3 || meta[1] != "blob" {
		return nil, "", fmt.Errorf("path is not a file")
	}
	if parts[1] != path {
		return nil, "", fmt.Errorf("path not found")
	}
	return body, meta[2], nil
}

// ListTreeFiles returns the file paths in the repository tree at HEAD.
func (s *Store) ListTreeFiles(ctx context.Context, fullName string) ([]string, error) {
	return s.ListTreeFilesAtRef(ctx, fullName, "")
}

// ListTreeFilesAtRef returns the file paths in the repository tree at ref.
// When ref is empty, it resolves HEAD and falls back to the first available
// branch only if HEAD itself is dangling.
func (s *Store) ListTreeFilesAtRef(ctx context.Context, fullName, ref string) ([]string, error) {
	dir, err := s.repoPath(ctx, fullName)
	if err != nil {
		return nil, err
	}
	commit, err := s.resolveContentCommit(ctx, dir, ref)
	if err != nil {
		return nil, err
	}
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "ls-tree", "-r", commit, "--name-only").Output()
	if err != nil {
		return nil, err
	}
	var files []string
	for _, line := range strings.Split(string(out), "\n") {
		if line != "" {
			files = append(files, line)
		}
	}
	return files, nil
}

// GrepFilesAtRef returns paths whose contents match any fixed string pattern at
// ref. Git grep returns exit 1 for no matches; callers receive an empty slice.
func (s *Store) GrepFilesAtRef(ctx context.Context, fullName, ref string, patterns []string) ([]string, error) {
	dir, err := s.repoPath(ctx, fullName)
	if err != nil {
		return nil, err
	}
	commit, err := s.resolveContentCommit(ctx, dir, ref)
	if err != nil {
		return nil, err
	}

	seenPatterns := make(map[string]struct{}, len(patterns))
	args := []string{"-C", dir, "grep", "-I", "-i", "-l", "-F"}
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		if _, ok := seenPatterns[pattern]; ok {
			continue
		}
		seenPatterns[pattern] = struct{}{}
		args = append(args, "-e", pattern)
	}
	if len(seenPatterns) == 0 {
		return []string{}, nil
	}
	args = append(args, commit, "--")

	out, err := exec.CommandContext(ctx, "git", args...).CombinedOutput()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return []string{}, nil
		}
		return nil, fmt.Errorf("git grep files failed: %w, output: %s", err, out)
	}

	seenPaths := make(map[string]struct{})
	var paths []string
	prefix := commit + ":"
	for _, raw := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if raw == "" {
			continue
		}
		path := raw
		if strings.HasPrefix(path, prefix) {
			path = strings.TrimPrefix(path, prefix)
		} else if idx := strings.IndexByte(path, ':'); idx >= 0 {
			path = path[idx+1:]
		}
		if path == "" {
			continue
		}
		if _, ok := seenPaths[path]; ok {
			continue
		}
		seenPaths[path] = struct{}{}
		paths = append(paths, path)
	}
	return paths, nil
}

// TreeEntry represents an entry in a git tree (file or directory).
type TreeEntry struct {
	Type string // "blob" or "tree"
	Name string // filename or directory name
	Path string // full path relative to repo root
	SHA  string // object SHA
	Size int64  // size in bytes (0 for directories)
}

// ListDir returns the contents of a directory at the given path in the repository.
// Returns a list of TreeEntry objects for files and subdirectories.
func (s *Store) ListDir(ctx context.Context, fullName, dirPath string) ([]TreeEntry, error) {
	return s.ListDirAtRef(ctx, fullName, dirPath, "")
}

// ListDirAtRef returns the contents of a directory at the given path and ref.
// When ref is empty, it resolves HEAD and falls back to the first available
// branch only if HEAD itself is dangling.
func (s *Store) ListDirAtRef(ctx context.Context, fullName, dirPath, ref string) ([]TreeEntry, error) {
	dir, err := s.repoPath(ctx, fullName)
	if err != nil {
		return nil, err
	}
	commit, err := s.resolveContentCommit(ctx, dir, ref)
	if err != nil {
		return nil, err
	}

	// Use ls-tree to list directory contents (not recursive)
	// Format: <mode> <type> <object>\t<path>
	// Use "." for root directory, add trailing slash for subdirectories
	lsPath := dirPath
	if lsPath == "" {
		lsPath = "."
	} else if !strings.HasSuffix(lsPath, "/") {
		lsPath = lsPath + "/"
	}
	// `ls-tree -l` emits blob sizes inline as a 4th meta column (`-` for
	// non-blobs), avoiding a `git cat-file -s` fork per blob entry.
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "ls-tree", "-l", commit, lsPath)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var entries []TreeEntry
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		// Parse: "100644 blob <sha> <size>\t<path>" or "040000 tree <sha> -\t<path>"
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		metaParts := strings.Fields(parts[0])
		if len(metaParts) != 4 {
			continue
		}
		objType := metaParts[1]
		sha := metaParts[2]
		sizeField := metaParts[3]
		fullPath := parts[1]
		name := fullPath
		if idx := strings.LastIndex(fullPath, "/"); idx >= 0 {
			name = fullPath[idx+1:]
		}

		entry := TreeEntry{
			Type: objType,
			Name: name,
			Path: fullPath,
			SHA:  sha,
			Size: 0,
		}
		if objType == "blob" && sizeField != "-" {
			fmt.Sscanf(sizeField, "%d", &entry.Size)
		}

		entries = append(entries, entry)
	}

	return entries, nil
}

// BlobSHAAtRef returns the blob SHA for an exact path at ref.
func (s *Store) BlobSHAAtRef(ctx context.Context, fullName, path, ref string) (string, error) {
	dir, err := s.repoPath(ctx, fullName)
	if err != nil {
		return "", err
	}
	commit, err := s.resolveContentCommit(ctx, dir, ref)
	if err != nil {
		return "", err
	}

	cmd := exec.CommandContext(ctx, "git", "-C", dir, "ls-tree", "-z", commit, "--", path)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("ls-tree failed: %w, output: %s", err, out)
	}
	raw := strings.TrimRight(string(out), "\x00")
	if raw == "" {
		return "", fmt.Errorf("path not found")
	}
	parts := strings.SplitN(raw, "\t", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid ls-tree entry")
	}
	meta := strings.Fields(parts[0])
	if len(meta) < 3 || meta[1] != "blob" {
		return "", fmt.Errorf("path is not a file")
	}
	if parts[1] != path {
		return "", fmt.Errorf("path not found")
	}
	return meta[2], nil
}

// IsDir checks if the given path is a directory in the repository.
func (s *Store) IsDir(ctx context.Context, fullName, path string) (bool, error) {
	return s.IsDirAtRef(ctx, fullName, path, "")
}

// IsDirAtRef checks if the given path is a directory in the repository at ref.
// When ref is empty, it resolves HEAD and falls back to the first available
// branch only if HEAD itself is dangling.
func (s *Store) IsDirAtRef(ctx context.Context, fullName, path, ref string) (bool, error) {
	dir, err := s.repoPath(ctx, fullName)
	if err != nil {
		return false, err
	}
	commit, err := s.resolveContentCommit(ctx, dir, ref)
	if err != nil {
		return false, err
	}

	// Use ls-tree to check if path is a tree (directory)
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "ls-tree", commit, path)
	out, err := cmd.Output()
	if err != nil {
		return false, err
	}

	// If the output contains entries with "tree" type, it's a directory
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		metaParts := strings.Fields(parts[0])
		if len(metaParts) >= 2 && metaParts[1] == "tree" {
			return true, nil
		}
	}

	// Check if it's a single tree entry (when path is a directory itself)
	// git ls-tree on a directory shows its contents, not the directory itself
	// So if we got multiple entries or entries with "tree" type, it's a directory
	if len(lines) > 0 && lines[0] != "" {
		// Multiple entries means it's a directory
		if len(lines) > 1 {
			return true, nil
		}
		// Single entry - check if it's a tree
		parts := strings.SplitN(lines[0], "\t", 2)
		if len(parts) == 2 {
			metaParts := strings.Fields(parts[0])
			if len(metaParts) >= 2 && metaParts[1] == "tree" {
				return true, nil
			}
		}
	}

	return false, nil
}

func (s *Store) resolveContentCommit(ctx context.Context, dir, ref string) (string, error) {
	if ref != "" {
		return resolveCommitish(ctx, dir, ref)
	}

	commit, err := resolveCommitish(ctx, dir, "HEAD")
	if err == nil {
		return commit, nil
	}

	branch, branchErr := firstAvailableBranch(ctx, dir)
	if branchErr != nil || branch == "" {
		return "", err
	}
	return resolveCommitish(ctx, dir, branch)
}

func resolveCommitish(ctx context.Context, dir, ref string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func firstAvailableBranch(ctx context.Context, dir string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "for-each-ref", "--format=%(refname:short)", RefsHeadsPrefix, "--count=1")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// TagInfo holds a tag name and the commit SHA it points to.
type TagInfo struct {
	Name string
	SHA  string
}

// ListTags returns all tags in a repository.
func (s *Store) ListTags(ctx context.Context, fullName string) ([]TagInfo, error) {
	dir, err := s.repoPath(ctx, fullName)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "for-each-ref", "--sort=-creatordate",
		"--format=%(refname:short) %(objectname:short)", RefsTagsPrefix)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var tags []TagInfo
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " ", 2)
		if len(parts) == 2 {
			tags = append(tags, TagInfo{Name: parts[0], SHA: parts[1]})
		}
	}
	return tags, nil
}

// DiffNameStatus returns the `git diff --name-status base...head` output.
func (s *Store) DiffNameStatus(ctx context.Context, fullName, base, head string) (string, error) {
	if !IsValidRev(base) || !IsValidRev(head) {
		return "", fmt.Errorf("invalid base or head revision")
	}
	dir, err := s.repoPath(ctx, fullName)
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, "git", "diff", "--name-status", base+"..."+head)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git diff failed: %v\n%s", err, out)
	}
	return string(out), nil
}

// DiffNumStat returns the `git diff --numstat base...head` output with per-file additions/deletions.
func (s *Store) DiffNumStat(ctx context.Context, fullName, base, head string) (string, error) {
	if !IsValidRev(base) || !IsValidRev(head) {
		return "", fmt.Errorf("invalid base or head revision")
	}
	dir, err := s.repoPath(ctx, fullName)
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, "git", "diff", "--numstat", base+"..."+head)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git diff --numstat failed: %v\n%s", err, out)
	}
	return string(out), nil
}

// DiffRaw returns the full unified diff output for base...head.
func (s *Store) DiffRaw(ctx context.Context, fullName, base, head string) (string, error) {
	if !IsValidRev(base) || !IsValidRev(head) {
		return "", fmt.Errorf("invalid base or head revision")
	}
	dir, err := s.repoPath(ctx, fullName)
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, "git", "diff", base+"..."+head)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git diff failed: %v\n%s", err, out)
	}
	return string(out), nil
}

func branchRef(branch string) string {
	return plumbing.NewBranchReferenceName(branch).String()
}

func (s *Store) resolveBranchParent(ctx context.Context, dir, branch string, require bool) (string, string, error) {
	ref := branchRef(branch)
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", ref).Output()
	if err != nil {
		if require {
			return ref, "", fmt.Errorf("branch %s not found: %w", branch, err)
		}
		return ref, "", nil
	}
	return ref, strings.TrimSpace(string(out)), nil
}

func commitEnv() []string {
	return append(os.Environ(),
		fmt.Sprintf("GIT_AUTHOR_NAME=%s", defaultCommitName),
		fmt.Sprintf("GIT_AUTHOR_EMAIL=%s", defaultCommitEmail),
		fmt.Sprintf("GIT_COMMITTER_NAME=%s", defaultCommitName),
		fmt.Sprintf("GIT_COMMITTER_EMAIL=%s", defaultCommitEmail),
	)
}

func (s *Store) commitTree(ctx context.Context, dir, treeSHA, parentSHA, message string) (string, error) {
	return s.commitTreeAt(ctx, dir, treeSHA, parentSHA, message, time.Time{})
}

func (s *Store) commitTreeAt(ctx context.Context, dir, treeSHA, parentSHA, message string, at time.Time) (string, error) {
	commitArgs := []string{"-C", dir, "commit-tree", treeSHA, "-m", message}
	if parentSHA != "" {
		commitArgs = append(commitArgs, "-p", parentSHA)
	}
	commitCmd := exec.CommandContext(ctx, "git", commitArgs...)
	if at.IsZero() {
		commitCmd.Env = commitEnv()
	} else {
		commitCmd.Env = append(commitEnv(),
			"GIT_AUTHOR_DATE="+at.Format(time.RFC3339),
			"GIT_COMMITTER_DATE="+at.Format(time.RFC3339),
		)
	}
	commitOut, err := commitCmd.Output()
	if err != nil {
		return "", fmt.Errorf("commit-tree failed: %w", err)
	}
	return strings.TrimSpace(string(commitOut)), nil
}

func (s *Store) updateRef(ctx context.Context, dir, ref, commitSHA string) error {
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "update-ref", ref, commitSHA).CombinedOutput()
	if err != nil {
		if isRefChangedOutput(string(out)) {
			return ErrRefChanged
		}
		return fmt.Errorf("update-ref failed: %w, output: %s", err, out)
	}
	return nil
}

func (s *Store) updateRefCAS(ctx context.Context, dir, ref, commitSHA, expectedOldSHA string) error {
	args := []string{"-C", dir, "update-ref", ref, commitSHA, expectedOldSHA}
	out, err := exec.CommandContext(ctx, "git", args...).CombinedOutput()
	if err != nil {
		if isRefChangedOutput(string(out)) {
			return ErrRefChanged
		}
		return fmt.Errorf("update-ref failed: %w, output: %s", err, out)
	}
	return nil
}

func isRefChangedOutput(out string) bool {
	return strings.Contains(out, "cannot lock ref")
}

// WriteFile creates or updates a file in the repository by creating a commit on the given branch.
// Returns the commit SHA of the new commit.
func (s *Store) WriteFile(ctx context.Context, fullName, branch, path, message string, content []byte) (string, error) {
	return s.writeFile(ctx, fullName, branch, path, message, content, "", false)
}

// WriteFileIfBranchHead creates a commit only if the target branch still points
// at expectedHeadSHA. When expectedHeadSHA is empty, the branch must not exist.
func (s *Store) WriteFileIfBranchHead(ctx context.Context, fullName, branch, path, message string, content []byte, expectedHeadSHA string) (string, error) {
	return s.writeFile(ctx, fullName, branch, path, message, content, expectedHeadSHA, true)
}

func (s *Store) writeFile(ctx context.Context, fullName, branch, path, message string, content []byte, expectedHeadSHA string, useCAS bool) (string, error) {
	dir, err := s.repoPath(ctx, fullName)
	if err != nil {
		return "", err
	}

	// 1. Get the current commit (if branch exists)
	ref, parentSHA, err := s.resolveBranchParent(ctx, dir, branch, false)
	if err != nil {
		return "", err
	}

	// 2. Allocate a unique index path. We delete the placeholder file
	// CreateTemp leaves behind so git can create a fresh valid index —
	// otherwise update-index on an empty 0-byte file fails with
	// "index file smaller than expected" on a fresh repo with no
	// parent commit.
	tmpIndex, err := os.CreateTemp("", "git-index-*")
	if err != nil {
		return "", fmt.Errorf("failed to create temp index: %w", err)
	}
	tmpIndexPath := tmpIndex.Name()
	tmpIndex.Close()
	os.Remove(tmpIndexPath)
	defer os.Remove(tmpIndexPath)

	indexEnv := append(os.Environ(), "GIT_INDEX_FILE="+tmpIndexPath)

	// 3. Read the existing tree into the index (if parent exists)
	if parentSHA != "" {
		cmd := exec.CommandContext(ctx, "git", "-C", dir, "read-tree", parentSHA)
		cmd.Env = indexEnv
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("read-tree failed: %w", err)
		}
	}

	// 4. Hash the blob and add it to the index
	// Using update-index with --stdin allows nested paths
	blobSHA, err := s.hashBlob(ctx, dir, content)
	if err != nil {
		return "", err
	}

	// Add the file to the index - this handles nested paths automatically
	addCmd := exec.CommandContext(ctx, "git", "-C", dir, "update-index", "--add", "--cacheinfo", "100644", blobSHA, path)
	addCmd.Env = indexEnv
	if out, err := addCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("update-index failed: %w, output: %s", err, out)
	}

	// 5. Write the tree from the index
	treeCmd := exec.CommandContext(ctx, "git", "-C", dir, "write-tree")
	treeCmd.Env = indexEnv
	treeOut, err := treeCmd.Output()
	if err != nil {
		return "", fmt.Errorf("write-tree failed: %w", err)
	}
	newTreeSHA := strings.TrimSpace(string(treeOut))

	// 6. Create the commit
	commitSHA, err := s.commitTree(ctx, dir, newTreeSHA, parentSHA, message)
	if err != nil {
		return "", err
	}

	// 7. Update the ref
	if useCAS {
		if err := s.updateRefCAS(ctx, dir, ref, commitSHA, expectedHeadSHA); err != nil {
			return "", err
		}
	} else {
		if err := s.updateRef(ctx, dir, ref, commitSHA); err != nil {
			return "", err
		}
	}

	return commitSHA, nil
}

// hashBlob hashes the content and returns the blob SHA.
func (s *Store) hashBlob(ctx context.Context, dir string, content []byte) (string, error) {
	hashCmd := exec.CommandContext(ctx, "git", "-C", dir, "hash-object", "-w", "--stdin")
	hashCmd.Stdin = strings.NewReader(string(content))
	blobOut, err := hashCmd.Output()
	if err != nil {
		return "", fmt.Errorf("hash-object failed: %w", err)
	}
	return strings.TrimSpace(string(blobOut)), nil
}

// DeleteFileFromRepo removes a file from a repository by creating a commit on the given branch.
// Returns the commit SHA of the new commit.
func (s *Store) DeleteFileFromRepo(ctx context.Context, fullName, branch, path, message string) (string, error) {
	dir, err := s.repoPath(ctx, fullName)
	if err != nil {
		return "", err
	}

	// 1. Get parent commit
	ref, parentSHA, err := s.resolveBranchParent(ctx, dir, branch, true)
	if err != nil {
		return "", err
	}

	// 2. Create a temporary index file
	tmpIndex, err := os.CreateTemp("", "git-index-*")
	if err != nil {
		return "", fmt.Errorf("failed to create temp index: %w", err)
	}
	tmpIndex.Close()
	defer os.Remove(tmpIndex.Name())

	// Helper to run git commands with the temp index
	runGit := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", "--git-dir", dir)
		cmd.Env = append(os.Environ(), "GIT_INDEX_FILE="+tmpIndex.Name())
		cmd.Args = append(cmd.Args, args...)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	// 3. Start with an empty index
	if _, err := runGit("read-tree", "--empty"); err != nil {
		return "", fmt.Errorf("read-tree --empty failed: %w", err)
	}

	// 4. Read the existing tree and add all entries except the one to delete
	if parentSHA != "" {
		lsOut, err := exec.CommandContext(ctx, "git", "-C", dir, "ls-tree", "-r", parentSHA).Output()
		if err != nil {
			return "", fmt.Errorf("ls-tree failed: %w", err)
		}
		lines := strings.Split(strings.TrimSpace(string(lsOut)), "\n")
		for _, line := range lines {
			if line == "" {
				continue
			}
			// Parse: "mode type sha\tpath"
			parts := strings.SplitN(line, "\t", 2)
			if len(parts) != 2 {
				continue
			}
			filePath := parts[1]
			if filePath == path {
				continue // skip the deleted file
			}
			// Parse mode, type, sha from first part
			metaParts := strings.Fields(parts[0])
			if len(metaParts) != 3 {
				continue
			}
			mode := metaParts[0]
			sha := metaParts[2]
			// Add entry to index using cacheinfo
			_, err := runGit("update-index", "--add", "--cacheinfo", mode+","+sha+","+filePath)
			if err != nil {
				return "", fmt.Errorf("update-index --cacheinfo failed for %s: %w", filePath, err)
			}
		}
	}

	// 5. Write the tree from the index
	newTreeSHA, err := runGit("write-tree")
	if err != nil {
		return "", fmt.Errorf("write-tree failed: %w", err)
	}
	newTreeSHA = strings.TrimSpace(newTreeSHA)

	// 6. Create the commit
	commitSHA, err := s.commitTree(ctx, dir, newTreeSHA, parentSHA, message)
	if err != nil {
		return "", err
	}

	// 7. Update the ref only if no concurrent writer moved the branch.
	if err := s.updateRefCAS(ctx, dir, ref, commitSHA, parentSHA); err != nil {
		return "", err
	}

	return commitSHA, nil
}

// MoveFile renames a file in the repository by creating a single commit on the
// given branch. The destination path must not already exist.
func (s *Store) MoveFile(ctx context.Context, fullName, branch, oldPath, newPath, message string) (string, error) {
	return s.MoveFiles(ctx, fullName, branch, []FileMove{{
		OldPath: oldPath,
		NewPath: newPath,
	}}, message)
}

// MoveFiles renames multiple files in the repository by creating a single
// commit on the given branch. Callers are responsible for validating that the
// destination paths do not already exist and that the move set is conflict-free.
func (s *Store) MoveFiles(ctx context.Context, fullName, branch string, moves []FileMove, message string) (string, error) {
	if len(moves) == 0 {
		return "", fmt.Errorf("no files to move")
	}

	dir, err := s.repoPath(ctx, fullName)
	if err != nil {
		return "", err
	}

	ref, parentSHA, err := s.resolveBranchParent(ctx, dir, branch, true)
	if err != nil {
		return "", err
	}
	if parentSHA == "" {
		return "", fmt.Errorf("path not found")
	}

	oldPaths := make([]string, 0, len(moves))
	for _, move := range moves {
		oldPaths = append(oldPaths, move.OldPath)
	}

	entryCmd := exec.CommandContext(ctx, "git", "--git-dir", dir, "ls-tree", "-z", parentSHA, "--")
	entryCmd.Args = append(entryCmd.Args, oldPaths...)
	entryOut, err := entryCmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("ls-tree failed: %w, output: %s", err, entryOut)
	}

	type treeEntry struct {
		mode string
		blob string
	}
	entries := make(map[string]treeEntry, len(moves))
	rawEntries := strings.TrimRight(string(entryOut), "\x00")
	if rawEntries != "" {
		for _, rawEntry := range strings.Split(rawEntries, "\x00") {
			parts := strings.SplitN(rawEntry, "\t", 2)
			if len(parts) != 2 {
				continue
			}
			metaParts := strings.Fields(parts[0])
			if len(metaParts) < 3 || metaParts[1] != "blob" {
				return "", fmt.Errorf("path is not a file")
			}
			entries[parts[1]] = treeEntry{
				mode: metaParts[0],
				blob: metaParts[2],
			}
		}
	}

	tmpIndex, err := os.CreateTemp("", "git-index-*")
	if err != nil {
		return "", fmt.Errorf("failed to create temp index: %w", err)
	}
	tmpIndexPath := tmpIndex.Name()
	tmpIndex.Close()
	os.Remove(tmpIndexPath)
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

	removed := make(map[string]struct{}, len(moves))
	for _, move := range moves {
		entry, ok := entries[move.OldPath]
		if !ok {
			return "", fmt.Errorf("path not found: %s", move.OldPath)
		}
		if _, seen := removed[move.OldPath]; !seen {
			if out, err := runGit("update-index", "--force-remove", move.OldPath); err != nil {
				return "", fmt.Errorf("update-index remove failed: %w, output: %s", err, out)
			}
			removed[move.OldPath] = struct{}{}
		}
		if out, err := runGit("update-index", "--add", "--cacheinfo", entry.mode, entry.blob, move.NewPath); err != nil {
			return "", fmt.Errorf("update-index add failed: %w, output: %s", err, out)
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

// GetDiffHunk extracts the specific unified diff hunk from a PR diff spanning base to head for a given file and target RHS line.
func (s *Store) GetDiffHunk(ctx context.Context, fullName, base, head, path string, line int) (string, error) {
	dir, err := s.repoPath(ctx, fullName)
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, "git", "diff", "-U4", base+"..."+head, "--", path)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git diff failed: %w", err)
	}

	lines := strings.Split(string(out), "\n")
	var currentHunk []string
	rhsLine := 0
	targetFound := false

	for _, l := range lines {
		if strings.HasPrefix(l, "@@ ") {
			if targetFound {
				break
			}
			currentHunk = []string{l}
			parts := strings.Split(l, " ")
			if len(parts) >= 3 {
				rhsHeader := parts[2]
				if strings.HasPrefix(rhsHeader, "+") {
					rhsParts := strings.Split(rhsHeader[1:], ",")
					fmt.Sscanf(rhsParts[0], "%d", &rhsLine)
				}
			}
			continue
		}

		if len(currentHunk) > 0 {
			if strings.HasPrefix(l, "diff ") || strings.HasPrefix(l, "index ") || strings.HasPrefix(l, "--- ") || strings.HasPrefix(l, "+++ ") {
				if targetFound {
					break
				}
				currentHunk = nil
				continue
			}

			currentHunk = append(currentHunk, l)

			if strings.HasPrefix(l, "+") || strings.HasPrefix(l, " ") {
				if rhsLine == line {
					targetFound = true
				}
				rhsLine++
			} else if strings.HasPrefix(l, "-") || strings.HasPrefix(l, "\\") {
				// rhsLine doesn't increment
			} else {
				if targetFound {
					break
				}
				currentHunk = nil
			}
		}
	}

	if targetFound {
		return strings.Join(currentHunk, "\n"), nil
	}
	return "", nil
}
