package service

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"
	applog "github.com/ngaut/agent-git-service/internal/logging"

	"gopkg.in/yaml.v3"
)

// workflowDef represents a parsed workflow YAML file.
type workflowDef struct {
	Name string                 `yaml:"name"`
	Jobs map[string]workflowJob `yaml:"jobs"`
}

type workflowJob struct {
	RunsOn      string         `yaml:"runs-on"`
	Environment string         `yaml:"environment"`
	Steps       []workflowStep `yaml:"steps"`
}

type workflowStep struct {
	Name string         `yaml:"name"`
	Run  string         `yaml:"run"`
	Uses string         `yaml:"uses"`
	With map[string]any `yaml:"with"`
	Env  map[string]any `yaml:"env"`
}

const (
	defaultWorkflowExecImage   = "bash:5.2"
	defaultWorkflowExecTimeout = 2 * time.Minute
	defaultWorkflowExecCPUs    = "1.0"
	defaultWorkflowExecMemory  = "256m"
	defaultWorkflowExecPids    = 128
	defaultWorkflowExecNoFile  = 1024
	defaultWorkflowExecTmpfs   = "64m"

	workflowWorkspaceMount   = "/workspace"
	workflowTmpMount         = "/tmp"
	workflowStepScriptName   = ".gh-server-workflow-step.sh"
	workflowLauncherFileName = ".gh-server-workflow-launch.sh"
	workflowExecPath         = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
)

