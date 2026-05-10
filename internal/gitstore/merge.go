package gitstore

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"
)

const (
	// defaultCommitName is the committer name used for system-generated commits.
	defaultCommitName = "gh-server"
	// defaultCommitEmail is the committer email used for system-generated commits.
	defaultCommitEmail = "gh-server@localhost"
)

// MergeOptions defines options for the Merge operation.
type MergeOptions struct {
	FullName     string
	BaseBranch   string
	HeadBranch   string
	Committer    string
	Email        string
	MergeMessage string
}

// SquashMergeOptions defines options for the SquashMerge operation.
type SquashMergeOptions struct {
	FullName      string
	BaseBranch    string
	HeadBranch    string
	Committer     string
	Email         string
	SquashMessage string
}

// RebaseOptions defines options for the Rebase operation.
type RebaseOptions struct {
	FullName   string
	BaseBranch string
	HeadBranch string
	Committer  string
	Email      string
}

// UpdatePRBranchOptions defines options for the UpdatePRBranch operation.
type UpdatePRBranchOptions struct {
	FullName     string
	BaseBranch   string
	HeadBranch   string
	Committer    string
	Email        string
	UpdateMethod string
}

// cloneToTmp creates a temporary working-tree clone of repoDir.
// The returned cleanup function removes the temporary directory.
func cloneToTmp(ctx context.Context, repoDir, prefix string) (string, func(), error) {
	tmpDir, err := os.MkdirTemp("", prefix)
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(tmpDir) }
	if out, err := exec.CommandContext(ctx, "git", "clone", repoDir, tmpDir).CombinedOutput(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("git clone failed: %v\n%s", err, string(out))
	}
	return tmpDir, cleanup, nil
}

// configUser sets git user.name and user.email in the given repo dir.
func configUser(ctx context.Context, dir, name, email string) error {
	if err := exec.CommandContext(ctx, "git", "-C", dir, "config", "user.name", name).Run(); err != nil {
		return fmt.Errorf("git config user.name: %w", err)
	}
	if err := exec.CommandContext(ctx, "git", "-C", dir, "config", "user.email", email).Run(); err != nil {
		return fmt.Errorf("git config user.email: %w", err)
	}
	return nil
}

type tempCloneOptions struct {
	opName         string
	prefix         string
	committer      string
	email          string
	pushBranch     string
	allowForcePush bool
}

func (s *Store) withTempClone(ctx context.Context, fullName string, opts tempCloneOptions, fn func(tmpDir string) error) (string, error) {
	mu := s.repoLock(ctx, fullName)
	mu.Lock()
	defer mu.Unlock()

	repoDir, err := s.repoPath(ctx, fullName)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(repoDir); err != nil {
		return "", fmt.Errorf("gitstore %s: repo not found: %w", opts.opName, err)
	}

	tmpDir, cleanup, err := cloneToTmp(ctx, repoDir, opts.prefix)
	if err != nil {
		return "", err
	}
	defer cleanup()

	committer := opts.committer
	email := opts.email
	if committer == "" {
		committer = defaultCommitName
	}
	if email == "" {
		email = defaultCommitEmail
	}
	if err := configUser(ctx, tmpDir, committer, email); err != nil {
		return "", err
	}

	if err := fn(tmpDir); err != nil {
		return "", err
	}

	out, err := exec.CommandContext(ctx, "git", "-C", tmpDir, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse HEAD failed: %v", err)
	}
	sha := strings.TrimSpace(string(out))

	if opts.pushBranch != "" {
		if pushOut, err := exec.CommandContext(ctx, "git", "-C", tmpDir, "push", "origin", opts.pushBranch).CombinedOutput(); err != nil {
			if opts.allowForcePush {
				if pushOut2, err2 := exec.CommandContext(ctx, "git", "-C", tmpDir, "push", "--force", "origin", opts.pushBranch).CombinedOutput(); err2 != nil {
					return "", fmt.Errorf("git push failed: %v\n%s", err2, string(pushOut2))
				}
			} else {
				return "", fmt.Errorf("git push failed: %v\n%s", err, string(pushOut))
			}
		}
	}

	return sha, nil
}

