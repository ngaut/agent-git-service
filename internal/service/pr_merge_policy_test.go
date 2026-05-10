package service_test

import (
	"context"
	"errors"
	"testing"

	"gh-server/internal/db"
	"gh-server/internal/service"
)

func setupProtectedPR(t testing.TB, svc *service.Service, login, repoName string, allowAutoMerge bool) (db.PullRequest, context.Context, db.User) {
	t.Helper()
	ctx := context.Background()

	user := db.User{Login: login, Name: login, Type: db.TypeUser}
	if err := svc.DB.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin:     login,
		Name:           repoName,
		DefaultBranch:  "main",
		AddReadme:      true,
		AllowAutoMerge: &allowAutoMerge,
	})
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}

	if err := svc.Git.CreateBranch(ctx, repo.FullName, "feature", "main"); err != nil {
		t.Fatalf("create branch: %v", err)
	}
	if _, err := svc.Git.WriteFile(ctx, repo.FullName, "feature", "hello.txt", "add test file", []byte("hello world\n")); err != nil {
		t.Fatalf("write file: %v", err)
	}

	authCtx := service.ContextWithUser(ctx, user)
	pr, err := svc.CreatePR(authCtx, service.CreatePRInput{
		RepoFullName: repo.FullName,
		Title:        "Protected PR",
		Body:         "test body",
		HeadRef:      "feature",
		BaseRef:      "main",
		AuthorLogin:  login,
	})
	if err != nil {
		t.Fatalf("create PR: %v", err)
	}
	return pr, authCtx, user
}

func protectBranch(t testing.TB, svc *service.Service, repoID uint, branch, requiredStatusJSON, requiredReviewJSON string, enforceAdmins bool) {
	t.Helper()
	err := svc.UpdateBranchProtection(context.Background(), &db.BranchProtection{
		RepositoryID:             repoID,
		BranchName:               branch,
		RequiredStatusChecksJSON: requiredStatusJSON,
		RequiredPullRequestJSON:  requiredReviewJSON,
		EnforceAdmins:            enforceAdmins,
	})
	if err != nil {
		t.Fatalf("UpdateBranchProtection: %v", err)
	}
}

func TestMergePR_BranchProtectionRequiresApproval(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	pr, authCtx, _ := setupProtectedPR(t, svc, "review-user", "review-repo", false)
	protectBranch(t, svc, pr.RepositoryID, pr.BaseRef, "", `{"required_approving_review_count":1}`, true)

	_, err := svc.MergePR(authCtx, "review-user/review-repo", pr.Number, "merge", "")
	if !errors.Is(err, service.ErrInvalidState) {
		t.Fatalf("expected ErrInvalidState, got %v", err)
	}

	stored, getErr := svc.GetPRByID(authCtx, pr.ID)
	if getErr != nil {
		t.Fatalf("GetPRByID: %v", getErr)
	}
	if stored.Merged {
		t.Fatal("expected PR to remain unmerged")
	}
}

func TestMergePR_BranchProtectionBypassUser(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	pr, authCtx, user := setupProtectedPR(t, svc, "bypass-user", "bypass-repo", false)
	protectBranch(t, svc, pr.RepositoryID, pr.BaseRef, "", `{"required_approving_review_count":1,"bypass_pull_request_allowances":{"users":["`+user.Login+`"]}}`, true)

	merged, err := svc.MergePR(authCtx, "bypass-user/bypass-repo", pr.Number, "merge", "")
	if err != nil {
		t.Fatalf("MergePR: %v", err)
	}
	if !merged.Merged {
		t.Fatal("expected PR to be merged")
	}
}

func TestMergePR_BranchProtectionBypassUserStillRequiresStatusChecks(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	pr, authCtx, user := setupProtectedPR(t, svc, "bypass-status-user", "bypass-status-repo", false)
	protectBranch(t, svc, pr.RepositoryID, pr.BaseRef, `{"contexts":["CI"]}`, `{"required_approving_review_count":1,"bypass_pull_request_allowances":{"users":["`+user.Login+`"]}}`, true)

	_, err := svc.MergePR(authCtx, "bypass-status-user/bypass-status-repo", pr.Number, "merge", "")
	if !errors.Is(err, service.ErrInvalidState) {
		t.Fatalf("expected ErrInvalidState before required status checks pass, got %v", err)
	}

	status := &db.CommitStatus{
		RepositoryID: pr.RepositoryID,
		CommitSHA:    pr.HeadSHA,
		State:        "success",
		Context:      "CI",
		CreatorID:    user.ID,
	}
	if err := svc.CreateCommitStatus(authCtx, status); err != nil {
		t.Fatalf("CreateCommitStatus: %v", err)
	}

	merged, err := svc.MergePR(authCtx, "bypass-status-user/bypass-status-repo", pr.Number, "merge", "")
	if err != nil {
		t.Fatalf("MergePR after status success: %v", err)
	}
	if !merged.Merged {
		t.Fatal("expected PR to merge once required status checks pass")
	}
}

