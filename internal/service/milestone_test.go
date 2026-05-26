package service_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/service"
)

// setupMilestoneTest creates a unique user+repo per test to avoid shared-DB conflicts.
func setupMilestoneTest(t *testing.T) (*service.Service, string, func()) {
	t.Helper()
	svc, cleanup := setupTestService(t)
	ctx := context.Background()

	// Use sanitized test name for unique user/repo to avoid UNIQUE constraint conflicts.
	safe := strings.ReplaceAll(t.Name(), "/", "_")
	login := fmt.Sprintf("ms_%s", safe)
	repoName := fmt.Sprintf("repo_%s", safe)
	fullName := login + "/" + repoName

	svc.DB.Create(&db.User{Login: login, Name: login, Type: db.TypeUser})
	_, err := svc.CreateRepo(ctx, service.CreateRepoInput{OwnerLogin: login, Name: repoName})
	if err != nil {
		t.Fatalf("failed to setup repo: %v", err)
	}
	return svc, fullName, cleanup
}

func TestMilestoneCreateAndGet(t *testing.T) {
	svc, full, cleanup := setupMilestoneTest(t)
	defer cleanup()
	ctx := context.Background()

	// Create milestone
	m, err := svc.CreateMilestone(ctx, full, "v1.0", "First release", "")
	if err != nil {
		t.Fatalf("CreateMilestone failed: %v", err)
	}
	if m.Number != 1 {
		t.Errorf("expected number 1, got %d", m.Number)
	}
	if m.Title != "v1.0" {
		t.Errorf("expected title v1.0, got %s", m.Title)
	}
	if m.State != db.StateOpen {
		t.Errorf("expected state open, got %s", m.State)
	}

	// Get by number
	got, err := svc.GetMilestoneByNumber(ctx, full, 1)
	if err != nil {
		t.Fatalf("GetMilestoneByNumber failed: %v", err)
	}
	if got.Title != "v1.0" {
		t.Errorf("expected title v1.0, got %s", got.Title)
	}

	// Get by ID
	gotByID, err := svc.GetMilestoneByID(ctx, m.ID)
	if err != nil {
		t.Fatalf("GetMilestoneByID failed: %v", err)
	}
	if gotByID.Title != "v1.0" {
		t.Errorf("expected title v1.0, got %s", gotByID.Title)
	}

	// Get by title (case-insensitive)
	gotByTitle, err := svc.GetMilestoneByTitle(ctx, full, "V1.0")
	if err != nil {
		t.Fatalf("GetMilestoneByTitle failed: %v", err)
	}
	if gotByTitle.ID != m.ID {
		t.Errorf("expected id %d, got %d", m.ID, gotByTitle.ID)
	}
}

func TestMilestoneAutoNumbering(t *testing.T) {
	svc, full, cleanup := setupMilestoneTest(t)
	defer cleanup()
	ctx := context.Background()

	m1, err := svc.CreateMilestone(ctx, full, "v1", "", "")
	if err != nil {
		t.Fatalf("CreateMilestone(v1) failed: %v", err)
	}
	m2, err := svc.CreateMilestone(ctx, full, "v2", "", "")
	if err != nil {
		t.Fatalf("CreateMilestone(v2) failed: %v", err)
	}
	m3, err := svc.CreateMilestone(ctx, full, "v3", "", "")
	if err != nil {
		t.Fatalf("CreateMilestone(v3) failed: %v", err)
	}

	if m1.Number != 1 || m2.Number != 2 || m3.Number != 3 {
		t.Errorf("expected sequential numbers 1,2,3 — got %d,%d,%d", m1.Number, m2.Number, m3.Number)
	}
}