// CanMerge checks whether headBranch can be cleanly merged into baseBranch
// using git merge-tree (no worktree needed). Returns "MERGEABLE", "CONFLICTING", or "UNKNOWN".
func (s *Store) CanMerge(ctx context.Context, fullName, baseBranch, headBranch string) string {
	repoDir, err := s.repoPath(ctx, fullName)
	if err != nil {
		return "UNKNOWN"
	}
	cmd := exec.CommandContext(ctx, "git", "-C", repoDir, "merge-tree",
		"--write-tree", "--no-messages",
		plumbing.NewBranchReferenceName(baseBranch).String(),
		plumbing.NewBranchReferenceName(headBranch).String())
	if err := cmd.Run(); err != nil {
		return "CONFLICTING"
	}
	return "MERGEABLE"
}

// IsEmpty reports whether the repository has no commits.
func (s *Store) IsEmpty(ctx context.Context, fullName string) bool {
	repoDir, err := s.repoPath(ctx, fullName)
	if err != nil {
		return true
	}
	cmd := exec.CommandContext(ctx, "git", "-C", repoDir, "cat-file", "-t", "HEAD")
	return cmd.Run() != nil
}

// DiskUsageKB returns the approximate size of a repository in kilobytes.
func (s *Store) DiskUsageKB(ctx context.Context, fullName string) int {
	repoDir, err := s.repoPath(ctx, fullName)
	if err != nil {
		return 0
	}
	cmd := exec.CommandContext(ctx, "du", "-sk", repoDir)
	out, err := cmd.Output()
	if err != nil {
		return 0
	}
	var kb int
	fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &kb)
	return kb
}

// Merge performs a real merge of headBranch into baseBranch creating a proper merge commit.
// Returns the resulting merge commit SHA.
func (s *Store) Merge(ctx context.Context, opts MergeOptions) (string, error) {
	return s.withTempClone(ctx, opts.FullName, tempCloneOptions{
		opName:     "merge",
		prefix:     "gh-server-merge-*",
		committer:  opts.Committer,
		email:      opts.Email,
		pushBranch: opts.BaseBranch,
	}, func(tmpDir string) error {
		// Checkout base branch
		if out, err := exec.CommandContext(ctx, "git", "-C", tmpDir, "checkout", opts.BaseBranch).CombinedOutput(); err != nil {
			return fmt.Errorf("git checkout %s failed: %v\n%s", opts.BaseBranch, err, string(out))
		}

		// Merge head branch into base
		if out, err := exec.CommandContext(ctx, "git", "-C", tmpDir, "merge", "origin/"+opts.HeadBranch, "-m", opts.MergeMessage, "--no-ff").CombinedOutput(); err != nil {
			return fmt.Errorf("git merge failed: %v\n%s", err, string(out))
		}

		return nil
	})
}

// SquashMerge squashes all commits from headBranch into a single commit on baseBranch.
// Unlike Merge (which creates a 2-parent merge commit), this creates a 1-parent commit
// containing the combined diff of the entire head branch. Returns the new HEAD SHA.
func (s *Store) SquashMerge(ctx context.Context, opts SquashMergeOptions) (string, error) {
	return s.withTempClone(ctx, opts.FullName, tempCloneOptions{
		opName:     "squash-merge",
		prefix:     "gh-server-squash-*",
		committer:  opts.Committer,
		email:      opts.Email,
		pushBranch: opts.BaseBranch,
	}, func(tmpDir string) error {
		// Checkout base branch
		if out, err := exec.CommandContext(ctx, "git", "-C", tmpDir, "checkout", opts.BaseBranch).CombinedOutput(); err != nil {
			return fmt.Errorf("git checkout %s failed: %v\n%s", opts.BaseBranch, err, string(out))
		}

		// Squash-merge: merge with --squash stages all changes without committing
		if out, err := exec.CommandContext(ctx, "git", "-C", tmpDir, "merge", "--squash", "origin/"+opts.HeadBranch).CombinedOutput(); err != nil {
			return fmt.Errorf("git merge --squash failed: %v\n%s", err, string(out))
		}

		// Commit the squashed changes as a single 1-parent commit
		if out, err := exec.CommandContext(ctx, "git", "-C", tmpDir, "commit", "-m", opts.SquashMessage).CombinedOutput(); err != nil {
			return fmt.Errorf("git commit (squash) failed: %v\n%s", err, string(out))
		}

		return nil
	})
}

