package graphql

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/gitstore"
	"github.com/ngaut/agent-git-service/internal/service"
	"github.com/ngaut/agent-git-service/internal/testharness/testdb"
)

// setupTestServer creates a minimal Server instance for testing.
func setupTestServer(t *testing.T) *Server {
	gdb, dbCleanup := testdb.OpenRaw(t, "gql_pr_status")

	_ = gdb.AutoMigrate(
		&db.User{}, &db.Repository{}, &db.RepoRedirect{}, &db.PullRequest{}, &db.Token{},
	)

	tmpDir, err := os.MkdirTemp("", "gh-server-test-prstatus-")
	require.NoError(t, err)

	gitStore, err := gitstore.New(tmpDir)
	require.NoError(t, err)

	svc := &service.Service{
		DB:      gdb,
		Git:     gitStore,
		BaseURL: "http://localhost:8080",
	}

	u := db.User{Login: "tester", Name: "tester", Type: db.TypeUser}
	gdb.Create(&u)
	gdb.Create(&db.Token{UserID: u.ID, Value: "test-token"})

	srv := NewServer(svc)

	t.Cleanup(func() {
		dbCleanup()
		os.RemoveAll(tmpDir)
	})

	return srv
}

// =============================================================================
// jobToCheckNode Tests
// =============================================================================

func TestJobToCheckNode_BasicConversion(t *testing.T) {
	now := time.Now()
	job := db.WorkflowRunJob{
		Name:        "test-job",
		Status:      "completed",
		Conclusion:  "success",
		StartedAt:   now,
		CompletedAt: now.Add(5 * time.Minute),
	}
	wf := db.Workflow{Name: "CI"}
	run := db.WorkflowRun{ID: 123}

	node := jobToCheckNode(job, wf, run, "https://github.com", "owner/repo")

	require.Equal(t, "CheckRun", node["__typename"])
	require.Equal(t, "test-job", node["name"])
	require.Equal(t, "COMPLETED", node["status"])
	require.Equal(t, "SUCCESS", node["conclusion"])
	require.Equal(t, now.Format(time.RFC3339), node["startedAt"])
	require.Equal(t, now.Add(5*time.Minute).Format(time.RFC3339), node["completedAt"])
	require.Equal(t, "https://github.com/owner/repo/actions/runs/123", node["detailsUrl"])
	require.Equal(t, false, node["isRequired"])

	checkSuite := node["checkSuite"].(map[string]any)
	workflowRun := checkSuite["workflowRun"].(map[string]any)
	workflow := workflowRun["workflow"].(map[string]any)
	require.Equal(t, "CI", workflow["name"])
}

func TestJobToCheckNode_DefaultStatusAndConclusion(t *testing.T) {
	now := time.Now()
	job := db.WorkflowRunJob{
		Name:        "empty-job",
		Status:      "",
		Conclusion:  "",
		StartedAt:   now,
		CompletedAt: now,
	}
	wf := db.Workflow{Name: "Workflow"}
	run := db.WorkflowRun{ID: 456}

	node := jobToCheckNode(job, wf, run, "https://github.com", "org/repo")

	// Empty status should default to COMPLETED
	require.Equal(t, "COMPLETED", node["status"])
	// Empty conclusion should default to SUCCESS
	require.Equal(t, "SUCCESS", node["conclusion"])
}

func TestJobToCheckNode_CaseNormalization(t *testing.T) {
	now := time.Now()
	testCases := []struct {
		name       string
		status     string
		conclusion string
		wantStatus string
		wantConcl  string
	}{
		{"lowercase", "completed", "success", "COMPLETED", "SUCCESS"},
		{"uppercase", "IN_PROGRESS", "FAILURE", "IN_PROGRESS", "FAILURE"},
		{"mixed", "CoMpLeTeD", "SuCcEsS", "COMPLETED", "SUCCESS"},
		{"queued", "queued", "pending", "QUEUED", "PENDING"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			job := db.WorkflowRunJob{
				Name:        tc.name,
				Status:      tc.status,
				Conclusion:  tc.conclusion,
				StartedAt:   now,
				CompletedAt: now,
			}
			wf := db.Workflow{Name: "Test"}
			run := db.WorkflowRun{ID: 1}

			node := jobToCheckNode(job, wf, run, "https://github.com", "owner/repo")

			require.Equal(t, tc.wantStatus, node["status"])
			require.Equal(t, tc.wantConcl, node["conclusion"])
		})
	}
}

