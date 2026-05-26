package service_test

import (
	"context"
	"testing"

	"github.com/ngaut/agent-git-service/internal/db"
)

// TestDeploymentCRUD tests deployment and deployment status CRUD operations.
func TestDeploymentCRUD(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	// Setup user and repo
	svc.DB.Create(&db.User{Login: "deployuser", Type: db.TypeUser})
	var u db.User
	svc.DB.First(&u, "login = ?", "deployuser")

	repoName := "test-repo"
	fullName := u.Login + "/" + repoName
	svc.DB.Create(&db.Repository{
		OwnerID:       u.ID,
		Owner:         u,
		Name:          repoName,
		FullName:      fullName,
		DefaultBranch: "main",
	})
	var repo db.Repository
	svc.DB.First(&repo, "full_name = ?", fullName)

	// Create deployment
	dep := &db.Deployment{
		RepositoryID: repo.ID,
		Ref:          "main",
		Task:         "deploy",
		Environment:  "production",
		Description:  "Test deployment",
		PayloadJSON:  `{"version": "1.0.0"}`,
		CreatorID:    u.ID,
	}
	err := svc.CreateDeployment(ctx, dep)
	if err != nil {
		t.Fatalf("CreateDeployment failed: %v", err)
	}

	// Get deployment
	got, err := svc.GetDeployment(ctx, repo.ID, dep.ID)
	if err != nil {
		t.Fatalf("GetDeployment failed: %v", err)
	}
	if got.Ref != "main" {
		t.Errorf("expected ref 'main', got %s", got.Ref)
	}

	// List deployments
	deps, err := svc.ListDeployments(ctx, repo.ID)
	if err != nil {
		t.Fatalf("ListDeployments failed: %v", err)
	}
	if len(deps) != 1 {
		t.Errorf("expected 1 deployment, got %d", len(deps))
	}

	// Create deployment status
	status := &db.DeploymentStatus{
		DeploymentID:   dep.ID,
		State:          "success",
		Description:    "Deployment successful",
		EnvironmentURL: "https://example.com",
		LogURL:         "https://example.com/logs",
		CreatorID:      u.ID,
	}
	err = svc.CreateDeploymentStatus(ctx, status)
	if err != nil {
		t.Fatalf("CreateDeploymentStatus failed: %v", err)
	}

	// Get deployment status
	gotStatus, err := svc.GetDeploymentStatus(ctx, repo.ID, dep.ID, status.ID)
	if err != nil {
		t.Fatalf("GetDeploymentStatus failed: %v", err)
	}
	if gotStatus.State != "success" {
		t.Errorf("expected state 'success', got %s", gotStatus.State)
	}

	// List deployment statuses
	statuses, err := svc.ListDeploymentStatuses(ctx, repo.ID, dep.ID)
	if err != nil {
		t.Fatalf("ListDeploymentStatuses failed: %v", err)
	}
	if len(statuses) != 1 {
		t.Errorf("expected 1 status, got %d", len(statuses))
	}

	// Delete deployment
	err = svc.DeleteDeployment(ctx, repo.ID, dep.ID)
	if err != nil {
		t.Fatalf("DeleteDeployment failed: %v", err)
	}

	// Verify deletion
	_, err = svc.GetDeployment(ctx, repo.ID, dep.ID)
	if err == nil {
		t.Error("expected error when getting deleted deployment")
	}
}

// TestWebhookCRUD tests webhook CRUD operations.
func TestWebhookCRUD(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	// Setup user and repo
	svc.DB.Create(&db.User{Login: "webhookuser", Type: db.TypeUser})
	var u db.User
	svc.DB.First(&u, "login = ?", "webhookuser")

	repoName := "test-repo"
	fullName := u.Login + "/" + repoName
	svc.DB.Create(&db.Repository{
		OwnerID:       u.ID,
		Owner:         u,
		Name:          repoName,
		FullName:      fullName,
		DefaultBranch: "main",
	})
	var repo db.Repository
	svc.DB.First(&repo, "full_name = ?", fullName)

	// Create webhook
	webhook := &db.Webhook{
		RepositoryID: repo.ID,
		Name:         "web",
		Active:       true,
		EventsJSON:   `["push", "pull_request"]`,
		ConfigJSON:   `{"url": "https://example.com/webhook", "content_type": "json"}`,
	}
	err := svc.DB.Create(webhook).Error
	if err != nil {
		t.Fatalf("Create webhook failed: %v", err)
	}

	// Get webhook
	var got db.Webhook
	err = svc.DB.First(&got, webhook.ID).Error
	if err != nil {
		t.Fatalf("GetWebhook failed: %v", err)
	}
	if got.Name != "web" {
		t.Errorf("expected name 'web', got %s", got.Name)
	}

	// Update webhook
	got.Active = false
	err = svc.DB.Save(&got).Error
	if err != nil {
		t.Fatalf("UpdateWebhook failed: %v", err)
	}

	// List webhooks
	var webhooks []db.Webhook
	err = svc.DB.Where("repository_id = ?", repo.ID).Find(&webhooks).Error
	if err != nil {
		t.Fatalf("ListWebhooks failed: %v", err)
	}
	if len(webhooks) != 1 {
		t.Errorf("expected 1 webhook, got %d", len(webhooks))
	}

	// Delete webhook
	err = svc.DB.Delete(&db.Webhook{}, webhook.ID).Error
	if err != nil {
		t.Fatalf("DeleteWebhook failed: %v", err)
	}

	// Verify deletion
	err = svc.DB.First(&db.Webhook{}, webhook.ID).Error
	if err == nil {
		t.Error("expected error when getting deleted webhook")
	}
}