// Rebase performs a git rebase of headBranch onto baseBranch fast-forward-style.
// Returns the new HEAD SHA of baseBranch after rebase.
func (s *Store) Rebase(ctx context.Context, opts RebaseOptions) (string, error) {
	return s.withTempClone(ctx, opts.FullName, tempCloneOptions{
		opName:     "rebase",
		prefix:     "gh-server-rebase-*",
		committer:  opts.Committer,
		email:      opts.Email,
		pushBranch: opts.BaseBranch,
	}, func(tmpDir string) error {
		// Checkout the head branch and rebase it onto base
		if out, err := exec.CommandContext(ctx, "git", "-C", tmpDir, "checkout", opts.HeadBranch).CombinedOutput(); err != nil {
			// Try with origin/ prefix
			if out2, err2 := exec.CommandContext(ctx, "git", "-C", tmpDir, "checkout", "-b", opts.HeadBranch, "origin/"+opts.HeadBranch).CombinedOutput(); err2 != nil {
				return fmt.Errorf("git checkout %s failed: %v\n%s", opts.HeadBranch, err, string(out)+string(out2))
			}
		}

		if out, err := exec.CommandContext(ctx, "git", "-C", tmpDir, "rebase", "origin/"+opts.BaseBranch).CombinedOutput(); err != nil {
			return fmt.Errorf("git rebase failed: %v\n%s", err, string(out))
		}

		// Checkout base branch and fast-forward merge rebased head
		if out, err := exec.CommandContext(ctx, "git", "-C", tmpDir, "checkout", opts.BaseBranch).CombinedOutput(); err != nil {
			return fmt.Errorf("git checkout %s failed: %v\n%s", opts.BaseBranch, err, string(out))
		}
		if out, err := exec.CommandContext(ctx, "git", "-C", tmpDir, "merge", "--ff-only", opts.HeadBranch).CombinedOutput(); err != nil {
			return fmt.Errorf("git ff-only merge failed: %v\n%s", err, string(out))
		}

		return nil
	})
}

// RevertMerge creates a revert branch that reverts a merge commit on baseBranch.
// Returns the new branch name and its HEAD SHA.
func (s *Store) RevertMerge(ctx context.Context, fullName, baseBranch, mergeCommitSHA, revertBranchName string) (string, error) {
	mu := s.repoLock(ctx, fullName)
	mu.Lock()
	defer mu.Unlock()
	repoDir, err := s.repoPath(ctx, fullName)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(repoDir); err != nil {
		return "", fmt.Errorf("gitstore revert: repo not found: %w", err)
	}

	tmpDir, cleanup, err := cloneToTmp(ctx, repoDir, "gh-server-revert-*")
	if err != nil {
		return "", err
	}
	defer cleanup()

	if err := configUser(ctx, tmpDir, defaultCommitName, defaultCommitEmail); err != nil {
		return "", err
	}

	// Checkout base branch
	if out, err := exec.CommandContext(ctx, "git", "-C", tmpDir, "checkout", baseBranch).CombinedOutput(); err != nil {
		return "", fmt.Errorf("git checkout %s failed: %v\n%s", baseBranch, err, string(out))
	}

	// Create the revert branch from base
	if out, err := exec.CommandContext(ctx, "git", "-C", tmpDir, "checkout", "-b", revertBranchName).CombinedOutput(); err != nil {
		return "", fmt.Errorf("git checkout -b %s failed: %v\n%s", revertBranchName, err, string(out))
	}

	// Revert the merge commit (use -m 1 for merge commits to specify mainline parent)
	revertArgs := []string{"-C", tmpDir, "revert", "--no-edit", mergeCommitSHA}
	// Inspect the commit to determine if it's a merge commit.
	// Error is intentionally discarded: if cat-file fails, parentCount
	// stays 0 and revert proceeds without -m 1 (correct for non-merge commits).
	cmd := exec.CommandContext(ctx, "git", "-C", tmpDir, "cat-file", "-p", mergeCommitSHA)
	catOut, _ := cmd.Output() //nolint:errcheck // see above
	parentCount := strings.Count(string(catOut), "\nparent ")
	if parentCount > 1 {
		revertArgs = []string{"-C", tmpDir, "revert", "--no-edit", "-m", "1", mergeCommitSHA}
	}

	if out, err := exec.CommandContext(ctx, "git", revertArgs...).CombinedOutput(); err != nil {
		return "", fmt.Errorf("git revert failed: %v\n%s", err, string(out))
	}

	out, err := exec.CommandContext(ctx, "git", "-C", tmpDir, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse HEAD failed: %v", err)
	}
	sha := strings.TrimSpace(string(out))

	// Push the revert branch to the bare repo
	if pushOut, err := exec.CommandContext(ctx, "git", "-C", tmpDir, "push", "origin", revertBranchName).CombinedOutput(); err != nil {
		return "", fmt.Errorf("git push failed: %v\n%s", err, string(pushOut))
	}

	return sha, nil
}