func TestMilestoneList(t *testing.T) {
	svc, full, cleanup := setupMilestoneTest(t)
	defer cleanup()
	ctx := context.Background()

	svc.CreateMilestone(ctx, full, "open1", "", "open")
	svc.CreateMilestone(ctx, full, "open2", "", "open")
	svc.CreateMilestone(ctx, full, "closed1", "", "closed")

	// List all
	all, _, err := svc.ListMilestones(ctx, full, "all", "", "", 1, 0)
	if err != nil {
		t.Fatalf("ListMilestones(all) failed: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("expected 3 milestones, got %d", len(all))
	}

	// List open only
	open, _, err := svc.ListMilestones(ctx, full, "open", "", "", 1, 0)
	if err != nil {
		t.Fatalf("ListMilestones(open) failed: %v", err)
	}
	if len(open) != 2 {
		t.Errorf("expected 2 open milestones, got %d", len(open))
	}

	// List closed only
	closed, _, err := svc.ListMilestones(ctx, full, "closed", "", "", 1, 0)
	if err != nil {
		t.Fatalf("ListMilestones(closed) failed: %v", err)
	}
	if len(closed) != 1 {
		t.Errorf("expected 1 closed milestone, got %d", len(closed))
	}
}

func TestMilestoneUpdate(t *testing.T) {
	svc, full, cleanup := setupMilestoneTest(t)
	defer cleanup()
	ctx := context.Background()

	svc.CreateMilestone(ctx, full, "v1", "old desc", "")

	newTitle := "v1.1"
	newDesc := "new desc"
	newState := "closed"
	updated, err := svc.UpdateMilestone(ctx, full, 1, service.UpdateMilestoneInput{
		Title:       &newTitle,
		Description: &newDesc,
		State:       &newState,
	})
	if err != nil {
		t.Fatalf("UpdateMilestone failed: %v", err)
	}
	if updated.Title != "v1.1" {
		t.Errorf("expected title v1.1, got %s", updated.Title)
	}
	if updated.Description != "new desc" {
		t.Errorf("expected description 'new desc', got %s", updated.Description)
	}
	if updated.State != "closed" {
		t.Errorf("expected state closed, got %s", updated.State)
	}
	if updated.ClosedAt == nil {
		t.Error("expected closed_at to be set")
	}

	// Reopen
	reopenState := "open"
	reopened, err := svc.UpdateMilestone(ctx, full, 1, service.UpdateMilestoneInput{
		State: &reopenState,
	})
	if err != nil {
		t.Fatalf("UpdateMilestone(reopen) failed: %v", err)
	}
	if reopened.State != "open" {
		t.Errorf("expected state open, got %s", reopened.State)
	}
}

func TestMilestoneDelete(t *testing.T) {
	svc, full, cleanup := setupMilestoneTest(t)
	defer cleanup()
	ctx := context.Background()

	svc.CreateMilestone(ctx, full, "to-delete", "", "")

	err := svc.DeleteMilestone(ctx, full, 1)
	if err != nil {
		t.Fatalf("DeleteMilestone failed: %v", err)
	}

	// Verify deleted
	_, err = svc.GetMilestoneByNumber(ctx, full, 1)
	if err == nil {
		t.Error("expected error after delete, got nil")
	}

	// List should be empty
	all, _, _ := svc.ListMilestones(ctx, full, "all", "", "", 1, 0)
	if len(all) != 0 {
		t.Errorf("expected 0 milestones after delete, got %d", len(all))
	}
}

func TestMilestoneNotFound(t *testing.T) {
	svc, full, cleanup := setupMilestoneTest(t)
	defer cleanup()
	ctx := context.Background()

	_, err := svc.GetMilestoneByNumber(ctx, full, 999)
	if err == nil {
		t.Error("expected error for non-existent milestone")
	}

	_, err = svc.GetMilestoneByTitle(ctx, full, "nonexistent")
	if err == nil {
		t.Error("expected error for non-existent title")
	}

	err = svc.DeleteMilestone(ctx, full, 999)
	if err == nil {
		t.Error("expected error for deleting non-existent milestone")
	}
}

func TestMilestoneSetOnIssue(t *testing.T) {
	svc, full, cleanup := setupMilestoneTest(t)
	defer cleanup()
	ctx := context.Background()

	// Extract login from fullName
	parts := strings.SplitN(full, "/", 2)
	login := parts[0]

	// Create milestone
	m, _ := svc.CreateMilestone(ctx, full, "v1", "", "")

	// Create issue
	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: full,
		Title:        "test issue",
		AuthorLogin:  login,
	})
	if err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// Set milestone
	if err := svc.SetIssueMilestone(ctx, issue.ID, &m.ID); err != nil {
		t.Fatalf("SetIssueMilestone failed: %v", err)
	}

	// Verify
	updated, _ := svc.ReloadIssue(ctx, issue.ID)
	if updated.MilestoneID == nil || *updated.MilestoneID != m.ID {
		t.Errorf("expected milestone ID %d, got %v", m.ID, updated.MilestoneID)
	}

	// Clear milestone
	if err := svc.SetIssueMilestone(ctx, issue.ID, nil); err != nil {
		t.Fatalf("SetIssueMilestone(nil) failed: %v", err)
	}
	cleared, _ := svc.ReloadIssue(ctx, issue.ID)
	if cleared.MilestoneID != nil {
		t.Errorf("expected nil milestone ID after clear, got %v", cleared.MilestoneID)
	}
}