var (
	secretRefRe     = regexp.MustCompile(`\$\{\{\s*secrets\.(\w+)\s*\}\}`)
	workflowEnvName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

type workflowStepRequest struct {
	Dir          string
	Script       string
	Env          []string
	RepoFullName string
	RunID        uint
	JobName      string
	StepName     string
}

type workflowStepResult struct {
	Output   []byte
	ExitCode int
	TimedOut bool
}

type workflowStepRunner interface {
	Run(ctx context.Context, req workflowStepRequest) (workflowStepResult, error)
}

type workflowStepRunnerFunc func(ctx context.Context, req workflowStepRequest) (workflowStepResult, error)

func (f workflowStepRunnerFunc) Run(ctx context.Context, req workflowStepRequest) (workflowStepResult, error) {
	return f(ctx, req)
}

// executeWorkflow runs all jobs in a workflow and marks the run as completed.
// Accepts a context for tracing; the caller is responsible for ensuring it
// outlives the goroutine (e.g. context.Background() at the dispatch site).
func (s *Service) executeWorkflow(ctx context.Context, run db.WorkflowRun, repo db.Repository, wf db.Workflow) {
	finalizeCtx := s.workflowDetachedContext(ctx)
	if !s.WorkflowExecEnabled {
		slog.WarnContext(finalizeCtx,
			"workflow execution blocked",
			"audit", true,
			"repo", repo.FullName,
			"workflow_run_id", run.ID,
			"workflow_path", wf.Path,
			"reason", "ENABLE_WORKFLOW_EXEC not enabled",
		)
		s.completeRun(finalizeCtx, run.ID, db.ConclusionFailure)
		return
	}

	timeout := s.workflowExecTimeoutValue()
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if err := s.DBForCtx(finalizeCtx).Model(&db.WorkflowRun{}).Where("id = ?", run.ID).
		Update("status", db.RunInProgress).Error; err != nil {
		slog.ErrorContext(finalizeCtx, "workflow start update failed", "workflow_run_id", run.ID, "error", err)
	}

	slog.InfoContext(execCtx,
		"workflow execution started",
		"audit", true,
		"repo", repo.FullName,
		"workflow_run_id", run.ID,
		"workflow_path", wf.Path,
		"image", s.workflowExecImageValue(),
		"timeout", timeout,
		"cpus", s.workflowExecCPUsValue(),
		"memory", s.workflowExecMemoryValue(),
		"pids_limit", s.workflowExecPidsValue(),
		"nofile_limit", s.workflowExecNoFileValue(),
		"tmpfs_size", s.workflowExecTmpfsValue(),
	)

	yamlContent, err := s.Git.ReadFile(execCtx, repo.FullName, wf.Path)
	if err != nil {
		slog.ErrorContext(execCtx, "workflow read failed", "repo", repo.FullName, "path", wf.Path, "workflow_run_id", run.ID, "error", err)
		s.completeRun(finalizeCtx, run.ID, db.ConclusionFailure)
		return
	}

	var workflow workflowDef
	if err := yaml.Unmarshal(yamlContent, &workflow); err != nil {
		slog.ErrorContext(execCtx, "workflow yaml parse failed", "repo", repo.FullName, "path", wf.Path, "workflow_run_id", run.ID, "error", err)
		s.completeRun(finalizeCtx, run.ID, db.ConclusionFailure)
		return
	}

	// Load all secrets accessible to this repo.
	secrets := s.loadSecrets(execCtx, repo)

	allOK := true
	for jobName, job := range workflow.Jobs {
		if execCtx.Err() != nil {
			allOK = false
			break
		}

		// Merge environment-specific secrets.
		jobSecrets := make(map[string]string, len(secrets))
		for k, v := range secrets {
			jobSecrets[k] = v
		}
		if job.Environment != "" {
			envSecrets := s.loadEnvSecrets(execCtx, repo, job.Environment)
			for k, v := range envSecrets {
				jobSecrets[k] = v
			}
		}

		if !s.executeJob(execCtx, run, repo, jobName, job, jobSecrets) {
			allOK = false
			if execCtx.Err() != nil {
				break
			}
		}
	}
	if execCtx.Err() != nil {
		allOK = false
	}

	conclusion := db.ConclusionSuccess
	if !allOK {
		conclusion = workflowConclusionFromError(execCtx.Err())
	}
	s.completeRun(finalizeCtx, run.ID, conclusion)

	logAttrs := []any{
		"audit", true,
		"repo", repo.FullName,
		"workflow_run_id", run.ID,
		"workflow_path", wf.Path,
		"conclusion", conclusion,
	}
	if execCtx.Err() != nil {
		logAttrs = append(logAttrs, "error", execCtx.Err())
	}
	if conclusion == db.ConclusionSuccess {
		slog.InfoContext(finalizeCtx, "workflow execution completed", logAttrs...)
		return
	}
	slog.WarnContext(finalizeCtx, "workflow execution completed", logAttrs...)
}

// completeRun marks a workflow run and its jobs as completed.
func (s *Service) completeRun(ctx context.Context, runID uint, conclusion string) {
	if err := s.DBForCtx(ctx).Model(&db.WorkflowRun{}).Where("id = ?", runID).
		Updates(map[string]any{"status": db.RunCompleted, "conclusion": conclusion}).Error; err != nil {
		slog.ErrorContext(ctx, "workflow completion update run failed", "workflow_run_id", runID, "error", err)
	}
	if err := s.DBForCtx(ctx).Model(&db.WorkflowRunJob{}).Where("run_id = ? AND status <> ?", runID, db.RunCompleted).
		Updates(map[string]any{"status": db.RunCompleted, "conclusion": conclusion}).Error; err != nil {
		slog.ErrorContext(ctx, "workflow completion update jobs failed", "workflow_run_id", runID, "error", err)
	}
	run, err := s.GetWorkflowRunByID(ctx, runID)
	if err != nil {
		slog.ErrorContext(ctx, "workflow completion fetch run failed", "workflow_run_id", runID, "error", err)
		return
	}
	s.ReevaluateAutoMergeForSHA(ctx, run.RepositoryID, run.HeadSHA)
	if run.ActorID == nil {
		return
	}
	if _, err := s.CreateWorkflowEventNotification(ctx, *run.ActorID, *run.ActorID, run.ID, run.RepositoryID); err != nil {
		slog.ErrorContext(ctx, "workflow completion notification failed", "workflow_run_id", runID, "error", err)
	}
}

// executeJob runs all steps of a single job, stores logs, and creates artifacts.
func (s *Service) executeJob(ctx context.Context, run db.WorkflowRun, repo db.Repository, jobName string, job workflowJob, secrets map[string]string) bool {
	persistCtx := s.workflowDetachedContext(ctx)
	start := time.Now()
	var logBuf bytes.Buffer
	ok := true
	conclusion := db.ConclusionSuccess

	tmpDir, err := os.MkdirTemp("", "workflow-"+jobName+"-")
	if err != nil {
		slog.ErrorContext(ctx, "workflow tempdir create failed", "workflow_run_id", run.ID, "job", jobName, "error", err)
		return false
	}
	defer os.RemoveAll(tmpDir)

	for _, step := range job.Steps {
		if ctx.Err() != nil {
			ok = false
			conclusion = workflowConclusionFromError(ctx.Err())
			logBuf.WriteString("workflow job aborted: " + ctx.Err().Error() + "\n")
			break
		}

		if step.Run != "" {
			stepName := strings.TrimSpace(step.Name)
			if stepName == "" {
				stepName = "run"
			}
			env := s.resolveEnv(step.Env, secrets)
			result := s.runBashStep(ctx, workflowStepRequest{
				Dir:          tmpDir,
				Script:       step.Run,
				Env:          env,
				RepoFullName: repo.FullName,
				RunID:        run.ID,
				JobName:      jobName,
				StepName:     stepName,
			})
			logBuf.Write(result.Output)
			if result.ExitCode != 0 {
				ok = false
				conclusion = workflowConclusionFromError(ctx.Err())
				break
			}
		} else if strings.HasPrefix(step.Uses, "actions/upload-artifact") {
			artName := "artifact"
			if n, exists := step.With["name"]; exists {
				if ns, isStr := n.(string); isStr {
					artName = ns
				}
			}
			artPath := "."
			if p, exists := step.With["path"]; exists {
				if ps, isStr := p.(string); isStr {
					artPath = ps
				}
			}
			slog.InfoContext(ctx,
				"workflow artifact step handled",
				"audit", true,
				"repo", repo.FullName,
				"workflow_run_id", run.ID,
				"job", jobName,
				"step", strings.TrimSpace(step.Name),
				"artifact_name", artName,
				"artifact_path", artPath,
			)
			artifactPath, err := workflowArtifactPath(tmpDir, artPath)
			if err == nil {
				err = s.createArtifactFromPath(ctx, run.ID, artName, artifactPath)
			}
			if err != nil {
				ok = false
				conclusion = workflowConclusionFromError(ctx.Err())
				if ctx.Err() == nil {
					conclusion = db.ConclusionFailure
				}
				logBuf.WriteString("workflow artifact step failed: " + err.Error() + "\n")
				break
			}
		}
	}

	end := time.Now()
	if err := s.DBForCtx(persistCtx).Create(&db.WorkflowRunJob{
		RunID:       run.ID,
		Name:        jobName,
		Status:      db.RunCompleted,
		Conclusion:  conclusion,
		StartedAt:   start,
		CompletedAt: end,
		Logs:        logBuf.Bytes(),
	}).Error; err != nil {
		slog.ErrorContext(persistCtx, "workflow job create failed", "workflow_run_id", run.ID, "job", jobName, "error", err)
	}

	return ok
}

// resolveEnv builds environment variables for a step, replacing ${{ secrets.XXX }} references.
func (s *Service) resolveEnv(stepEnv map[string]any, secrets map[string]string) []string {
	var env []string
	for k, v := range stepEnv {
		val := fmt.Sprintf("%v", v)
		val = secretRefRe.ReplaceAllStringFunc(val, func(match string) string {
			m := secretRefRe.FindStringSubmatch(match)
			if len(m) == 2 {
				if sv, ok := secrets[m[1]]; ok {
					return sv
				}
			}
			return ""
		})
		env = append(env, k+"="+val)
	}
	return env
}

// runBashStep executes a shell script in an isolated sandbox.
func (s *Service) runBashStep(ctx context.Context, req workflowStepRequest) workflowStepResult {
	start := time.Now()
	slog.InfoContext(ctx,
		"workflow step started",
		"audit", true,
		"repo", req.RepoFullName,
		"workflow_run_id", req.RunID,
		"job", req.JobName,
		"step", req.StepName,
	)

	result, err := s.workflowRunner().Run(ctx, req)
	if err != nil {
		if result.ExitCode == 0 {
			result.ExitCode = 1
		}
		result.Output = appendWorkflowError(result.Output, err.Error())
	}
	if result.TimedOut {
		result.Output = appendWorkflowError(result.Output, "workflow step timed out after "+s.workflowExecTimeoutValue().String())
	}

	logAttrs := []any{
		"audit", true,
		"repo", req.RepoFullName,
		"workflow_run_id", req.RunID,
		"job", req.JobName,
		"step", req.StepName,
		"exit_code", result.ExitCode,
		"timed_out", result.TimedOut,
		"duration", time.Since(start),
	}
	if err != nil {
		logAttrs = append(logAttrs, "error", err)
	}
	if result.ExitCode == 0 {
		slog.InfoContext(ctx, "workflow step completed", logAttrs...)
		return result
	}
	slog.WarnContext(ctx, "workflow step completed", logAttrs...)
	return result
}

func (s *Service) workflowRunner() workflowStepRunner {
	if s.workflowStepRunner != nil {
		return s.workflowStepRunner
	}
	return workflowStepRunnerFunc(s.runDockerWorkflowStep)
}

func (s *Service) runDockerWorkflowStep(ctx context.Context, req workflowStepRequest) (workflowStepResult, error) {
	absDir, err := filepath.Abs(req.Dir)
	if err != nil {
		return workflowStepResult{ExitCode: 1}, fmt.Errorf("workflow sandbox path: %w", err)
	}

	cleanup, err := writeWorkflowSandboxFiles(absDir, req.Script, req.Env)
	if err != nil {
		return workflowStepResult{ExitCode: 1}, err
	}
	defer cleanup()

	containerName := fmt.Sprintf("gh-server-workflow-%d-%d", req.RunID, time.Now().UnixNano())
	cmd := exec.CommandContext(ctx, "docker", s.workflowDockerArgs(absDir, containerName)...)
	output, runErr := cmd.CombinedOutput()
	result := workflowStepResult{Output: output}
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
		} else {
			result.ExitCode = 1
		}
	}

	if ctxErr := ctx.Err(); ctxErr != nil {
		result.TimedOut = errors.Is(ctxErr, context.DeadlineExceeded)
		result.ExitCode = 1
		if err := forceRemoveWorkflowContainer(containerName); err != nil {
			result.Output = appendWorkflowError(result.Output, err.Error())
		}
	}
	if runErr != nil && ctx.Err() == nil {
		if err := forceRemoveWorkflowContainer(containerName); err != nil {
			result.Output = appendWorkflowError(result.Output, err.Error())
		}
	}

	if runErr != nil {
		return result, runErr
	}
	return result, nil
}

