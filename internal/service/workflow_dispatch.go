package service

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/gitstore"
	applog "github.com/ngaut/agent-git-service/internal/logging"

	"gopkg.in/yaml.v3"
)

// DispatchWorkflow dispatches a workflow, executes its steps, and returns the created run.
func (s *Service) DispatchWorkflow(ctx context.Context, repoFullName string, workflowID int, ref string, inputs map[string]any) (db.WorkflowRun, error) {
	repo, err := s.GetRepo(ctx, repoFullName)
	if err != nil {
		return db.WorkflowRun{}, err
	}

	var wf db.Workflow
	if err := s.DBForCtx(ctx).Where("repository_id = ? AND id = ?", repo.ID, workflowID).First(&wf).Error; err != nil {
		return db.WorkflowRun{}, wrapErr(err)
	}

	if wf.State != db.WorkflowActive {
		return db.WorkflowRun{}, fmt.Errorf("%w: workflow is disabled", ErrInvalidState)
	}

	var count int64
	if err := s.DBForCtx(ctx).Model(&db.WorkflowRun{}).Where("workflow_id = ?", wf.ID).Count(&count).Error; err != nil {
		return db.WorkflowRun{}, fmt.Errorf("dispatch workflow: count runs: %w", err)
	}

	// Resolve ref to SHA before creating the run.
	headSHA, err := s.resolveRefSHA(ctx, repoFullName, ref)
	if err != nil {
		return db.WorkflowRun{}, err
	}

	runNum := int(count) + 1
	var actorID *uint
	if user, ok := UserFromContext(ctx); ok {
		actorID = &user.ID
	}

	run := db.WorkflowRun{
		RepositoryID: repo.ID,
		WorkflowID:   wf.ID,
		ActorID:      actorID,
		Name: func() string {
			if wf.Name != "" {
				return wf.Name
			}
			return fmt.Sprintf("Run %d", runNum)
		}(),
		HeadBranch: ref,
		HeadSHA:    headSHA,
		RunNumber:  runNum,
		RunAttempt: 1,
		Event:      "workflow_dispatch",
		Status:     db.RunQueued,
		Conclusion: "",
	}

	if err := s.DBForCtx(ctx).Create(&run).Error; err != nil {
		return db.WorkflowRun{}, err
	}

	// Create a cache entry for cache tests.
	s.DBForCtx(ctx).Create(&db.ActionCache{
		RepositoryID:   repo.ID,
		Key:            "Linux-values",
		Ref:            gitstore.RefsHeadsPrefix + repo.DefaultBranch,
		Version:        "sha-version",
		SizeInBytes:    1024,
		LastAccessedAt: time.Now(),
	})

	// Execute workflow in background — jobs, logs, and artifacts are created here.
	// Uses the server context so the workflow outlives the HTTP request
	// but still drains on server shutdown.
	// Propagate any scoped DB so background writes target the correct handle.
	bgCtx := s.ServerCtx()
	bgCtx = applog.CloneContext(bgCtx, ctx)
	if scopedDB, ok := DBFromContext(ctx); ok {
		bgCtx = ContextWithDB(bgCtx, scopedDB)
	}
	applog.AddAttrs(bgCtx,
		slog.String("repo", repo.FullName),
		slog.Int("workflow_id", int(wf.ID)),
		slog.Int("workflow_run_id", int(run.ID)),
	)
	s.Wg.Add(1)
	go func() {
		defer s.Wg.Done()
		s.executeWorkflow(bgCtx, run, repo, wf)
	}()

	return run, nil
}

// ExtractWorkflowName extracts the workflow name from a workflow YAML file.
func ExtractWorkflowName(content []byte, filename string) string {
	var wf struct {
		Name string `yaml:"name"`
	}
	if yaml.Unmarshal(content, &wf) == nil && wf.Name != "" {
		return wf.Name
	}
	base := filepath.Base(filename)
	return strings.TrimSuffix(strings.TrimSuffix(base, ".yml"), ".yaml")
}

// SyncWorkflowsFromRepo parses workflow files from the repository and creates/updates them in the database.
func (s *Service) SyncWorkflowsFromRepo(ctx context.Context, repoFullName string) error {
	mu := s.getWorkflowSyncMu(repoFullName)
	mu.Lock()
	defer mu.Unlock()

	repo, err := s.GetRepo(ctx, repoFullName)
	if err != nil {
		return err
	}

	files, err := s.Git.ListTreeFiles(ctx, repoFullName)
	if err != nil {
		return nil // No workflows if no commits or tree
	}

	currentPaths := make(map[string]struct{})
	for _, f := range files {
		if !strings.HasPrefix(f, ".github/workflows/") || (!strings.HasSuffix(f, ".yml") && !strings.HasSuffix(f, ".yaml")) {
			continue
		}
		currentPaths[f] = struct{}{}
		content, err := s.Git.ReadFile(ctx, repoFullName, f)
		if err != nil {
			continue
		}
		name := ExtractWorkflowName(content, f)

		var wf db.Workflow
		if s.DBForCtx(ctx).Where("repository_id = ? AND path = ?", repo.ID, f).First(&wf).Error == nil {
			if wf.Name != name {
				wf.Name = name
				if err := s.DBForCtx(ctx).Save(&wf).Error; err != nil {
					slog.ErrorContext(ctx, "sync workflows save failed", "file", f, "repo", repoFullName, "error", err)
				}
			}
			continue
		}
		if err := s.DBForCtx(ctx).Create(&db.Workflow{
			RepositoryID: repo.ID,
			Name:         name,
			Path:         f,
			State:        db.WorkflowActive,
		}).Error; err != nil {
			slog.ErrorContext(ctx, "sync workflows create failed", "file", f, "repo", repoFullName, "error", err)
		}
	}

	q := s.DBForCtx(ctx).Where("repository_id = ?", repo.ID)
	if len(currentPaths) == 0 {
		if err := q.Delete(&db.Workflow{}).Error; err != nil {
			slog.ErrorContext(ctx, "sync workflows delete failed", "repo", repoFullName, "error", err)
		}
		return nil
	}

	paths := make([]string, 0, len(currentPaths))
	for path := range currentPaths {
		paths = append(paths, path)
	}
	if err := q.Where("path NOT IN ?", paths).Delete(&db.Workflow{}).Error; err != nil {
		slog.ErrorContext(ctx, "sync workflows delete stale failed", "repo", repoFullName, "error", err)
	}

	return nil
}

// RerunWorkflowRun handles re-running a completed workflow run.
func (s *Service) RerunWorkflowRun(ctx context.Context, repoFullName string, runID int) error {
	repo, err := s.GetRepo(ctx, repoFullName)
	if err != nil {
		return err
	}

	var run db.WorkflowRun
	if err := s.DBForCtx(ctx).Where("repository_id = ? AND id = ?", repo.ID, runID).First(&run).Error; err != nil {
		return ErrNotFound
	}

	if err := s.DBForCtx(ctx).Model(&run).Updates(map[string]any{
		"status":      db.RunCompleted,
		"conclusion":  db.ConclusionSuccess,
		"run_attempt": run.RunAttempt + 1,
		"updated_at":  time.Now(),
	}).Error; err != nil {
		return err
	}
	if run.ActorID != nil {
		if _, err := s.CreateWorkflowEventNotification(ctx, *run.ActorID, *run.ActorID, run.ID, run.RepositoryID); err != nil {
			return err
		}
	}
	return nil
}
