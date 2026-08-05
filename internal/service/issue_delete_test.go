package service_test

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"gorm.io/gorm"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/service"
)

func TestDeleteIssueByID_CascadeAndIsolation(t *testing.T) {
	svc, cleanup := setupTestServiceWithRealDB(t)
	defer cleanup()

	ctx := context.Background()
	owner := createUser(t, svc, "issue-owner")
	other := createUser(t, svc, "issue-other")
	reactor := createUser(t, svc, "issue-reactor")

	repo := createRepo(t, svc, ctx, owner.Login, "repo")
	otherRepo := createRepo(t, svc, ctx, other.Login, "other")

	label := db.Label{RepositoryID: repo.ID, Name: "issue-label", Color: "ffffff"}
	if err := svc.DB.Create(&label).Error; err != nil {
		t.Fatalf("create label: %v", err)
	}
	project := db.Project{Number: 1, OwnerLogin: owner.Login, Title: "Project"}
	if err := svc.DB.Create(&project).Error; err != nil {
		t.Fatalf("create project: %v", err)
	}

	issue := db.Issue{RepositoryID: repo.ID, Number: 1, Title: "issue", AuthorID: owner.ID}
	if err := svc.DB.Create(&issue).Error; err != nil {
		t.Fatalf("create issue: %v", err)
	}
	if err := svc.DB.Model(&issue).Association("Labels").Append(&label); err != nil {
		t.Fatalf("associate issue label: %v", err)
	}
	if err := svc.DB.Create(&db.IssueComment{RepositoryID: repo.ID, IssueNumber: issue.Number, Body: "comment", AuthorID: owner.ID}).Error; err != nil {
		t.Fatalf("create issue comment: %v", err)
	}
	if err := svc.DB.Create(&db.IssueEvent{IssueID: issue.ID, EventType: "labeled", ActorLogin: owner.Login}).Error; err != nil {
		t.Fatalf("create issue event: %v", err)
	}
	if err := svc.DB.Create(&db.Reaction{IssueID: &issue.ID, UserID: reactor.ID, Content: "+1"}).Error; err != nil {
		t.Fatalf("create reaction: %v", err)
	}
	if err := svc.DB.Create(&db.ProjectItem{ProjectID: project.ID, ContentID: fmt.Sprintf("Issue_%d", issue.ID), Type: "ISSUE"}).Error; err != nil {
		t.Fatalf("create project item: %v", err)
	}
	if err := svc.DB.Create(&db.LinkedBranch{RepositoryID: repo.ID, IssueID: issue.ID, BranchName: "issue-1"}).Error; err != nil {
		t.Fatalf("create linked branch: %v", err)
	}

	otherLabel := db.Label{RepositoryID: otherRepo.ID, Name: "issue-other-label", Color: "000000"}
	if err := svc.DB.Create(&otherLabel).Error; err != nil {
		t.Fatalf("create other label: %v", err)
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
	if err := svc.DB.Create(&db.Reaction{IssueID: &otherIssue.ID, UserID: other.ID, Content: "heart"}).Error; err != nil {
		t.Fatalf("create other reaction: %v", err)
	}

	ownerCtx := service.ContextWithUser(ctx, owner)
	if err := svc.DeleteIssueByID(ownerCtx, issue.ID); err != nil {
		t.Fatalf("DeleteIssueByID: %v", err)
	}

	assertCount(t, svc, &db.Issue{}, 0, "id = ?", issue.ID)
	assertCount(t, svc, &db.IssueComment{}, 0, "repository_id = ? AND issue_number = ?", repo.ID, issue.Number)
	assertCount(t, svc, &db.IssueEvent{}, 0, "issue_id = ?", issue.ID)
	assertCount(t, svc, &db.Reaction{}, 0, "issue_id = ?", issue.ID)
	assertCount(t, svc, &db.ProjectItem{}, 0, "type = ? AND content_id = ?", "ISSUE", fmt.Sprintf("Issue_%d", issue.ID))
	assertCount(t, svc, &db.LinkedBranch{}, 0, "issue_id = ?", issue.ID)

	var joinCount int64
	if err := svc.DB.Table("issue_labels").Where("issue_id = ?", issue.ID).Count(&joinCount).Error; err != nil {
		t.Fatalf("count issue_labels: %v", err)
	}
	if joinCount != 0 {
		t.Fatalf("expected issue_labels to be empty, got %d", joinCount)
	}
	assertCount(t, svc, &db.Issue{}, 1, "id = ?", otherIssue.ID)
	assertCount(t, svc, &db.IssueComment{}, 1, "repository_id = ? AND issue_number = ?", otherRepo.ID, otherIssue.Number)
	assertCount(t, svc, &db.Reaction{}, 1, "issue_id = ?", otherIssue.ID)
	if err := svc.DB.Table("issue_labels").Where("issue_id = ?", otherIssue.ID).Count(&joinCount).Error; err != nil {
		t.Fatalf("count other issue_labels: %v", err)
	}
	if joinCount != 1 {
		t.Fatalf("expected other issue_labels to remain, got %d", joinCount)
	}

	if err := svc.DeleteIssueByID(ownerCtx, issue.ID); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("expected idempotent delete to return not found, got %v", err)
	}
}