func (s *Service) workflowDockerArgs(workspaceDir, containerName string) []string {
	nofile := s.workflowExecNoFileValue()
	return []string{
		"run",
		"--rm",
		"--name", containerName,
		"--init",
		"--network", "none",
		"--read-only",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--memory", s.workflowExecMemoryValue(),
		"--memory-swap", s.workflowExecMemoryValue(),
		"--cpus", s.workflowExecCPUsValue(),
		"--pids-limit", strconv.Itoa(s.workflowExecPidsValue()),
		"--ulimit", fmt.Sprintf("nofile=%d:%d", nofile, nofile),
		"--tmpfs", fmt.Sprintf("%s:rw,noexec,nosuid,nodev,size=%s", workflowTmpMount, s.workflowExecTmpfsValue()),
		"--mount", fmt.Sprintf("type=bind,src=%s,dst=%s", workspaceDir, workflowWorkspaceMount),
		"--workdir", workflowWorkspaceMount,
		s.workflowExecImageValue(),
		"bash",
		"-e",
		filepath.Join(workflowWorkspaceMount, workflowLauncherFileName),
	}
}

func writeWorkflowSandboxFiles(dir, script string, env []string) (func(), error) {
	stepPath := filepath.Join(dir, workflowStepScriptName)
	launcherPath := filepath.Join(dir, workflowLauncherFileName)

	if err := os.WriteFile(stepPath, []byte(ensureTrailingNewline(script)), 0o700); err != nil {
		return nil, fmt.Errorf("workflow sandbox step script: %w", err)
	}

	launcher, err := buildWorkflowLauncherScript(env)
	if err != nil {
		_ = os.Remove(stepPath)
		return nil, err
	}
	if err := os.WriteFile(launcherPath, launcher, 0o700); err != nil {
		_ = os.Remove(stepPath)
		return nil, fmt.Errorf("workflow sandbox launcher: %w", err)
	}

	return func() {
		_ = os.Remove(stepPath)
		_ = os.Remove(launcherPath)
	}, nil
}

