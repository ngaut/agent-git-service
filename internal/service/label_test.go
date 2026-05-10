package service_test

import (
	"context"
	"testing"

	"gh-server/internal/db"
	"gh-server/internal/service"
)

func TestLabelCreateAndList(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "lbluser", "lblrepo")

	// Create labels in an empty repo.
	l1, err := svc.CreateLabel(ctx, "lbluser/lblrepo", "my-custom-bug", "ff0000", "Something broken")
	if err != nil {
		t.Fatalf("CreateLabel failed: %v", err)
	}
	if l1.Name != "my-custom-bug" {
		t.Errorf("expected name my-custom-bug, got %s", l1.Name)
	}
	if l1.Color != "ff0000" {
		t.Errorf("expected color ff0000, got %s", l1.Color)
	}

	_, err = svc.CreateLabel(ctx, "lbluser/lblrepo", "my-enhancement", "00ff00", "")
	if err != nil {
		t.Fatalf("CreateLabel(my-enhancement) failed: %v", err)
	}

	// Duplicate should fail
	_, err = svc.CreateLabel(ctx, "lbluser/lblrepo", "my-custom-bug", "0000ff", "")
	if err == nil {
		t.Error("expected error for duplicate label")
	}

	// List the two custom labels we created.
	labels, err := svc.ListLabels(ctx, "lbluser/lblrepo")
	if err != nil {
		t.Fatalf("ListLabels failed: %v", err)
	}
	if len(labels) != 2 {
		t.Errorf("expected 2 labels, got %d", len(labels))
	}
}

func TestLabelEditAndDelete(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "lbluser2", "lblrepo2")

	svc.CreateLabel(ctx, "lbluser2/lblrepo2", "old-name", "aabbcc", "old desc")

	// Edit
	newName := "new-name"
	newColor := "ddeeff"
	newDesc := "new desc"
	edited, err := svc.EditLabel(ctx, "lbluser2/lblrepo2", "old-name", service.EditLabelInput{
		NewName:     &newName,
		Color:       &newColor,
		Description: &newDesc,
	})
	if err != nil {
		t.Fatalf("EditLabel failed: %v", err)
	}
	if edited.Name != "new-name" {
		t.Errorf("expected name new-name, got %s", edited.Name)
	}
	if edited.Color != "ddeeff" {
		t.Errorf("expected color ddeeff, got %s", edited.Color)
	}

	// Delete
	if err := svc.DeleteLabel(ctx, "lbluser2/lblrepo2", "new-name"); err != nil {
		t.Fatalf("DeleteLabel failed: %v", err)
	}

	// Delete non-existent
	err = svc.DeleteLabel(ctx, "lbluser2/lblrepo2", "nonexistent")
	if err == nil {
		t.Error("expected error for deleting non-existent label")
	}

	// List should be empty after deleting our custom label.
	labels, _ := svc.ListLabels(ctx, "lbluser2/lblrepo2")
	if len(labels) != 0 {
		t.Errorf("expected 0 labels, got %d", len(labels))
	}
}

func TestLabelColorStripHash(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "lbluser3", "lblrepo3")

	// Create repo's first label (no defaults from CreateRepo)
	// Color with # prefix should be stripped
	l, err := svc.CreateLabel(ctx, "lbluser3/lblrepo3", "test", "#abcdef", "")
	if err != nil {
		t.Fatalf("CreateLabel failed: %v", err)
	}
	if l.Color != "abcdef" {
		t.Errorf("expected # stripped: abcdef, got %s", l.Color)
	}
}

func TestLabelAddToIssue(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "lbluser4", "lblrepo4")

	label, err := svc.CreateLabel(ctx, "lbluser4/lblrepo4", "my-custom-label", "ff0000", "")
	if err != nil {
		t.Fatalf("CreateLabel failed: %v", err)
	}
	issue, _ := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "lbluser4/lblrepo4",
		Title:        "Test issue",
		AuthorLogin:  "lbluser4",
	})

	// First add should succeed.
	if err := svc.AddIssueLabelByID(ctx, issue.ID, label.ID); err != nil {
		t.Fatalf("AddIssueLabelByID failed: %v", err)
	}

	// Duplicate add must be idempotent (no error).
	if err := svc.AddIssueLabelByID(ctx, issue.ID, label.ID); err != nil {
		t.Fatalf("duplicate AddIssueLabelByID should be idempotent, got: %v", err)
	}

	// Verify only one junction row exists.
	labels, err := svc.ListIssueLabels(ctx, "lbluser4/lblrepo4", issue.Number)
	if err != nil {
		t.Fatalf("ListIssueLabels failed: %v", err)
	}
	if len(labels) != 1 {
		t.Errorf("expected 1 label after duplicate add, got %d", len(labels))
	}
	var labeledCount int64
	if err := svc.DB.Model(&db.IssueEvent{}).
		Where("issue_id = ? AND event_type = ?", issue.ID, "labeled").
		Count(&labeledCount).Error; err != nil {
		t.Fatalf("count labeled events: %v", err)
	}
	if labeledCount != 1 {
		t.Fatalf("expected 1 labeled event after duplicate add, got %d", labeledCount)
	}
}