func TestJobToCheckNode_EdgeStatuses(t *testing.T) {
	now := time.Now()
	testCases := []struct {
		name       string
		status     string
		conclusion string
		wantStatus string
		wantConcl  string
	}{
		{"pending_status", "pending", "", "PENDING", "SUCCESS"},
		{"failure_conclusion", "completed", "failure", "COMPLETED", "FAILURE"},
		{"neutral_conclusion", "completed", "neutral", "COMPLETED", "NEUTRAL"},
		{"cancelled_conclusion", "completed", "cancelled", "COMPLETED", "CANCELLED"},
		{"timed_out", "completed", "timed_out", "COMPLETED", "TIMED_OUT"},
		{"action_required", "completed", "action_required", "COMPLETED", "ACTION_REQUIRED"},
		{"stale", "completed", "stale", "COMPLETED", "STALE"},
		{"skipped", "completed", "skipped", "COMPLETED", "SKIPPED"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			job := db.WorkflowRunJob{
				Name:        tc.name,
				Status:      tc.status,
				Conclusion:  tc.conclusion,
				StartedAt:   now,
				CompletedAt: now,
			}
			wf := db.Workflow{Name: "Edge"}
			run := db.WorkflowRun{ID: 1}

			node := jobToCheckNode(job, wf, run, "https://github.com", "owner/repo")

			require.Equal(t, tc.wantStatus, node["status"], "status mismatch for %s", tc.name)
			require.Equal(t, tc.wantConcl, node["conclusion"], "conclusion mismatch for %s", tc.name)
		})
	}
}

func TestJobToCheckNode_MissingMetadata(t *testing.T) {
	now := time.Now()
	job := db.WorkflowRunJob{
		Name:        "minimal-job",
		Status:      "completed",
		Conclusion:  "success",
		StartedAt:   now,
		CompletedAt: now,
	}
	// Empty workflow and run
	wf := db.Workflow{}
	run := db.WorkflowRun{}

	node := jobToCheckNode(job, wf, run, "https://github.com", "owner/repo")

	require.Equal(t, "minimal-job", node["name"])
	require.Equal(t, "https://github.com/owner/repo/actions/runs/0", node["detailsUrl"])

	checkSuite := node["checkSuite"].(map[string]any)
	workflowRun := checkSuite["workflowRun"].(map[string]any)
	workflow := workflowRun["workflow"].(map[string]any)
	require.Equal(t, "", workflow["name"])
}

// =============================================================================
// countChecksByState Tests
// =============================================================================

func TestCountChecksByState_EmptyInput(t *testing.T) {
	result := countChecksByState([]any{})
	require.Len(t, result, 0)
}

func TestCountChecksByState_SingleCheck(t *testing.T) {
	checkNodes := []any{
		map[string]any{"conclusion": "SUCCESS"},
	}

	result := countChecksByState(checkNodes)

	require.Len(t, result, 1)
	countMap := result[0].(map[string]any)
	require.Equal(t, "SUCCESS", countMap["state"])
	require.Equal(t, 1, countMap["count"])
}

func TestCountChecksByState_MultipleChecks_SameState(t *testing.T) {
	checkNodes := []any{
		map[string]any{"conclusion": "SUCCESS"},
		map[string]any{"conclusion": "SUCCESS"},
		map[string]any{"conclusion": "SUCCESS"},
	}

	result := countChecksByState(checkNodes)

	require.Len(t, result, 1)
	countMap := result[0].(map[string]any)
	require.Equal(t, "SUCCESS", countMap["state"])
	require.Equal(t, 3, countMap["count"])
}

func TestCountChecksByState_MixedStates(t *testing.T) {
	checkNodes := []any{
		map[string]any{"conclusion": "SUCCESS"},
		map[string]any{"conclusion": "FAILURE"},
		map[string]any{"conclusion": "SUCCESS"},
		map[string]any{"conclusion": "PENDING"},
		map[string]any{"conclusion": "FAILURE"},
		map[string]any{"conclusion": "SUCCESS"},
	}

	result := countChecksByState(checkNodes)

	// Build a map for easier assertions
	counts := make(map[string]int)
	for _, r := range result {
		m := r.(map[string]any)
		state := m["state"].(string)
		count := m["count"].(int)
		counts[state] = count
	}

	require.Equal(t, 3, counts["SUCCESS"])
	require.Equal(t, 2, counts["FAILURE"])
	require.Equal(t, 1, counts["PENDING"])
}

