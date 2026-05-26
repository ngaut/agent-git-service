package service

import (
	"context"
	"fmt"
	"os/exec"
	"time"

	"gh-server/internal/db"
)

// Test helper methods - exported only for testing purposes.
// These methods provide access to internal functions for test validation.

// LoadSecretsForTest exposes loadSecrets for testing.
func (s *Service) LoadSecretsForTest(ctx context.Context, repo db.Repository) map[string]string {
	return s.loadSecrets(ctx, repo)
}

// LoadEnvSecretsForTest exposes loadEnvSecrets for testing.
func (s *Service) LoadEnvSecretsForTest(ctx context.Context, repo db.Repository, env string) map[string]string {
	return s.loadEnvSecrets(ctx, repo, env)
}

// CreateArtifactFromPathForTest exposes createArtifactFromPath for testing.
func (s *Service) CreateArtifactFromPathForTest(ctx context.Context, runID uint, name, basePath string) error {
	return s.createArtifactFromPath(ctx, runID, name, basePath)
}

// CompleteRunForTest exposes completeRun for testing.
func (s *Service) CompleteRunForTest(ctx context.Context, runID uint, conclusion string) {
	s.completeRun(ctx, runID, conclusion)
}

// EnableWorkflowExecForTest enables workflow execution with a host-shell runner.
// Production code always uses the Docker sandbox when WorkflowExecEnabled is set.
func (s *Service) EnableWorkflowExecForTest(timeout time.Duration) {
	s.WorkflowExecEnabled = true
	if timeout > 0 {
		s.WorkflowExecTimeout = timeout
	}
	s.workflowStepRunner = workflowStepRunnerFunc(func(ctx context.Context, req workflowStepRequest) (workflowStepResult, error) {
		cmd := exec.CommandContext(ctx, "bash", "-e", "-c", req.Script)
		cmd.Dir = req.Dir
		cmd.Env = append([]string{
			"HOME=" + workflowTmpMount,
			"PATH=" + workflowExecPath,
			"CI=true",
			"GITHUB_ACTIONS=true",
		}, req.Env...)
		out, err := cmd.CombinedOutput()
		result := workflowStepResult{Output: out}
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				result.ExitCode = exitErr.ExitCode()
			} else {
				result.ExitCode = 1
			}
			if ctx.Err() == context.DeadlineExceeded {
				result.TimedOut = true
				result.ExitCode = 1
			}
			return result, err
		}
		return result, nil
	})
}

// DisableWorkflowExecForTest disables workflow execution and clears any custom runner.
func (s *Service) DisableWorkflowExecForTest() {
	s.WorkflowExecEnabled = false
	s.workflowStepRunner = nil
}

// SetWorkflowStepRunnerForTest replaces the workflow runner with a custom test hook.
func (s *Service) SetWorkflowStepRunnerForTest(timeout time.Duration, fn func(ctx context.Context, dir, script string, env []string) ([]byte, int, error)) {
	s.WorkflowExecEnabled = true
	if timeout > 0 {
		s.WorkflowExecTimeout = timeout
	}
	s.workflowStepRunner = workflowStepRunnerFunc(func(ctx context.Context, req workflowStepRequest) (workflowStepResult, error) {
		out, exitCode, err := fn(ctx, req.Dir, req.Script, req.Env)
		result := workflowStepResult{
			Output:   out,
			ExitCode: exitCode,
			TimedOut: ctx.Err() == context.DeadlineExceeded,
		}
		if err != nil {
			return result, err
		}
		if exitCode != 0 {
			return result, fmt.Errorf("workflow test runner exit code %d", exitCode)
		}
		return result, nil
	})
}

// SetWikiMigrationAfterSnapshotHookForTest installs a test-only hook
// after migrateOneWiki snapshots the migrated commit set and before it
// replays any git commits.
func (s *Service) SetWikiMigrationAfterSnapshotHookForTest(fn func(repoFullName string)) {
	s.testWikiMigrationAfterSnapshot = fn
}

// SetWikiBackgroundMigrationStartedHookForTest installs a test-only hook fired
// when a repo-scoped background wiki migration is claimed and scheduled.
func (s *Service) SetWikiBackgroundMigrationStartedHookForTest(fn func(repoFullName string)) {
	s.testWikiBackgroundMigrationStarted = fn
}

// IsPublicRepoForTest exposes isPublicRepo to external-package tests.
func IsPublicRepoForTest(s *Service, ctx context.Context, repoID uint) bool {
	return s.isPublicRepo(ctx, repoID)
}

// SetTestWikiCompactRefUpdateFailureForTest installs a test-only hook that can
// force CompactWikiHistory to fail after the catalog transaction commits.
func SetTestWikiCompactRefUpdateFailureForTest(s *Service, fn func(repoFullName, commitSHA string) error) {
	s.testWikiCompactRefUpdateFailure = fn
}

// SetTestWikiCompactionJobStartedForTest installs a test-only hook fired after
// the async compaction worker marks a job running.
func SetTestWikiCompactionJobStartedForTest(s *Service, fn func(jobID string)) {
	s.testWikiCompactionJobStarted = fn
}

// SetTestWikiCompactionJobContinueForTest installs a test-only hook that can
// block the async compaction worker until tests allow it to proceed.
func SetTestWikiCompactionJobContinueForTest(s *Service, fn func(jobID string)) {
	s.testWikiCompactionJobContinue = fn
}

// ClaimWikiBackgroundMigrationForTest exposes background migration slot claims for tests.
func (s *Service) ClaimWikiBackgroundMigrationForTest(ctx context.Context, repoFullName string) bool {
	repo, err := s.LookupRepoIdentity(ctx, repoFullName)
	if err != nil {
		return false
	}
	return s.claimWikiBackgroundMigration(s.wikiRepoKey(ctx, repo))
}

// ReleaseWikiBackgroundMigrationForTest exposes background migration cleanup for tests.
func (s *Service) ReleaseWikiBackgroundMigrationForTest(ctx context.Context, repoFullName string) {
	repo, err := s.LookupRepoIdentity(ctx, repoFullName)
	if err != nil {
		return
	}
	s.releaseWikiBackgroundMigration(s.wikiRepoKey(ctx, repo))
}