func buildWorkflowLauncherScript(env []string) ([]byte, error) {
	allEnv := append([]string{
		"HOME=" + workflowTmpMount,
		"PATH=" + workflowExecPath,
		"CI=true",
		"GITHUB_ACTIONS=true",
	}, env...)

	var builder strings.Builder
	builder.WriteString("#!/usr/bin/env bash\n")
	builder.WriteString("set -euo pipefail\n")
	builder.WriteString("exec env -i")
	for _, raw := range allEnv {
		key, value, err := parseWorkflowEnv(raw)
		if err != nil {
			return nil, err
		}
		builder.WriteString(" \\\n  ")
		builder.WriteString(key)
		builder.WriteString("=")
		builder.WriteString(shellQuote(value))
	}
	builder.WriteString(" \\\n  bash -e ")
	builder.WriteString(shellQuote(filepath.Join(workflowWorkspaceMount, workflowStepScriptName)))
	builder.WriteByte('\n')
	return []byte(builder.String()), nil
}

func parseWorkflowEnv(raw string) (string, string, error) {
	key, value, ok := strings.Cut(raw, "=")
	if !ok {
		return "", "", fmt.Errorf("workflow sandbox env %q must contain '='", raw)
	}
	if !workflowEnvName.MatchString(key) {
		return "", "", fmt.Errorf("workflow sandbox env %q has invalid name", key)
	}
	return key, value, nil
}