// UpdatePRBranch merges baseBranch into headBranch (or rebases headBranch onto baseBranch),
// effectively updating the PR branch with the latest changes from its base.
// Returns the new HEAD SHA of the head branch.
func (s *Store) UpdatePRBranch(ctx context.Context, opts UpdatePRBranchOptions) (string, error) {
	allowForcePush := strings.EqualFold(opts.UpdateMethod, "rebase")
	return s.withTempClone(ctx, opts.FullName, tempCloneOptions{
		opName:         "update-branch",
		prefix:         "gh-server-update-branch-*",
		committer:      opts.Committer,
		email:          opts.Email,
		pushBranch:     opts.HeadBranch,
		allowForcePush: allowForcePush,
	}, func(tmpDir string) error {
		// Checkout the head (PR) branch
		if out, err := exec.CommandContext(ctx, "git", "-C", tmpDir, "checkout", opts.HeadBranch).CombinedOutput(); err != nil {
			if out2, err2 := exec.CommandContext(ctx, "git", "-C", tmpDir, "checkout", "-b", opts.HeadBranch, "origin/"+opts.HeadBranch).CombinedOutput(); err2 != nil {
				return fmt.Errorf("git checkout %s failed: %v\n%s", opts.HeadBranch, err, string(out)+string(out2))
			}
		}

		if strings.EqualFold(opts.UpdateMethod, "rebase") {
			// Rebase head onto base
			if out, err := exec.CommandContext(ctx, "git", "-C", tmpDir, "rebase", "origin/"+opts.BaseBranch).CombinedOutput(); err != nil {
				return fmt.Errorf("merge conflict between base and head (updatePullRequestBranch): %v\n%s", err, string(out))
			}
		} else {
			// Merge base into head
			msg := fmt.Sprintf("Merge branch '%s' into %s", opts.BaseBranch, opts.HeadBranch)
			if out, err := exec.CommandContext(ctx, "git", "-C", tmpDir, "merge", "origin/"+opts.BaseBranch, "-m", msg, "--no-ff").CombinedOutput(); err != nil {
				return fmt.Errorf("merge conflict between base and head (updatePullRequestBranch): %v\n%s", err, string(out))
			}
		}

		return nil
	})
}

// SimulateMerge simulates a merge of headSHA into baseSHA using git merge-tree
// and creates a dangling merge commit using git commit-tree.
// Returns the resulting merge commit SHA without altering any branches.
func (s *Store) SimulateMerge(ctx context.Context, fullName, baseSHA, headSHA string) (string, error) {
	repoDir, err := s.repoPath(ctx, fullName)
	if err != nil {
		return "", err
	}

	// 1. git merge-tree --write-tree
	cmd := exec.CommandContext(ctx, "git", "-C", repoDir, "merge-tree", "--write-tree", "--no-messages", baseSHA, headSHA)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git merge-tree failed: %w", err)
	}
	treeSHA := strings.TrimSpace(string(out))
	if treeSHA == "" {
		return "", fmt.Errorf("git merge-tree returned empty tree")
	}

	// 2. git commit-tree
	msg := fmt.Sprintf("Merge commit simulator for PR: %s into %s", headSHA, baseSHA)
	cmd = exec.CommandContext(ctx, "git", "-C", repoDir, "commit-tree", treeSHA, "-p", baseSHA, "-p", headSHA, "-m", msg)

	cmd.Env = append(os.Environ(),
		fmt.Sprintf("GIT_AUTHOR_NAME=%s", defaultCommitName),
		fmt.Sprintf("GIT_AUTHOR_EMAIL=%s", defaultCommitEmail),
		fmt.Sprintf("GIT_COMMITTER_NAME=%s", defaultCommitName),
		fmt.Sprintf("GIT_COMMITTER_EMAIL=%s", defaultCommitEmail),
	)

	out, err = cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git commit-tree failed: %w (tree=%s, base=%s, head=%s)", err, treeSHA, baseSHA, headSHA)
	}
	return strings.TrimSpace(string(out)), nil
}
