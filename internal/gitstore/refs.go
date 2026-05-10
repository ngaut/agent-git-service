package gitstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"
)

// HeadSHA returns the commit SHA of the named branch.
func (s *Store) HeadSHA(ctx context.Context, fullName, branch string) (string, error) {
	repo, err := s.open(ctx, fullName)
	if err != nil {
		return "", err
	}
	ref, err := repo.Reference(plumbing.NewBranchReferenceName(branch), true)
	if err != nil {
		return "", err
	}
	return ref.Hash().String(), nil
}

// CreateBranch creates branchName pointing at fromBranch HEAD.
func (s *Store) CreateBranch(ctx context.Context, fullName, branchName, fromBranch string) error {
	repo, err := s.open(ctx, fullName)
	if err != nil {
		return err
	}
	fromRef, err := repo.Reference(plumbing.NewBranchReferenceName(fromBranch), true)
	if err != nil {
		return fmt.Errorf("gitstore: resolve %s: %w", fromBranch, err)
	}
	newRef := plumbing.NewHashReference(plumbing.NewBranchReferenceName(branchName), fromRef.Hash())
	return repo.Storer.SetReference(newRef)
}

// CreateBranchFromOid creates a new branch starting at a specific commit OID.
func (s *Store) CreateBranchFromOid(ctx context.Context, fullName, branchName, oid string) error {
	dir, err := s.repoPath(ctx, fullName)
	if err != nil {
		return err
	}
	if oid == "HEAD" {
		cmd := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "HEAD")
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("gitstore: resolve commit %s: %v\n%s", oid, err, out)
		}
		oid = strings.TrimSpace(string(out))
	}
	if !plumbing.IsHash(oid) {
		return fmt.Errorf("gitstore: invalid oid %q", oid)
	}
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "cat-file", "-t", oid)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("gitstore: resolve commit %s: %v\n%s", oid, err, out)
	}
	if objType := strings.TrimSpace(string(out)); objType != "commit" {
		return fmt.Errorf("gitstore: resolve commit %s: expected commit, got %s", oid, objType)
	}
	repo, err := s.open(ctx, fullName)
	if err != nil {
		return err
	}
	hash := plumbing.NewHash(oid)
	newRef := plumbing.NewHashReference(plumbing.NewBranchReferenceName(branchName), hash)
	return repo.Storer.SetReference(newRef)
}

// CreatePRRef creates a refs/pull/ID/head reference in the base repository,
// fetching the commit from the head repository if necessary.
func (s *Store) CreatePRRef(ctx context.Context, baseRepo, headRepo, headSHA string, number int) error {
	baseDir, err := s.repoPath(ctx, baseRepo)
	if err != nil {
		return err
	}
	if baseRepo != headRepo {
		headDir, err := s.repoPath(ctx, headRepo)
		if err != nil {
			return err
		}
		cmd := exec.CommandContext(ctx, "git", "fetch", headDir, headSHA)
		cmd.Dir = baseDir
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git fetch %s %s: %v\n%s", headDir, headSHA, err, out)
		}
	}
	refName := fmt.Sprintf("refs/pull/%d/head", number)
	cmd := exec.CommandContext(ctx, "git", "update-ref", refName, headSHA)
	cmd.Dir = baseDir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git update-ref %s %s: %v\n%s", refName, headSHA, err, out)
	}
	return nil
}

// UpdateRef updates a git reference to point to a new SHA.
func (s *Store) UpdateRef(ctx context.Context, fullName, ref, sha string) error {
	if !plumbing.IsHash(sha) {
		return fmt.Errorf("%w: %q", ErrInvalidSHA, sha)
	}
	dir, err := s.repoPath(ctx, fullName)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "update-ref", ref, sha)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git update-ref %s %s: %v\n%s", ref, sha, err, out)
	}
	return nil
}

// ErrNonFastForward is returned by UpdateRefSafe when the proposed SHA
// is not a fast-forward of the existing ref's SHA and the caller has
// not opted into the force path. Callers (REST PATCH /git/refs/...)
// map this to HTTP 422 "Update is not a fast forward" to match
// github.com's documented contract.
var ErrNonFastForward = errors.New("non-fast-forward update")