func TestLabelAddToIssueDuplicateViaAddIssueLabels(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "lbluser4b", "lblrepo4b")

	svc.CreateLabel(ctx, "lbluser4b/lblrepo4b", "dup-label", "112233", "")
	issue, _ := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "lbluser4b/lblrepo4b",
		Title:        "Dup test",
		AuthorLogin:  "lbluser4b",
	})

	// Add label once.
	if _, err := svc.AddIssueLabels(ctx, "lbluser4b/lblrepo4b", issue.Number, []string{"dup-label"}); err != nil {
		t.Fatalf("first AddIssueLabels failed: %v", err)
	}

	// Add the same label again — must succeed (idempotent).
	labels, err := svc.AddIssueLabels(ctx, "lbluser4b/lblrepo4b", issue.Number, []string{"dup-label"})
	if err != nil {
		t.Fatalf("duplicate AddIssueLabels should be idempotent, got: %v", err)
	}
	if len(labels) != 1 {
		t.Errorf("expected 1 label after duplicate add, got %d", len(labels))
	}
	var labeledCount int64
	if err := svc.DB.Model(&db.IssueEvent{}).
		Where("issue_id = ? AND event_type = ?", issue.ID, "labeled").
		Count(&labeledCount).Error; err != nil {
		t.Fatalf("count labeled events: %v", err)
	}
	if labeledCount != 1 {
		t.Fatalf("expected 1 labeled event after duplicate batch add, got %d", labeledCount)
	}
}

func TestAddPRLabelByID_Idempotent(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	pr, _, _ := setupPRWithRealBranches(t, svc, "prlbluser", "prlblrepo")

	label, err := svc.CreateLabel(ctx, "prlbluser/prlblrepo", "pr-label", "aa00ff", "")
	if err != nil {
		t.Fatalf("CreateLabel failed: %v", err)
	}

	// First attach should succeed.
	if err := svc.AddPRLabelByID(ctx, pr.ID, label.ID); err != nil {
		t.Fatalf("first AddPRLabelByID failed: %v", err)
	}

	// Duplicate attach must be idempotent (no error).
	if err := svc.AddPRLabelByID(ctx, pr.ID, label.ID); err != nil {
		t.Fatalf("duplicate AddPRLabelByID should be idempotent, got: %v", err)
	}

	// Verify only one junction row exists.
	labels, err := svc.ListPRLabels(ctx, "prlbluser/prlblrepo", pr.Number)
	if err != nil {
		t.Fatalf("ListPRLabels failed: %v", err)
	}
	if len(labels) != 1 {
		t.Errorf("expected 1 label after duplicate add, got %d", len(labels))
	}
}

// TestAddIssueLabelByID_ConcurrentIdempotent verifies that concurrent goroutines
// calling AddIssueLabelByID for the same issue+label pair all succeed without
// error and result in exactly one junction row.
func TestAddIssueLabelByID_ConcurrentIdempotent(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "conclbluser", "conclblrepo")

	label, err := svc.CreateLabel(ctx, "conclbluser/conclblrepo", "conc-label", "ff0000", "")
	if err != nil {
		t.Fatalf("CreateLabel failed: %v", err)
	}
	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "conclbluser/conclblrepo",
		Title:        "Concurrent label test",
		AuthorLogin:  "conclbluser",
	})
	if err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	const goroutines = 10
	start := make(chan struct{})
	errs := make(chan error, goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			<-start // barrier: all goroutines start at the same time
			errs <- svc.AddIssueLabelByID(ctx, issue.ID, label.ID)
		}()
	}
	close(start)

	for i := 0; i < goroutines; i++ {
		if err := <-errs; err != nil {
			t.Errorf("concurrent AddIssueLabelByID returned error: %v", err)
		}
	}

	// Verify exactly one junction row.
	labels, err := svc.ListIssueLabels(ctx, "conclbluser/conclblrepo", issue.Number)
	if err != nil {
		t.Fatalf("ListIssueLabels failed: %v", err)
	}
	if len(labels) != 1 {
		t.Errorf("expected 1 label after concurrent adds, got %d", len(labels))
	}
	var labeledCount int64
	if err := svc.DB.Model(&db.IssueEvent{}).
		Where("issue_id = ? AND event_type = ?", issue.ID, "labeled").
		Count(&labeledCount).Error; err != nil {
		t.Fatalf("count labeled events: %v", err)
	}
	if labeledCount != 1 {
		t.Fatalf("expected 1 labeled event after concurrent adds, got %d", labeledCount)
	}
}