func TestMilestoneSetOnIssue_RejectCrossRepo(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	login := "ms_cross_issue_user"
	if err := svc.DB.Create(&db.User{Login: login, Name: login, Type: db.TypeUser}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{OwnerLogin: login, Name: "repo-a"}); err != nil {
		t.Fatalf("create repo-a: %v", err)
	}
	if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{OwnerLogin: login, Name: "repo-b"}); err != nil {
		t.Fatalf("create repo-b: %v", err)
	}

	ms, err := svc.CreateMilestone(ctx, login+"/repo-a", "v1.0", "", "open")
	if err != nil {
		t.Fatalf("CreateMilestone: %v", err)
	}

	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: login + "/repo-b",
		Title:        "cross-repo issue",
		AuthorLogin:  login,
	})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	err = svc.SetIssueMilestone(ctx, issue.ID, &ms.ID)
	if !errors.Is(err, service.ErrValidation) {
		t.Fatalf("expected ErrValidation, got %v", err)
	}

	updated, err := svc.ReloadIssue(ctx, issue.ID)
	if err != nil {
		t.Fatalf("ReloadIssue: %v", err)
	}
	if updated.MilestoneID != nil {
		t.Fatalf("expected milestone to remain nil, got %v", updated.MilestoneID)
	}
}

func TestMilestoneSetOnPR_RejectCrossRepo(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	login := "ms_cross_pr_user"
	if err := svc.DB.Create(&db.User{Login: login, Name: login, Type: db.TypeUser}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	repoA := login + "/repo-a-pr"
	repoB := login + "/repo-b-pr"
	if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{OwnerLogin: login, Name: "repo-a-pr", AutoInit: true}); err != nil {
		t.Fatalf("create repo-a-pr: %v", err)
	}
	if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{OwnerLogin: login, Name: "repo-b-pr", AutoInit: true}); err != nil {
		t.Fatalf("create repo-b-pr: %v", err)
	}

	if err := svc.Git.CreateBranch(ctx, repoB, "feature", "main"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}

	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: repoB,
		Title:        "cross-repo pr",
		HeadRef:      "feature",
		BaseRef:      "main",
		AuthorLogin:  login,
	})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}

	ms, err := svc.CreateMilestone(ctx, repoA, "v1.0", "", "open")
	if err != nil {
		t.Fatalf("CreateMilestone: %v", err)
	}

	err = svc.SetPRMilestone(ctx, pr.ID, &ms.ID)
	if !errors.Is(err, service.ErrValidation) {
		t.Fatalf("expected ErrValidation, got %v", err)
	}

	updated, err := svc.GetPRByID(ctx, pr.ID)
	if err != nil {
		t.Fatalf("GetPRByID: %v", err)
	}
	if updated.MilestoneID != nil {
		t.Fatalf("expected milestone to remain nil, got %v", updated.MilestoneID)
	}
}

func TestMilestoneLabels_Basic(t *testing.T) {
	svc, full, cleanup := setupMilestoneTest(t)
	defer cleanup()
	ctx := context.Background()

	login := strings.SplitN(full, "/", 2)[0]

	ms, err := svc.CreateMilestone(ctx, full, "v1.0", "", "open")
	if err != nil {
		t.Fatalf("CreateMilestone: %v", err)
	}
	if _, err := svc.CreateLabel(ctx, full, "ms-basic-bug", "ff0000", ""); err != nil {
		t.Fatalf("CreateLabel(ms-basic-bug): %v", err)
	}
	if _, err := svc.CreateLabel(ctx, full, "ms-basic-docs", "00ff00", ""); err != nil {
		t.Fatalf("CreateLabel(ms-basic-docs): %v", err)
	}

	createMilestoneIssue(t, svc, ctx, full, login, ms.ID, "issue-bug", []string{"ms-basic-bug"})
	createMilestoneIssue(t, svc, ctx, full, login, ms.ID, "issue-docs", []string{"ms-basic-docs"})

	labels, err := svc.ListMilestoneLabels(ctx, full, ms.Number)
	if err != nil {
		t.Fatalf("ListMilestoneLabels: %v", err)
	}
	if len(labels) != 2 {
		t.Fatalf("expected 2 labels, got %d", len(labels))
	}
	got := labelNameSet(labels)
	if !got["ms-basic-bug"] || !got["ms-basic-docs"] {
		t.Fatalf("expected labels ms-basic-bug and ms-basic-docs, got %v", got)
	}
}

