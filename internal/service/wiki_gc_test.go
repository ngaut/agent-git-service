package service_test

import (
	"context"
	"testing"

	"gh-server/internal/db"
	"gh-server/internal/wikicatalog"
)

func TestWikiCatalogPostCommit_MigrationIndexesSynchronously(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	repoFullName := seedRepoForWikiMigration(t, svc, "alice", "sync-index")
	repo, err := svc.GetRepo(ctx, repoFullName)
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}
	svc.WikiCatalog.OnChangeSetCommitted = svc.WikiCatalogPostCommit

	result, err := svc.WikiCatalog.ApplyChangeSet(ctx, wikicatalog.ChangeSetRequest{
		RepositoryID: repo.ID,
		Source:       wikicatalog.SourceMigration,
		Changes: []wikicatalog.Change{{
			Op:   wikicatalog.OpUpsert,
			Slug: "home",
			Body: []byte("catalog body"),
		}},
	})
	if err != nil {
		t.Fatalf("ApplyChangeSet: %v", err)
	}

	var doc db.WikiSearchDocument
	if err := svc.DB.Where("repository_id = ? AND slug = ?", repo.ID, "home").First(&doc).Error; err != nil {
		t.Fatalf("search document not written before ApplyChangeSet returned: %v", err)
	}
	if string(doc.Body) != "catalog body" {
		t.Fatalf("search body = %q, want %q", string(doc.Body), "catalog body")
	}
	if result.Source != wikicatalog.SourceMigration {
		t.Fatalf("result source = %q, want %q", result.Source, wikicatalog.SourceMigration)
	}
}
