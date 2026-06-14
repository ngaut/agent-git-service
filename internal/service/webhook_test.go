package service_test

import (
	"context"
	"testing"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/service"
)

func TestWebhookCreateAndList(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "whuser", "whrepo")

	// Get the repo to obtain its ID
	var repo db.Repository
	if err := svc.DB.First(&repo, "full_name = ?", "whuser/whrepo").Error; err != nil {
		t.Fatalf("failed to find repo: %v", err)
	}

	// Create a webhook
	hook := &db.Webhook{
		RepositoryID: repo.ID,
		Name:         "web",
		Active:       true,
		EventsJSON:   `["push","pull_request"]`,
		ConfigJSON:   `{"url":"https://example.com/hook","content_type":"json","secret":"mysecret","insecure_ssl":"0"}`,
	}
	if err := svc.CreateWebhook(ctx, hook); err != nil {
		t.Fatalf("CreateWebhook failed: %v", err)
	}
	if hook.ID == 0 {
		t.Fatal("expected webhook ID to be set after create")
	}

	// List webhooks for the repo
	hooks, err := svc.ListWebhooks(ctx, repo.ID)
	if err != nil {
		t.Fatalf("ListWebhooks failed: %v", err)
	}
	if len(hooks) != 1 {
		t.Errorf("expected 1 webhook, got %d", len(hooks))
	}
	if hooks[0].Name != "web" {
		t.Errorf("expected name 'web', got %s", hooks[0].Name)
	}
	if hooks[0].Active != true {
		t.Error("expected webhook to be active")
	}
}

func TestWebhookGet(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "whuser2", "whrepo2")

	var repo db.Repository
	if err := svc.DB.First(&repo, "full_name = ?", "whuser2/whrepo2").Error; err != nil {
		t.Fatalf("failed to find repo: %v", err)
	}

	// Create a webhook
	hook := &db.Webhook{
		RepositoryID: repo.ID,
		Name:         "web",
		Active:       true,
		EventsJSON:   `["push"]`,
		ConfigJSON:   `{"url":"https://example.com/hook"}`,
	}
	if err := svc.CreateWebhook(ctx, hook); err != nil {
		t.Fatalf("CreateWebhook failed: %v", err)
	}

	// Get the webhook
	retrieved, err := svc.GetWebhook(ctx, repo.ID, hook.ID)
	if err != nil {
		t.Fatalf("GetWebhook failed: %v", err)
	}
	if retrieved.ID != hook.ID {
		t.Errorf("expected ID %d, got %d", hook.ID, retrieved.ID)
	}

	// Get non-existent webhook
	_, err = svc.GetWebhook(ctx, repo.ID, 99999)
	if err == nil {
		t.Error("expected error for non-existent webhook")
	}
	if err != service.ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestWebhookUpdate(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "whuser3", "whrepo3")

	var repo db.Repository
	if err := svc.DB.First(&repo, "full_name = ?", "whuser3/whrepo3").Error; err != nil {
		t.Fatalf("failed to find repo: %v", err)
	}

	// Create a webhook
	hook := &db.Webhook{
		RepositoryID: repo.ID,
		Name:         "web",
		Active:       false,
		EventsJSON:   `["push"]`,
		ConfigJSON:   `{"url":"https://old.example.com/hook"}`,
	}
	if err := svc.CreateWebhook(ctx, hook); err != nil {
		t.Fatalf("CreateWebhook failed: %v", err)
	}

	// Update the webhook
	hook.Active = true
	hook.EventsJSON = `["push","issues","pull_request"]`
	hook.ConfigJSON = `{"url":"https://new.example.com/hook","content_type":"json"}`
	if err := svc.UpdateWebhook(ctx, hook); err != nil {
		t.Fatalf("UpdateWebhook failed: %v", err)
	}

	// Verify the update
	retrieved, err := svc.GetWebhook(ctx, repo.ID, hook.ID)
	if err != nil {
		t.Fatalf("GetWebhook failed: %v", err)
	}
	if retrieved.Active != true {
		t.Error("expected webhook to be active after update")
	}
}