// UpdateRefSafe updates a ref to point at newSHA with GitHub-style
// fast-forward semantics:
//
//   - If the ref doesn't exist, returns ErrRefNotFound (the create
//     path goes through CreateRef, which is its own CAS).
//   - If newSHA equals the current SHA, the call is a no-op and
//     succeeds — matches GitHub's idempotent PATCH behaviour.
//   - When force is false, the current SHA must be an ancestor of
//     newSHA (fast-forward). Otherwise returns ErrNonFastForward.
//   - When force is true, the FF check is skipped and any update is
//     accepted, including rewinds and disjoint history.
//
// The actual update uses `git update-ref ref NEW OLD`, which git
// applies atomically: if a concurrent writer has advanced the ref
// since LookupRef, the second update fails because OLD no longer
// matches. This serialises racing writers without an external lock,
// which is what callers like the audit-ref CAS workflow on
// refs/locks/* depend on.
func (s *Store) UpdateRefSafe(ctx context.Context, fullName, ref, newSHA string, force bool) error {
	if !plumbing.IsHash(newSHA) {
		return fmt.Errorf("%w: %q", ErrInvalidSHA, newSHA)
	}
	dir, err := s.repoPath(ctx, fullName)
	if err != nil {
		return err
	}
	currentSHA, err := s.LookupRef(ctx, fullName, ref)
	if err != nil {
		return err
	}
	if currentSHA == newSHA {
		return nil
	}
	if !force {
		// `git merge-base --is-ancestor A B` exits 0 if A is reachable
		// from B (i.e. moving from A to B is a fast-forward), 1 if it
		// is not. Other exit codes are real errors.
		cmd := exec.CommandContext(ctx, "git", "-C", dir, "merge-base", "--is-ancestor", currentSHA, newSHA)
		if err := cmd.Run(); err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
				return ErrNonFastForward
			}
			return fmt.Errorf("git merge-base --is-ancestor: %w", err)
		}
	}
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "update-ref", ref, newSHA, currentSHA)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git update-ref %s %s %s: %v\n%s", ref, newSHA, currentSHA, err, out)
	}
	return nil
}

// RefInfo pairs a full ref name (e.g. "refs/locks/issue-42") with its
// current target SHA. Returned by LookupRef and ListRefsWithPrefix.
type RefInfo struct {
	Ref string
	SHA string
}

// ErrRefAlreadyExists is returned by CreateRef when the target ref already
// exists. Callers map this to HTTP 422 "Reference already exists" to match
// github.com's POST /repos/:o/:r/git/refs contract.
var ErrRefAlreadyExists = errors.New("ref already exists")

// ErrRefNotFound is returned by LookupRef when the ref does not exist.
var ErrRefNotFound = errors.New("ref not found")

// CreateRef atomically creates a new reference pointing at sha. If the ref
// already exists, returns ErrRefAlreadyExists; the existing target is NOT
// overwritten. Implemented via `git update-ref ref NEW ""` — git's native
// compare-and-swap where the empty "old" SHA means "must not exist". This
// is race-safe even under concurrent POSTs because git refuses the update
// in the loser's transaction log.
func (s *Store) CreateRef(ctx context.Context, fullName, ref, sha string) error {
	dir, err := s.repoPath(ctx, fullName)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "update-ref", ref, sha, "")
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	// git-update-ref prints "cannot lock ref '<ref>': reference already
	// exists" when the empty-old-sha guard catches an existing ref.
	if strings.Contains(string(out), "reference already exists") {
		return ErrRefAlreadyExists
	}
	return fmt.Errorf("git update-ref %s %s: %v\n%s", ref, sha, err, out)
}

// LookupRef resolves a fully-qualified ref (e.g. "refs/locks/issue-42") to
// its current SHA. Unlike HeadSHA (which is hard-coded to refs/heads/*),
// this works for any namespace — tags, custom, pull — which is what
// GET /repos/:o/:r/git/refs/:ref needs to support distributed-coordination
// use cases. Returns ErrRefNotFound if the ref isn't present.
func (s *Store) LookupRef(ctx context.Context, fullName, ref string) (string, error) {
	dir, err := s.repoPath(ctx, fullName)
	if err != nil {
		return "", err
	}
	// `show-ref --verify --hash` emits just the SHA (or exits non-zero on miss).
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "show-ref", "--verify", "--hash", ref)
	out, err := cmd.Output()
	if err != nil {
		return "", ErrRefNotFound
	}
	sha := strings.TrimSpace(string(out))
	if sha == "" {
		return "", ErrRefNotFound
	}
	return sha, nil
}

