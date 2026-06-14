package service_test

import (
	"context"
	"testing"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/service"
	"github.com/ngaut/agent-git-service/internal/testharness"
)

// setupTestServiceWithRealDB builds a service fixture with the real test DB
// and the connection pool pinned to a single connection. Used only by
// cascade-delete tests — most service tests should use setupTestService.
func setupTestServiceWithRealDB(t *testing.T) (*service.Service, func()) {
	return testharness.NewService(t, testharness.ServiceConfig{MaxOpenConns: 1})
}

func TestDeleteRepo_CascadeHonorsFKs(t *testing.T) {
	svc, cleanup := setupTestServiceWithRealDB(t)
	defer cleanup()

	ctx := context.Background()

	owner := db.User{Login: "owner", Name: "owner", Type: db.TypeUser}
	collab := db.User{Login: "collab", Name: "collab", Type: db.TypeUser}
	invitee := db.User{Login: "invitee", Name: "invitee", Type: db.TypeUser}
	if err := svc.DB.Create(&owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	if err := svc.DB.Create(&collab).Error; err != nil {
		t.Fatalf("create collab: %v", err)
	}
	if err := svc.DB.Create(&invitee).Error; err != nil {
		t.Fatalf("create invitee: %v", err)
	}

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin:    owner.Login,
		Name:          "to-delete",
		DefaultBranch: "main",
		AutoInit:      true,
	})
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}

	// Create FK-bound child rows.
	if err := svc.DB.Create(&db.BranchProtection{RepositoryID: repo.ID, BranchName: "main"}).Error; err != nil {
		t.Fatalf("create branch protection: %v", err)
	}
	if err := svc.DB.Create(&db.Collaborator{RepositoryID: repo.ID, UserID: collab.ID, Permission: "read"}).Error; err != nil {
		t.Fatalf("create collaborator: %v", err)
	}
	if err := svc.DB.Create(&db.CommitStatus{
		RepositoryID: repo.ID,
		CreatorID:    owner.ID,
		CommitSHA:    "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		State:        "success",
	}).Error; err != nil {
		t.Fatalf("create commit status: %v", err)
	}
	if err := svc.DB.Create(&db.DependabotAlert{RepositoryID: repo.ID, Number: 1, State: "open"}).Error; err != nil {
		t.Fatalf("create dependabot alert: %v", err)
	}
	dep := db.Deployment{RepositoryID: repo.ID, CreatorID: owner.ID, Ref: "main"}
	if err := svc.DB.Create(&dep).Error; err != nil {
		t.Fatalf("create deployment: %v", err)
	}
	if err := svc.DB.Create(&db.DeploymentStatus{DeploymentID: dep.ID, CreatorID: owner.ID, State: "success"}).Error; err != nil {
		t.Fatalf("create deployment status: %v", err)
	}
	if err := svc.DB.Create(&db.RepositoryInvitation{RepositoryID: repo.ID, InviteeID: invitee.ID, InviterID: owner.ID, Permissions: "read"}).Error; err != nil {
		t.Fatalf("create invitation: %v", err)
	}
	if err := svc.DB.Create(&db.Webhook{RepositoryID: repo.ID, Name: "web", Active: true, EventsJSON: `["push"]`, ConfigJSON: `{}`}).Error; err != nil {
		t.Fatalf("create webhook: %v", err)
	}
	if err := svc.DB.Create(&db.RepoRedirect{OldFullName: owner.Login + "/old-to-delete", RepoID: repo.ID}).Error; err != nil {
		t.Fatalf("create repo redirect: %v", err)
	}
	ms := db.Milestone{
		RepositoryID: repo.ID,
		Number:       1,
		Title:        "v1.0",
		CreatorID:    owner.ID,
	}
	if err := svc.DB.Create(&ms).Error; err != nil {
		t.Fatalf("create milestone: %v", err)
	}
	if err := svc.DB.Create(&db.Issue{
		RepositoryID: repo.ID,
		Number:       1,
		Title:        "issue bound to milestone",
		AuthorID:     owner.ID,
		MilestoneID:  &ms.ID,
	}).Error; err != nil {
		t.Fatalf("create issue with milestone: %v", err)
	}
	if err := svc.DB.Omit("Embedding").Create(&db.WikiSearchDocument{
		RepositoryID: repo.ID,
		Slug:         "docs/home",
		Title:        "Home",
		Body:         db.LargeText("wiki body"),
		RevisionSHA:  "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
	}).Error; err != nil {
		t.Fatalf("create wiki search document: %v", err)
	}
	if err := svc.DB.Create(&db.WikiCompactionJob{
		ID:           "delete-repo-job",
		RepositoryID: repo.ID,
		Status:       service.WikiCompactionJobSucceeded,
		PreviousHead: "1111111111111111111111111111111111111111",
		NewHead:      "2222222222222222222222222222222222222222",
	}).Error; err != nil {
		t.Fatalf("create wiki compaction job: %v", err)
	}

	if err := svc.DeleteRepo(ctx, repo.FullName); err != nil {
		t.Fatalf("delete repo: %v", err)
	}

	var count int64
	if err := svc.DB.Model(&db.WikiSearchDocument{}).Where("repository_id = ?", repo.ID).Count(&count).Error; err != nil {
		t.Fatalf("count wiki search documents: %v", err)
	}
	if count != 0 {
		t.Fatalf("wiki search documents remaining after repo delete: %d", count)
	}
	if err := svc.DB.Model(&db.WikiCompactionJob{}).Where("repository_id = ?", repo.ID).Count(&count).Error; err != nil {
		t.Fatalf("count wiki compaction jobs: %v", err)
	}
	if count != 0 {
		t.Fatalf("wiki compaction jobs remaining after repo delete: %d", count)
	}
}
