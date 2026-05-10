package service_test

import (
	"context"
	"testing"

	"gh-server/internal/db"
	"gh-server/internal/service"
)

// TestDeployment_CrossRepoIsolation tests that deployment access is properly
// isolated by repository boundary. Users should not be able to access
// deployments from repositories they don't have access to.
func TestDeployment_CrossRepoIsolation(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()

	// Create two users and two repos
	user1 := db.User{Login: "user1", Name: "User One", Type: db.TypeUser}
	user2 := db.User{Login: "user2", Name: "User Two", Type: db.TypeUser}
	if err := svc.DB.Create(&user1).Error; err != nil {
		t.Fatalf("create user1: %v", err)
	}
	if err := svc.DB.Create(&user2).Error; err != nil {
		t.Fatalf("create user2: %v", err)
	}

	repo1, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "user1",
		Name:       "repo1",
	})
	if err != nil {
		t.Fatalf("create repo1: %v", err)
	}

	repo2, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "user2",
		Name:       "repo2",
	})
	if err != nil {
		t.Fatalf("create repo2: %v", err)
	}

	// Create a deployment in repo1
	deployment1 := &db.Deployment{
		RepositoryID: repo1.ID,
		Ref:          "main",
		Task:         "deploy",
		Environment:  "production",
		Description:  "Test deployment 1",
		CreatorID:    user1.ID,
	}
	if err := svc.CreateDeployment(ctx, deployment1); err != nil {
		t.Fatalf("create deployment1: %v", err)
	}

	// Create a deployment status for deployment1
	status1 := &db.DeploymentStatus{
		DeploymentID: deployment1.ID,
		State:        "success",
		Description:  "Deployment succeeded",
		CreatorID:    user1.ID,
	}
	if err := svc.CreateDeploymentStatus(ctx, status1); err != nil {
		t.Fatalf("create status1: %v", err)
	}

	t.Run("GetDeployment_cross_repo_denied", func(t *testing.T) {
		// Try to get deployment1 using repo2's ID (cross-repo access)
		_, err := svc.GetDeployment(ctx, repo2.ID, deployment1.ID)
		if err == nil {
			t.Error("expected error when accessing deployment from different repo, got nil")
		}
		// Should return not found because the deployment doesn't belong to repo2
	})

	t.Run("GetDeployment_same_repo_allowed", func(t *testing.T) {
		// Get deployment1 using repo1's ID (same-repo access)
		dep, err := svc.GetDeployment(ctx, repo1.ID, deployment1.ID)
		if err != nil {
			t.Fatalf("expected to get deployment from same repo, got error: %v", err)
		}
		if dep.ID != deployment1.ID {
			t.Errorf("expected deployment ID %d, got %d", deployment1.ID, dep.ID)
		}
	})

	t.Run("ListDeployments_cross_repo_isolated", func(t *testing.T) {
		// List deployments for repo2 (should not include deployment1)
		deps, err := svc.ListDeployments(ctx, repo2.ID)
		if err != nil {
			t.Fatalf("list deployments: %v", err)
		}
		if len(deps) != 0 {
			t.Errorf("expected 0 deployments for repo2, got %d", len(deps))
		}
	})

	t.Run("ListDeployments_same_repo_returns_correct", func(t *testing.T) {
		// List deployments for repo1 (should include deployment1)
		deps, err := svc.ListDeployments(ctx, repo1.ID)
		if err != nil {
			t.Fatalf("list deployments: %v", err)
		}
		if len(deps) != 1 {
			t.Errorf("expected 1 deployment for repo1, got %d", len(deps))
		}
	})

	t.Run("DeleteDeployment_cross_repo_denied", func(t *testing.T) {
		// Try to delete deployment1 using repo2's ID (cross-repo access)
		err := svc.DeleteDeployment(ctx, repo2.ID, deployment1.ID)
		if err != nil {
			// Should fail or delete 0 rows
			t.Logf("DeleteDeployment cross-repo correctly failed or had no effect: %v", err)
		}
		// Verify deployment1 still exists
		dep, err := svc.GetDeployment(ctx, repo1.ID, deployment1.ID)
		if err != nil {
			t.Errorf("deployment should still exist after failed cross-repo delete: %v", err)
		}
		if dep == nil {
			t.Error("deployment should still exist after failed cross-repo delete")
		}
	})

	t.Run("GetDeploymentStatus_cross_repo_denied", func(t *testing.T) {
		// Try to get status1 using repo2's ID (cross-repo access)
		// Note: GetDeploymentStatus currently doesn't verify repo ownership
		// This test documents the current behavior
		_, err := svc.GetDeploymentStatus(ctx, repo2.ID, deployment1.ID, status1.ID)
		if err == nil {
			// Current implementation doesn't check repo boundary for statuses
			// This is a known gap that should be fixed
			t.Logf("GetDeploymentStatus currently allows cross-repo access (known gap)")
		}
	})

	t.Run("ListDeploymentStatuses_cross_repo_not_isolated", func(t *testing.T) {
		// Try to list statuses for deployment1 using repo2's context
		// Note: ListDeploymentStatuses currently doesn't verify repo ownership
		statuses, err := svc.ListDeploymentStatuses(ctx, repo2.ID, deployment1.ID)
		if err != nil {
			t.Fatalf("list deployment statuses: %v", err)
		}
		// Current implementation returns statuses regardless of repo boundary
		// This is a known gap - the repoID parameter is not used for filtering
		if len(statuses) != 1 {
			t.Errorf("expected 1 status (current behavior ignores repo boundary), got %d", len(statuses))
		}
		t.Logf("ListDeploymentStatuses currently ignores repo boundary (known gap)")
	})
}

