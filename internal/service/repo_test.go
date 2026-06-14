package service_test

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/service"
	"gorm.io/gorm"
)

func TestCreateRepo(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	if err := svc.DB.Create(&db.User{Login: "testuser", Name: "testuser", Type: db.TypeUser}).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}

	in := service.CreateRepoInput{
		OwnerLogin:    "testuser",
		Name:          "testrepo",
		Description:   "A test repo",
		Private:       false,
		DefaultBranch: "main",
	}

	// 1. Create Repository
	repo, err := svc.CreateRepo(ctx, in)
	if err != nil {
		t.Fatalf("failed to create repo: %v", err)
	}

	if repo.Name != "testrepo" {
		t.Errorf("expected repo name 'testrepo', got %s", repo.Name)
	}

	if repo.FullName != "testuser/testrepo" {
		t.Errorf("expected repo fullname 'testuser/testrepo', got %s", repo.FullName)
	}

	if repo.Owner.Login != "testuser" {
		t.Errorf("expected owner login 'testuser', got %s", repo.Owner.Login)
	}

	// 2. Fetch Repository
	fetched, err := svc.GetRepo(ctx, "testuser/testrepo")
	if err != nil {
		t.Fatalf("failed to get repo: %v", err)
	}

	if fetched.ID != repo.ID {
		t.Errorf("expected ID %d, got %d", repo.ID, fetched.ID)
	}

	// 3. Update Repository
	desc := "Updated description"
	archived := true
	allowAutoMerge := true
	deleteBranchOnMerge := true
	allowMergeCommit := false

	updRepo, err := svc.UpdateRepo(ctx, "testuser/testrepo", service.UpdateRepoInput{
		Description:         &desc,
		Archived:            &archived,
		AllowAutoMerge:      &allowAutoMerge,
		DeleteBranchOnMerge: &deleteBranchOnMerge,
		AllowMergeCommit:    &allowMergeCommit,
	})
	if err != nil {
		t.Fatalf("failed to update repo: %v", err)
	}

	if updRepo.Description != desc {
		t.Errorf("expected description %q, got %q", desc, updRepo.Description)
	}
	if !updRepo.Archived {
		t.Errorf("expected repo to be archived")
	}
	if !updRepo.AllowAutoMerge {
		t.Errorf("expected AllowAutoMerge to be true")
	}
	if !updRepo.DeleteBranchOnMerge {
		t.Errorf("expected DeleteBranchOnMerge to be true")
	}
	if updRepo.AllowMergeCommit {
		t.Errorf("expected AllowMergeCommit to be false")
	}

	// 4. Delete Repository
	err = svc.DeleteRepo(ctx, "testuser/testrepo")
	if err != nil {
		t.Fatalf("failed to delete repo: %v", err)
	}

	_, err = svc.GetRepo(ctx, "testuser/testrepo")
	if err == nil {
		t.Fatalf("expected error getting deleted repo, got nil")
	}
}

func TestCreateRepo_PreservesExplicitFalseForDirectCallers(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	if err := svc.DB.Create(&db.User{Login: "directcaller", Name: "directcaller", Type: db.TypeUser}).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin:   "directcaller",
		Name:         "explicit-false",
		HasIssues:    false,
		HasIssuesSet: true,
		HasWiki:      false,
		HasWikiSet:   true,
		AutoInit:     true,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	if repo.HasIssues {
		t.Fatalf("expected HasIssues to remain false")
	}
	if repo.HasWiki {
		t.Fatalf("expected HasWiki to remain false")
	}

	stored, err := svc.GetRepo(ctx, "directcaller/explicit-false")
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}
	if stored.HasIssues {
		t.Fatalf("stored HasIssues = true, want false")
	}
	if stored.HasWiki {
		t.Fatalf("stored HasWiki = true, want false")
	}
}

func TestCreateRepo_AllowsInternalVisibilityForDirectCallers(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	if err := svc.DB.Create(&db.User{Login: "internalcaller", Name: "internalcaller", Type: db.TypeUser}).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "internalcaller",
		Name:       "internal-repo",
		Visibility: "internal",
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	if repo.Visibility != "internal" {
		t.Fatalf("repo.Visibility = %q, want internal", repo.Visibility)
	}
	if !repo.Private {
		t.Fatalf("repo.Private = false, want true for internal visibility")
	}
}