// TestStarCRUD tests star operations.
func TestStarCRUD(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	// Setup users and repo
	svc.DB.Create(&db.User{Login: "staruser", Type: db.TypeUser})
	svc.DB.Create(&db.User{Login: "repoowner", Type: db.TypeUser})
	var starUser, repoOwner db.User
	svc.DB.First(&starUser, "login = ?", "staruser")
	svc.DB.First(&repoOwner, "login = ?", "repoowner")

	repoName := "test-repo"
	fullName := repoOwner.Login + "/" + repoName
	svc.DB.Create(&db.Repository{
		OwnerID:       repoOwner.ID,
		Owner:         repoOwner,
		Name:          repoName,
		FullName:      fullName,
		DefaultBranch: "main",
	})

	// Star repo
	err := svc.StarRepo(ctx, fullName, starUser.Login)
	if err != nil {
		t.Fatalf("StarRepo failed: %v", err)
	}

	// IsStarred should be true
	isStarred, err := svc.IsStarred(ctx, starUser.ID, fullName)
	if err != nil {
		t.Fatalf("IsStarred failed: %v", err)
	}
	if !isStarred {
		t.Error("expected repo to be starred")
	}

	// Get repo for ID
	var repo db.Repository
	svc.DB.First(&repo, "full_name = ?", fullName)

	// Star count should be 1
	count := svc.StarCount(ctx, repo.ID)
	if count != 1 {
		t.Errorf("expected star count 1, got %d", count)
	}

	// List starred repos
	starred, err := svc.ListStarred(ctx, starUser.ID)
	if err != nil {
		t.Fatalf("ListStarred failed: %v", err)
	}
	if len(starred) != 1 {
		t.Errorf("expected 1 starred repo, got %d", len(starred))
	}

	// Unstar repo
	err = svc.UnstarRepo(ctx, fullName, starUser.Login)
	if err != nil {
		t.Fatalf("UnstarRepo failed: %v", err)
	}

	// IsStarred should be false
	isStarred, err = svc.IsStarred(ctx, starUser.ID, fullName)
	if err != nil {
		t.Fatalf("IsStarred failed: %v", err)
	}
	if isStarred {
		t.Error("expected repo to not be starred after unstar")
	}

	// Star count should be 0
	count = svc.StarCount(ctx, repo.ID)
	if count != 0 {
		t.Errorf("expected star count 0, got %d", count)
	}
}

// TestCreateReaction tests reaction creation.
func TestCreateReaction(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	// Setup user and issue
	svc.DB.Create(&db.User{Login: "reactionuser", Type: db.TypeUser})
	var u db.User
	svc.DB.First(&u, "login = ?", "reactionuser")

	svc.DB.Create(&db.User{Login: "issueowner", Type: db.TypeUser})
	var owner db.User
	svc.DB.First(&owner, "login = ?", "issueowner")

	repoName := "test-repo"
	fullName := owner.Login + "/" + repoName
	svc.DB.Create(&db.Repository{
		OwnerID:       owner.ID,
		Owner:         owner,
		Name:          repoName,
		FullName:      fullName,
		DefaultBranch: "main",
	})
	var repo db.Repository
	svc.DB.First(&repo, "full_name = ?", fullName)

	issue := &db.Issue{
		RepositoryID: repo.ID,
		Number:       1,
		Title:        "Test Issue",
		Body:         "Test body",
		AuthorID:     owner.ID,
	}
	svc.DB.Create(issue)

	// Create reaction
	created, err := svc.CreateReaction(ctx, &issue.ID, nil, u.ID, "+1")
	if err != nil {
		t.Fatalf("CreateReaction failed: %v", err)
	}

	// Verify reaction was saved
	var got db.Reaction
	err = svc.DB.First(&got, created.ID).Error
	if err != nil {
		t.Fatalf("Get reaction failed: %v", err)
	}
	if got.Content != "+1" {
		t.Errorf("expected content '+1', got %s", got.Content)
	}
	if got.UserID != u.ID {
		t.Errorf("expected userID %d, got %d", u.ID, got.UserID)
	}
}

