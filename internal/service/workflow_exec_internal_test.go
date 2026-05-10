package service

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestBuildWorkflowLauncherScript_UsesEnvIAndExplicitVarsOnly(t *testing.T) {
	t.Setenv("HOST_SECRET", "should-not-leak")

	script, err := buildWorkflowLauncherScript([]string{"SECRET=value with spaces", "EMPTY="})
	if err != nil {
		t.Fatalf("buildWorkflowLauncherScript: %v", err)
	}

	got := string(script)
	if !strings.Contains(got, "exec env -i") {
		t.Fatalf("expected launcher to clear inherited env, got %q", got)
	}
	if !strings.Contains(got, "SECRET='value with spaces'") {
		t.Fatalf("expected launcher to include explicit SECRET env, got %q", got)
	}
	if !strings.Contains(got, "EMPTY=''") {
		t.Fatalf("expected launcher to preserve empty env values, got %q", got)
	}
	if strings.Contains(got, "HOST_SECRET") {
		t.Fatalf("launcher leaked host environment: %q", got)
	}
}

func TestWorkflowDockerArgsIncludeSandboxingAndQuotas(t *testing.T) {
	svc := &Service{
		WorkflowExecImage:  "custom/bash:latest",
		WorkflowExecCPUs:   "0.5",
		WorkflowExecMemory: "128m",
		WorkflowExecPids:   64,
		WorkflowExecNoFile: 256,
		WorkflowExecTmpfs:  "16m",
	}

	got := strings.Join(svc.workflowDockerArgs("/tmp/workspace", "runner-1"), " ")

	for _, want := range []string{
		"--network none",
		"--read-only",
		"--cap-drop ALL",
		"--security-opt no-new-privileges",
		"--memory 128m",
		"--memory-swap 128m",
		"--cpus 0.5",
		"--pids-limit 64",
		"--ulimit nofile=256:256",
		"--tmpfs /tmp:rw,noexec,nosuid,nodev,size=16m",
		"--workdir /workspace",
		"custom/bash:latest bash -e /workspace/.gh-server-workflow-launch.sh",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected docker args to contain %q, got %q", want, got)
		}
	}
}

func TestRunBashStep_LogsAuditEvents(t *testing.T) {
	var buf bytes.Buffer
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(oldLogger)

	svc := &Service{}
	svc.workflowStepRunner = workflowStepRunnerFunc(func(ctx context.Context, req workflowStepRequest) (workflowStepResult, error) {
		return workflowStepResult{Output: []byte("ok\n")}, nil
	})

	result := svc.runBashStep(context.Background(), workflowStepRequest{
		RepoFullName: "owner/repo",
		RunID:        42,
		JobName:      "build",
		StepName:     "echo",
	})
	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", result.ExitCode)
	}

	logs := buf.String()
	for _, want := range []string{
		"workflow step started",
		"workflow step completed",
		"audit=true",
		"workflow_run_id=42",
		"job=build",
		"step=echo",
	} {
		if !strings.Contains(logs, want) {
			t.Fatalf("expected logs to contain %q, got %q", want, logs)
		}
	}
}