func TestCountChecksByState_UnknownConclusion(t *testing.T) {
	checkNodes := []any{
		map[string]any{"conclusion": ""},
		map[string]any{"conclusion": "SUCCESS"},
		map[string]any{}, // missing conclusion key
	}

	result := countChecksByState(checkNodes)

	counts := make(map[string]int)
	for _, r := range result {
		m := r.(map[string]any)
		state := m["state"].(string)
		count := m["count"].(int)
		counts[state] = count
	}

	// Empty or missing conclusion should be counted as UNKNOWN
	require.Equal(t, 2, counts["UNKNOWN"])
	require.Equal(t, 1, counts["SUCCESS"])
}

func TestCountChecksByState_VariousConclusions(t *testing.T) {
	checkNodes := []any{
		map[string]any{"conclusion": "SUCCESS"},
		map[string]any{"conclusion": "FAILURE"},
		map[string]any{"conclusion": "NEUTRAL"},
		map[string]any{"conclusion": "CANCELLED"},
		map[string]any{"conclusion": "TIMED_OUT"},
		map[string]any{"conclusion": "ACTION_REQUIRED"},
		map[string]any{"conclusion": "STALE"},
		map[string]any{"conclusion": "SKIPPED"},
	}

	result := countChecksByState(checkNodes)

	counts := make(map[string]int)
	for _, r := range result {
		m := r.(map[string]any)
		state := m["state"].(string)
		count := m["count"].(int)
		counts[state] = count
	}

	require.Equal(t, 1, counts["SUCCESS"])
	require.Equal(t, 1, counts["FAILURE"])
	require.Equal(t, 1, counts["NEUTRAL"])
	require.Equal(t, 1, counts["CANCELLED"])
	require.Equal(t, 1, counts["TIMED_OUT"])
	require.Equal(t, 1, counts["ACTION_REQUIRED"])
	require.Equal(t, 1, counts["STALE"])
	require.Equal(t, 1, counts["SKIPPED"])
}

func TestCountChecksByState_InvalidNodeTypes(t *testing.T) {
	checkNodes := []any{
		map[string]any{"conclusion": "SUCCESS"},
		"not a map",
		123,
		nil,
		map[string]any{"conclusion": "FAILURE"},
	}

	result := countChecksByState(checkNodes)

	counts := make(map[string]int)
	for _, r := range result {
		m := r.(map[string]any)
		state := m["state"].(string)
		count := m["count"].(int)
		counts[state] = count
	}

	// Only valid map nodes should be counted
	require.Equal(t, 1, counts["SUCCESS"])
	require.Equal(t, 1, counts["FAILURE"])
}

// =============================================================================
// prMergeable Tests
// =============================================================================

func TestPRMergeable_MergedPR(t *testing.T) {
	srv := setupTestServer(t)
	ctx := context.Background()

	pr := db.PullRequest{
		Merged: true,
		State:  db.StateOpen,
	}

	result := srv.prMergeable(ctx, pr)
	require.Equal(t, "UNKNOWN", result)
}

func TestPRMergeable_ClosedPR(t *testing.T) {
	srv := setupTestServer(t)
	ctx := context.Background()

	pr := db.PullRequest{
		Merged: false,
		State:  db.StateClosed,
	}

	result := srv.prMergeable(ctx, pr)
	require.Equal(t, "UNKNOWN", result)
}

func TestPRMergeable_DraftPR(t *testing.T) {
	srv := setupTestServer(t)
	ctx := context.Background()

	// Note: prMergeable doesn't check Draft status directly
	// It delegates to Git.CanMerge which determines actual mergeability
	// Draft PRs with valid repo will return CLEAN or CONFLICTING from git
	owner := db.User{Login: "draft-owner", Name: "Draft Owner", Type: db.TypeUser}
	srv.Svc.DB.Create(&owner)

	repo := db.Repository{
		Name:          "draft-repo",
		FullName:      "draft-owner/draft-repo",
		OwnerID:       owner.ID,
		Owner:         owner,
		DefaultBranch: "main",
	}
	srv.Svc.DB.Create(&repo)

	pr := db.PullRequest{
		RepositoryID: repo.ID,
		Repository:   repo,
		Merged:       false,
		State:        db.StateOpen,
		Draft:        true,
		BaseRef:      "main",
		HeadRef:      "main",
	}

	result := srv.prMergeable(ctx, pr)
	// prMergeable delegates to git.CanMerge, doesn't check Draft flag
	// Should return CLEAN or CONFLICTING, not UNKNOWN
	require.NotEqual(t, "UNKNOWN", result)
}

