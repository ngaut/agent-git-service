package service_test

import (
	"context"
	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/service"
	"testing"
)

func TestPRFlow(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	// 1. Setup
	svc.DB.Create(&db.User{Login: "pruser"})
	_, err := svc.CreateRepo(ctx, service.CreateRepoInput{OwnerLogin: "pruser", Name: "pr-repo", DefaultBranch: "main"})
	if err != nil {
		t.Fatalf("failed to setup repo: %v", err)
	}

	// 2. Create PR
	in := service.CreatePRInput{
		RepoFullName: "pruser/pr-repo",
		Title:        "New Feature",
		Body:         "PR Body",
		HeadRef:      "feature-branch",
		BaseRef:      "main",
		AuthorLogin:  "pruser",
	}
	pr, err := svc.CreatePR(ctx, in)
	if err != nil {
		t.Fatalf("failed to create pr: %v", err)
	}
	if pr.Number != 1 {
		t.Errorf("expected pr number 1, got %d", pr.Number)
	}

	// 3. Update PR
	newTitle := "Updated Feature"
	updated, err := svc.UpdatePR(ctx, "pruser/pr-repo", pr.Number, service.UpdatePRInput{Title: &newTitle})
	if err != nil {
		t.Fatalf("failed to update pr: %v", err)
	}
	if updated.Title != newTitle {
		t.Errorf("expected updated title %s, got %s", newTitle, updated.Title)
	}

	// 4. List PRs
	prs, err := svc.ListPRs(ctx, "pruser/pr-repo", "open")
	if err != nil {
		t.Fatalf("failed to list prs: %v", err)
	}
	if len(prs) != 1 {
		t.Errorf("expected 1 pr, got %d", len(prs))
	}
}

func TestListPRsFiltered_MentionedUsesTokenBoundaries(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	if err := svc.DB.Create(&db.User{Login: "pruser", Name: "pruser", Type: db.TypeUser}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	_, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin:    "pruser",
		Name:          "mention-pr-repo",
		DefaultBranch: "main",
		AutoInit:      true,
	})
	if err != nil {
		t.Fatalf("failed to setup repo: %v", err)
	}

	for _, branch := range []string{"exact-mention", "substring-mention", "email-mention"} {
		if err := svc.Git.CreateBranch(ctx, "pruser/mention-pr-repo", branch, "main"); err != nil {
			t.Fatalf("create branch %s: %v", branch, err)
		}
	}

	exactPR, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "pruser/mention-pr-repo",
		Title:        "Exact mention",
		Body:         "please review @pruser",
		HeadRef:      "exact-mention",
		BaseRef:      "main",
		AuthorLogin:  "pruser",
	})
	if err != nil {
		t.Fatalf("create exact mention pr: %v", err)
	}
	if _, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "pruser/mention-pr-repo",
		Title:        "Substring mention",
		Body:         "please review @pruser2",
		HeadRef:      "substring-mention",
		BaseRef:      "main",
		AuthorLogin:  "pruser",
	}); err != nil {
		t.Fatalf("create substring mention pr: %v", err)
	}
	if _, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "pruser/mention-pr-repo",
		Title:        "Email mention",
		Body:         "please review foo@pruser",
		HeadRef:      "email-mention",
		BaseRef:      "main",
		AuthorLogin:  "pruser",
	}); err != nil {
		t.Fatalf("create email mention pr: %v", err)
	}

	prs, err := svc.ListPRsFiltered(ctx, service.PRListFilter{
		RepoFullName: "pruser/mention-pr-repo",
		State:        "all",
		Mentioned:    "pruser",
	})
	if err != nil {
		t.Fatalf("ListPRsFiltered: %v", err)
	}
	if len(prs) != 1 {
		t.Fatalf("expected 1 exact mention pr, got %d", len(prs))
	}
	if prs[0].ID != exactPR.ID {
		t.Fatalf("expected exact mention pr ID %d, got %d", exactPR.ID, prs[0].ID)
	}
}