// ListRefsWithPrefix returns every ref whose full name begins with prefix,
// using GitHub's character-prefix semantics (not git for-each-ref's default
// "match up to a slash" rule). prefix is expected in git's "refs/..." form
// (e.g. "refs/locks", "refs/heads/octoswarm/fix-"); an empty prefix lists
// every ref in the repo. Used by the REST GET /git/matching-refs/:ref
// endpoint. Results are sorted by ref name for deterministic output.
func (s *Store) ListRefsWithPrefix(ctx context.Context, fullName, prefix string) ([]RefInfo, error) {
	dir, err := s.repoPath(ctx, fullName)
	if err != nil {
		return nil, err
	}
	args := []string{"-C", dir, "for-each-ref", "--format=%(objectname) %(refname)"}
	// git for-each-ref matches "literal up to a slash" or wildmatch with
	// FNM_PATHNAME — "*" does not cross "/". For a mid-component prefix
	// like "refs/heads/octoswarm/fix-" this misses GitHub's character-
	// prefix semantics, so we narrow the pattern to the path up to the
	// last "/" and filter by the full prefix below. When the prefix
	// already ends at a slash boundary, git's native matching is correct
	// and listing the wider namespace would over-fetch.
	gitPattern, needFilter := prefix, false
	if !strings.HasSuffix(prefix, "/") {
		if idx := strings.LastIndex(prefix, "/"); idx > 0 {
			gitPattern, needFilter = prefix[:idx], true
		} else if idx == 0 {
			gitPattern, needFilter = "", true
		} else if prefix != "" {
			needFilter = true
		}
	}
	if gitPattern != "" {
		args = append(args, gitPattern)
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git for-each-ref %s: %w", prefix, err)
	}
	var refs []RefInfo
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " ", 2)
		if len(parts) != 2 {
			continue
		}
		if needFilter && !strings.HasPrefix(parts[1], prefix) {
			continue
		}
		refs = append(refs, RefInfo{SHA: parts[0], Ref: parts[1]})
	}
	return refs, nil
}

// DeleteRef deletes a git reference (branch or tag).
func (s *Store) DeleteRef(ctx context.Context, fullName, ref string) error {
	dir, err := s.repoPath(ctx, fullName)
	if err != nil {
		return err
	}
	verifyCmd := exec.CommandContext(ctx, "git", "-C", dir, "show-ref", "--verify", ref)
	if out, err := verifyCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git show-ref --verify %s: %v\n%s", ref, err, out)
	}
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "update-ref", "-d", ref)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git update-ref -d %s: %v\n%s", ref, err, out)
	}
	return nil
}

// BranchInfo holds a branch name and its commit SHA.
type BranchInfo struct {
	Name string
	SHA  string
}

// ListBranches returns all branches in a bare git repository.
func (s *Store) ListBranches(ctx context.Context, fullName string) ([]BranchInfo, error) {
	repo, err := s.open(ctx, fullName)
	if err != nil {
		return nil, err
	}
	iter, err := repo.Branches()
	if err != nil {
		return nil, err
	}
	var branches []BranchInfo
	if err := iter.ForEach(func(ref *plumbing.Reference) error {
		branches = append(branches, BranchInfo{
			Name: ref.Name().Short(),
			SHA:  ref.Hash().String(),
		})
		return nil
	}); err != nil {
		return nil, fmt.Errorf("iterating branches: %w", err)
	}
	return branches, nil
}

// CreateTagIfNotExists creates an annotated tag if it doesn't already exist.
// Returns nil if the tag already exists.
func (s *Store) CreateTagIfNotExists(ctx context.Context, fullName, tagName, message, sha string) error {
	dir, err := s.repoPath(ctx, fullName)
	if err != nil {
		return err
	}

	// Check if tag already exists
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", plumbing.NewTagReferenceName(tagName).String())
	if err := cmd.Run(); err == nil {
		// Tag already exists
		return nil
	}

	// Create annotated tag with proper git identity
	cmd = exec.CommandContext(ctx, "git", "-C", dir, "tag", "-a", tagName, sha, "-m", message)
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("GIT_AUTHOR_NAME=%s", defaultCommitName),
		fmt.Sprintf("GIT_AUTHOR_EMAIL=%s", defaultCommitEmail),
		fmt.Sprintf("GIT_COMMITTER_NAME=%s", defaultCommitName),
		fmt.Sprintf("GIT_COMMITTER_EMAIL=%s", defaultCommitEmail),
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git tag -a %s %s: %v\n%s", tagName, sha, err, out)
	}
	return nil
}