func TestPRMergeable_NoGitService(t *testing.T) {
	srv := setupTestServer(t)
	ctx := context.Background()

	// Remove git service
	srv.Svc.Git = nil

	pr := db.PullRequest{
		Merged: false,
		State:  db.StateOpen,
		Draft:  false,
	}

	result := srv.prMergeable(ctx, pr)
	require.Equal(t, "UNKNOWN", result)
}

func TestPRMergeable_EmptyRepoFullName(t *testing.T) {
	srv := setupTestServer(t)
	ctx := context.Background()

	pr := db.PullRequest{
		Merged:     false,
		State:      db.StateOpen,
		Draft:      false,
		Repository: db.Repository{FullName: ""},
	}

	result := srv.prMergeable(ctx, pr)
	require.Equal(t, "UNKNOWN", result)
}

func TestPRMergeable_CanMergeTrue(t *testing.T) {
	srv := setupTestServer(t)
	ctx := context.Background()

	// Setup: create a repo directly in DB
	owner := db.User{Login: "test-owner", Name: "Test Owner", Type: db.TypeUser}
	srv.Svc.DB.Create(&owner)

	repo := db.Repository{
		Name:          "test-repo",
		FullName:      "test-owner/test-repo",
		OwnerID:       owner.ID,
		Owner:         owner,
		DefaultBranch: "main",
	}
	srv.Svc.DB.Create(&repo)

	pr := db.PullRequest{
		RepositoryID: repo.ID,
		Repository:   repo,
		Merged:       false,
		State:        db.StateOpen,
		Draft:        false,
		BaseRef:      "main",
		HeadRef:      "main", // Same branch should be mergeable
	}

	result := srv.prMergeable(ctx, pr)
	// Should be either "CLEAN" or "CONFLICTING" depending on git state
	// The important thing is it's not UNKNOWN or DRAFT
	require.NotEqual(t, "UNKNOWN", result)
	require.NotEqual(t, "DRAFT", result)
}

func TestPRMergeable_StatesMatrix(t *testing.T) {
	srv := setupTestServer(t)
	ctx := context.Background()

	testCases := []struct {
		name string
		pr   db.PullRequest
		want string
	}{
		{
			name: "merged_open",
			pr:   db.PullRequest{Merged: true, State: db.StateOpen},
			want: "UNKNOWN",
		},
		{
			name: "merged_closed",
			pr:   db.PullRequest{Merged: true, State: db.StateClosed},
			want: "UNKNOWN",
		},
		{
			name: "closed_not_merged",
			pr:   db.PullRequest{Merged: false, State: db.StateClosed},
			want: "UNKNOWN",
		},
		{
			name: "draft_closed",
			pr:   db.PullRequest{Merged: false, State: db.StateClosed, Draft: true},
			want: "UNKNOWN", // closed takes precedence
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := srv.prMergeable(ctx, tc.pr)
			require.Equal(t, tc.want, result)
		})
	}
}

// =============================================================================
// statusCheckRollupGQL Helper Tests (via jobToCheckNode and countChecksByState)
// =============================================================================