func TestDeleteIssueByID_RollbackOnError(t *testing.T) {
	svc, cleanup := setupTestServiceWithRealDB(t)
	defer cleanup()

	ctx := context.Background()
	owner := createUser(t, svc, "issue-rollback")
	reactor := createUser(t, svc, "issue-rollback-reactor")

	repo := createRepo(t, svc, ctx, owner.Login, "repo")

	label := db.Label{RepositoryID: repo.ID, Name: "issue-rollback-label", Color: "ffffff"}
	if err := svc.DB.Create(&label).Error; err != nil {
		t.Fatalf("create label: %v", err)
	}
	project := db.Project{Number: 1, OwnerLogin: owner.Login, Title: "Project"}
	if err := svc.DB.Create(&project).Error; err != nil {
		t.Fatalf("create project: %v", err)
	}

	issue := db.Issue{RepositoryID: repo.ID, Number: 1, Title: "issue", AuthorID: owner.ID}
	if err := svc.DB.Create(&issue).Error; err != nil {
		t.Fatalf("create issue: %v", err)
	}
	if err := svc.DB.Model(&issue).Association("Labels").Append(&label); err != nil {
		t.Fatalf("associate issue label: %v", err)
	}
	if err := svc.DB.Create(&db.IssueComment{RepositoryID: repo.ID, IssueNumber: issue.Number, Body: "comment", AuthorID: owner.ID}).Error; err != nil {
		t.Fatalf("create issue comment: %v", err)
	}
	if err := svc.DB.Create(&db.IssueEvent{IssueID: issue.ID, EventType: "labeled", ActorLogin: owner.Login}).Error; err != nil {
		t.Fatalf("create issue event: %v", err)
	}
	if err := svc.DB.Create(&db.Reaction{IssueID: &issue.ID, UserID: reactor.ID, Content: "+1"}).Error; err != nil {
		t.Fatalf("create reaction: %v", err)
	}
	if err := svc.DB.Create(&db.ProjectItem{ProjectID: project.ID, ContentID: fmt.Sprintf("Issue_%d", issue.ID), Type: "ISSUE"}).Error; err != nil {
		t.Fatalf("create project item: %v", err)
	}
	if err := svc.DB.Create(&db.LinkedBranch{RepositoryID: repo.ID, IssueID: issue.ID, BranchName: "issue-1"}).Error; err != nil {
		t.Fatalf("create linked branch: %v", err)
	}

	const cbName = "test:issue_delete_fail_project_item"
	var failOnce atomic.Bool
	failOnce.Store(true)
	if err := svc.DB.Callback().Delete().Before("gorm:delete").Register(cbName, func(tx *gorm.DB) {
		if (tx.Statement.Table == "project_items" || (tx.Statement.Schema != nil && tx.Statement.Schema.Table == "project_items")) && failOnce.CompareAndSwap(true, false) {
			tx.AddError(errors.New("forced project item delete failure"))
		}
	}); err != nil {
		t.Fatalf("register delete callback: %v", err)
	}
	defer func() {
		_ = svc.DB.Callback().Delete().Remove(cbName)
	}()

	ownerCtx := service.ContextWithUser(ctx, owner)
	if err := svc.DeleteIssueByID(ownerCtx, issue.ID); err == nil {
		t.Fatalf("expected DeleteIssueByID to fail")
	}
	if failOnce.Load() {
		t.Fatalf("expected injected delete failure to trigger")
	}

	assertCount(t, svc, &db.Issue{}, 1, "id = ?", issue.ID)
	assertCount(t, svc, &db.IssueComment{}, 1, "repository_id = ? AND issue_number = ?", repo.ID, issue.Number)
	assertCount(t, svc, &db.IssueEvent{}, 1, "issue_id = ?", issue.ID)
	assertCount(t, svc, &db.Reaction{}, 1, "issue_id = ?", issue.ID)
	assertCount(t, svc, &db.ProjectItem{}, 1, "type = ? AND content_id = ?", "ISSUE", fmt.Sprintf("Issue_%d", issue.ID))
	assertCount(t, svc, &db.LinkedBranch{}, 1, "issue_id = ?", issue.ID)

	var joinCount int64
	if err := svc.DB.Table("issue_labels").Where("issue_id = ?", issue.ID).Count(&joinCount).Error; err != nil {
		t.Fatalf("count issue_labels: %v", err)
	}
	if joinCount != 1 {
		t.Fatalf("expected issue_labels to remain, got %d", joinCount)
	}
}