func shellQuote(raw string) string {
	if raw == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(raw, "'", `'"'"'`) + "'"
}

func ensureTrailingNewline(raw string) string {
	if strings.HasSuffix(raw, "\n") {
		return raw
	}
	return raw + "\n"
}

func appendWorkflowError(output []byte, msg string) []byte {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return output
	}
	if len(output) > 0 && output[len(output)-1] != '\n' {
		output = append(output, '\n')
	}
	output = append(output, []byte(msg)...)
	output = append(output, '\n')
	return output
}

func forceRemoveWorkflowContainer(containerName string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, "docker", "rm", "-f", containerName).CombinedOutput()
	if err == nil {
		return nil
	}
	msg := strings.TrimSpace(string(out))
	if strings.Contains(msg, "No such container") {
		return nil
	}
	if msg == "" {
		return fmt.Errorf("workflow sandbox cleanup %q: %w", containerName, err)
	}
	return fmt.Errorf("workflow sandbox cleanup %q: %w: %s", containerName, err, msg)
}

func workflowConclusionFromError(err error) string {
	if errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		return db.ConclusionCancelled
	}
	return db.ConclusionFailure
}

func (s *Service) workflowDetachedContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	detached := applog.CloneContext(context.Background(), ctx)
	if scopedDB, ok := DBFromContext(ctx); ok {
		detached = ContextWithDB(detached, scopedDB)
	}
	return detached
}

func (s *Service) workflowExecImageValue() string {
	if strings.TrimSpace(s.WorkflowExecImage) != "" {
		return s.WorkflowExecImage
	}
	return defaultWorkflowExecImage
}

func (s *Service) workflowExecTimeoutValue() time.Duration {
	if s.WorkflowExecTimeout > 0 {
		return s.WorkflowExecTimeout
	}
	return defaultWorkflowExecTimeout
}

func (s *Service) workflowExecCPUsValue() string {
	if strings.TrimSpace(s.WorkflowExecCPUs) != "" {
		return s.WorkflowExecCPUs
	}
	return defaultWorkflowExecCPUs
}

func (s *Service) workflowExecMemoryValue() string {
	if strings.TrimSpace(s.WorkflowExecMemory) != "" {
		return s.WorkflowExecMemory
	}
	return defaultWorkflowExecMemory
}

func (s *Service) workflowExecPidsValue() int {
	if s.WorkflowExecPids > 0 {
		return s.WorkflowExecPids
	}
	return defaultWorkflowExecPids
}

func (s *Service) workflowExecNoFileValue() int {
	if s.WorkflowExecNoFile > 0 {
		return s.WorkflowExecNoFile
	}
	return defaultWorkflowExecNoFile
}

func (s *Service) workflowExecTmpfsValue() string {
	if strings.TrimSpace(s.WorkflowExecTmpfs) != "" {
		return s.WorkflowExecTmpfs
	}
	return defaultWorkflowExecTmpfs
}