func TestMilestoneLabels_Empty(t *testing.T) {
	svc, full, cleanup := setupMilestoneTest(t)
	defer cleanup()
	ctx := context.Background()

	login := strings.SplitN(full, "/", 2)[0]

	ms, err := svc.CreateMilestone(ctx, full, "v1.0", "", "open")
	if err != nil {
		t.Fatalf("CreateMilestone: %v", err)
	}
	createMilestoneIssue(t, svc, ctx, full, login, ms.ID, "issue-no-labels", nil)

	labels, err := svc.ListMilestoneLabels(ctx, full, ms.Number)
	if err != nil {
		t.Fatalf("ListMilestoneLabels: %v", err)
	}
	if len(labels) != 0 {
		t.Fatalf("expected 0 labels, got %d", len(labels))
	}
}

func TestMilestoneLabels_NotFound(t *testing.T) {
	svc, full, cleanup := setupMilestoneTest(t)
	defer cleanup()
	ctx := context.Background()

	_, err := svc.ListMilestoneLabels(ctx, full, 999)
	if !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestMilestoneLabels_Deduplication(t *testing.T) {
	svc, full, cleanup := setupMilestoneTest(t)
	defer cleanup()
	ctx := context.Background()

	login := strings.SplitN(full, "/", 2)[0]

	ms, err := svc.CreateMilestone(ctx, full, "v1.0", "", "open")
	if err != nil {
		t.Fatalf("CreateMilestone: %v", err)
	}
	if _, err := svc.CreateLabel(ctx, full, "ms-dedupe-bug", "ffaa00", ""); err != nil {
		t.Fatalf("CreateLabel(ms-dedupe-bug): %v", err)
	}

	createMilestoneIssue(t, svc, ctx, full, login, ms.ID, "issue-1", []string{"ms-dedupe-bug"})
	createMilestoneIssue(t, svc, ctx, full, login, ms.ID, "issue-2", []string{"ms-dedupe-bug"})

	labels, err := svc.ListMilestoneLabels(ctx, full, ms.Number)
	if err != nil {
		t.Fatalf("ListMilestoneLabels: %v", err)
	}
	if len(labels) != 1 {
		t.Fatalf("expected 1 label, got %d", len(labels))
	}
	if labels[0].Name != "ms-dedupe-bug" {
		t.Fatalf("expected label ms-dedupe-bug, got %s", labels[0].Name)
	}
}

func TestMilestoneLabels_NoCrossMilestoneLeakage(t *testing.T) {
	svc, full, cleanup := setupMilestoneTest(t)
	defer cleanup()
	ctx := context.Background()

	login := strings.SplitN(full, "/", 2)[0]

	ms1, err := svc.CreateMilestone(ctx, full, "v1.0", "", "open")
	if err != nil {
		t.Fatalf("CreateMilestone(ms1): %v", err)
	}
	ms2, err := svc.CreateMilestone(ctx, full, "v2.0", "", "open")
	if err != nil {
		t.Fatalf("CreateMilestone(ms2): %v", err)
	}
	if _, err := svc.CreateLabel(ctx, full, "ms-scope-bug", "cc0000", ""); err != nil {
		t.Fatalf("CreateLabel(ms-scope-bug): %v", err)
	}
	if _, err := svc.CreateLabel(ctx, full, "ms-scope-docs", "00ccff", ""); err != nil {
		t.Fatalf("CreateLabel(ms-scope-docs): %v", err)
	}

	createMilestoneIssue(t, svc, ctx, full, login, ms1.ID, "issue-ms1", []string{"ms-scope-bug"})
	createMilestoneIssue(t, svc, ctx, full, login, ms2.ID, "issue-ms2", []string{"ms-scope-docs"})

	labels, err := svc.ListMilestoneLabels(ctx, full, ms1.Number)
	if err != nil {
		t.Fatalf("ListMilestoneLabels: %v", err)
	}
	if len(labels) != 1 {
		t.Fatalf("expected 1 label, got %d", len(labels))
	}
	got := labelNameSet(labels)
	if !got["ms-scope-bug"] {
		t.Fatalf("expected ms-scope-bug label, got %v", got)
	}
	if got["ms-scope-docs"] {
		t.Fatalf("expected ms-scope-docs to be excluded, got %v", got)
	}
}

func TestCountMilestoneIssuesBatch(t *testing.T) {
	svc, full, cleanup := setupMilestoneTest(t)
	defer cleanup()
	ctx := context.Background()

	login := strings.SplitN(full, "/", 2)[0]

	ms1, err := svc.CreateMilestone(ctx, full, "v1.0", "", "open")
	if err != nil {
		t.Fatalf("CreateMilestone ms1: %v", err)
	}
	ms2, err := svc.CreateMilestone(ctx, full, "v2.0", "", "open")
	if err != nil {
		t.Fatalf("CreateMilestone ms2: %v", err)
	}

	createMilestoneIssue(t, svc, ctx, full, login, ms1.ID, "issue-open", nil)
	issueClosed := createMilestoneIssue(t, svc, ctx, full, login, ms1.ID, "issue-closed", nil)
	closed := db.StateClosed
	if _, err := svc.UpdateIssue(ctx, full, issueClosed.Number, service.UpdateIssueInput{State: &closed}); err != nil {
		t.Fatalf("UpdateIssue issue-closed: %v", err)
	}

	repo, err := svc.GetRepo(ctx, full)
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}
	if err := svc.DB.Create(&db.PullRequest{
		Number:           1,
		RepositoryID:     repo.ID,
		HeadRepositoryID: repo.ID,
		Title:            "pr-open",
		HeadRef:          "feature-open",
		BaseRef:          "main",
		State:            db.StateOpen,
		AuthorID:         repo.OwnerID,
		MilestoneID:      &ms1.ID,
	}).Error; err != nil {
		t.Fatalf("create open PR row: %v", err)
	}
	if err := svc.DB.Create(&db.PullRequest{
		Number:           2,
		RepositoryID:     repo.ID,
		HeadRepositoryID: repo.ID,
		Title:            "pr-closed",
		HeadRef:          "feature-closed",
		BaseRef:          "main",
		State:            db.StateClosed,
		AuthorID:         repo.OwnerID,
		MilestoneID:      &ms1.ID,
	}).Error; err != nil {
		t.Fatalf("create closed PR row: %v", err)
	}

	createMilestoneIssue(t, svc, ctx, full, login, ms2.ID, "issue-other", nil)

	countsByMilestone, err := svc.CountMilestoneIssuesBatch(ctx, []uint{ms1.ID, ms2.ID})
	if err != nil {
		t.Fatalf("CountMilestoneIssuesBatch: %v", err)
	}
	if got := countsByMilestone[ms1.ID]; got.OpenIssues != 2 || got.ClosedIssues != 2 {
		t.Fatalf("ms1 counts: got %+v, want open=2 closed=2", got)
	}
	if got := countsByMilestone[ms2.ID]; got.OpenIssues != 1 || got.ClosedIssues != 0 {
		t.Fatalf("ms2 counts: got %+v, want open=1 closed=0", got)
	}

	openIssues, closedIssues, err := svc.CountMilestoneIssues(ctx, ms1.ID)
	if err != nil {
		t.Fatalf("CountMilestoneIssues: %v", err)
	}
	if openIssues != 2 || closedIssues != 2 {
		t.Fatalf("single counts: got open=%d closed=%d, want open=2 closed=2", openIssues, closedIssues)
	}
}

func createMilestoneIssue(t *testing.T, svc *service.Service, ctx context.Context, full, login string, milestoneID uint, title string, labels []string) db.Issue {
	t.Helper()

	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: full,
		Title:        title,
		AuthorLogin:  login,
	})
	if err != nil {
		t.Fatalf("CreateIssue(%s): %v", title, err)
	}
	if err := svc.SetIssueMilestone(ctx, issue.ID, &milestoneID); err != nil {
		t.Fatalf("SetIssueMilestone(%s): %v", title, err)
	}
	if len(labels) > 0 {
		if _, err := svc.AddIssueLabels(ctx, full, issue.Number, labels); err != nil {
			t.Fatalf("AddIssueLabels(%s): %v", title, err)
		}
	}
	return issue
}

func labelNameSet(labels []db.Label) map[string]bool {
	out := make(map[string]bool, len(labels))
	for _, l := range labels {
		out[l.Name] = true
	}
	return out
}
