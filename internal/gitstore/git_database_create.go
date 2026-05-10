package gitstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"
)

// GitBlobObject is the low-level git database blob representation returned
// by CreateBlobObject and GetGitBlob. It mirrors GitTreeObject / GitCommitObject.
// CreateBlobObject leaves Content empty; GetGitBlob fills it with the raw bytes.
type GitBlobObject struct {
	SHA     string
	Size    int64
	Content []byte
}

// ErrNotBlob is returned by GetGitBlob when the requested SHA exists but
// is not a blob object (e.g. it is a tree, commit, or annotated tag).
var ErrNotBlob = errors.New("object is not a blob")

// ErrBlobTooLarge is returned by GetGitBlob when the blob exceeds
// gitBlobMaxBytes. Mirrors GitHub's 100 MiB cap; protects the server from
// per-request 1.33x memory expansion (raw -> base64 -> JSON) on any blob
// that landed via push or libgit2 outside this REST surface's control.
var ErrBlobTooLarge = errors.New("blob is too large to retrieve via this API")

// gitBlobMaxBytes caps the response size for GET /git/blobs/{sha}.
// Matches GitHub's documented 100 MiB cap.
const gitBlobMaxBytes int64 = 100 * 1024 * 1024

// CreateTreeEntryInput is one entry passed to CreateTreeObject.
//
// Exactly one of SHA, Content, or DeleteSHA must be set:
//   - SHA references an existing object (blob/tree/commit).
//   - Content is only valid when Type == "blob"; the bytes are written as a
//     new blob and the resulting sha is used in the tree.
//   - DeleteSHA removes the path from BaseTree.
type CreateTreeEntryInput struct {
	Path      string
	Mode      string
	Type      string
	SHA       string
	Content   *string
	DeleteSHA bool
}

// CreateTreeOptions configures CreateTreeObject.
type CreateTreeOptions struct {
	BaseTree string
	Entries  []CreateTreeEntryInput
}

// ErrInvalidTreeEntry is returned when CreateTreeObject is called with a
// tree entry whose path / mode / type / sha / content combination is invalid.
var ErrInvalidTreeEntry = errors.New("invalid tree entry")

// ErrBaseTreeNotFound is returned when CreateTreeObject's BaseTree does not
// resolve to an existing tree object in the repository.
var ErrBaseTreeNotFound = errors.New("base_tree not found")

// allowed git tree entry modes (GitHub's documented set).
var validTreeModes = map[string]string{
	"100644": "blob",
	"100755": "blob",
	"120000": "blob",
	"040000": "tree",
	"40000":  "tree", // git canonicalizes this form; accept both
	"160000": "commit",
}

// CreateBlobObject writes content to the object database as a blob and
// returns the resulting SHA. It does not modify any ref.
func (s *Store) CreateBlobObject(ctx context.Context, fullName string, content []byte) (GitBlobObject, error) {
	dir, err := s.repoPath(ctx, fullName)
	if err != nil {
		return GitBlobObject{}, err
	}
	if _, err := os.Stat(dir); err != nil {
		return GitBlobObject{}, err
	}

	cmd := exec.CommandContext(ctx, "git", "-C", dir, "hash-object", "-w", "--stdin")
	cmd.Stdin = bytes.NewReader(content)
	out, err := cmd.Output()
	if err != nil {
		return GitBlobObject{}, fmt.Errorf("hash-object failed: %w", err)
	}
	sha := strings.TrimSpace(string(out))
	if sha == "" {
		return GitBlobObject{}, fmt.Errorf("hash-object returned empty sha")
	}
	return GitBlobObject{SHA: sha, Size: int64(len(content))}, nil
}

