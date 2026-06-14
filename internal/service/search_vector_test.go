package service_test

import (
	"context"
	"testing"

	"github.com/ngaut/agent-git-service/internal/embedding"
	"github.com/ngaut/agent-git-service/internal/service"
)

// TestSearchIssues_EmbeddingFailureGracefulDegradation tests that search falls back to LIKE
// when embedding API fails or is unavailable.
func TestSearchIssues_EmbeddingFailureGracefulDegradation(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	// Setup failing embedder
	svc.Embedder = &failingEmbedder{}

	setupRepoForTest(t, svc, "failtest", "failtest-repo")

	// Create repo and issue
	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "failtest/failtest-repo",
		Title:        "Test issue with unique keyword",
		Body:         "This issue has a UNIQUEKEYWORD12345 in the body",
		AuthorLogin:  "failtest",
	})
	if err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// Search should still work via LIKE fallback
	results, err := svc.SearchIssues(ctx, "repo:failtest/failtest-repo UNIQUEKEYWORD12345")
	if err != nil {
		t.Fatalf("SearchIssues failed: %v", err)
	}

	// Verify issue is found via LIKE
	found := false
	for _, r := range results {
		if r.ID == issue.ID {
			found = true
			break
		}
	}
	if !found {
		t.Error("Expected issue to be found via LIKE fallback when embedding fails")
	}
}

// TestSearchIssues_WithNopEmbedder tests that search works correctly with NopEmbedder.
func TestSearchIssues_WithNopEmbedder(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	// Ensure embedder is NopEmbedder (default when no API key)
	svc.Embedder = embedding.NopEmbedder{}

	setupRepoForTest(t, svc, "noptest", "noptest-repo")

	// Create repo and issue
	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: "noptest/noptest-repo",
		Title:        "Test issue nop embedder",
		Body:         "This issue tests NOP embedder behavior",
		AuthorLogin:  "noptest",
	})
	if err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// Search should work via LIKE
	results, err := svc.SearchIssues(ctx, "repo:noptest/noptest-repo NOP")
	if err != nil {
		t.Fatalf("SearchIssues failed: %v", err)
	}

	// Verify issue is found
	found := false
	for _, r := range results {
		if r.ID == issue.ID {
			found = true
			break
		}
	}
	if !found {
		t.Error("Expected issue to be found with NopEmbedder")
	}
}

// TestSearchPRs_WithNopEmbedder tests PR search with NopEmbedder.
func TestSearchPRs_WithNopEmbedder(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	svc.Embedder = embedding.NopEmbedder{}

	setupRepoForTest(t, svc, "prnoptest", "prnoptest-repo")

	// Create PR
	pr, err := svc.CreatePR(ctx, service.CreatePRInput{
		RepoFullName: "prnoptest/prnoptest-repo",
		Title:        "Test PR nop embedder",
		Body:         "This PR tests NOP embedder",
		AuthorLogin:  "prnoptest",
		HeadRef:      "test-branch",
		BaseRef:      "main",
	})
	if err != nil {
		t.Fatalf("CreatePR failed: %v", err)
	}

	// Search should work via LIKE
	results, err := svc.SearchPRs(ctx, "repo:prnoptest/prnoptest-repo PR")
	if err != nil {
		t.Fatalf("SearchPRs failed: %v", err)
	}

	// Verify PR is found
	found := false
	for _, r := range results {
		if r.ID == pr.ID {
			found = true
			break
		}
	}
	if !found {
		t.Error("Expected PR to be found with NopEmbedder")
	}
}

type failingEmbedder struct{}

func (f *failingEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	return nil, ctx.Err()
}

func (f *failingEmbedder) Dimensions() int {
	return 0
}
