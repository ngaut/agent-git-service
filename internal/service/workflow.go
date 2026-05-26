package service

import (
	"context"
	"fmt"
	"strconv"

	"github.com/ngaut/agent-git-service/internal/db"

	"github.com/go-git/go-git/v5/plumbing"
)

// resolveRefSHA resolves a git ref (branch name) to its commit SHA.
// Returns ErrValidation if the ref does not exist or if the git backend is not configured.
func (s *Service) resolveRefSHA(ctx context.Context, repoFullName, ref string) (string, error) {
	if s.Git == nil {
		return "", fmt.Errorf("%w: git backend not configured", ErrValidation)
	}
	sha, err := s.Git.HeadSHA(ctx, repoFullName, ref)
	if err != nil {
		if err == plumbing.ErrReferenceNotFound {
			return "", fmt.Errorf("%w: ref %q not found", ErrValidation, ref)
		}
		return "", fmt.Errorf("resolve ref %q: %w", ref, err)
	}
	return sha, nil
}

// ListWorkflows retrieves all workflows for a repository.
func (s *Service) ListWorkflows(ctx context.Context, repoFullName string) ([]db.Workflow, error) {
	repo, err := s.GetRepo(ctx, repoFullName)
	if err != nil {
		return nil, err
	}

	var workflows []db.Workflow
	if err := s.DBForCtx(ctx).Where("repository_id = ?", repo.ID).Find(&workflows).Error; err != nil {
		return nil, err
	}
	return workflows, nil
}

// SetWorkflowState updates a workflow's state (e.g. "active", "disabled_manually").
func (s *Service) SetWorkflowState(ctx context.Context, repoFullName string, workflowID int, state string) error {
	repo, err := s.GetRepo(ctx, repoFullName)
	if err != nil {
		return err
	}

	var wf db.Workflow
	if err := s.DBForCtx(ctx).Where("repository_id = ? AND id = ?", repo.ID, workflowID).First(&wf).Error; err != nil {
		return wrapErr(err)
	}

	return s.DBForCtx(ctx).Model(&wf).Update("state", state).Error
}

// EnableWorkflow sets a workflow's state to "active".
func (s *Service) EnableWorkflow(ctx context.Context, repoFullName string, workflowID int) error {
	return s.SetWorkflowState(ctx, repoFullName, workflowID, db.WorkflowActive)
}

// DisableWorkflow sets a workflow's state to "disabled_manually".
func (s *Service) DisableWorkflow(ctx context.Context, repoFullName string, workflowID int) error {
	return s.SetWorkflowState(ctx, repoFullName, workflowID, db.WorkflowDisabled)
}

// GetWorkflow retrieves a specific workflow by ID.
func (s *Service) GetWorkflow(ctx context.Context, repoFullName string, workflowID int) (db.Workflow, error) {
	repo, err := s.GetRepo(ctx, repoFullName)
	if err != nil {
		return db.Workflow{}, err
	}

	var wf db.Workflow
	if err := s.DBForCtx(ctx).Where("repository_id = ? AND id = ?", repo.ID, workflowID).First(&wf).Error; err != nil {
		return db.Workflow{}, wrapErr(err)
	}

	return wf, nil
}

// ListWorkflowRuns retrieves runs, optionally filtered by workflow ID.
func (s *Service) ListWorkflowRuns(ctx context.Context, repoFullName string, workflowID int) ([]db.WorkflowRun, error) {
	repo, err := s.GetRepo(ctx, repoFullName)
	if err != nil {
		return nil, err
	}

	var runs []db.WorkflowRun
	q := s.DBForCtx(ctx).Where("repository_id = ?", repo.ID).Order("created_at desc")
	if workflowID > 0 {
		q = q.Where("workflow_id = ?", workflowID)
	}
	if err := q.Find(&runs).Error; err != nil {
		return nil, err
	}
	return runs, nil
}

// ListWorkflowRunsBySHA returns workflow runs matching a specific commit SHA.
func (s *Service) ListWorkflowRunsBySHA(ctx context.Context, repoID uint, sha string) ([]db.WorkflowRun, error) {
	var runs []db.WorkflowRun
	if sha == "" {
		return runs, nil
	}
	err := s.DBForCtx(ctx).Where("repository_id = ? AND head_sha = ?", repoID, sha).Order("created_at desc").Find(&runs).Error
	return runs, err
}