func workflowArtifactPath(tmpDir, artPath string) (string, error) {
	absTmpDir, err := filepath.Abs(tmpDir)
	if err != nil {
		return "", err
	}
	absArtifactPath, err := filepath.Abs(filepath.Join(absTmpDir, artPath))
	if err != nil {
		return "", err
	}
	withinTmpDir, err := workflowPathWithinRoot(absTmpDir, absArtifactPath)
	if err != nil {
		return "", err
	}
	if !withinTmpDir {
		return "", fmt.Errorf("artifact path %q escapes job workspace", artPath)
	}
	return absArtifactPath, nil
}

func workflowPathWithinRoot(rootPath, targetPath string) (bool, error) {
	absRoot, err := filepath.Abs(rootPath)
	if err != nil {
		return false, err
	}
	absTarget, err := filepath.Abs(targetPath)
	if err != nil {
		return false, err
	}
	if absTarget == absRoot {
		return true, nil
	}
	return strings.HasPrefix(absTarget, absRoot+string(filepath.Separator)), nil
}

// collectSecrets queries secrets matching the given condition and merges them into dst.
func (s *Service) collectSecrets(ctx context.Context, dst map[string]string, query string, args ...any) {
	var secrets []db.Secret
	if err := s.DBForCtx(ctx).Where(query, args...).Find(&secrets).Error; err != nil {
		slog.ErrorContext(ctx, "workflow secret collection failed", "error", err)
		return
	}
	for _, sec := range secrets {
		if sec.Value != "" {
			dst[sec.Name] = sec.Value
		}
	}
}

// loadSecrets loads repo-level and org-level secrets for a repository.
func (s *Service) loadSecrets(ctx context.Context, repo db.Repository) map[string]string {
	secrets := make(map[string]string)
	s.collectSecrets(ctx, secrets, "owner_id = ? AND repository_id IS NULL AND environment = ''", repo.OwnerID)
	s.collectSecrets(ctx, secrets, "repository_id = ? AND environment = ''", repo.ID)
	return secrets
}

// loadEnvSecrets loads environment-specific secrets for a repository.
func (s *Service) loadEnvSecrets(ctx context.Context, repo db.Repository, env string) map[string]string {
	secrets := make(map[string]string)
	s.collectSecrets(ctx, secrets, "repository_id = ? AND environment = ?", repo.ID, env)
	return secrets
}

// createArtifactFromPath zips the contents of a directory and stores it as an artifact.
func (s *Service) createArtifactFromPath(ctx context.Context, runID uint, name, basePath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	if info, err := os.Lstat(basePath); !(err == nil && info.Mode()&os.ModeSymlink != 0) {
		resolvedBase, err := filepath.EvalSymlinks(basePath)
		if err != nil {
			resolvedBase = basePath
		}
		absBase, err := filepath.Abs(resolvedBase)
		if err != nil {
			_ = zw.Close()
			return err
		}

		walkErr := filepath.WalkDir(basePath, func(path string, d fs.DirEntry, err error) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err != nil || d.IsDir() {
				return err
			}
			if d.Type()&fs.ModeSymlink != 0 {
				return nil
			}

			// Resolve symlinks before reading so workspace-controlled links cannot exfiltrate host files.
			resolvedTarget, err := filepath.EvalSymlinks(path)
			if err != nil {
				return err
			}
			withinBase, err := workflowPathWithinRoot(absBase, resolvedTarget)
			if err != nil {
				return err
			}
			if !withinBase {
				return nil
			}

			rel, _ := filepath.Rel(basePath, path)
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			w, err := zw.Create(rel)
			if err != nil {
				return err
			}
			if _, err := w.Write(content); err != nil {
				return err
			}
			return nil
		})
		if err := ctx.Err(); err != nil {
			_ = zw.Close()
			return err
		}
		if walkErr != nil {
			slog.WarnContext(ctx, "workflow artifact collection skipped path", "path", basePath, "error", walkErr)
		}
	}
	if err := ctx.Err(); err != nil {
		_ = zw.Close()
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}

	zipBytes := buf.Bytes()
	if err := s.DBForCtx(ctx).Create(&db.Artifact{
		RunID:       runID,
		Name:        name,
		SizeInBytes: int64(len(zipBytes)),
		Expired:     false,
		Content:     zipBytes,
		ContentType: "application/zip",
	}).Error; err != nil {
		slog.Error("workflow exec: create artifact", "name", name, "error", err)
		return err
	}
	return nil
}