func TestUpdateRepo_PrivateTruePreservesInternalVisibility(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	if err := svc.DB.Create(&db.User{Login: "internalpatch", Name: "internalpatch", Type: db.TypeUser}).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "internalpatch",
		Name:       "internal-patch",
		Visibility: "internal",
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	private := true
	updated, err := svc.UpdateRepo(ctx, repo.FullName, service.UpdateRepoInput{Private: &private})
	if err != nil {
		t.Fatalf("UpdateRepo: %v", err)
	}
	if updated.Visibility != "internal" {
		t.Fatalf("updated.Visibility = %q, want internal", updated.Visibility)
	}
	if !updated.Private {
		t.Fatalf("updated.Private = false, want true")
	}
}

func TestGetRepoByID_UnauthorizedAccess(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()

	owner := db.User{Login: "owner", Name: "owner", Type: db.TypeUser}
	if err := svc.DB.Create(&owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin:    owner.Login,
		Name:          "private-repo",
		Private:       true,
		DefaultBranch: "main",
	})
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}

	outsider := db.User{Login: "outsider", Name: "outsider", Type: db.TypeUser}
	if err := svc.DB.Create(&outsider).Error; err != nil {
		t.Fatalf("create outsider: %v", err)
	}
	outsiderCtx := service.ContextWithUser(ctx, outsider)

	if _, err := svc.GetRepoByID(outsiderCtx, fmt.Sprint(repo.ID)); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for unauthorized access, got %v", err)
	}
}

func TestLookupRepoIdentity_ResolvesRedirectWithoutPermissionCheck(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	owner := createUser(t, svc, "lookup-owner")
	repo := createRepo(t, svc, ctx, owner.Login, "lookup-repo")

	if err := svc.DB.Create(&db.RepoRedirect{
		RepoID:      repo.ID,
		OldFullName: "legacy-owner/legacy-repo",
	}).Error; err != nil {
		t.Fatalf("create repo redirect: %v", err)
	}

	got, err := svc.LookupRepoIdentity(ctx, "legacy-owner/legacy-repo")
	if err != nil {
		t.Fatalf("LookupRepoIdentity: %v", err)
	}
	if got.ID != repo.ID {
		t.Fatalf("expected repo id %d, got %d", repo.ID, got.ID)
	}
	if got.FullName != repo.FullName {
		t.Fatalf("expected full name %q, got %q", repo.FullName, got.FullName)
	}
	if got.DefaultBranch != repo.DefaultBranch {
		t.Fatalf("expected default branch %q, got %q", repo.DefaultBranch, got.DefaultBranch)
	}
}

func TestUpdateRepoTopics_ReplaceAndRemove(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	owner := db.User{Login: "topicuser", Name: "topicuser", Type: db.TypeUser}
	if err := svc.DB.Create(&owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin:    owner.Login,
		Name:          "topicrepo",
		DefaultBranch: "main",
	})
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}

	ownerCtx := service.ContextWithUser(ctx, owner)
	if err := svc.UpdateRepoTopics(ownerCtx, repo.FullName, "go,testing"); err != nil {
		t.Fatalf("UpdateRepoTopics initial: %v", err)
	}

	updated, err := svc.GetRepo(ownerCtx, repo.FullName)
	if err != nil {
		t.Fatalf("GetRepo after update: %v", err)
	}
	if updated.Topics != "go,testing" {
		t.Fatalf("expected topics to be %q, got %q", "go,testing", updated.Topics)
	}

	if err := svc.UpdateRepoTopics(ownerCtx, repo.FullName, "rust"); err != nil {
		t.Fatalf("UpdateRepoTopics replace: %v", err)
	}
	updated, err = svc.GetRepo(ownerCtx, repo.FullName)
	if err != nil {
		t.Fatalf("GetRepo after replace: %v", err)
	}
	if updated.Topics != "rust" {
		t.Fatalf("expected topics to be %q, got %q", "rust", updated.Topics)
	}

	if err := svc.UpdateRepoTopics(ownerCtx, repo.FullName, ""); err != nil {
		t.Fatalf("UpdateRepoTopics remove: %v", err)
	}
	updated, err = svc.GetRepo(ownerCtx, repo.FullName)
	if err != nil {
		t.Fatalf("GetRepo after remove: %v", err)
	}
	if updated.Topics != "" {
		t.Fatalf("expected topics to be cleared, got %q", updated.Topics)
	}
}