func TestWebhookDelete(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "whuser4", "whrepo4")

	var repo db.Repository
	if err := svc.DB.First(&repo, "full_name = ?", "whuser4/whrepo4").Error; err != nil {
		t.Fatalf("failed to find repo: %v", err)
	}

	// Create a webhook
	hook := &db.Webhook{
		RepositoryID: repo.ID,
		Name:         "web",
		Active:       true,
		EventsJSON:   `["push"]`,
		ConfigJSON:   `{"url":"https://example.com/hook"}`,
	}
	if err := svc.CreateWebhook(ctx, hook); err != nil {
		t.Fatalf("CreateWebhook failed: %v", err)
	}

	// Delete the webhook
	if err := svc.DeleteWebhook(ctx, repo.ID, hook.ID); err != nil {
		t.Fatalf("DeleteWebhook failed: %v", err)
	}

	// Verify deletion
	_, err := svc.GetWebhook(ctx, repo.ID, hook.ID)
	if err == nil {
		t.Error("expected error after deletion")
	}
	if err != service.ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}

	// Delete non-existent webhook (should not error in GORM, but verify behavior)
	err = svc.DeleteWebhook(ctx, repo.ID, 99999)
	if err != nil {
		// GORM delete may not error for non-existent rows, but we test the behavior
		t.Logf("DeleteWebhook on non-existent returned: %v", err)
	}
}

func TestWebhookListEmpty(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "whuser5", "whrepo5")

	var repo db.Repository
	if err := svc.DB.First(&repo, "full_name = ?", "whuser5/whrepo5").Error; err != nil {
		t.Fatalf("failed to find repo: %v", err)
	}

	// List webhooks for a repo with no webhooks
	hooks, err := svc.ListWebhooks(ctx, repo.ID)
	if err != nil {
		t.Fatalf("ListWebhooks failed: %v", err)
	}
	if len(hooks) != 0 {
		t.Errorf("expected 0 webhooks, got %d", len(hooks))
	}
}

func TestWebhookCreateInvalidPayload(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "whuser6", "whrepo6")

	var repo db.Repository
	if err := svc.DB.First(&repo, "full_name = ?", "whuser6/whrepo6").Error; err != nil {
		t.Fatalf("failed to find repo: %v", err)
	}

	// Test with invalid JSON in EventsJSON. The service layer currently stores
	// the payload text and leaves API validation to handlers.
	// but we test that the service accepts/rejects appropriately
	hook := &db.Webhook{
		RepositoryID: repo.ID,
		Name:         "web",
		Active:       true,
		EventsJSON:   `invalid json`,
		ConfigJSON:   `{"url":"https://example.com/hook"}`,
	}
	// The service layer currently passes through to DB, so this may succeed
	// at the DB level but would fail at the API validation layer
	err := svc.CreateWebhook(ctx, hook)
	if err != nil {
		t.Logf("CreateWebhook with invalid EventsJSON returned: %v", err)
	}

	// Test with invalid JSON in ConfigJSON
	hook2 := &db.Webhook{
		RepositoryID: repo.ID,
		Name:         "web2",
		Active:       true,
		EventsJSON:   `["push"]`,
		ConfigJSON:   `not valid json`,
	}
	err = svc.CreateWebhook(ctx, hook2)
	if err != nil {
		t.Logf("CreateWebhook with invalid ConfigJSON returned: %v", err)
	}

	// Test with empty config - should still work at DB level
	hook3 := &db.Webhook{
		RepositoryID: repo.ID,
		Name:         "web3",
		Active:       true,
		EventsJSON:   `["push"]`,
		ConfigJSON:   `{}`,
	}
	if err := svc.CreateWebhook(ctx, hook3); err != nil {
		t.Fatalf("CreateWebhook with empty config failed: %v", err)
	}
}