// TestAddPRLabelByID_ConcurrentIdempotent verifies that concurrent goroutines
// calling AddPRLabelByID for the same PR+label pair all succeed without error
// and result in exactly one junction row.
func TestAddPRLabelByID_ConcurrentIdempotent(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	pr, _, _ := setupPRWithRealBranches(t, svc, "concprlbluser", "concprlblrepo")

	label, err := svc.CreateLabel(ctx, "concprlbluser/concprlblrepo", "conc-pr-label", "00ff00", "")
	if err != nil {
		t.Fatalf("CreateLabel failed: %v", err)
	}

	const goroutines = 10
	start := make(chan struct{})
	errs := make(chan error, goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			<-start
			errs <- svc.AddPRLabelByID(ctx, pr.ID, label.ID)
		}()
	}
	close(start)

	for i := 0; i < goroutines; i++ {
		if err := <-errs; err != nil {
			t.Errorf("concurrent AddPRLabelByID returned error: %v", err)
		}
	}

	labels, err := svc.ListPRLabels(ctx, "concprlbluser/concprlblrepo", pr.Number)
	if err != nil {
		t.Fatalf("ListPRLabels failed: %v", err)
	}
	if len(labels) != 1 {
		t.Errorf("expected 1 label after concurrent adds, got %d", len(labels))
	}
}

// TestAddIssueLabels_ConcurrentIdempotent verifies that concurrent goroutines
// calling AddIssueLabels with the same label all succeed without error.
func TestAddIssueLabels_ConcurrentIdempotent(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "conclbluser2", "conclblrepo2")

	svc.CreateLabel(ctx, "conclbluser2/conclblrepo2", "conc-batch-label", "aabb00", "")
	issue, _ := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "conclbluser2/conclblrepo2",
		Title:        "Concurrent batch label test",
		AuthorLogin:  "conclbluser2",
	})

	const goroutines = 10
	start := make(chan struct{})
	errs := make(chan error, goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			<-start
			_, err := svc.AddIssueLabels(ctx, "conclbluser2/conclblrepo2", issue.Number, []string{"conc-batch-label"})
			errs <- err
		}()
	}
	close(start)

	for i := 0; i < goroutines; i++ {
		if err := <-errs; err != nil {
			t.Errorf("concurrent AddIssueLabels returned error: %v", err)
		}
	}

	labels, err := svc.ListIssueLabels(ctx, "conclbluser2/conclblrepo2", issue.Number)
	if err != nil {
		t.Fatalf("ListIssueLabels failed: %v", err)
	}
	if len(labels) != 1 {
		t.Errorf("expected 1 label after concurrent adds, got %d", len(labels))
	}
	var labeledCount int64
	if err := svc.DB.Model(&db.IssueEvent{}).
		Where("issue_id = ? AND event_type = ?", issue.ID, "labeled").
		Count(&labeledCount).Error; err != nil {
		t.Fatalf("count labeled events: %v", err)
	}
	if labeledCount != 1 {
		t.Fatalf("expected 1 labeled event after concurrent batch adds, got %d", labeledCount)
	}
}

func TestLabelNotFound(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "lbluser5", "lblrepo5")

	_, err := svc.EditLabel(ctx, "lbluser5/lblrepo5", "nonexistent", service.EditLabelInput{})
	if err == nil {
		t.Error("expected error for editing non-existent label")
	}
}