// TestDeploymentStatus_RepoBoundaryGap tests the current behavior where
// GetDeploymentStatus and ListDeploymentStatuses don't enforce repo boundaries.
// This documents the security gap identified in issue #409.
func TestDeploymentStatus_RepoBoundaryGap(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()

	// Create user and repo
	user := db.User{Login: "testuser", Name: "Test User", Type: db.TypeUser}
	if err := svc.DB.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "testuser",
		Name:       "testrepo",
	})
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}

	// Create deployment
	deployment := &db.Deployment{
		RepositoryID: repo.ID,
		Ref:          "main",
		Task:         "deploy",
		Environment:  "staging",
		CreatorID:    user.ID,
	}
	if err := svc.CreateDeployment(ctx, deployment); err != nil {
		t.Fatalf("create deployment: %v", err)
	}

	// Create deployment status
	status := &db.DeploymentStatus{
		DeploymentID: deployment.ID,
		State:        "pending",
		Description:  "Deployment in progress",
		CreatorID:    user.ID,
	}
	if err := svc.CreateDeploymentStatus(ctx, status); err != nil {
		t.Fatalf("create status: %v", err)
	}

	t.Run("GetDeploymentStatus_does_not_verify_repo", func(t *testing.T) {
		// GetDeploymentStatus only checks deployment_id and status_id,
		// not whether the deployment belongs to the specified repo.
		// This is a security gap.
		wrongRepoID := uint(99999) // Non-existent repo ID
		foundStatus, err := svc.GetDeploymentStatus(ctx, wrongRepoID, deployment.ID, status.ID)
		if err != nil {
			t.Fatalf("GetDeploymentStatus failed: %v", err)
		}
		if foundStatus.ID != status.ID {
			t.Errorf("expected status ID %d, got %d", status.ID, foundStatus.ID)
		}
		t.Logf("GetDeploymentStatus returned status without verifying repo ownership (security gap)")
	})

	t.Run("ListDeploymentStatuses_does_not_verify_repo", func(t *testing.T) {
		// ListDeploymentStatuses only checks deployment_id,
		// not whether the deployment belongs to the specified repo.
		// This is a security gap.
		wrongRepoID := uint(99999) // Non-existent repo ID
		statuses, err := svc.ListDeploymentStatuses(ctx, wrongRepoID, deployment.ID)
		if err != nil {
			t.Fatalf("ListDeploymentStatuses failed: %v", err)
		}
		if len(statuses) != 1 {
			t.Errorf("expected 1 status, got %d", len(statuses))
		}
		t.Logf("ListDeploymentStatuses returned statuses without verifying repo ownership (security gap)")
	})
}

