package service_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"gorm.io/gorm"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/service"
)

func TestDeleteRepoCascade_TiDB(t *testing.T) {
	svc, cleanup := setupTestServiceWithRealDB(t)
	defer cleanup()

	ctx := context.Background()
	owner := createUser(t, svc, "cascade-owner-fk")
	other := createUser(t, svc, "cascade-other-fk")
	reviewer := createUser(t, svc, "cascade-reviewer-fk")
	forkOwner := createUser(t, svc, "cascade-fork-fk")

	repo := createRepo(t, svc, ctx, owner.Login, "repo")
	otherRepo := createRepo(t, svc, ctx, other.Login, "other")
	forkRepo := createRepo(t, svc, ctx, forkOwner.Login, "fork")

	if err := svc.DB.Model(&db.Repository{}).Where("id = ?", forkRepo.ID).
		Updates(map[string]any{"parent_id": repo.ID, "fork": true}).Error; err != nil {
		t.Fatalf("attach fork: %v", err)
	}

	workflow := db.Workflow{RepositoryID: repo.ID, Name: "CI", Path: ".github/workflows/ci.yml"}
	if err := svc.DB.Create(&workflow).Error; err != nil {
		t.Fatalf("create workflow: %v", err)
	}
	workflowRun := db.WorkflowRun{RepositoryID: repo.ID, WorkflowID: workflow.ID, Name: "CI", HeadBranch: "main", HeadSHA: "abc123", RunNumber: 1}
	if err := svc.DB.Create(&workflowRun).Error; err != nil {
		t.Fatalf("create workflow run: %v", err)
	}
	workflowJob := testWorkflowRunJob(workflowRun.ID, "build")
	if err := svc.DB.Create(&workflowJob).Error; err != nil {
		t.Fatalf("create workflow job: %v", err)
	}
	if err := svc.DB.Create(&db.Artifact{RunID: workflowRun.ID, Name: "artifact", SizeInBytes: 10}).Error; err != nil {
		t.Fatalf("create artifact: %v", err)
	}
	actionCache := testActionCache(repo.ID, "cache", "refs/heads/main", "v1")
	if err := svc.DB.Create(&actionCache).Error; err != nil {
		t.Fatalf("create action cache: %v", err)
	}

	otherWorkflow := db.Workflow{RepositoryID: otherRepo.ID, Name: "Other", Path: ".github/workflows/other.yml"}
	if err := svc.DB.Create(&otherWorkflow).Error; err != nil {
		t.Fatalf("create other workflow: %v", err)
	}
	otherRun := db.WorkflowRun{RepositoryID: otherRepo.ID, WorkflowID: otherWorkflow.ID, Name: "Other", HeadBranch: "main", HeadSHA: "def456", RunNumber: 1}
	if err := svc.DB.Create(&otherRun).Error; err != nil {
		t.Fatalf("create other workflow run: %v", err)
	}
	otherWorkflowJob := testWorkflowRunJob(otherRun.ID, "build")
	if err := svc.DB.Create(&otherWorkflowJob).Error; err != nil {
		t.Fatalf("create other workflow job: %v", err)
	}

	milestone := db.Milestone{RepositoryID: repo.ID, Number: 1, Title: "v1", CreatorID: owner.ID}
	if err := svc.DB.Create(&milestone).Error; err != nil {
		t.Fatalf("create milestone: %v", err)
	}
	issue := db.Issue{RepositoryID: repo.ID, Number: 1, Title: "issue", AuthorID: owner.ID, MilestoneID: &milestone.ID}
	if err := svc.DB.Create(&issue).Error; err != nil {
		t.Fatalf("create issue: %v", err)
	}
	if err := svc.DB.Create(&db.IssueComment{RepositoryID: repo.ID, IssueNumber: issue.Number, Body: "comment", AuthorID: owner.ID}).Error; err != nil {
		t.Fatalf("create issue comment: %v", err)
	}
	if err := svc.DB.Create(&db.IssueEvent{IssueID: issue.ID, EventType: "labeled", ActorLogin: owner.Login}).Error; err != nil {
		t.Fatalf("create issue event: %v", err)
	}
	if err := svc.DB.Create(&db.LinkedBranch{RepositoryID: repo.ID, IssueID: issue.ID, BranchName: "issue-1"}).Error; err != nil {
		t.Fatalf("create linked branch: %v", err)
	}

	label := db.Label{RepositoryID: repo.ID, Name: "cascade-label-fk", Color: "ffffff"}
	if err := svc.DB.Create(&label).Error; err != nil {
		t.Fatalf("create label: %v", err)
	}
	if err := svc.DB.Model(&issue).Association("Labels").Append(&label); err != nil {
		t.Fatalf("associate issue label: %v", err)
	}

	pr := db.PullRequest{
		RepositoryID:     repo.ID,
		HeadRepositoryID: repo.ID,
		Number:           1,
		Title:            "pr",
		AuthorID:         owner.ID,
		HeadRef:          "feature",
		HeadSHA:          "abc123",
		BaseRef:          "main",
		BaseSHA:          "def456",
	}
	if err := svc.DB.Create(&pr).Error; err != nil {
		t.Fatalf("create pr: %v", err)
	}
	prReview := db.PullRequestReview{PullRequestID: pr.ID, AuthorLogin: owner.Login, State: "APPROVED"}
	if err := svc.DB.Create(&prReview).Error; err != nil {
		t.Fatalf("create pr review: %v", err)
	}
	if err := svc.DB.Create(&db.ReviewRequest{PullRequestID: pr.ID, Login: reviewer.Login}).Error; err != nil {
		t.Fatalf("create review request: %v", err)
	}
	if err := svc.DB.Create(&db.PRReviewComment{PullRequestID: pr.ID, PullRequestReviewID: &prReview.ID, AuthorLogin: owner.Login, Body: "comment", Path: "file.go", CommitID: "abc123", Line: 1}).Error; err != nil {
		t.Fatalf("create pr review comment: %v", err)
	}

	prOther := db.PullRequest{
		RepositoryID:     otherRepo.ID,
		HeadRepositoryID: otherRepo.ID,
		Number:           1,
		Title:            "other-pr",
		AuthorID:         other.ID,
		HeadRef:          "feature",
		HeadSHA:          "abc123",
		BaseRef:          "main",
		BaseSHA:          "def456",
	}
	if err := svc.DB.Create(&prOther).Error; err != nil {
		t.Fatalf("create other pr: %v", err)
	}

	prCross := db.PullRequest{
		RepositoryID:     otherRepo.ID,
		HeadRepositoryID: repo.ID,
		Number:           2,
		Title:            "pr-cross",
		AuthorID:         other.ID,
		HeadRef:          "feature",
		HeadSHA:          "abc123",
		BaseRef:          "main",
		BaseSHA:          "def456",
	}
	if err := svc.DB.Create(&prCross).Error; err != nil {
		t.Fatalf("create cross pr: %v", err)
	}
	crossReview := db.PullRequestReview{PullRequestID: prCross.ID, AuthorLogin: other.Login, State: "COMMENTED"}
	if err := svc.DB.Create(&crossReview).Error; err != nil {
		t.Fatalf("create cross review: %v", err)
	}
	if err := svc.DB.Create(&db.ReviewRequest{PullRequestID: prCross.ID, Login: reviewer.Login}).Error; err != nil {
		t.Fatalf("create cross review request: %v", err)
	}
	if err := svc.DB.Create(&db.PRReviewComment{PullRequestID: prCross.ID, PullRequestReviewID: &crossReview.ID, AuthorLogin: other.Login, Body: "comment", Path: "file.go", CommitID: "abc123", Line: 1}).Error; err != nil {
		t.Fatalf("create cross review comment: %v", err)
	}

	otherLabel := db.Label{RepositoryID: otherRepo.ID, Name: "cascade-other-label-fk", Color: "000000"}
	if err := svc.DB.Create(&otherLabel).Error; err != nil {
		t.Fatalf("create other label: %v", err)
	}
	if err := svc.DB.Model(&pr).Association("Labels").Append(&label); err != nil {
		t.Fatalf("associate pr label: %v", err)
	}
	if err := svc.DB.Model(&prCross).Association("Labels").Append(&otherLabel); err != nil {
		t.Fatalf("associate cross pr label: %v", err)
	}
	if err := svc.DB.Model(&prOther).Association("Labels").Append(&otherLabel); err != nil {
		t.Fatalf("associate other pr label: %v", err)
	}

	otherIssue := db.Issue{RepositoryID: otherRepo.ID, Number: 1, Title: "other issue", AuthorID: other.ID}
	if err := svc.DB.Create(&otherIssue).Error; err != nil {
		t.Fatalf("create other issue: %v", err)
	}
	if err := svc.DB.Model(&otherIssue).Association("Labels").Append(&otherLabel); err != nil {
		t.Fatalf("associate other issue label: %v", err)
	}
	if err := svc.DB.Create(&db.IssueComment{RepositoryID: otherRepo.ID, IssueNumber: otherIssue.Number, Body: "comment", AuthorID: other.ID}).Error; err != nil {
		t.Fatalf("create other issue comment: %v", err)
	}

	release := db.Release{RepositoryID: repo.ID, TagName: "v1.0.0", Name: "v1", AuthorID: owner.ID}
	if err := svc.DB.Create(&release).Error; err != nil {
		t.Fatalf("create release: %v", err)
	}
	if err := svc.DB.Create(&db.ReleaseAsset{ReleaseID: release.ID, Name: "asset", ContentType: "text/plain", Size: 1}).Error; err != nil {
		t.Fatalf("create release asset: %v", err)
	}

	otherRelease := db.Release{RepositoryID: otherRepo.ID, TagName: "v0.1.0", Name: "v0.1", AuthorID: other.ID}
	if err := svc.DB.Create(&otherRelease).Error; err != nil {
		t.Fatalf("create other release: %v", err)
	}
	if err := svc.DB.Create(&db.ReleaseAsset{ReleaseID: otherRelease.ID, Name: "other-asset", ContentType: "text/plain", Size: 1}).Error; err != nil {
		t.Fatalf("create other release asset: %v", err)
	}

	ownerCtx := service.ContextWithUser(ctx, owner)
	if err := svc.DeleteRepo(ownerCtx, repo.FullName); err != nil {
		t.Fatalf("DeleteRepo: %v", err)
	}

	if _, err := svc.GetRepo(ctx, repo.FullName); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("expected repo to be deleted, got %v", err)
	}
	if svc.Git.Exists(ctx, repo.FullName) {
		t.Fatalf("expected git repo to be removed")
	}

	var forkCheck db.Repository
	if err := svc.DB.First(&forkCheck, "id = ?", forkRepo.ID).Error; err != nil {
		t.Fatalf("load fork repo: %v", err)
	}
	if forkCheck.ParentID != nil {
		t.Fatalf("expected fork parent_id to be nil, got %v", *forkCheck.ParentID)
	}
	if forkCheck.Fork {
		t.Fatalf("expected fork flag to be false")
	}

	assertCount(t, svc, &db.WorkflowRunJob{}, 0, "run_id = ?", workflowRun.ID)
	assertCount(t, svc, &db.Artifact{}, 0, "run_id = ?", workflowRun.ID)
	assertCount(t, svc, &db.WorkflowRun{}, 0, "repository_id = ?", repo.ID)
	assertCount(t, svc, &db.Workflow{}, 0, "repository_id = ?", repo.ID)
	assertCount(t, svc, &db.ActionCache{}, 0, "repository_id = ?", repo.ID)

	assertCount(t, svc, &db.IssueComment{}, 0, "repository_id = ?", repo.ID)
	assertCount(t, svc, &db.IssueEvent{}, 0, "issue_id = ?", issue.ID)
	assertCount(t, svc, &db.Issue{}, 0, "repository_id = ?", repo.ID)
	assertCount(t, svc, &db.LinkedBranch{}, 0, "repository_id = ?", repo.ID)
	assertCount(t, svc, &db.Milestone{}, 0, "repository_id = ?", repo.ID)

	assertCount(t, svc, &db.PRReviewComment{}, 0, "pull_request_id IN (?, ?)", pr.ID, prCross.ID)
	assertCount(t, svc, &db.PullRequestReview{}, 0, "pull_request_id IN (?, ?)", pr.ID, prCross.ID)
	assertCount(t, svc, &db.ReviewRequest{}, 0, "pull_request_id IN (?, ?)", pr.ID, prCross.ID)
	assertCount(t, svc, &db.PullRequest{}, 0, "id IN (?, ?)", pr.ID, prCross.ID)

	assertCount(t, svc, &db.ReleaseAsset{}, 0, "release_id = ?", release.ID)
	assertCount(t, svc, &db.Release{}, 0, "repository_id = ?", repo.ID)
	var joinCount int64
	if err := svc.DB.Table("issue_labels").Where("issue_id = ?", issue.ID).Count(&joinCount).Error; err != nil {
		t.Fatalf("count issue_labels: %v", err)
	}
	if joinCount != 0 {
		t.Fatalf("expected issue_labels to be empty, got %d", joinCount)
	}
	if err := svc.DB.Table("pr_labels").Where("pull_request_id IN (?, ?)", pr.ID, prCross.ID).Count(&joinCount).Error; err != nil {
		t.Fatalf("count pr_labels: %v", err)
	}
	if joinCount != 0 {
		t.Fatalf("expected pr_labels to be empty, got %d", joinCount)
	}

	if _, err := svc.GetRepo(ctx, otherRepo.FullName); err != nil {
		t.Fatalf("expected other repo to remain, got %v", err)
	}
	if !svc.Git.Exists(ctx, otherRepo.FullName) {
		t.Fatalf("expected other repo git data to remain")
	}

	assertCount(t, svc, &db.Issue{}, 1, "id = ?", otherIssue.ID)
	assertCount(t, svc, &db.IssueComment{}, 1, "repository_id = ? AND issue_number = ?", otherRepo.ID, otherIssue.Number)
	assertCount(t, svc, &db.WorkflowRunJob{}, 1, "run_id = ?", otherRun.ID)
	assertCount(t, svc, &db.PullRequest{}, 1, "id = ?", prOther.ID)
	assertCount(t, svc, &db.Release{}, 1, "repository_id = ?", otherRepo.ID)
	assertCount(t, svc, &db.ReleaseAsset{}, 1, "release_id = ?", otherRelease.ID)

	if err := svc.DB.Table("issue_labels").Where("issue_id = ?", otherIssue.ID).Count(&joinCount).Error; err != nil {
		t.Fatalf("count other issue_labels: %v", err)
	}
	if joinCount != 1 {
		t.Fatalf("expected other issue_labels to remain, got %d", joinCount)
	}
	if err := svc.DB.Table("pr_labels").Where("pull_request_id = ?", prOther.ID).Count(&joinCount).Error; err != nil {
		t.Fatalf("count other pr_labels: %v", err)
	}
	if joinCount != 1 {
		t.Fatalf("expected other pr_labels to remain, got %d", joinCount)
	}

	if err := svc.DeleteRepo(ownerCtx, repo.FullName); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("expected idempotent delete to return not found, got %v", err)
	}
}

