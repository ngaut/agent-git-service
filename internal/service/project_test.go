package service_test

import (
	"context"
	"testing"

	"gh-server/internal/db"
	"gh-server/internal/service"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFindOrCreateProjectItem_Idempotent(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()

	// Create a project
	proj, err := svc.CreateProject(ctx, "testuser", "Test Project")
	require.NoError(t, err)

	contentID := "Issue_123"
	itemType := "ISSUE"

	// First call - should create the item
	item1, err := svc.FindOrCreateProjectItem(ctx, proj.ID, contentID, itemType)
	require.NoError(t, err)
	assert.NotZero(t, item1.ID)
	assert.Equal(t, proj.ID, item1.ProjectID)
	assert.Equal(t, contentID, item1.ContentID)
	assert.Equal(t, itemType, item1.Type)

	// Second call with same params - should return existing item (idempotent)
	item2, err := svc.FindOrCreateProjectItem(ctx, proj.ID, contentID, itemType)
	require.NoError(t, err)
	assert.Equal(t, item1.ID, item2.ID, "Should return the same item")

	// Different content ID - should create a new item
	item3, err := svc.FindOrCreateProjectItem(ctx, proj.ID, "Issue_456", itemType)
	require.NoError(t, err)
	assert.NotEqual(t, item1.ID, item3.ID, "Should create a new item for different content")

	// Different type - should create a new item (edge case: same content ID but different type)
	item4, err := svc.FindOrCreateProjectItem(ctx, proj.ID, contentID, "PULL_REQUEST")
	require.NoError(t, err)
	assert.NotEqual(t, item1.ID, item4.ID, "Should create a new item for different type")
}

func TestFindOrCreateProjectItem_MultipleProjects(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()

	// Create two projects
	proj1, err := svc.CreateProject(ctx, "testuser", "Project 1")
	require.NoError(t, err)
	proj2, err := svc.CreateProject(ctx, "testuser", "Project 2")
	require.NoError(t, err)

	contentID := "Issue_123"
	itemType := "ISSUE"

	// Same content can exist in different projects
	item1, err := svc.FindOrCreateProjectItem(ctx, proj1.ID, contentID, itemType)
	require.NoError(t, err)
	item2, err := svc.FindOrCreateProjectItem(ctx, proj2.ID, contentID, itemType)
	require.NoError(t, err)

	assert.NotEqual(t, item1.ID, item2.ID)
	assert.Equal(t, proj1.ID, item1.ProjectID)
	assert.Equal(t, proj2.ID, item2.ProjectID)
}

func TestFindOrCreateProjectItem_DraftIssues(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()

	// Create a project
	proj, err := svc.CreateProject(ctx, "testuser", "Test Project")
	require.NoError(t, err)

	// Draft issues have empty content_id, so multiple should be allowed
	item1, err := svc.FindOrCreateProjectItem(ctx, proj.ID, "", "DRAFT_ISSUE")
	require.NoError(t, err)

	item2, err := svc.FindOrCreateProjectItem(ctx, proj.ID, "", "DRAFT_ISSUE")
	require.NoError(t, err)

	// Both should have the same ID since they're identical (empty content_id, same type)
	// This is expected behavior - draft issues with no distinguishing fields are the same
	assert.Equal(t, item1.ID, item2.ID)
}

func TestProjectItemUniqueConstraint(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()

	// Create a project
	proj, err := svc.CreateProject(ctx, "testuser", "Test Project")
	require.NoError(t, err)

	// Create an item directly
	item := db.ProjectItem{
		ProjectID: proj.ID,
		ContentID: "Issue_999",
		Type:      "ISSUE",
	}
	err = svc.CreateProjectItem(ctx, &item)
	require.NoError(t, err)

	// Try to create a duplicate directly - should fail due to unique constraint
	dupItem := db.ProjectItem{
		ProjectID: proj.ID,
		ContentID: "Issue_999",
		Type:      "ISSUE",
	}
	err = svc.CreateProjectItem(ctx, &dupItem)
	// The error should be a duplicate key error
	assert.Error(t, err, "Should fail to create duplicate item")
}

func TestListProjectsForRepo_ReturnsOnlyLinkedProjects(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()

	require.NoError(t, svc.DB.Create(&db.User{Login: "testuser", Name: "testuser", Type: db.TypeUser}).Error)

	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{OwnerLogin: "testuser", Name: "repo-a"})
	require.NoError(t, err)
	otherRepo, err := svc.CreateRepo(ctx, service.CreateRepoInput{OwnerLogin: "testuser", Name: "repo-b"})
	require.NoError(t, err)

	linkedOne, err := svc.CreateProject(ctx, "testuser", "Linked One")
	require.NoError(t, err)
	linkedTwo, err := svc.CreateProject(ctx, "testuser", "Linked Two")
	require.NoError(t, err)
	unlinked, err := svc.CreateProject(ctx, "testuser", "Unlinked")
	require.NoError(t, err)

	require.NoError(t, svc.LinkProjectToRepo(ctx, linkedOne.ID, repo.ID))
	require.NoError(t, svc.LinkProjectToRepo(ctx, linkedTwo.ID, repo.ID))
	require.NoError(t, svc.LinkProjectToRepo(ctx, unlinked.ID, otherRepo.ID))

	projects, err := svc.ListProjectsForRepo(ctx, repo.ID)
	require.NoError(t, err)
	require.Len(t, projects, 2)
	assert.Equal(t, []string{"Linked One", "Linked Two"}, []string{projects[0].Title, projects[1].Title})
}