// ListWorkflowRunJobsByRun returns jobs for a specific workflow run.
func (s *Service) ListWorkflowRunJobsByRun(ctx context.Context, runID uint) ([]db.WorkflowRunJob, error) {
	var jobs []db.WorkflowRunJob
	err := s.DBForCtx(ctx).Where("run_id = ?", runID).Find(&jobs).Error
	return jobs, err
}

// GetWorkflowByID returns a workflow by its DB ID.
func (s *Service) GetWorkflowByID(ctx context.Context, id uint) (db.Workflow, error) {
	return getByID[db.Workflow](s, ctx, id)
}

// FindWorkflow retrieves a specific workflow by ID, name, or filename.
func (s *Service) FindWorkflow(ctx context.Context, repoFullName string, idOrName string) (*db.Workflow, error) {
	repo, err := s.GetRepo(ctx, repoFullName)
	if err != nil {
		return nil, err
	}

	var wf db.Workflow
	// Try by ID first
	if id, err := strconv.ParseUint(idOrName, 10, 64); err == nil {
		if s.DBForCtx(ctx).First(&wf, id).Error == nil && wf.RepositoryID == repo.ID {
			return &wf, nil
		}
	}
	// Try by name
	if s.DBForCtx(ctx).Where("repository_id = ? AND name = ?", repo.ID, idOrName).First(&wf).Error == nil {
		return &wf, nil
	}
	// Try by filename
	if s.DBForCtx(ctx).Where("repository_id = ? AND path LIKE ?", repo.ID, "%/"+escapeLike(idOrName)).First(&wf).Error == nil {
		return &wf, nil
	}
	return nil, ErrNotFound
}

// GetWorkflowRunJob returns a workflow run job by its DB ID.
func (s *Service) GetWorkflowRunJob(ctx context.Context, id uint) (db.WorkflowRunJob, error) {
	return getByID[db.WorkflowRunJob](s, ctx, id)
}

// GetWorkflowRunByID returns a workflow run by its DB ID.
func (s *Service) GetWorkflowRunByID(ctx context.Context, id uint) (db.WorkflowRun, error) {
	return getByID[db.WorkflowRun](s, ctx, id)
}

// GetWorkflowRun retrieves a specific workflow run by ID, scoped to the repository.
func (s *Service) GetWorkflowRun(ctx context.Context, repoFullName string, runID int) (db.WorkflowRun, error) {
	repo, err := s.GetRepo(ctx, repoFullName)
	if err != nil {
		return db.WorkflowRun{}, err
	}

	var run db.WorkflowRun
	if err := s.DBForCtx(ctx).Where("repository_id = ? AND id = ?", repo.ID, runID).First(&run).Error; err != nil {
		return run, wrapErrf(err, "workflow run %d", runID)
	}

	return run, nil
}

// DeleteWorkflowRun deletes a specific workflow run, scoped to the repository.
func (s *Service) DeleteWorkflowRun(ctx context.Context, repoFullName string, runID int) error {
	repo, err := s.GetRepo(ctx, repoFullName)
	if err != nil {
		return err
	}

	var run db.WorkflowRun
	if err := s.DBForCtx(ctx).Where("repository_id = ? AND id = ?", repo.ID, runID).First(&run).Error; err != nil {
		return wrapErr(err)
	}

	if err := s.DBForCtx(ctx).Where("run_id = ?", run.ID).Delete(&db.WorkflowRunJob{}).Error; err != nil {
		return err
	}

	res := s.DBForCtx(ctx).Delete(&run)
	return checkAffected(res)
}

// CancelWorkflowRun marks a workflow run as cancelled, scoped to the repository.
func (s *Service) CancelWorkflowRun(ctx context.Context, repoFullName string, runID int) error {
	repo, err := s.GetRepo(ctx, repoFullName)
	if err != nil {
		return err
	}

	var run db.WorkflowRun
	if err := s.DBForCtx(ctx).Where("repository_id = ? AND id = ?", repo.ID, runID).First(&run).Error; err != nil {
		return wrapErr(err)
	}

	if run.Status == db.RunCompleted {
		return fmt.Errorf("%w: cannot cancel a completed run", ErrInvalidState)
	}

	return s.DBForCtx(ctx).Model(&run).Updates(map[string]any{"status": db.RunCompleted, "conclusion": db.ConclusionCancelled}).Error
}

