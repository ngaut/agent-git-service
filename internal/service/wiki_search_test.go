package service_test

import (
	"context"
	"strings"
	"testing"

	"gh-server/internal/db"
	"gh-server/internal/service"
	"gh-server/internal/testharness"
)

type semanticWikiEmbedder struct{}

func (semanticWikiEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	if strings.Contains(text, "session expiry") || strings.Contains(text, "Session expiry") {
		return []float32{1, 0, 0}, nil
	}
	if strings.Contains(text, "Cache invalidation") || strings.Contains(text, "cache invalidation") {
		return []float32{0, 1, 0}, nil
	}
	return []float32{0, 0, 1}, nil
}

func (semanticWikiEmbedder) Dimensions() int { return 3 }

func TestWikiSearchLifecycleAndFallback_Issue1362(t *testing.T) {
	svc, cleanup := testharness.NewService(t, testharness.ServiceConfig{})
	defer cleanup()
	ctx := context.Background()
	if err := svc.DB.Create(&db.User{
		Login: "testuser",
		Name:  "Test User",
		Type:  db.TypeUser,
	}).Error; err != nil {
		t.Fatalf("seed owner: %v", err)
	}

	if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "testuser",
		Name:       "wiki-search",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	full := "testuser/wiki-search"

	page, err := svc.PutWikiPage(ctx, full, "tutorial/auth", "# Authentication\n\nThe session token expires after 30 minutes of inactivity.", "create auth", "")
	if err != nil {
		t.Fatalf("PutWikiPage(create): %v", err)
	}
	svc.Wg.Wait()

	resp, err := svc.SearchWikiPages(ctx, full, "token expires", 20, 0)
	if err != nil {
		t.Fatalf("SearchWikiPages(create): %v", err)
	}
	if resp.Method != "substring" {
		t.Fatalf("method = %q, want substring", resp.Method)
	}
	if len(resp.Results) != 1 || resp.Results[0].Slug != "tutorial/auth" {
		t.Fatalf("results after create = %#v, want tutorial/auth", resp.Results)
	}

	page, err = svc.PutWikiPage(ctx, full, "tutorial/auth", "# Authentication\n\nRefresh tokens rotate automatically; expiry wording removed.", "update auth", page.SHA)
	if err != nil {
		t.Fatalf("PutWikiPage(update): %v", err)
	}
	svc.Wg.Wait()

	resp, err = svc.SearchWikiPages(ctx, full, "token expires", 20, 0)
	if err != nil {
		t.Fatalf("SearchWikiPages(update): %v", err)
	}
	if len(resp.Results) != 0 {
		t.Fatalf("results after update = %#v, want empty", resp.Results)
	}

	moved, err := svc.MoveWikiPage(ctx, full, "tutorial/auth", "guides/authentication", page.SHA, "move auth")
	if err != nil {
		t.Fatalf("MoveWikiPage: %v", err)
	}
	svc.Wg.Wait()

	resp, err = svc.SearchWikiPages(ctx, full, "refresh tokens", 20, 0)
	if err != nil {
		t.Fatalf("SearchWikiPages(move): %v", err)
	}
	if len(resp.Results) != 1 || resp.Results[0].Slug != moved.Moved.Slug {
		t.Fatalf("results after move = %#v, want %q", resp.Results, moved.Moved.Slug)
	}

	if err := svc.DeleteWikiPage(ctx, full, moved.Moved.Slug, "delete auth"); err != nil {
		t.Fatalf("DeleteWikiPage: %v", err)
	}
	svc.Wg.Wait()

	resp, err = svc.SearchWikiPages(ctx, full, "refresh tokens", 20, 0)
	if err != nil {
		t.Fatalf("SearchWikiPages(delete): %v", err)
	}
	if len(resp.Results) != 0 {
		t.Fatalf("results after delete = %#v, want empty", resp.Results)
	}
}