func TestStatusCheckRollup_ComputedFromJobs(t *testing.T) {
	// This test verifies the composition of statusCheckRollup from job data
	// by testing the helper functions that build it

	now := time.Now()
	jobs := []db.WorkflowRunJob{
		{Name: "build", Status: "completed", Conclusion: "success", StartedAt: now, CompletedAt: now},
		{Name: "test", Status: "completed", Conclusion: "failure", StartedAt: now, CompletedAt: now},
		{Name: "lint", Status: "completed", Conclusion: "success", StartedAt: now, CompletedAt: now},
		{Name: "deploy", Status: "pending", Conclusion: "", StartedAt: now, CompletedAt: now},
	}

	wf := db.Workflow{Name: "CI"}
	run := db.WorkflowRun{ID: 999}

	var checkNodes []any
	for _, job := range jobs {
		checkNodes = append(checkNodes, jobToCheckNode(job, wf, run, "https://github.com", "owner/repo"))
	}

	// Verify node count
	require.Len(t, checkNodes, 4)

	// Verify conclusions are normalized
	conclusions := []string{
		checkNodes[0].(map[string]any)["conclusion"].(string),
		checkNodes[1].(map[string]any)["conclusion"].(string),
		checkNodes[2].(map[string]any)["conclusion"].(string),
		checkNodes[3].(map[string]any)["conclusion"].(string),
	}

	require.Equal(t, "SUCCESS", conclusions[0])
	require.Equal(t, "FAILURE", conclusions[1])
	require.Equal(t, "SUCCESS", conclusions[2])
	require.Equal(t, "SUCCESS", conclusions[3]) // empty conclusion defaults to SUCCESS

	// Verify count by state
	counts := countChecksByState(checkNodes)
	countMap := make(map[string]int)
	for _, r := range counts {
		m := r.(map[string]any)
		state := m["state"].(string)
		count := m["count"].(int)
		countMap[state] = count
	}

	require.Equal(t, 3, countMap["SUCCESS"])
	require.Equal(t, 1, countMap["FAILURE"])
}

func TestStatusCheckRollup_MixedCheckStates(t *testing.T) {
	// Test with a realistic mix of check states
	now := time.Now()
	scenarios := []struct {
		name        string
		jobs        []db.WorkflowRunJob
		wantSuccess int
		wantFailure int
		wantPending int
		wantOther   map[string]int
	}{
		{
			name: "all_success",
			jobs: []db.WorkflowRunJob{
				{Name: "a", Status: "completed", Conclusion: "success", StartedAt: now, CompletedAt: now},
				{Name: "b", Status: "completed", Conclusion: "success", StartedAt: now, CompletedAt: now},
			},
			wantSuccess: 2,
		},
		{
			name: "mixed_results",
			jobs: []db.WorkflowRunJob{
				{Name: "build", Status: "completed", Conclusion: "success", StartedAt: now, CompletedAt: now},
				{Name: "test", Status: "completed", Conclusion: "failure", StartedAt: now, CompletedAt: now},
				{Name: "lint", Status: "completed", Conclusion: "success", StartedAt: now, CompletedAt: now},
			},
			wantSuccess: 2,
			wantFailure: 1,
		},
		{
			name: "with_pending",
			jobs: []db.WorkflowRunJob{
				{Name: "build", Status: "completed", Conclusion: "success", StartedAt: now, CompletedAt: now},
				{Name: "test", Status: "in_progress", Conclusion: "", StartedAt: now, CompletedAt: now},
			},
			wantSuccess: 2, // in_progress with empty conclusion -> COMPLETED/SUCCESS (defaults applied)
		},
		{
			name: "all_failures",
			jobs: []db.WorkflowRunJob{
				{Name: "a", Status: "completed", Conclusion: "failure", StartedAt: now, CompletedAt: now},
				{Name: "b", Status: "completed", Conclusion: "timed_out", StartedAt: now, CompletedAt: now},
				{Name: "c", Status: "completed", Conclusion: "cancelled", StartedAt: now, CompletedAt: now},
			},
			wantFailure: 1,
			wantOther:   map[string]int{"TIMED_OUT": 1, "CANCELLED": 1},
		},
	}

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			wf := db.Workflow{Name: "CI"}
			run := db.WorkflowRun{ID: 1}

			var checkNodes []any
			for _, job := range sc.jobs {
				checkNodes = append(checkNodes, jobToCheckNode(job, wf, run, "https://github.com", "owner/repo"))
			}

			counts := countChecksByState(checkNodes)
			countMap := make(map[string]int)
			for _, r := range counts {
				m := r.(map[string]any)
				state := m["state"].(string)
				count := m["count"].(int)
				countMap[state] = count
			}

			if sc.wantSuccess > 0 {
				require.Equal(t, sc.wantSuccess, countMap["SUCCESS"], "success count mismatch")
			}
			if sc.wantFailure > 0 {
				require.Equal(t, sc.wantFailure, countMap["FAILURE"], "failure count mismatch")
			}
			if sc.wantPending > 0 {
				require.Equal(t, sc.wantPending, countMap["PENDING"], "pending count mismatch")
			}
			for state, wantCount := range sc.wantOther {
				require.Equal(t, wantCount, countMap[state], "count mismatch for %s", state)
			}
		})
	}
}