func TestDeleteRepoCascade_FullCascade(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	owner := createUser(t, svc, "cascade-owner")
	starrer := createUser(t, svc, "cascade-star")
	other := createUser(t, svc, "cascade-other")
	forkOwner := createUser(t, svc, "cascade-fork")

	repo := createRepo(t, svc, ctx, owner.Login, "repo")
	otherRepo := createRepo(t, svc, ctx, other.Login, "otherrepo")
	forkRepo := createRepo(t, svc, ctx, forkOwner.Login, "forkrepo")

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

	label := db.Label{RepositoryID: repo.ID, Name: "cascade-label", Color: "ffffff"}
	if err := svc.DB.Create(&label).Error; err != nil {
		t.Fatalf("create label: %v", err)
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
	prCross := db.PullRequest{
		RepositoryID:     otherRepo.ID,
		HeadRepositoryID: repo.ID,
		Number:           1,
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

	review := db.PullRequestReview{PullRequestID: pr.ID, AuthorLogin: owner.Login, State: "APPROVED"}
	if err := svc.DB.Create(&review).Error; err != nil {
		t.Fatalf("create review: %v", err)
	}
	if err := svc.DB.Create(&db.ReviewRequest{PullRequestID: pr.ID, Login: "reviewer"}).Error; err != nil {
		t.Fatalf("create review request: %v", err)
	}
	if err := svc.DB.Create(&db.PRReviewComment{PullRequestID: pr.ID, PullRequestReviewID: &review.ID, AuthorLogin: owner.Login, Body: "comment", Path: "file.go", CommitID: "abc123", Line: 1}).Error; err != nil {
		t.Fatalf("create review comment: %v", err)
	}

	crossReview := db.PullRequestReview{PullRequestID: prCross.ID, AuthorLogin: other.Login, State: "COMMENTED"}
	if err := svc.DB.Create(&crossReview).Error; err != nil {
		t.Fatalf("create cross review: %v", err)
	}
	if err := svc.DB.Create(&db.ReviewRequest{PullRequestID: prCross.ID, Login: "reviewer2"}).Error; err != nil {
		t.Fatalf("create cross review request: %v", err)
	}
	if err := svc.DB.Create(&db.PRReviewComment{PullRequestID: prCross.ID, PullRequestReviewID: &crossReview.ID, AuthorLogin: other.Login, Body: "comment", Path: "file.go", CommitID: "abc123", Line: 1}).Error; err != nil {
		t.Fatalf("create cross review comment: %v", err)
	}

	if err := svc.DB.Model(&issue).Association("Labels").Append(&label); err != nil {
		t.Fatalf("associate issue label: %v", err)
	}
	if err := svc.DB.Model(&pr).Association("Labels").Append(&label); err != nil {
		t.Fatalf("associate pr label: %v", err)
	}
	if err := svc.DB.Model(&prCross).Association("Labels").Append(&label); err != nil {
		t.Fatalf("associate cross pr label: %v", err)
	}

	if err := svc.DB.Create(&db.DeployKey{RepositoryID: repo.ID, Title: "key", Key: "ssh-rsa AAA"}).Error; err != nil {
		t.Fatalf("create deploy key: %v", err)
	}
	release := db.Release{RepositoryID: repo.ID, TagName: "v1.0.0", Name: "v1", AuthorID: owner.ID}
	if err := svc.DB.Create(&release).Error; err != nil {
		t.Fatalf("create release: %v", err)
	}
	if err := svc.DB.Create(&db.ReleaseAsset{ReleaseID: release.ID, Name: "asset", ContentType: "text/plain", Size: 1}).Error; err != nil {
		t.Fatalf("create release asset: %v", err)
	}
	if err := svc.DB.Create(&db.Variable{RepositoryID: &repo.ID, Name: "VAR", Value: "1"}).Error; err != nil {
		t.Fatalf("create variable: %v", err)
	}
	if err := svc.DB.Create(&db.Secret{RepositoryID: &repo.ID, Name: "SECRET", Value: "secret"}).Error; err != nil {
		t.Fatalf("create secret: %v", err)
	}
	if err := svc.DB.Create(&db.Ruleset{RepositoryID: repo.ID, Name: "rules", Target: "branch", Enforcement: "active"}).Error; err != nil {
		t.Fatalf("create ruleset: %v", err)
	}
	if err := svc.DB.Create(&db.Autolink{RepositoryFullName: repo.FullName, KeyPrefix: "JIRA-", URLTemplate: "https://example.com/<num>"}).Error; err != nil {
		t.Fatalf("create autolink: %v", err)
	}
	if err := svc.DB.Create(&db.Star{RepositoryID: repo.ID, UserID: starrer.ID}).Error; err != nil {
		t.Fatalf("create star: %v", err)
	}

	const cbName = "test:cascade_order"
	if err := svc.DB.Callback().Delete().Before("gorm:delete").Register(cbName, func(tx *gorm.DB) {
		if tx.Statement.Table == "pull_request_reviews" || (tx.Statement.Schema != nil && tx.Statement.Schema.Table == "pull_request_reviews") {
			var count int64
			checkDB := tx.Session(&gorm.Session{NewDB: true})
			if err := checkDB.Model(&db.PRReviewComment{}).
				Where("pull_request_id IN (?)", checkDB.Model(&db.PullRequest{}).Select("id").Where("repository_id = ? OR head_repository_id = ?", repo.ID, repo.ID)).
				Count(&count).Error; err != nil {
				tx.AddError(err)
				return
			}
			if count != 0 {
				tx.AddError(fmt.Errorf("expected PR review comments deleted before reviews, found %d", count))
			}
		}
	}); err != nil {
		t.Fatalf("register order callback: %v", err)
	}
	defer func() {
		_ = svc.DB.Callback().Delete().Remove(cbName)
	}()

	ownerCtx := service.ContextWithUser(ctx, owner)
	if err := svc.DeleteRepo(ownerCtx, repo.FullName); err != nil {
		t.Fatalf("DeleteRepo: %v", err)
	}

	if _, err := svc.GetRepo(ctx, repo.FullName); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("expected repo to be deleted, got %v", err)
	}
	if _, err := svc.GetRepo(ctx, otherRepo.FullName); err != nil {
		t.Fatalf("expected other repo to remain, got %v", err)
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

	assertCount(t, svc, &db.PRReviewComment{}, 0, "pull_request_id IN (?, ?)", pr.ID, prCross.ID)
	assertCount(t, svc, &db.PullRequestReview{}, 0, "pull_request_id IN (?, ?)", pr.ID, prCross.ID)
	assertCount(t, svc, &db.ReviewRequest{}, 0, "pull_request_id IN (?, ?)", pr.ID, prCross.ID)
	assertCount(t, svc, &db.PullRequest{}, 0, "id IN (?, ?)", pr.ID, prCross.ID)
	assertCount(t, svc, &db.LinkedBranch{}, 0, "repository_id = ?", repo.ID)

	assertCount(t, svc, &db.Milestone{}, 0, "repository_id = ?", repo.ID)
	assertCount(t, svc, &db.IssueComment{}, 0, "repository_id = ?", repo.ID)
	assertCount(t, svc, &db.IssueEvent{}, 0, "issue_id = ?", issue.ID)
	assertCount(t, svc, &db.Issue{}, 0, "repository_id = ?", repo.ID)

	assertCount(t, svc, &db.Label{}, 0, "repository_id = ?", repo.ID)
	assertCount(t, svc, &db.DeployKey{}, 0, "repository_id = ?", repo.ID)
	assertCount(t, svc, &db.ReleaseAsset{}, 0, "release_id = ?", release.ID)
	assertCount(t, svc, &db.Release{}, 0, "repository_id = ?", repo.ID)
	assertCount(t, svc, &db.Variable{}, 0, "repository_id = ?", repo.ID)
	assertCount(t, svc, &db.Secret{}, 0, "repository_id = ?", repo.ID)
	assertCount(t, svc, &db.Ruleset{}, 0, "repository_id = ?", repo.ID)
	assertCount(t, svc, &db.Autolink{}, 0, "repository_full_name = ?", repo.FullName)
	assertCount(t, svc, &db.Star{}, 0, "repository_id = ?", repo.ID)

	var joinCount int64
	if err := svc.DB.Table("pr_labels").Where("pull_request_id IN (?, ?)", pr.ID, prCross.ID).Count(&joinCount).Error; err != nil {
		t.Fatalf("count pr_labels: %v", err)
	}
	if joinCount != 0 {
		t.Fatalf("expected pr_labels to be empty, got %d", joinCount)
	}
	if err := svc.DB.Table("issue_labels").Where("issue_id = ?", issue.ID).Count(&joinCount).Error; err != nil {
		t.Fatalf("count issue_labels: %v", err)
	}
	if joinCount != 0 {
		t.Fatalf("expected issue_labels to be empty, got %d", joinCount)
	}
}

func TestDeleteRepoCascade_RollbackOnError(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	owner := createUser(t, svc, "rollback-owner")

	repo := createRepo(t, svc, ctx, owner.Login, "rollback-repo")

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

	label := db.Label{RepositoryID: repo.ID, Name: "rollback-label", Color: "ffffff"}
	if err := svc.DB.Create(&label).Error; err != nil {
		t.Fatalf("create label: %v", err)
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
	review := db.PullRequestReview{PullRequestID: pr.ID, AuthorLogin: owner.Login, State: "APPROVED"}
	if err := svc.DB.Create(&review).Error; err != nil {
		t.Fatalf("create review: %v", err)
	}
	if err := svc.DB.Create(&db.PRReviewComment{PullRequestID: pr.ID, PullRequestReviewID: &review.ID, AuthorLogin: owner.Login, Body: "comment", Path: "file.go", CommitID: "abc123", Line: 1}).Error; err != nil {
		t.Fatalf("create review comment: %v", err)
	}

	var expectedLabelCount int64
	if err := svc.DB.Model(&db.Label{}).Where("repository_id = ?", repo.ID).Count(&expectedLabelCount).Error; err != nil {
		t.Fatalf("count labels before delete: %v", err)
	}

	const cbName = "test:cascade_fail_label"
	var failOnce atomic.Bool
	failOnce.Store(true)
	if err := svc.DB.Callback().Delete().Before("gorm:delete").Register(cbName, func(tx *gorm.DB) {
		if (tx.Statement.Table == "labels" || (tx.Statement.Schema != nil && tx.Statement.Schema.Table == "labels")) && failOnce.CompareAndSwap(true, false) {
			tx.AddError(errors.New("forced label delete failure"))
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
	assertCount(t, svc, &db.Label{}, expectedLabelCount, "repository_id = ?", repo.ID)
	assertCount(t, svc, &db.PRReviewComment{}, 1, "pull_request_id = ?", pr.ID)
}

func createUser(t *testing.T, svc *service.Service, login string) db.User {
	t.Helper()
	user := db.User{Login: login, Name: login, Type: db.TypeUser}
	if err := svc.DB.Create(&user).Error; err != nil {
		t.Fatalf("create user %s: %v", login, err)
	}
	return user
}

func createRepo(t *testing.T, svc *service.Service, ctx context.Context, ownerLogin, name string) db.Repository {
	t.Helper()
	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin:    ownerLogin,
		Name:          name,
		DefaultBranch: "main",
		AutoInit:      true,
	})
	if err != nil {
		t.Fatalf("create repo %s/%s: %v", ownerLogin, name, err)
	}
	return repo
}

func assertCount(t *testing.T, svc *service.Service, model any, expected int64, where string, args ...any) {
	t.Helper()
	var count int64
	if err := svc.DB.Model(model).Where(where, args...).Count(&count).Error; err != nil {
		t.Fatalf("count %T: %v", model, err)
	}
	if count != expected {
		t.Fatalf("expected %d %T rows, got %d", expected, model, count)
	}
}