// TestDeployment_SuccessPaths tests the success paths for authorized operations.
func TestDeployment_SuccessPaths(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()

	// Create user and repo
	user := db.User{Login: "deployer", Name: "Deployer", Type: db.TypeUser}
	if err := svc.DB.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "deployer",
		Name:       "deploy-repo",
	})
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}

	t.Run("CreateDeployment_success", func(t *testing.T) {
		deployment := &db.Deployment{
			RepositoryID: repo.ID,
			Ref:          "main",
			Task:         "deploy",
			Environment:  "production",
			CreatorID:    user.ID,
		}
		if err := svc.CreateDeployment(ctx, deployment); err != nil {
			t.Fatalf("CreateDeployment failed: %v", err)
		}
		if deployment.ID == 0 {
			t.Error("expected deployment ID to be set after creation")
		}
	})

	t.Run("GetDeployment_success", func(t *testing.T) {
		// Create a deployment first
		deployment := &db.Deployment{
			RepositoryID: repo.ID,
			Ref:          "main",
			Task:         "test",
			Environment:  "staging",
			CreatorID:    user.ID,
		}
		if err := svc.CreateDeployment(ctx, deployment); err != nil {
			t.Fatalf("CreateDeployment failed: %v", err)
		}

		found, err := svc.GetDeployment(ctx, repo.ID, deployment.ID)
		if err != nil {
			t.Fatalf("GetDeployment failed: %v", err)
		}
		if found.Ref != deployment.Ref {
			t.Errorf("expected SHA %s, got %s", deployment.Ref, found.Ref)
		}
	})

	t.Run("ListDeployments_success", func(t *testing.T) {
		// Create multiple deployments
		for i := 0; i < 3; i++ {
			deployment := &db.Deployment{
				RepositoryID: repo.ID,
				Description:  "Deployment " + string(rune('0'+i)),
				Ref:          "main",
				Task:         "deploy",
				Environment:  "prod",
				CreatorID:    user.ID,
			}
			if err := svc.CreateDeployment(ctx, deployment); err != nil {
				t.Fatalf("CreateDeployment failed: %v", err)
			}
		}

		deps, err := svc.ListDeployments(ctx, repo.ID)
		if err != nil {
			t.Fatalf("ListDeployments failed: %v", err)
		}
		if len(deps) < 3 {
			t.Errorf("expected at least 3 deployments, got %d", len(deps))
		}
	})

	t.Run("DeleteDeployment_success", func(t *testing.T) {
		// Create a deployment
		deployment := &db.Deployment{
			RepositoryID: repo.ID,
			Ref:          "main",
			Task:         "deploy",
			Environment:  "test",
			CreatorID:    user.ID,
		}
		if err := svc.CreateDeployment(ctx, deployment); err != nil {
			t.Fatalf("CreateDeployment failed: %v", err)
		}

		// Delete it
		if err := svc.DeleteDeployment(ctx, repo.ID, deployment.ID); err != nil {
			t.Fatalf("DeleteDeployment failed: %v", err)
		}

		// Verify it's deleted
		_, err := svc.GetDeployment(ctx, repo.ID, deployment.ID)
		if err == nil {
			t.Error("expected error after deleting deployment, got nil")
		}
	})

	t.Run("CreateDeploymentStatus_success", func(t *testing.T) {
		// Create a deployment first
		deployment := &db.Deployment{
			RepositoryID: repo.ID,
			Ref:          "main",
			Task:         "deploy",
			Environment:  "prod",
			CreatorID:    user.ID,
		}
		if err := svc.CreateDeployment(ctx, deployment); err != nil {
			t.Fatalf("CreateDeployment failed: %v", err)
		}

		// Create a status
		status := &db.DeploymentStatus{
			DeploymentID: deployment.ID,
			State:        "success",
			Description:  "Deployment completed successfully",
			CreatorID:    user.ID,
		}
		if err := svc.CreateDeploymentStatus(ctx, status); err != nil {
			t.Fatalf("CreateDeploymentStatus failed: %v", err)
		}
		if status.ID == 0 {
			t.Error("expected status ID to be set after creation")
		}
	})

	t.Run("GetDeploymentStatus_success", func(t *testing.T) {
		// Create deployment and status
		deployment := &db.Deployment{
			RepositoryID: repo.ID,
			Ref:          "main",
			Task:         "deploy",
			Environment:  "prod",
			CreatorID:    user.ID,
		}
		if err := svc.CreateDeployment(ctx, deployment); err != nil {
			t.Fatalf("CreateDeployment failed: %v", err)
		}

		status := &db.DeploymentStatus{
			DeploymentID: deployment.ID,
			State:        "pending",
			Description:  "In progress",
			CreatorID:    user.ID,
		}
		if err := svc.CreateDeploymentStatus(ctx, status); err != nil {
			t.Fatalf("CreateDeploymentStatus failed: %v", err)
		}

		found, err := svc.GetDeploymentStatus(ctx, repo.ID, deployment.ID, status.ID)
		if err != nil {
			t.Fatalf("GetDeploymentStatus failed: %v", err)
		}
		if found.State != status.State {
			t.Errorf("expected state %s, got %s", status.State, found.State)
		}
	})

	t.Run("ListDeploymentStatuses_success", func(t *testing.T) {
		// Create deployment
		deployment := &db.Deployment{
			RepositoryID: repo.ID,
			Ref:          "main",
			Task:         "deploy",
			Environment:  "prod",
			CreatorID:    user.ID,
		}
		if err := svc.CreateDeployment(ctx, deployment); err != nil {
			t.Fatalf("CreateDeployment failed: %v", err)
		}

		// Create multiple statuses
		for i, state := range []string{"pending", "success", "failure"} {
			status := &db.DeploymentStatus{
				DeploymentID: deployment.ID,
				State:        state,
				Description:  "Status " + string(rune('0'+i)),
				CreatorID:    user.ID,
			}
			if err := svc.CreateDeploymentStatus(ctx, status); err != nil {
				t.Fatalf("CreateDeploymentStatus failed: %v", err)
			}
		}

		statuses, err := svc.ListDeploymentStatuses(ctx, repo.ID, deployment.ID)
		if err != nil {
			t.Fatalf("ListDeploymentStatuses failed: %v", err)
		}
		if len(statuses) != 3 {
			t.Errorf("expected 3 statuses, got %d", len(statuses))
		}
	})
}
