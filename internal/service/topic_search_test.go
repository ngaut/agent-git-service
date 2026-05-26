package service_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/service"
)

func TestSearchTopics_AggregatesRepositoryTopics(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	owner := db.User{Login: "topic-owner", Type: db.TypeUser}
	if err := svc.DB.Create(&owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	repos := []db.Repository{
		{Name: "one", FullName: "topic-owner/one", OwnerID: owner.ID, Topics: "machine-learning,go"},
		{Name: "two", FullName: "topic-owner/two", OwnerID: owner.ID, Topics: "machine-learning,python"},
		{Name: "three", FullName: "topic-owner/three", OwnerID: owner.ID, Topics: "python"},
	}
	for _, repo := range repos {
		if err := svc.DB.Create(&repo).Error; err != nil {
			t.Fatalf("create repo %s: %v", repo.FullName, err)
		}
	}

	topics, err := svc.SearchTopics(ctx, "machine repositories:>=2")
	if err != nil {
		t.Fatalf("SearchTopics: %v", err)
	}
	if len(topics) != 1 {
		t.Fatalf("expected 1 topic, got %d: %#v", len(topics), topics)
	}
	if topics[0].Name != "machine-learning" {
		t.Fatalf("topic name: got %q, want machine-learning", topics[0].Name)
	}
	if topics[0].RepositoryCount != 2 {
		t.Fatalf("repository count: got %d, want 2", topics[0].RepositoryCount)
	}

	topics, err = svc.SearchTopics(ctx, "machine is:featured")
	if err != nil {
		t.Fatalf("SearchTopics unsupported filter: %v", err)
	}
	if len(topics) != 0 {
		t.Fatalf("expected unsupported metadata filter to return no results, got %#v", topics)
	}

	topics, err = svc.SearchTopics(ctx, "machine repositories:not-a-number")
	if err != nil {
		t.Fatalf("SearchTopics invalid repositories filter: %v", err)
	}
	if len(topics) != 0 {
		t.Fatalf("expected invalid repositories filter to return no results, got %#v", topics)
	}
}

func TestSearchTopics_RespectsRepositoryVisibility(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	viewer := db.User{Login: "viewer", Type: db.TypeUser}
	owner := db.User{Login: "topic-owner", Type: db.TypeUser}
	if err := svc.DB.Create(&viewer).Error; err != nil {
		t.Fatalf("create viewer: %v", err)
	}
	if err := svc.DB.Create(&owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}

	repos := []db.Repository{
		{Name: "public-one", FullName: "topic-owner/public-one", OwnerID: owner.ID, Private: false, Topics: "machine-learning"},
		{Name: "private-one", FullName: "topic-owner/private-one", OwnerID: owner.ID, Private: true, Topics: "machine-learning"},
	}
	for _, repo := range repos {
		if err := svc.DB.Create(&repo).Error; err != nil {
			t.Fatalf("create repo %s: %v", repo.FullName, err)
		}
	}

	viewerCtx := service.ContextWithUser(context.Background(), viewer)
	topics, err := svc.SearchTopics(viewerCtx, "machine repositories:>=1")
	if err != nil {
		t.Fatalf("SearchTopics: %v", err)
	}
	if len(topics) != 1 {
		t.Fatalf("expected 1 visible topic, got %d: %#v", len(topics), topics)
	}
	if topics[0].Name != "machine-learning" {
		t.Fatalf("topic name: got %q, want machine-learning", topics[0].Name)
	}
	if topics[0].RepositoryCount != 1 {
		t.Fatalf("repository count: got %d, want 1", topics[0].RepositoryCount)
	}
}

func TestSearchTopics_AggregatesBeyondDefaultListLimit(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	owner := db.User{Login: "topic-owner", Type: db.TypeUser}
	if err := svc.DB.Create(&owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}

	for i := 0; i < 1001; i++ {
		repo := db.Repository{
			Name:     fmt.Sprintf("repo-%04d", i),
			FullName: fmt.Sprintf("topic-owner/repo-%04d", i),
			OwnerID:  owner.ID,
			Topics:   "machine-learning",
		}
		if err := svc.DB.Create(&repo).Error; err != nil {
			t.Fatalf("create repo %d: %v", i, err)
		}
	}

	topics, err := svc.SearchTopics(ctx, "machine repositories:>=1001")
	if err != nil {
		t.Fatalf("SearchTopics: %v", err)
	}
	if len(topics) != 1 {
		t.Fatalf("expected 1 topic, got %d: %#v", len(topics), topics)
	}
	if topics[0].RepositoryCount != 1001 {
		t.Fatalf("repository count: got %d, want 1001", topics[0].RepositoryCount)
	}
}