// GetGitBlob reads a blob object by SHA and returns its raw bytes.
// Returns ErrNotBlob if the SHA exists but identifies a non-blob object.
func (s *Store) GetGitBlob(ctx context.Context, fullName, sha string) (GitBlobObject, error) {
	dir, err := s.repoPath(ctx, fullName)
	if err != nil {
		return GitBlobObject{}, err
	}
	if _, err := os.Stat(dir); err != nil {
		return GitBlobObject{}, err
	}

	objSHA, err := s.resolveObjectSHA(ctx, dir, sha)
	if err != nil {
		return GitBlobObject{}, err
	}

	typeOut, err := exec.CommandContext(ctx, "git", "-C", dir, "cat-file", "-t", objSHA).Output()
	if err != nil {
		return GitBlobObject{}, fmt.Errorf("cat-file -t failed: %w", err)
	}
	if t := strings.TrimSpace(string(typeOut)); t != "blob" {
		return GitBlobObject{}, fmt.Errorf("%w: %s", ErrNotBlob, t)
	}

	// Check size before reading content to avoid OOM on large blobs.
	sizeOut, err := exec.CommandContext(ctx, "git", "-C", dir, "cat-file", "-s", objSHA).Output()
	if err != nil {
		return GitBlobObject{}, fmt.Errorf("cat-file -s failed: %w", err)
	}
	size, err := strconv.ParseInt(strings.TrimSpace(string(sizeOut)), 10, 64)
	if err != nil {
		return GitBlobObject{}, fmt.Errorf("parse blob size %q: %w", sizeOut, err)
	}
	if size > gitBlobMaxBytes {
		return GitBlobObject{}, fmt.Errorf("%w: %d bytes exceeds %d", ErrBlobTooLarge, size, gitBlobMaxBytes)
	}

	content, err := exec.CommandContext(ctx, "git", "-C", dir, "cat-file", "blob", objSHA).Output()
	if err != nil {
		return GitBlobObject{}, fmt.Errorf("cat-file blob failed: %w", err)
	}
	return GitBlobObject{SHA: objSHA, Size: int64(len(content)), Content: content}, nil
}

// CreateTreeObject builds a new tree from a base tree and a list of entries
// using a temporary git index, then returns the resulting tree.
func (s *Store) CreateTreeObject(ctx context.Context, fullName string, opts CreateTreeOptions) (GitTreeObject, error) {
	dir, err := s.repoPath(ctx, fullName)
	if err != nil {
		return GitTreeObject{}, err
	}
	if _, err := os.Stat(dir); err != nil {
		return GitTreeObject{}, err
	}

	if len(opts.Entries) == 0 {
		return GitTreeObject{}, fmt.Errorf("%w: tree must contain at least one entry", ErrInvalidTreeEntry)
	}

	// Make a unique path *without* creating the file: git's update-index
	// rejects an existing zero-byte index ("index file smaller than expected"),
	// but happily creates a fresh one if the path does not yet exist.
	tmpIndex, err := os.CreateTemp("", "git-index-*")
	if err != nil {
		return GitTreeObject{}, fmt.Errorf("failed to create temp index: %w", err)
	}
	indexPath := tmpIndex.Name()
	tmpIndex.Close()
	if err := os.Remove(indexPath); err != nil {
		return GitTreeObject{}, fmt.Errorf("failed to clear temp index: %w", err)
	}
	defer os.Remove(indexPath)

	indexEnv := append(os.Environ(), "GIT_INDEX_FILE="+indexPath)

	baseTree := strings.TrimSpace(opts.BaseTree)
	if baseTree != "" {
		baseSHA, err := s.resolveTreeSHA(ctx, dir, baseTree)
		if err != nil {
			return GitTreeObject{}, fmt.Errorf("%w: %v", ErrBaseTreeNotFound, err)
		}
		cmd := exec.CommandContext(ctx, "git", "-C", dir, "read-tree", baseSHA)
		cmd.Env = indexEnv
		if out, err := cmd.CombinedOutput(); err != nil {
			return GitTreeObject{}, fmt.Errorf("read-tree failed: %w, output: %s", err, out)
		}
	}

	for i, entry := range opts.Entries {
		if entry.DeleteSHA && baseTree == "" {
			return GitTreeObject{}, fmt.Errorf("entry %d: %w: sha:null requires base_tree", i, ErrInvalidTreeEntry)
		}
		if err := validateTreeEntry(entry); err != nil {
			return GitTreeObject{}, fmt.Errorf("entry %d: %w", i, err)
		}

		if entry.DeleteSHA {
			paths, err := indexMatchedPaths(ctx, dir, indexEnv, entry.Path)
			if err != nil {
				return GitTreeObject{}, fmt.Errorf("entry %d: checking delete path: %w", i, err)
			}
			if len(paths) == 0 {
				continue
			}

			var removals strings.Builder
			for _, matchedPath := range paths {
				removals.WriteString("0 ")
				removals.WriteString(ZeroSHA)
				removals.WriteByte('\t')
				removals.WriteString(matchedPath)
				removals.WriteByte('\n')
			}

			removeCmd := exec.CommandContext(ctx, "git", "-C", dir, "update-index", "--index-info")
			removeCmd.Env = indexEnv
			removeCmd.Stdin = strings.NewReader(removals.String())
			if out, err := removeCmd.CombinedOutput(); err != nil {
				return GitTreeObject{}, fmt.Errorf("entry %d: update-index remove failed: %w, output: %s", i, err, out)
			}
			continue
		}

		mode := canonicalTreeMode(entry.Mode)
		sha := strings.TrimSpace(entry.SHA)
		if sha == "" && entry.Content != nil {
			blob, err := s.CreateBlobObject(ctx, fullName, []byte(*entry.Content))
			if err != nil {
				return GitTreeObject{}, fmt.Errorf("entry %d: hashing inline content: %w", i, err)
			}
			sha = blob.SHA
		}

		addCmd := exec.CommandContext(ctx, "git", "-C", dir, "update-index", "--add", "--cacheinfo", mode, sha, entry.Path)
		addCmd.Env = indexEnv
		if out, err := addCmd.CombinedOutput(); err != nil {
			return GitTreeObject{}, fmt.Errorf("entry %d: update-index failed: %w, output: %s", i, err, out)
		}
	}

	treeCmd := exec.CommandContext(ctx, "git", "-C", dir, "write-tree")
	treeCmd.Env = indexEnv
	treeOut, err := treeCmd.Output()
	if err != nil {
		return GitTreeObject{}, fmt.Errorf("write-tree failed: %w", err)
	}
	newTreeSHA := strings.TrimSpace(string(treeOut))
	if newTreeSHA == "" {
		return GitTreeObject{}, fmt.Errorf("write-tree returned empty sha")
	}

	return s.GetGitTree(ctx, fullName, newTreeSHA, false)
}