func TestDeleteRepoCascade_TiDBRollback(t *testing.T) {
	svc, cleanup := setupTestServiceWithRealDB(t)
	defer cleanup()

	ctx := context.Background()
	owner := createUser(t, svc, "rollback-owner-fk")

	repo := createRepo(t, svc, ctx, owner.Login, "rollback")

	workflow := db.Workflow{RepositoryID: repo.ID, Name: "CI", Path: ".github/workflows/ci.yml"}
	if err := svc.DB.Create(&workflow).Error; err != nil {
		t.Fatalf("create workflow: %v", err)
	}
	workflowRun := db.WorkflowRun{RepositoryID: repo.ID, WorkflowID: workflow.ID, Name: "CI", HeadBranch: "main", HeadSHA: "abc123", RunNumber: 1}
	if err := svc.DB.Create(&workflowRun).Error; err != nil {
		t.Fatalf("create workflow run: %v", err)
	}
	workflowJob := testWorkflowRunJob(workflowRun.ID, "build")
	if err := svc.DB.Create(&workflowJob).Error; err != nil {
		t.Fatalf("create workflow job: %v", err)
	}

	issue := db.Issue{RepositoryID: repo.ID, Number: 1, Title: "issue", AuthorID: owner.ID}
	if err := svc.DB.Create(&issue).Error; err != nil {
		t.Fatalf("create issue: %v", err)
	}
	if err := svc.DB.Create(&db.IssueComment{RepositoryID: repo.ID, IssueNumber: issue.Number, Body: "comment", AuthorID: owner.ID}).Error; err != nil {
		t.Fatalf("create issue comment: %v", err)
	}

	pr := db.PullRequest{
		RepositoryID:     repo.ID,
		HeadRepositoryID: repo.ID,
		Number:           1,
		Title:            "pr",
		AuthorID:         owner.ID,
		HeadRef:          "feature",
		HeadSHA:          "abc123",
		BaseRef:          "main",
		BaseSHA:          "def456",
	}
	if err := svc.DB.Create(&pr).Error; err != nil {
		t.Fatalf("create pr: %v", err)
	}
	prReview := db.PullRequestReview{PullRequestID: pr.ID, AuthorLogin: owner.Login, State: "APPROVED"}
	if err := svc.DB.Create(&prReview).Error; err != nil {
		t.Fatalf("create review: %v", err)
	}
	if err := svc.DB.Create(&db.PRReviewComment{PullRequestID: pr.ID, PullRequestReviewID: &prReview.ID, AuthorLogin: owner.Login, Body: "comment", Path: "file.go", CommitID: "abc123", Line: 1}).Error; err != nil {
		t.Fatalf("create review comment: %v", err)
	}

	release := db.Release{RepositoryID: repo.ID, TagName: "v1.0.0", Name: "v1", AuthorID: owner.ID}
	if err := svc.DB.Create(&release).Error; err != nil {
		t.Fatalf("create release: %v", err)
	}
	if err := svc.DB.Create(&db.ReleaseAsset{ReleaseID: release.ID, Name: "asset", ContentType: "text/plain", Size: 1}).Error; err != nil {
		t.Fatalf("create release asset: %v", err)
	}

	const cbName = "test:cascade_fail_release_asset"
	var failOnce atomic.Bool
	failOnce.Store(true)
	if err := svc.DB.Callback().Delete().Before("gorm:delete").Register(cbName, func(tx *gorm.DB) {
		if (tx.Statement.Table == "release_assets" || (tx.Statement.Schema != nil && tx.Statement.Schema.Table == "release_assets")) && failOnce.CompareAndSwap(true, false) {
			tx.AddError(errors.New("forced release asset delete failure"))
		}
	}); err != nil {
		t.Fatalf("register delete callback: %v", err)
	}
	defer func() {
		_ = svc.DB.Callback().Delete().Remove(cbName)
	}()

	ownerCtx := service.ContextWithUser(ctx, owner)
	if err := svc.DeleteRepo(ownerCtx, repo.FullName); err == nil {
		t.Fatalf("expected DeleteRepo to fail")
	}
	if failOnce.Load() {
		t.Fatalf("expected injected delete failure to trigger")
	}

	if _, err := svc.GetRepo(ctx, repo.FullName); err != nil {
		t.Fatalf("expected repo to remain after rollback, got %v", err)
	}
	if !svc.Git.Exists(ctx, repo.FullName) {
		t.Fatalf("expected git repo to remain after rollback")
	}

	assertCount(t, svc, &db.WorkflowRunJob{}, 1, "run_id = ?", workflowRun.ID)
	assertCount(t, svc, &db.Issue{}, 1, "repository_id = ?", repo.ID)
	assertCount(t, svc, &db.PRReviewComment{}, 1, "pull_request_id = ?", pr.ID)
	assertCount(t, svc, &db.ReleaseAsset{}, 1, "release_id = ?", release.ID)
}