func TestWikiSearchFallsBackToGitScanWhenIndexUnavailable(t *testing.T) {
	svc, cleanup := testharness.NewService(t, testharness.ServiceConfig{})
	defer cleanup()
	ctx := context.Background()
	if err := svc.DB.Create(&db.User{
		Login: "testuser",
		Name:  "Test User",
		Type:  db.TypeUser,
	}).Error; err != nil {
		t.Fatalf("seed owner: %v", err)
	}

	if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "testuser",
		Name:       "wiki-search-fallback",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	full := "testuser/wiki-search-fallback"

	if _, err := svc.PutWikiPage(ctx, full, "tutorial/auth", "# Authentication\n\nThe session token expires after 30 minutes of inactivity.", "create auth", ""); err != nil {
		t.Fatalf("PutWikiPage: %v", err)
	}
	svc.Wg.Wait()
	if err := svc.DB.Migrator().DropTable(&db.WikiSearchDocument{}); err != nil {
		t.Fatalf("drop wiki search table: %v", err)
	}

	resp, err := svc.SearchWikiPages(ctx, full, "token expires", 20, 0)
	if err != nil {
		t.Fatalf("SearchWikiPages(index unavailable): %v", err)
	}
	if resp.Method != "substring" {
		t.Fatalf("method = %q, want substring", resp.Method)
	}
	if len(resp.Results) != 1 || resp.Results[0].Slug != "tutorial/auth" {
		t.Fatalf("fallback results = %#v, want tutorial/auth", resp.Results)
	}
}

func TestWikiSearchSemanticAndReindex_Issue1362(t *testing.T) {
	embedder := semanticWikiEmbedder{}
	svc, cleanup := testharness.NewService(t, testharness.ServiceConfig{Embedder: embedder})
	defer cleanup()
	ctx := context.Background()
	if err := svc.DB.Create(&db.User{
		Login: "testuser",
		Name:  "Test User",
		Type:  db.TypeUser,
	}).Error; err != nil {
		t.Fatalf("seed owner: %v", err)
	}

	if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: "testuser",
		Name:       "wiki-semantic",
		AutoInit:   true,
	}); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	full := "testuser/wiki-semantic"

	if _, err := svc.PutWikiPage(ctx, full, "ops/session-expiry", "# Sessions\n\nSession expiry depends on tenant policy.", "create sessions", ""); err != nil {
		t.Fatalf("PutWikiPage(first): %v", err)
	}
	if _, err := svc.PutWikiPage(ctx, full, "ops/cache", "# Cache\n\nCache invalidation guide.", "create cache", ""); err != nil {
		t.Fatalf("PutWikiPage(second): %v", err)
	}
	svc.Wg.Wait()

	resp, err := svc.SearchWikiPages(ctx, full, "how do we handle session expiry", 20, 0)
	if err != nil {
		t.Fatalf("SearchWikiPages(semantic): %v", err)
	}
	if resp.Method != "vector" {
		t.Fatalf("method = %q, want vector", resp.Method)
	}
	if len(resp.Results) == 0 || resp.Results[0].Slug != "ops/session-expiry" {
		t.Fatalf("semantic results = %#v, want ops/session-expiry first", resp.Results)
	}

	resp, err = svc.SearchWikiPages(ctx, full, "billing export retention", 20, 0)
	if err != nil {
		t.Fatalf("SearchWikiPages(unrelated): %v", err)
	}
	if resp.Method != "substring" {
		t.Fatalf("unrelated method = %q, want substring fallback", resp.Method)
	}
	if len(resp.Results) != 0 {
		t.Fatalf("unrelated results = %#v, want empty", resp.Results)
	}

	if err := svc.DB.Where("repository_id > 0").Delete(&db.WikiSearchDocument{}).Error; err != nil {
		t.Fatalf("clear search docs: %v", err)
	}
	count, err := svc.ReindexWikiSearch(ctx, full)
	if err != nil {
		t.Fatalf("ReindexWikiSearch: %v", err)
	}
	if count != 2 {
		t.Fatalf("ReindexWikiSearch count = %d, want 2", count)
	}
	resp, err = svc.SearchWikiPages(ctx, full, "session expiry", 20, 0)
	if err != nil {
		t.Fatalf("SearchWikiPages(after reindex): %v", err)
	}
	if len(resp.Results) == 0 {
		t.Fatal("expected results after reindex")
	}
}