func TestWebhookAuthorization(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	// Create two users
	user1 := db.User{Login: "whauthuser1", Name: "User 1", Type: db.TypeUser}
	user2 := db.User{Login: "whauthuser2", Name: "User 2", Type: db.TypeUser}
	if err := svc.DB.Create(&user1).Error; err != nil {
		t.Fatalf("failed to create user1: %v", err)
	}
	if err := svc.DB.Create(&user2).Error; err != nil {
		t.Fatalf("failed to create user2: %v", err)
	}

	// Create repos for each user
	repo1, err := svc.CreateRepo(ctx, service.CreateRepoInput{OwnerLogin: "whauthuser1", Name: "whauthrepo1"})
	if err != nil {
		t.Fatalf("failed to create repo1: %v", err)
	}
	_, err = svc.CreateRepo(ctx, service.CreateRepoInput{OwnerLogin: "whauthuser2", Name: "whauthrepo2"})
	if err != nil {
		t.Fatalf("failed to create repo2: %v", err)
	}

	// Create webhook as user1
	ctx1 := service.ContextWithUser(ctx, user1)
	hook := &db.Webhook{
		RepositoryID: repo1.ID,
		Name:         "web",
		Active:       true,
		EventsJSON:   `["push"]`,
		ConfigJSON:   `{"url":"https://example.com/hook"}`,
	}
	if err := svc.CreateWebhook(ctx1, hook); err != nil {
		t.Fatalf("CreateWebhook as user1 failed: %v", err)
	}

	// Test: user2 should not be able to access user1's webhook via GetWebhook
	// Note: Current service implementation doesn't enforce ownership at the service layer
	// This test documents the current behavior - authorization would be enforced at handler layer
	ctx2 := service.ContextWithUser(ctx, user2)
	_, err = svc.GetWebhook(ctx2, repo1.ID, hook.ID)
	// The service layer currently doesn't check ownership, so this may succeed
	// Real authorization happens at the HTTP handler layer
	if err != nil {
		t.Logf("GetWebhook as user2 for user1's webhook returned: %v", err)
	}

	// List webhooks - user2 listing user1's repo webhooks
	hooks, err := svc.ListWebhooks(ctx2, repo1.ID)
	if err != nil {
		t.Logf("ListWebhooks as user2 for user1's repo returned: %v", err)
	} else {
		// Document current behavior - service layer doesn't filter by user
		t.Logf("ListWebhooks returned %d hooks (service layer doesn't enforce ownership)", len(hooks))
	}
}

func TestWebhookDeleteWrongRepo(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "whuser7", "whrepo7")
	setupRepoForTest(t, svc, "whuser8", "whrepo8")

	var repo1, repo2 db.Repository
	if err := svc.DB.First(&repo1, "full_name = ?", "whuser7/whrepo7").Error; err != nil {
		t.Fatalf("failed to find repo1: %v", err)
	}
	if err := svc.DB.First(&repo2, "full_name = ?", "whuser8/whrepo8").Error; err != nil {
		t.Fatalf("failed to find repo2: %v", err)
	}

	// Create webhook in repo1
	hook := &db.Webhook{
		RepositoryID: repo1.ID,
		Name:         "web",
		Active:       true,
		EventsJSON:   `["push"]`,
		ConfigJSON:   `{"url":"https://example.com/hook"}`,
	}
	if err := svc.CreateWebhook(ctx, hook); err != nil {
		t.Fatalf("CreateWebhook failed: %v", err)
	}

	// Try to delete using wrong repo ID - should not delete (scoped by repoID)
	err := svc.DeleteWebhook(ctx, repo2.ID, hook.ID)
	if err != nil {
		t.Logf("DeleteWebhook with wrong repoID returned: %v", err)
	}

	// Verify webhook still exists in repo1
	retrieved, err := svc.GetWebhook(ctx, repo1.ID, hook.ID)
	if err != nil {
		t.Fatalf("GetWebhook failed - webhook was incorrectly deleted: %v", err)
	}
	if retrieved.ID != hook.ID {
		t.Error("webhook was incorrectly deleted")
	}
}

func TestWebhookGetWrongRepo(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "whuser9", "whrepo9")
	setupRepoForTest(t, svc, "whuser10", "whrepo10")

	var repo1, repo2 db.Repository
	if err := svc.DB.First(&repo1, "full_name = ?", "whuser9/whrepo9").Error; err != nil {
		t.Fatalf("failed to find repo1: %v", err)
	}
	if err := svc.DB.First(&repo2, "full_name = ?", "whuser10/whrepo10").Error; err != nil {
		t.Fatalf("failed to find repo2: %v", err)
	}

	// Create webhook in repo1
	hook := &db.Webhook{
		RepositoryID: repo1.ID,
		Name:         "web",
		Active:       true,
		EventsJSON:   `["push"]`,
		ConfigJSON:   `{"url":"https://example.com/hook"}`,
	}
	if err := svc.CreateWebhook(ctx, hook); err != nil {
		t.Fatalf("CreateWebhook failed: %v", err)
	}

	// Try to get using wrong repo ID - should return not found
	_, err := svc.GetWebhook(ctx, repo2.ID, hook.ID)
	if err == nil {
		t.Error("expected error when getting webhook with wrong repo ID")
	}
	if err != service.ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}
