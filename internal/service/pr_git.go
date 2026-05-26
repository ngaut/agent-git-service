package service

import (
	"context"

	"github.com/ngaut/agent-git-service/internal/gitstore"
)

// ComparePR returns the ahead/behind/commit diff between two refs on a repo.
// Thin pass-through over gitstore.Compare — exists so GraphQL resolvers can
// call the service instead of reaching into Svc.Git directly (the documented
// tech-debt coupling in docs/module-contracts.md).
func (s *Service) ComparePR(ctx context.Context, repoFullName, base, head string) (gitstore.DiffResult, error) {
	return s.Git.Compare(ctx, repoFullName, base, head)
}

// CanMergePR returns the GitHub-style mergeability token for a PR's
// base/head refs ("MERGEABLE", "CONFLICTING", "UNKNOWN"). Non-error return
// matches gitstore's signature — merge conflicts are not errors here.
// A nil gitstore collapses to "UNKNOWN" so callers don't need their own
// guard; mirrors the same pattern in service/pr_merge.go.
func (s *Service) CanMergePR(ctx context.Context, repoFullName, baseBranch, headBranch string) string {
	if s.Git == nil {
		return "UNKNOWN"
	}
	return s.Git.CanMerge(ctx, repoFullName, baseBranch, headBranch)
}

// SimulatePRMerge performs a dry-run three-way merge and returns the
// resulting tree's SHA, used to populate GraphQL's potentialMergeCommit.
func (s *Service) SimulatePRMerge(ctx context.Context, repoFullName, baseSHA, headSHA string) (string, error) {
	return s.Git.SimulateMerge(ctx, repoFullName, baseSHA, headSHA)
}

// UpdatePRBranch merges (or rebases) the PR's base into its head branch.
// Returns the new head SHA. Caller should persist this on the PR record.
func (s *Service) UpdatePRBranch(ctx context.Context, opts gitstore.UpdatePRBranchOptions) (string, error) {
	return s.Git.UpdatePRBranch(ctx, opts)
}

// RevertPRMerge creates a revert branch on the repo. Used by the
// revertPullRequest GraphQL mutation; the resulting branch is then handed
// to CreatePR to build the revert PR.
func (s *Service) RevertPRMerge(ctx context.Context, repoFullName, baseBranch, mergeCommitSHA, revertBranchName string) (string, error) {
	return s.Git.RevertMerge(ctx, repoFullName, baseBranch, mergeCommitSHA, revertBranchName)
}