func TestAutoMerge_WorkflowSuccessMergesPR(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	pr, authCtx, user := setupProtectedPR(t, svc, "auto-workflow-user", "auto-workflow-repo", true)
	protectBranch(t, svc, pr.RepositoryID, pr.BaseRef, `{"contexts":["CI"]}`, "", true)

	if _, err := svc.SetPRAutoMerge(authCtx, pr.ID, service.SetPRAutoMergeInput{
		Enabled:         true,
		MergeMethod:     "SQUASH",
		CommitHeadline:  "Queued merge",
		CommitBody:      "Merged by auto-merge",
		ExpectedHeadSHA: pr.HeadSHA,
		AuthorEmail:     "agent@example.com",
	}); err != nil {
		t.Fatalf("SetPRAutoMerge: %v", err)
	}

	wf := db.Workflow{RepositoryID: pr.RepositoryID, Name: "CI", Path: ".github/workflows/ci.yml", State: db.WorkflowActive}
	if err := svc.DB.Create(&wf).Error; err != nil {
		t.Fatalf("create workflow: %v", err)
	}
	run := db.WorkflowRun{
		RepositoryID: pr.RepositoryID,
		WorkflowID:   wf.ID,
		ActorID:      &user.ID,
		Name:         "CI",
		HeadBranch:   pr.HeadRef,
		HeadSHA:      pr.HeadSHA,
		Status:       db.RunQueued,
		Event:        "pull_request",
	}
	if err := svc.DB.Create(&run).Error; err != nil {
		t.Fatalf("create workflow run: %v", err)
	}

	svc.CompleteRunForTest(authCtx, run.ID, db.ConclusionSuccess)

	stored, err := svc.GetPRByID(authCtx, pr.ID)
	if err != nil {
		t.Fatalf("GetPRByID: %v", err)
	}
	if !stored.Merged {
		t.Fatal("expected PR to auto-merge after successful workflow")
	}
	if stored.AutoMerge {
		t.Fatal("expected auto-merge request to be cleared after merge")
	}
	if stored.MergedByLogin != user.Login {
		t.Fatalf("merged_by_login: got %q, want %q", stored.MergedByLogin, user.Login)
	}
}

func TestAutoMerge_BranchProtectionWithoutBypassKeepsPROpen(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	pr, authCtx, _ := setupProtectedPR(t, svc, "auto-review-user", "auto-review-repo", true)
	protectBranch(t, svc, pr.RepositoryID, pr.BaseRef, `{"contexts":["CI"]}`, `{"required_approving_review_count":1}`, true)

	if _, err := svc.SetPRAutoMerge(authCtx, pr.ID, service.SetPRAutoMergeInput{
		Enabled:         true,
		MergeMethod:     "MERGE",
		ExpectedHeadSHA: pr.HeadSHA,
	}); err != nil {
		t.Fatalf("SetPRAutoMerge: %v", err)
	}

	status := &db.CommitStatus{
		RepositoryID: pr.RepositoryID,
		CommitSHA:    pr.HeadSHA,
		State:        "success",
		Context:      "CI",
	}
	if err := svc.CreateCommitStatus(authCtx, status); err != nil {
		t.Fatalf("CreateCommitStatus: %v", err)
	}

	stored, err := svc.GetPRByID(authCtx, pr.ID)
	if err != nil {
		t.Fatalf("GetPRByID: %v", err)
	}
	if stored.Merged {
		t.Fatal("expected PR to remain open without bypass or approval")
	}
	if !stored.AutoMerge {
		t.Fatal("expected auto-merge request to remain queued")
	}
}

func TestAutoMerge_ExpectedHeadSHAMismatchBlocksMerge(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	pr, authCtx, user := setupProtectedPR(t, svc, "auto-race-user", "auto-race-repo", true)
	protectBranch(t, svc, pr.RepositoryID, pr.BaseRef, `{"contexts":["ci"]}`, "", true)

	if _, err := svc.SetPRAutoMerge(authCtx, pr.ID, service.SetPRAutoMergeInput{
		Enabled:         true,
		MergeMethod:     "MERGE",
		ExpectedHeadSHA: pr.HeadSHA,
	}); err != nil {
		t.Fatalf("SetPRAutoMerge: %v", err)
	}

	if _, err := svc.Git.WriteFile(authCtx, "auto-race-user/auto-race-repo", "feature", "second.txt", "second commit", []byte("second change\n")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := svc.PushHeadSHA(authCtx, "auto-race-user/auto-race-repo", pr.Number); err != nil {
		t.Fatalf("PushHeadSHA: %v", err)
	}

	updated, err := svc.GetPRByID(authCtx, pr.ID)
	if err != nil {
		t.Fatalf("GetPRByID after push: %v", err)
	}
	status := &db.CommitStatus{
		RepositoryID: pr.RepositoryID,
		CommitSHA:    updated.HeadSHA,
		State:        "success",
		Context:      "ci",
		CreatorID:    user.ID,
	}
	if err := svc.CreateCommitStatus(authCtx, status); err != nil {
		t.Fatalf("CreateCommitStatus: %v", err)
	}

	stored, err := svc.GetPRByID(authCtx, pr.ID)
	if err != nil {
		t.Fatalf("GetPRByID final: %v", err)
	}
	if stored.Merged {
		t.Fatal("expected PR to remain open when expected head sha no longer matches")
	}
	if !stored.AutoMerge {
		t.Fatal("expected auto-merge request to remain queued after head mismatch")
	}
}
