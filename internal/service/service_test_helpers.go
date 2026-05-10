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