// ListWorkflowRunJobs retrieves jobs for a specific workflow run, scoped to the repository.
func (s *Service) ListWorkflowRunJobs(ctx context.Context, repoFullName string, runID int) ([]db.WorkflowRunJob, error) {
	repo, err := s.GetRepo(ctx, repoFullName)
	if err != nil {
		return nil, err
	}

	var run db.WorkflowRun
	if err := s.DBForCtx(ctx).Where("repository_id = ? AND id = ?", repo.ID, runID).First(&run).Error; err != nil {
		return nil, wrapErr(err)
	}

	var jobs []db.WorkflowRunJob
	if err := s.DBForCtx(ctx).Where("run_id = ?", run.ID).Find(&jobs).Error; err != nil {
		return nil, err
	}
	return jobs, nil
}

// ListWorkflowRunArtifacts retrieves artifacts for a specific workflow run, scoped to the repository.
func (s *Service) ListWorkflowRunArtifacts(ctx context.Context, repoFullName string, runID int) ([]db.Artifact, error) {
	repo, err := s.GetRepo(ctx, repoFullName)
	if err != nil {
		return nil, err
	}

	var run db.WorkflowRun
	if err := s.DBForCtx(ctx).Where("repository_id = ? AND id = ?", repo.ID, runID).First(&run).Error; err != nil {
		return nil, wrapErr(err)
	}

	var artifacts []db.Artifact
	if err := s.DBForCtx(ctx).Where("run_id = ?", run.ID).Find(&artifacts).Error; err != nil {
		return nil, err
	}
	return artifacts, nil
}

// ListRepoArtifacts retrieves all artifacts for a repository (across all runs).
func (s *Service) ListRepoArtifacts(ctx context.Context, repoFullName string) ([]db.Artifact, error) {
	repo, err := s.GetRepo(ctx, repoFullName)
	if err != nil {
		return nil, err
	}
	var runIDs []uint
	s.DBForCtx(ctx).Model(&db.WorkflowRun{}).Where("repository_id = ?", repo.ID).Pluck("id", &runIDs)
	if len(runIDs) == 0 {
		return []db.Artifact{}, nil
	}
	var artifacts []db.Artifact
	if err := s.DBForCtx(ctx).Where("run_id IN ?", runIDs).Find(&artifacts).Error; err != nil {
		return nil, err
	}
	return artifacts, nil
}

// GetArtifact retrieves a specific artifact by ID, scoped to the repository.
func (s *Service) GetArtifact(ctx context.Context, repoFullName string, artifactID int) (db.Artifact, error) {
	repo, err := s.GetRepo(ctx, repoFullName)
	if err != nil {
		return db.Artifact{}, err
	}

	var art db.Artifact
	if err := s.DBForCtx(ctx).
		Joins("JOIN workflow_runs ON workflow_runs.id = artifacts.run_id").
		Where("artifacts.id = ? AND workflow_runs.repository_id = ?", artifactID, repo.ID).
		First(&art).Error; err != nil {
		return db.Artifact{}, wrapErr(err)
	}
	return art, nil
}

// ListActionCaches retrieves all caches for a repository.
func (s *Service) ListActionCaches(ctx context.Context, repoFullName string) ([]db.ActionCache, error) {
	repo, err := s.GetRepo(ctx, repoFullName)
	if err != nil {
		return nil, err
	}

	var caches []db.ActionCache
	if err := s.DBForCtx(ctx).Where("repository_id = ?", repo.ID).Find(&caches).Error; err != nil {
		return nil, err
	}
	return caches, nil
}

// DeleteActionCacheByID deletes a single cache by its ID.
func (s *Service) DeleteActionCacheByID(ctx context.Context, id uint) error {
	return checkAffected(s.DBForCtx(ctx).Delete(&db.ActionCache{}, id))
}

// DeleteActionCaches deletes caches associated with a repository cache key.
func (s *Service) DeleteActionCaches(ctx context.Context, repoFullName string, key string) error {
	repo, err := s.GetRepo(ctx, repoFullName)
	if err != nil {
		return err
	}

	res := s.DBForCtx(ctx).Where("repository_id = ? AND `key` = ?", repo.ID, key).Delete(&db.ActionCache{})
	return checkAffected(res)
}