// TestRulesetCRUD tests ruleset CRUD operations.
func TestRulesetCRUD(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	// Setup user and repo
	svc.DB.Create(&db.User{Login: "rulesetuser", Type: db.TypeUser})
	var u db.User
	svc.DB.First(&u, "login = ?", "rulesetuser")

	repoName := "test-repo"
	fullName := u.Login + "/" + repoName
	svc.DB.Create(&db.Repository{
		OwnerID:       u.ID,
		Owner:         u,
		Name:          repoName,
		FullName:      fullName,
		DefaultBranch: "main",
	})
	var repo db.Repository
	svc.DB.First(&repo, "full_name = ?", fullName)

	// Create ruleset
	ruleset := &db.Ruleset{
		RepositoryID:   repo.ID,
		Name:           "test-ruleset",
		Target:         "branch",
		Enforcement:    "active",
		ConditionsJSON: `{"ref_name": {"include": ["main"]}}`,
		RulesJSON:      `[{"type": "required_status_checks"}]`,
	}
	err := svc.DB.Create(ruleset).Error
	if err != nil {
		t.Fatalf("CreateRuleset failed: %v", err)
	}

	// Get ruleset
	var got db.Ruleset
	err = svc.DB.First(&got, ruleset.ID).Error
	if err != nil {
		t.Fatalf("GetRuleset failed: %v", err)
	}
	if got.Name != "test-ruleset" {
		t.Errorf("expected name 'test-ruleset', got %s", got.Name)
	}

	// List rulesets
	rulesets, err := svc.ListRulesets(ctx, repo.FullName)
	if err != nil {
		t.Fatalf("ListRulesets failed: %v", err)
	}
	if len(rulesets) != 1 {
		t.Errorf("expected 1 ruleset, got %d", len(rulesets))
	}

	// List branch rulesets
	branchRulesets, err := svc.ListBranchRulesets(ctx, repo.FullName)
	if err != nil {
		t.Fatalf("ListBranchRulesets failed: %v", err)
	}
	if len(branchRulesets) != 1 {
		t.Errorf("expected 1 branch ruleset, got %d", len(branchRulesets))
	}

	// Delete ruleset
	err = svc.DB.Delete(&db.Ruleset{}, ruleset.ID).Error
	if err != nil {
		t.Fatalf("DeleteRuleset failed: %v", err)
	}

	// Verify deletion
	err = svc.DB.First(&db.Ruleset{}, ruleset.ID).Error
	if err == nil {
		t.Error("expected error when getting deleted ruleset")
	}
}

// TestBranchProtectionCRUD tests branch protection CRUD operations.
func TestBranchProtectionCRUD(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	// Setup user and repo
	svc.DB.Create(&db.User{Login: "branchuser", Type: db.TypeUser})
	var u db.User
	svc.DB.First(&u, "login = ?", "branchuser")

	repoName := "test-repo"
	fullName := u.Login + "/" + repoName
	svc.DB.Create(&db.Repository{
		OwnerID:       u.ID,
		Owner:         u,
		Name:          repoName,
		FullName:      fullName,
		DefaultBranch: "main",
	})
	var repo db.Repository
	svc.DB.First(&repo, "full_name = ?", fullName)

	// Update branch protection
	protection := &db.BranchProtection{
		RepositoryID:             repo.ID,
		BranchName:               "main",
		RequiredStatusChecksJSON: `{"contexts": ["ci"]}`,
		EnforceAdmins:            true,
		RequiredPullRequestJSON:  `{"required_approving_review_count": 1}`,
		RestrictionsJSON:         `{}`,
	}
	err := svc.UpdateBranchProtection(ctx, protection)
	if err != nil {
		t.Fatalf("UpdateBranchProtection failed: %v", err)
	}

	// Get branch protection
	got, err := svc.GetBranchProtection(ctx, repo.ID, "main")
	if err != nil {
		t.Fatalf("GetBranchProtection failed: %v", err)
	}
	if got.BranchName != "main" {
		t.Errorf("expected branch name 'main', got %s", got.BranchName)
	}
	if !got.EnforceAdmins {
		t.Error("expected EnforceAdmins to be true")
	}
	initialID := got.ID
	initialCreatedAt := got.CreatedAt

	// Update the existing row and ensure it does not create a new record.
	protection.RequiredPullRequestJSON = `{"required_approving_review_count": 2}`
	err = svc.UpdateBranchProtection(ctx, protection)
	if err != nil {
		t.Fatalf("UpdateBranchProtection second update failed: %v", err)
	}
	got, err = svc.GetBranchProtection(ctx, repo.ID, "main")
	if err != nil {
		t.Fatalf("GetBranchProtection after second update failed: %v", err)
	}
	if got.ID != initialID {
		t.Errorf("expected branch protection ID to stay %d, got %d", initialID, got.ID)
	}
	if !got.CreatedAt.Equal(initialCreatedAt) {
		t.Errorf("expected created_at to remain %v, got %v", initialCreatedAt, got.CreatedAt)
	}
	if got.RequiredPullRequestJSON != `{"required_approving_review_count": 2}` {
		t.Errorf("expected updated required pull request JSON, got %s", got.RequiredPullRequestJSON)
	}

	// Delete branch protection
	err = svc.DeleteBranchProtection(ctx, repo.ID, "main")
	if err != nil {
		t.Fatalf("DeleteBranchProtection failed: %v", err)
	}

	// Verify deletion
	_, err = svc.GetBranchProtection(ctx, repo.ID, "main")
	if err == nil {
		t.Error("expected error when getting deleted branch protection")
	}
}