func validateTreeEntry(e CreateTreeEntryInput) error {
	if err := validateTreePath(e.Path); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidTreeEntry, err)
	}
	wantType, modeOK := validTreeModes[e.Mode]
	if !modeOK {
		return fmt.Errorf("%w: unsupported mode %q", ErrInvalidTreeEntry, e.Mode)
	}
	if e.Type == "" {
		return fmt.Errorf("%w: type is required", ErrInvalidTreeEntry)
	}
	if e.Type != wantType {
		return fmt.Errorf("%w: type %q does not match mode %q (expected %q)", ErrInvalidTreeEntry, e.Type, e.Mode, wantType)
	}
	hasSHA := strings.TrimSpace(e.SHA) != ""
	hasContent := e.Content != nil
	hasDelete := e.DeleteSHA
	setCount := 0
	if hasSHA {
		setCount++
	}
	if hasContent {
		setCount++
	}
	if hasDelete {
		setCount++
	}
	if setCount > 1 {
		return fmt.Errorf("%w: only one of sha, content, or sha:null may be set", ErrInvalidTreeEntry)
	}
	if setCount == 0 {
		return fmt.Errorf("%w: one of sha, content, or sha:null must be set", ErrInvalidTreeEntry)
	}
	if hasContent && e.Type != "blob" {
		return fmt.Errorf("%w: content is only valid for blob entries", ErrInvalidTreeEntry)
	}
	if hasSHA && !plumbing.IsHash(strings.TrimSpace(e.SHA)) {
		return fmt.Errorf("%w: sha %q is not a valid object id", ErrInvalidTreeEntry, e.SHA)
	}
	return nil
}

func indexMatchedPaths(ctx context.Context, dir string, indexEnv []string, path string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "ls-files", "-z", "--", path)
	cmd.Env = indexEnv
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("ls-files failed: %w, output: %s", err, out)
	}
	if len(out) == 0 {
		return nil, nil
	}

	matched := strings.Split(string(out[:len(out)-1]), "\x00")
	return matched, nil
}

func validateTreePath(path string) error {
	if path == "" {
		return errors.New("path is required")
	}
	if strings.HasPrefix(path, "/") {
		return errors.New("path must not start with /")
	}
	if strings.HasPrefix(path, "-") {
		return errors.New("path must not start with -")
	}
	if strings.HasSuffix(path, "/") {
		return errors.New("path must not end with /")
	}
	if strings.Contains(path, "\x00") {
		return errors.New("path must not contain NUL")
	}
	for _, seg := range strings.Split(path, "/") {
		if seg == "" {
			return errors.New("path must not contain empty segments")
		}
		if seg == "." || seg == ".." {
			return errors.New("path must not contain . or .. segments")
		}
	}
	return nil
}

func canonicalTreeMode(mode string) string {
	if mode == "40000" {
		return "040000"
	}
	return mode
}