// TestDeleteLabel_WithReferences tests that deleting a label referenced by issues/PRs succeeds
// by cascading the deletion to issue_labels and pr_labels join tables.
func TestDeleteLabel_WithReferences(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupRepoForTest(t, svc, "testowner", "testrepo")

	_, err := svc.CreateLabel(ctx, "testowner/testrepo", "testlabel", "ff0000", "Test label")
	if err != nil {
		t.Fatalf("CreateLabel failed: %v", err)
	}

	// Create an issue and attach the label
	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "testowner/testrepo",
		Title:        "Test Issue",
		Body:         "Test body",
		AuthorLogin:  "testowner",
		Labels:       []string{"testlabel"},
	})
	if err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// Verify label is attached
	issueLabels, err := svc.ListIssueLabels(ctx, "testowner/testrepo", issue.Number)
	if err != nil {
		t.Fatalf("ListIssueLabels failed: %v", err)
	}
	if len(issueLabels) != 1 {
		t.Fatalf("Expected 1 label, got %d", len(issueLabels))
	}

	// Delete the label - should cascade and succeed
	err = svc.DeleteLabel(ctx, "testowner/testrepo", "testlabel")
	if err != nil {
		t.Fatalf("DeleteLabel failed: %v", err)
	}

	// Verify label is deleted from the issue
	issueLabels, err = svc.ListIssueLabels(ctx, "testowner/testrepo", issue.Number)
	if err != nil {
		t.Fatalf("ListIssueLabels after delete failed: %v", err)
	}
	if len(issueLabels) != 0 {
		t.Errorf("Expected 0 labels after delete, got %d: %v", len(issueLabels), issueLabels)
	}

	// Verify label no longer exists
	_, err = svc.GetLabel(ctx, "testowner/testrepo", "testlabel")
	if err == nil {
		t.Error("Expected error getting deleted label, got nil")
	}
}

func TestLabelEditsRefreshWikiSearchDocuments(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	owner := db.User{Login: "wiki-label-refresh-owner", Name: "wiki-label-refresh-owner", Type: db.TypeUser}
	if err := svc.DB.Create(&owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: owner.Login,
		Name:       "wiki-label-refresh",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("create repo: %v", err)
	}
	full := owner.Login + "/wiki-label-refresh"

	if _, err := svc.CreateLabel(ctx, full, "auth", "d73a4a", "Authentication lifecycle and token rotation"); err != nil {
		t.Fatalf("create label: %v", err)
	}
	if _, err := svc.PutWikiPage(ctx, full, "guides/rotation", "# Rotation\n\nUse the admin console.", "create page", ""); err != nil {
		t.Fatalf("put wiki page: %v", err)
	}
	svc.Wg.Wait()
	if _, err := svc.SetWikiPageLabels(ctx, full, "guides/rotation", []string{"auth"}); err != nil {
		t.Fatalf("set wiki labels: %v", err)
	}
	svc.Wg.Wait()

	assertWikiSearchContainsSlug(t, svc, ctx, full, "token rotation", "guides/rotation")

	updatedDescription := "Release rollback and incident response"
	renamedLabel := "operations"
	if _, err := svc.EditLabel(ctx, full, "auth", service.EditLabelInput{
		NewName:     &renamedLabel,
		Description: &updatedDescription,
	}); err != nil {
		t.Fatalf("EditLabel: %v", err)
	}
	svc.Wg.Wait()

	assertWikiSearchExcludesSlug(t, svc, ctx, full, "token rotation", "guides/rotation")
	assertWikiSearchContainsSlug(t, svc, ctx, full, "incident response", "guides/rotation")

	if err := svc.DeleteLabel(ctx, full, "operations"); err != nil {
		t.Fatalf("DeleteLabel: %v", err)
	}
	svc.Wg.Wait()

	assertWikiSearchExcludesSlug(t, svc, ctx, full, "incident response", "guides/rotation")
}

func assertWikiSearchContainsSlug(t *testing.T, svc *service.Service, ctx context.Context, full, query, slug string) {
	t.Helper()
	resp, err := svc.SearchWikiPagesWithOptions(ctx, full, query, service.WikiSearchOptions{Limit: 20})
	if err != nil {
		t.Fatalf("SearchWikiPagesWithOptions(%q): %v", query, err)
	}
	for _, result := range resp.Results {
		if result.Slug == slug {
			return
		}
	}
	t.Fatalf("expected search %q to include %s, got %#v", query, slug, resp.Results)
}

func assertWikiSearchExcludesSlug(t *testing.T, svc *service.Service, ctx context.Context, full, query, slug string) {
	t.Helper()
	resp, err := svc.SearchWikiPagesWithOptions(ctx, full, query, service.WikiSearchOptions{Limit: 20})
	if err != nil {
		t.Fatalf("SearchWikiPagesWithOptions(%q): %v", query, err)
	}
	for _, result := range resp.Results {
		if result.Slug == slug {
			t.Fatalf("expected search %q to exclude %s, got %#v", query, slug, resp.Results)
		}
	}
}