// TestCommitStatusCRUD tests commit status CRUD operations.
func TestCommitStatusCRUD(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	// Setup user and repo
	svc.DB.Create(&db.User{Login: "statususer", Type: db.TypeUser})
	var u db.User
	svc.DB.First(&u, "login = ?", "statususer")

	repoName := "test-repo"
	fullName := u.Login + "/" + repoName
	svc.DB.Create(&db.Repository{
		OwnerID:       u.ID,
		Owner:         u,
		Name:          repoName,
		FullName:      fullName,
		DefaultBranch: "main",
	})
	var repo db.Repository
	svc.DB.First(&repo, "full_name = ?", fullName)

	commitSHA := "abc123def456789012345678901234567890abcd"

	// Create commit status
	status := &db.CommitStatus{
		RepositoryID: repo.ID,
		CommitSHA:    commitSHA,
		State:        "success",
		TargetURL:    "https://ci.example.com/build/123",
		Description:  "Build passed",
		Context:      "ci/build",
		CreatorID:    u.ID,
	}
	err := svc.CreateCommitStatus(ctx, status)
	if err != nil {
		t.Fatalf("CreateCommitStatus failed: %v", err)
	}

	// List commit statuses
	statuses, err := svc.ListCommitStatuses(ctx, repo.ID, commitSHA)
	if err != nil {
		t.Fatalf("ListCommitStatuses failed: %v", err)
	}
	if len(statuses) != 1 {
		t.Errorf("expected 1 commit status, got %d", len(statuses))
	}
	if statuses[0].State != "success" {
		t.Errorf("expected state 'success', got %s", statuses[0].State)
	}
}

// TestStarRepoNotFound tests error handling for non-existent repo.
func TestStarRepoNotFound(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	svc.DB.Create(&db.User{Login: "nostaruser", Type: db.TypeUser})
	var u db.User
	svc.DB.First(&u, "login = ?", "nostaruser")

	repo := db.Repository{FullName: "missing/repo"}
	err := svc.StarRepo(ctx, repo.FullName, u.Login)
	if err == nil {
		t.Error("expected error when starring non-existent repo")
	}
}

// TestUnstarRepoNotFound tests error handling for non-existent star.
func TestUnstarRepoNotFound(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	svc.DB.Create(&db.User{Login: "nounstaruser", Type: db.TypeUser})
	var u db.User
	svc.DB.First(&u, "login = ?", "nounstaruser")

	repoName := "test-repo"
	fullName := u.Login + "/" + repoName
	svc.DB.Create(&db.Repository{
		OwnerID:       u.ID,
		Owner:         u,
		Name:          repoName,
		FullName:      fullName,
		DefaultBranch: "main",
	})
	var repo db.Repository
	svc.DB.First(&repo, "full_name = ?", fullName)

	err := svc.UnstarRepo(ctx, repo.FullName, u.Login)
	if err != nil {
		t.Fatalf("UnstarRepo should not fail for non-existent star: %v", err)
	}
}

// TestGetBranchProtectionNotFound tests error handling for non-existent branch protection.
func TestGetBranchProtectionNotFound(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	_, err := svc.GetBranchProtection(ctx, 99999, "main")
	if err == nil {
		t.Error("expected error for non-existent branch protection")
	}
}
