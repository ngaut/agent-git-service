package gitstore

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5/storage/filesystem"
)

// Init creates a new bare git repository, optionally seeding a README commit.
func (s *Store) Init(ctx context.Context, fullName, defaultBranch string, seed bool) error {
	dir, err := s.repoPath(ctx, fullName)
	if err != nil {
		return err
	}
	if _, err := os.Stat(dir); err == nil {
		return nil // already exists
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("gitstore: mkdir %s: %w", dir, err)
	}

	stg := filesystem.NewStorage(osfs.New(dir), nil)

	repo, err := git.InitWithOptions(stg, nil, git.InitOptions{
		DefaultBranch: plumbing.NewBranchReferenceName(defaultBranch),
	})
	if err != nil {
		return fmt.Errorf("gitstore: git init: %w", err)
	}

	// Install a pre-receive hook that rejects non-fast-forward pushes on
	// non-standard ref namespaces. refs/heads/* and refs/tags/* remain
	// permissive (matching current server behaviour and preserving rebase
	// flows that re-push heads); custom namespaces like refs/locks/*,
	// refs/experiment/*, and refs/fleet/* require CAS, which is what
	// distributed-coordination schemes built on top of git refs rely on.
	if err := installNonFFRejectHook(dir); err != nil {
		return fmt.Errorf("gitstore: install pre-receive hook: %w", err)
	}

	if seed {
		if err := seedReadme(ctx, repo, defaultBranch); err != nil {
			return fmt.Errorf("gitstore: seed: %w", err)
		}
	}
	return nil
}

// Fork creates a copy of the repository by duplicating its directory structure.
func (s *Store) Fork(ctx context.Context, srcFullName, targetFullName string) error {
	srcPath, err := s.repoPath(ctx, srcFullName)
	if err != nil {
		return err
	}
	targetPath, err := s.repoPath(ctx, targetFullName)
	if err != nil {
		return err
	}

	if _, err := os.Stat(srcPath); err != nil {
		return fmt.Errorf("source repo does not exist: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(targetPath), 0o750); err != nil {
		return err
	}

	// Remove targetPath if it exists (e.g. from CreateRepo's InitBare)
	_ = os.RemoveAll(targetPath)

	cmd := exec.CommandContext(ctx, "cp", "-a", srcPath, targetPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("gitstore cp: %w (output: %s)", err, out)
	}

	// cp -a preserves the source's hooks/ directory, but re-install the
	// pre-receive hook defensively so a forked repo gets the current
	// policy even if the source was created before this behaviour landed.
	if err := installNonFFRejectHook(targetPath); err != nil {
		return fmt.Errorf("gitstore fork: %w", err)
	}

	return nil
}

// seedReadme creates an initial commit with README.md on defaultBranch.
func seedReadme(ctx context.Context, repo *git.Repository, defaultBranch string) error {
	stg := repo.Storer
	readmeContent := "# README\n\nInitialised by gh-server.\n"

	// Create blob.
	blobEnc := stg.NewEncodedObject()
	blobEnc.SetType(plumbing.BlobObject)
	w, err := blobEnc.Writer()
	if err != nil {
		return err
	}
	if _, err := fmt.Fprint(w, readmeContent); err != nil {
		_ = w.Close()
		return err
	}
	_ = w.Close()
	blobHash, err := stg.SetEncodedObject(blobEnc)
	if err != nil {
		return err
	}

	// Create tree.
	treeEnc := stg.NewEncodedObject()
	treeEnc.SetType(plumbing.TreeObject)
	tree := object.Tree{
		Entries: []object.TreeEntry{
			{Name: "README.md", Mode: filemode.Regular, Hash: blobHash},
		},
	}
	if err := tree.Encode(treeEnc); err != nil {
		return err
	}
	treeHash, err := stg.SetEncodedObject(treeEnc)
	if err != nil {
		return err
	}

	// Create commit.
	now := time.Now()
	sig := &object.Signature{Name: defaultCommitName, Email: defaultCommitEmail, When: now}
	commitEnc := stg.NewEncodedObject()
	commitEnc.SetType(plumbing.CommitObject)
	commit := object.Commit{
		Author:       *sig,
		Committer:    *sig,
		Message:      "Initial commit\n",
		TreeHash:     treeHash,
		ParentHashes: nil,
	}
	if err := commit.Encode(commitEnc); err != nil {
		return err
	}
	commitHash, err := stg.SetEncodedObject(commitEnc)
	if err != nil {
		return err
	}

	// Point HEAD → branch → commit.
	headRef := plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.NewBranchReferenceName(defaultBranch))
	if err := stg.SetReference(headRef); err != nil {
		return err
	}
	return stg.SetReference(plumbing.NewHashReference(plumbing.NewBranchReferenceName(defaultBranch), commitHash))
}

// installNonFFRejectHook writes a pre-receive hook that rejects non-fast-
// forward pushes for ref namespaces outside refs/heads/ and refs/tags/.
// Standard head/tag pushes continue to use git's built-in receive-pack
// rules (permissive by default; branch-protection layered on top);
// custom namespaces get strict CAS so downstream tools that use refs as
// distributed-coordination primitives (locks, leader election, fleet
// membership) can detect contention.
//
// The hook is a posix-sh script written to <bareRepoDir>/hooks/pre-receive.
// git-receive-pack invokes it during the receive phase, streaming
// "<oldrev> <newrev> <refname>" lines on stdin; a non-zero exit aborts
// the entire push.
func installNonFFRejectHook(bareRepoDir string) error {
	hooksDir := filepath.Join(bareRepoDir, "hooks")
	if err := os.MkdirAll(hooksDir, 0o750); err != nil {
		return fmt.Errorf("mkdir hooks: %w", err)
	}
	path := filepath.Join(hooksDir, "pre-receive")
	script := fmt.Sprintf(`#!/bin/sh
# gh-server: reject non-fast-forward pushes to non-standard ref namespaces.
# Standard refs/heads/* and refs/tags/* continue to use git's built-in
# receive-pack rules; custom namespaces (refs/locks/*, refs/experiment/*,
# refs/fleet/*, etc.) require compare-and-swap semantics.
exit_code=0
while read -r oldrev newrev refname; do
    case "$refname" in
        refs/heads/*|refs/tags/*)
            continue
            ;;
    esac
    # Ref creation (oldrev is all-zeros) and deletion (newrev is all-zeros)
    # are always allowed; CAS enforcement is per-create via POST /git/refs.
    zero="%s"
    if [ "$oldrev" = "$zero" ] || [ "$newrev" = "$zero" ]; then
        continue
    fi
    if ! git merge-base --is-ancestor "$oldrev" "$newrev"; then
        echo "error: non-fast-forward push to $refname rejected" >&2
        echo "hint: custom ref namespaces require a fast-forward update;" >&2
        echo "hint: use --force-with-lease=$refname:<current-sha> to retry intentionally." >&2
        exit_code=1
    fi
done
exit "$exit_code"
`, ZeroSHA)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		return fmt.Errorf("write pre-receive hook: %w", err)
	}
	return nil
}

// Delete removes the on-disk git repository.
func (s *Store) Delete(ctx context.Context, fullName string) error {
	path, err := s.repoPath(ctx, fullName)
	if err != nil {
		return err
	}
	return os.RemoveAll(path)
}
