package gitstore_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/ngaut/agent-git-service/internal/gitstore"
)

// TestStore_SearchCommits tests the SearchCommits function
func TestStore_SearchCommits(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-search-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	repoName := "user/repo-search"

	// Initialize repo
	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Create test commits with known messages using WriteFile
	if _, err := store.WriteFile(ctx, repoName, "main", "file1.txt", "initial commit: add file1", []byte("content1\n")); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	if _, err := store.WriteFile(ctx, repoName, "main", "file2.txt", "feature: add file2 for testing", []byte("content2\n")); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	if _, err := store.WriteFile(ctx, repoName, "main", "file1.txt", "fix: update file1 content", []byte("updated content1\n")); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	t.Run("valid query finds commits", func(t *testing.T) {
		results, err := store.SearchCommits(ctx, repoName, "file", nil)
		if err != nil {
			t.Fatalf("SearchCommits failed: %v", err)
		}
		if len(results) == 0 {
			t.Error("expected to find commits containing 'file'")
		}
		// Verify result structure
		for _, r := range results {
			if r.SHA == "" {
				t.Error("expected non-empty SHA")
			}
			if r.Message == "" {
				t.Error("expected non-empty Message")
			}
		}
	})

	t.Run("query with author filter", func(t *testing.T) {
		// All commits use "gh-server" as author, so filtering by it should match
		filters := &gitstore.CommitSearchFilters{Author: "gh-server"}
		results, err := store.SearchCommits(ctx, repoName, "file", filters)
		if err != nil {
			t.Fatalf("SearchCommits failed: %v", err)
		}
		if len(results) == 0 {
			t.Error("expected to find commits with matching author")
		}
	})

	t.Run("query with non-matching author filter", func(t *testing.T) {
		filters := &gitstore.CommitSearchFilters{Author: "nonexistent-author-xyz"}
		results, err := store.SearchCommits(ctx, repoName, "file", filters)
		if err != nil {
			t.Fatalf("SearchCommits failed: %v", err)
		}
		if len(results) != 0 {
			t.Errorf("expected 0 results for non-matching author, got %d", len(results))
		}
	})

	t.Run("query with no matches returns empty", func(t *testing.T) {
		results, err := store.SearchCommits(ctx, repoName, "nonexistent_keyword_xyz123", nil)
		if err != nil {
			t.Fatalf("SearchCommits failed: %v", err)
		}
		if len(results) != 0 {
			t.Errorf("expected 0 results, got %d", len(results))
		}
	})

	t.Run("empty query", func(t *testing.T) {
		results, err := store.SearchCommits(ctx, repoName, "", nil)
		if err != nil {
			t.Fatalf("SearchCommits failed: %v", err)
		}
		// Empty query should match all commits (git behavior)
		if len(results) == 0 {
			t.Error("expected commits for empty query")
		}
	})

	t.Run("special character query", func(t *testing.T) {
		results, err := store.SearchCommits(ctx, repoName, "file*", nil)
		if err != nil {
			t.Fatalf("SearchCommits failed: %v", err)
		}
		// Should not error, may return 0 or more results depending on git behavior
		t.Logf("Special char query returned %d results", len(results))
	})

	t.Run("nonexistent repo returns error", func(t *testing.T) {
		_, err := store.SearchCommits(ctx, "nonexistent/repo", "test", nil)
		if err == nil {
			t.Error("expected error for nonexistent repo")
		}
	})
}

// TestStore_ListCommits tests the ListCommits function
func TestStore_ListCommits(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-list-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	repoName := "user/repo-list"

	// Initialize repo
	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Create 10 test commits
	for i := 1; i <= 10; i++ {
		filename := "file" + string(rune('0'+i)) + ".txt"
		content := "content" + string(rune('0'+i)) + "\n"
		message := "commit " + string(rune('0'+i))
		if _, err := store.WriteFile(ctx, repoName, "main", filename, message, []byte(content)); err != nil {
			t.Fatalf("WriteFile failed: %v", err)
		}
	}

	t.Run("default maxCount", func(t *testing.T) {
		results, err := store.ListCommits(ctx, repoName, 0, nil)
		if err != nil {
			t.Fatalf("ListCommits failed: %v", err)
		}
		if len(results) == 0 {
			t.Error("expected commits with default maxCount")
		}
	})

	t.Run("negative maxCount uses default", func(t *testing.T) {
		results, err := store.ListCommits(ctx, repoName, -5, nil)
		if err != nil {
			t.Fatalf("ListCommits failed: %v", err)
		}
		if len(results) == 0 {
			t.Error("expected commits with negative maxCount")
		}
	})

	t.Run("specific maxCount", func(t *testing.T) {
		results, err := store.ListCommits(ctx, repoName, 5, nil)
		if err != nil {
			t.Fatalf("ListCommits failed: %v", err)
		}
		if len(results) != 5 {
			t.Errorf("expected 5 results, got %d", len(results))
		}
	})

	t.Run("maxCount greater than commits", func(t *testing.T) {
		results, err := store.ListCommits(ctx, repoName, 100, nil)
		if err != nil {
			t.Fatalf("ListCommits failed: %v", err)
		}
		// Note: Init with seed=true adds an initial commit, so we have 11 total (1 seed + 10 test commits)
		if len(results) != 11 {
			t.Errorf("expected 11 results (1 seed + 10 test), got %d", len(results))
		}
	})

	t.Run("list all commits is uncapped", func(t *testing.T) {
		fullRepoName := "user/list-all-commits"
		if err := store.Init(ctx, fullRepoName, "main", true); err != nil {
			t.Fatalf("Init failed: %v", err)
		}
		for i := 1; i <= 35; i++ {
			if _, err := store.WriteFile(ctx, fullRepoName, "main", "history.txt", fmt.Sprintf("commit %d", i), []byte(fmt.Sprintf("content %d\n", i))); err != nil {
				t.Fatalf("WriteFile #%d failed: %v", i, err)
			}
		}

		results, err := store.ListAllCommits(ctx, fullRepoName, nil)
		if err != nil {
			t.Fatalf("ListAllCommits failed: %v", err)
		}
		if len(results) != 36 {
			t.Fatalf("expected 36 results (1 seed + 35 test), got %d", len(results))
		}
		if results[0].Message != "commit 35" || results[len(results)-1].Message == "commit 6" {
			t.Fatalf("unexpected list-all ordering: first=%q last=%q", results[0].Message, results[len(results)-1].Message)
		}
	})

	t.Run("list commits range", func(t *testing.T) {
		fullRepoName := "user/list-commits-range"
		if err := store.Init(ctx, fullRepoName, "main", true); err != nil {
			t.Fatalf("Init failed: %v", err)
		}
		base, err := store.HeadSHA(ctx, fullRepoName, "main")
		if err != nil {
			t.Fatalf("HeadSHA(base): %v", err)
		}
		if _, err := store.WriteFile(ctx, fullRepoName, "main", "history.txt", "range commit 1", []byte("content 1\n")); err != nil {
			t.Fatalf("WriteFile #1 failed: %v", err)
		}
		head, err := store.WriteFile(ctx, fullRepoName, "main", "history.txt", "range commit 2", []byte("content 2\n"))
		if err != nil {
			t.Fatalf("WriteFile #2 failed: %v", err)
		}

		results, err := store.ListCommitsRange(ctx, fullRepoName, base, head, nil)
		if err != nil {
			t.Fatalf("ListCommitsRange failed: %v", err)
		}
		if len(results) != 2 {
			t.Fatalf("expected 2 range commits, got %d", len(results))
		}
		if results[0].Message != "range commit 2" || results[1].Message != "range commit 1" {
			t.Fatalf("unexpected range ordering: %+v", results)
		}
	})

	t.Run("empty repo returns seed commit", func(t *testing.T) {
		emptyRepoName := "user/empty-repo"
		if err := store.Init(ctx, emptyRepoName, "main", true); err != nil {
			t.Fatalf("Init failed: %v", err)
		}
		results, err := store.ListCommits(ctx, emptyRepoName, 10, nil)
		if err != nil {
			t.Fatalf("ListCommits failed: %v", err)
		}
		// Init with seed=true creates an initial commit
		if len(results) != 1 {
			t.Errorf("expected 1 result (seed commit) for initialized repo, got %d", len(results))
		}
	})

	t.Run("nonexistent repo returns error", func(t *testing.T) {
		_, err := store.ListCommits(ctx, "nonexistent/repo", 10, nil)
		if err == nil {
			t.Error("expected error for nonexistent repo")
		}
	})
}

// TestStore_ListCommits_PathFilter tests the path filtering feature of ListCommits
func TestStore_ListCommits_PathFilter(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-pathfilter-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	repoName := "user/repo-pathfilter"

	// Initialize repo
	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Create test commits with specific file paths
	// Commit 1: modify file1.txt
	if _, err := store.WriteFile(ctx, repoName, "main", "file1.txt", "add file1", []byte("content1\n")); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	// Commit 2: modify file2.txt
	if _, err := store.WriteFile(ctx, repoName, "main", "file2.txt", "add file2", []byte("content2\n")); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	// Commit 3: modify file1.txt again
	if _, err := store.WriteFile(ctx, repoName, "main", "file1.txt", "update file1", []byte("updated content1\n")); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	// Commit 4: modify skills/weekly-report/report.md
	if _, err := store.WriteFile(ctx, repoName, "main", "skills/weekly-report/report.md", "add weekly report", []byte("report content\n")); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	// Commit 5: modify skills/weekly-report/summary.md
	if _, err := store.WriteFile(ctx, repoName, "main", "skills/weekly-report/summary.md", "add summary", []byte("summary content\n")); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	// Commit 6: modify knowledge/api-rate-limiting.md
	if _, err := store.WriteFile(ctx, repoName, "main", "knowledge/api-rate-limiting.md", "add api docs", []byte("api docs content\n")); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	t.Run("filter by specific file returns only commits touching that file", func(t *testing.T) {
		opts := &gitstore.ListCommitsOptions{Path: "file1.txt"}
		results, err := store.ListCommits(ctx, repoName, 10, opts)
		if err != nil {
			t.Fatalf("ListCommits failed: %v", err)
		}
		// Should return 2 commits: "add file1" and "update file1" (plus seed commit if it exists)
		// The seed commit doesn't touch file1.txt, so we expect exactly 2
		if len(results) != 2 {
			t.Errorf("expected 2 results for file1.txt, got %d", len(results))
		}
		// Verify messages
		foundAdd, foundUpdate := false, false
		for _, r := range results {
			if r.Message == "add file1" {
				foundAdd = true
			}
			if r.Message == "update file1" {
				foundUpdate = true
			}
		}
		if !foundAdd || !foundUpdate {
			t.Errorf("expected commits 'update file1' and 'add file1', got %v", results)
		}
	})

	t.Run("path filter preserves subjects containing pipes", func(t *testing.T) {
		const pipeMessage = "revision 3 | keep metadata"
		if _, err := store.WriteFile(ctx, repoName, "main", "file1.txt", pipeMessage, []byte("pipe content\n")); err != nil {
			t.Fatalf("WriteFile failed: %v", err)
		}

		results, err := store.ListCommits(ctx, repoName, 10, &gitstore.ListCommitsOptions{Path: "file1.txt"})
		if err != nil {
			t.Fatalf("ListCommits failed: %v", err)
		}
		if len(results) != 3 {
			t.Fatalf("expected 3 results for file1.txt after pipe commit, got %d", len(results))
		}
		if results[0].Message != pipeMessage {
			t.Fatalf("message = %q, want %q", results[0].Message, pipeMessage)
		}
		if results[0].Committer == "" || results[0].CommitterEmail == "" || results[0].CommitterDate == "" {
			t.Fatalf("expected committer metadata to remain populated, got %+v", results[0])
		}
	})

	t.Run("filter by directory returns commits touching files in that directory", func(t *testing.T) {
		opts := &gitstore.ListCommitsOptions{Path: "skills/weekly-report"}
		results, err := store.ListCommits(ctx, repoName, 10, opts)
		if err != nil {
			t.Fatalf("ListCommits failed: %v", err)
		}
		// Should return 2 commits: "add weekly report" and "add summary"
		if len(results) != 2 {
			t.Errorf("expected 2 results for skills/weekly-report, got %d", len(results))
		}
	})

	t.Run("filter by specific file in subdirectory", func(t *testing.T) {
		opts := &gitstore.ListCommitsOptions{Path: "knowledge/api-rate-limiting.md"}
		results, err := store.ListCommits(ctx, repoName, 10, opts)
		if err != nil {
			t.Fatalf("ListCommits failed: %v", err)
		}
		// Should return 1 commit: "add api docs"
		if len(results) != 1 {
			t.Errorf("expected 1 result for knowledge/api-rate-limiting.md, got %d", len(results))
		}
		if len(results) > 0 && results[0].Message != "add api docs" {
			t.Errorf("expected 'add api docs', got %s", results[0].Message)
		}
	})

	t.Run("filter by nonexistent path returns empty", func(t *testing.T) {
		opts := &gitstore.ListCommitsOptions{Path: "nonexistent/path/file.txt"}
		results, err := store.ListCommits(ctx, repoName, 10, opts)
		if err != nil {
			t.Fatalf("ListCommits failed: %v", err)
		}
		if len(results) != 0 {
			t.Errorf("expected 0 results for nonexistent path, got %d", len(results))
		}
	})

	t.Run("no path filter returns all commits", func(t *testing.T) {
		results, err := store.ListCommits(ctx, repoName, 100, nil)
		if err != nil {
			t.Fatalf("ListCommits failed: %v", err)
		}
		// Should return all commits (seed + 7 test commits = 8, including the pipe-subject regression case)
		if len(results) != 8 {
			t.Errorf("expected 8 results without path filter, got %d", len(results))
		}
	})

	t.Run("empty path in opts returns all commits", func(t *testing.T) {
		opts := &gitstore.ListCommitsOptions{Path: ""}
		results, err := store.ListCommits(ctx, repoName, 100, opts)
		if err != nil {
			t.Fatalf("ListCommits failed: %v", err)
		}
		// Should return all commits (seed + 7 test commits = 8, including the pipe-subject regression case)
		if len(results) != 8 {
			t.Errorf("expected 8 results with empty path, got %d", len(results))
		}
	})
}

// TestStore_SearchCode tests the SearchCode function
func TestStore_SearchCode(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-searchcode-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	repoName := "user/repo-searchcode"

	// Initialize repo
	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Create files with known content
	if _, err := store.WriteFile(ctx, repoName, "main", "main.go", "add main.go", []byte("package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n")); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	if _, err := store.WriteFile(ctx, repoName, "main", "utils.go", "add utils.go", []byte("package utils\n\nfunc Helper() {}\n")); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	if _, err := store.WriteFile(ctx, repoName, "main", "internal/foo.go", "add internal/foo.go", []byte("package internal\n\nfunc Foo() {}\n")); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	if _, err := store.WriteFile(ctx, repoName, "main", "Internal/Bar.go", "add Internal/Bar.go", []byte("package internal\n\nfunc Bar() {}\n")); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	if _, err := store.WriteFile(ctx, repoName, "main", "src/internal/Baz.go", "add src/internal/Baz.go", []byte("package internal\n\nfunc Baz() {}\n")); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	if _, err := store.WriteFile(ctx, repoName, "main", "docs/notes.txt", "add docs/notes.txt", []byte("func notes\n")); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	t.Run("valid query finds files", func(t *testing.T) {
		results, err := store.SearchCode(ctx, repoName, []string{"func"}, nil, false)
		if err != nil {
			t.Fatalf("SearchCode failed: %v", err)
		}
		if len(results) == 0 {
			t.Error("expected to find files containing 'func'")
		}
		// Verify result structure
		for _, r := range results {
			if r.Path == "" {
				t.Error("expected non-empty Path")
			}
		}
	})

	t.Run("query with content returns lines", func(t *testing.T) {
		results, err := store.SearchCode(ctx, repoName, []string{"func"}, nil, true)
		if err != nil {
			t.Fatalf("SearchCode failed: %v", err)
		}
		if len(results) == 0 {
			t.Error("expected to find files containing 'func'")
		}
		// Verify content is populated
		for _, r := range results {
			if r.Content == "" {
				t.Errorf("expected non-empty Content for path %s", r.Path)
			}
			if !strings.Contains(strings.ToLower(r.Content), "func") {
				t.Errorf("expected content to contain 'func', got: %s", r.Content)
			}
		}
	})

	t.Run("filename filter", func(t *testing.T) {
		filters := &gitstore.CodeSearchFilters{Filename: "main.go"}
		results, err := store.SearchCode(ctx, repoName, []string{"func"}, filters, false)
		if err != nil {
			t.Fatalf("SearchCode failed: %v", err)
		}
		if len(results) == 0 {
			t.Error("expected to find main.go")
		}
		for _, r := range results {
			if !strings.HasSuffix(r.Path, "main.go") {
				t.Errorf("expected main.go, got %s", r.Path)
			}
		}
	})

	t.Run("extension filter", func(t *testing.T) {
		filters := &gitstore.CodeSearchFilters{Extensions: []string{".go"}}
		results, err := store.SearchCode(ctx, repoName, []string{"func"}, filters, false)
		if err != nil {
			t.Fatalf("SearchCode failed: %v", err)
		}
		if len(results) == 0 {
			t.Error("expected to find .go files")
		}
		for _, r := range results {
			if !strings.HasSuffix(r.Path, ".go") {
				t.Errorf("expected .go file, got %s", r.Path)
			}
		}
	})

	t.Run("path filter", func(t *testing.T) {
		filters := &gitstore.CodeSearchFilters{Path: "utils"}
		results, err := store.SearchCode(ctx, repoName, []string{"func"}, filters, false)
		if err != nil {
			t.Fatalf("SearchCode failed: %v", err)
		}
		if len(results) == 0 {
			t.Error("expected to find utils.go")
		}
		for _, r := range results {
			if !strings.Contains(r.Path, "utils") {
				t.Errorf("expected path to contain 'utils', got %s", r.Path)
			}
		}
	})

	t.Run("path filter case-insensitive", func(t *testing.T) {
		filters := &gitstore.CodeSearchFilters{Path: "internal"}
		results, err := store.SearchCode(ctx, repoName, []string{"func"}, filters, false)
		if err != nil {
			t.Fatalf("SearchCode failed: %v", err)
		}
		if len(results) == 0 {
			t.Error("expected to find internal paths")
		}
		found := make(map[string]bool)
		for _, r := range results {
			found[strings.ToLower(r.Path)] = true
		}
		expectedPaths := []string{"internal/foo.go", "internal/bar.go", "src/internal/baz.go"}
		for _, expected := range expectedPaths {
			if !found[expected] {
				t.Errorf("expected path %s in results", expected)
			}
		}
	})

	t.Run("path filter with leading slash", func(t *testing.T) {
		filters := &gitstore.CodeSearchFilters{Path: "/internal"}
		results, err := store.SearchCode(ctx, repoName, []string{"func"}, filters, false)
		if err != nil {
			t.Fatalf("SearchCode failed: %v", err)
		}
		if len(results) == 0 {
			t.Error("expected to find internal paths with leading slash filter")
		}
		found := false
		for _, r := range results {
			if strings.HasPrefix(strings.ToLower(r.Path), "internal/") {
				found = true
				break
			}
		}
		if !found {
			t.Error("expected to find internal paths with leading slash filter")
		}
	})

	t.Run("filename glob filter", func(t *testing.T) {
		filters := &gitstore.CodeSearchFilters{Filename: "*.GO"}
		results, err := store.SearchCode(ctx, repoName, []string{"func"}, filters, false)
		if err != nil {
			t.Fatalf("SearchCode failed: %v", err)
		}
		if len(results) == 0 {
			t.Error("expected to find .go files with glob filter")
		}
		for _, r := range results {
			if !strings.HasSuffix(strings.ToLower(r.Path), ".go") {
				t.Errorf("expected .go file, got %s", r.Path)
			}
		}
	})

	t.Run("filters intersect", func(t *testing.T) {
		filters := &gitstore.CodeSearchFilters{
			Path:       "internal",
			Extensions: []string{".txt"},
		}
		results, err := store.SearchCode(ctx, repoName, []string{"func"}, filters, false)
		if err != nil {
			t.Fatalf("SearchCode failed: %v", err)
		}
		if len(results) != 0 {
			t.Errorf("expected no results for intersecting filters, got %d", len(results))
		}
	})

	t.Run("language filter matches go files", func(t *testing.T) {
		filters := &gitstore.CodeSearchFilters{Language: "go"}
		results, err := store.SearchCode(ctx, repoName, []string{"func"}, filters, false)
		if err != nil {
			t.Fatalf("SearchCode failed: %v", err)
		}
		if len(results) == 0 {
			t.Error("expected to find .go files with language filter")
		}
		for _, r := range results {
			if !strings.HasSuffix(strings.ToLower(r.Path), ".go") {
				t.Errorf("expected .go file, got %s", r.Path)
			}
		}
	})

	t.Run("language filter excludes non-matching files", func(t *testing.T) {
		filters := &gitstore.CodeSearchFilters{Language: "go"}
		results, err := store.SearchCode(ctx, repoName, []string{"func"}, filters, false)
		if err != nil {
			t.Fatalf("SearchCode failed: %v", err)
		}
		// docs/notes.txt contains "func" but is not a .go file
		for _, r := range results {
			if strings.HasSuffix(strings.ToLower(r.Path), ".txt") {
				t.Errorf("language filter should exclude .txt files, got %s", r.Path)
			}
		}
	})

	t.Run("language and filename filters combined", func(t *testing.T) {
		filters := &gitstore.CodeSearchFilters{
			Language: "go",
			Filename: "main.go",
		}
		results, err := store.SearchCode(ctx, repoName, []string{"func"}, filters, false)
		if err != nil {
			t.Fatalf("SearchCode failed: %v", err)
		}
		if len(results) == 0 {
			t.Error("expected to find main.go with language and filename filters")
		}
		for _, r := range results {
			if !strings.HasSuffix(strings.ToLower(r.Path), "main.go") {
				t.Errorf("expected main.go, got %s", r.Path)
			}
		}
	})

	t.Run("language filter unknown language returns empty", func(t *testing.T) {
		filters := &gitstore.CodeSearchFilters{Language: "nonexistent_lang_xyz"}
		results, err := store.SearchCode(ctx, repoName, []string{"func"}, filters, false)
		if err != nil {
			t.Fatalf("SearchCode failed: %v", err)
		}
		if len(results) != 0 {
			t.Errorf("expected 0 results for unknown language, got %d", len(results))
		}
	})

	t.Run("query with no matches returns empty", func(t *testing.T) {
		results, err := store.SearchCode(ctx, repoName, []string{"nonexistent_keyword_xyz123"}, nil, false)
		if err != nil {
			t.Fatalf("SearchCode failed: %v", err)
		}
		if len(results) != 0 {
			t.Errorf("expected 0 results, got %d", len(results))
		}
	})

	t.Run("multi-token query with AND semantics", func(t *testing.T) {
		// Search for files containing both "func" AND "main"
		// Only main.go should match (contains both terms)
		results, err := store.SearchCode(ctx, repoName, []string{"func", "main"}, nil, false)
		if err != nil {
			t.Fatalf("SearchCode failed: %v", err)
		}
		if len(results) == 0 {
			t.Error("expected to find files containing both 'func' AND 'main'")
		}
		// Verify all results contain both terms
		for _, r := range results {
			if !strings.Contains(strings.ToLower(r.Path), "main.go") {
				t.Errorf("expected main.go in results for multi-token query, got %s", r.Path)
			}
		}
	})

	t.Run("empty query", func(t *testing.T) {
		results, err := store.SearchCode(ctx, repoName, []string{}, nil, false)
		if err != nil {
			t.Fatalf("SearchCode failed: %v", err)
		}
		// Empty query behavior depends on git grep
		t.Logf("Empty query returned %d results", len(results))
	})

	t.Run("nonexistent repo returns error", func(t *testing.T) {
		_, err := store.SearchCode(ctx, "nonexistent/repo", []string{"test"}, nil, false)
		if err == nil {
			t.Error("expected error for nonexistent repo")
		}
	})
}

// TestStore_Contributors tests the Contributors function
func TestStore_Contributors(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-contributors-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	repoName := "user/repo-contributors"

	// Initialize repo
	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	t.Run("single contributor", func(t *testing.T) {
		// The initial commit is from the seed (defaultCommitName/defaultCommitEmail)
		// Add one more commit
		if _, err := store.WriteFile(ctx, repoName, "main", "file1.txt", "commit by user1", []byte("content1\n")); err != nil {
			t.Fatalf("WriteFile failed: %v", err)
		}

		contributors, err := store.Contributors(ctx, repoName)
		if err != nil {
			t.Fatalf("Contributors failed: %v", err)
		}
		// Should have at least 1 contributor (the seed commit author)
		if len(contributors) < 1 {
			t.Errorf("expected at least 1 contributor, got %d", len(contributors))
		}
	})

	t.Run("multiple contributors", func(t *testing.T) {
		// Create a new repo for this test to have control over contributors
		multiRepoName := "user/repo-multi-contributors"
		if err := store.Init(ctx, multiRepoName, "main", true); err != nil {
			t.Fatalf("Init failed: %v", err)
		}

		// Add files - note that WriteFile uses default identity
		if _, err := store.WriteFile(ctx, multiRepoName, "main", "file1.txt", "commit 1", []byte("content1\n")); err != nil {
			t.Fatalf("WriteFile failed: %v", err)
		}
		if _, err := store.WriteFile(ctx, multiRepoName, "main", "file2.txt", "commit 2", []byte("content2\n")); err != nil {
			t.Fatalf("WriteFile failed: %v", err)
		}

		contributors, err := store.Contributors(ctx, multiRepoName)
		if err != nil {
			t.Fatalf("Contributors failed: %v", err)
		}
		// At least should have the default contributor
		if len(contributors) < 1 {
			t.Errorf("expected at least 1 contributor, got %d: %v", len(contributors), contributors)
		}
		t.Logf("Contributors: %v", contributors)
	})

	t.Run("empty repo returns empty", func(t *testing.T) {
		emptyRepoName := "user/empty-repo-contrib"
		// Init with seed=false to create truly empty repo
		if err := store.Init(ctx, emptyRepoName, "main", false); err != nil {
			t.Fatalf("Init failed: %v", err)
		}
		contributors, err := store.Contributors(ctx, emptyRepoName)
		if err != nil {
			t.Fatalf("Contributors failed: %v", err)
		}
		if len(contributors) != 0 {
			t.Errorf("expected 0 contributors for empty repo, got %d: %v", len(contributors), contributors)
		}
	})

	t.Run("nonexistent repo returns error", func(t *testing.T) {
		_, err := store.Contributors(ctx, "nonexistent/repo")
		if err == nil {
			t.Error("expected error for nonexistent repo")
		}
	})
}

// TestStore_LogBetweenTags tests the LogBetweenTags function
func TestStore_LogBetweenTags(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-logtags-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	repoName := "user/repo-logtags"

	// Initialize repo
	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Create commits
	if _, err := store.WriteFile(ctx, repoName, "main", "file1.txt", "commit 1", []byte("content1\n")); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	sha1, err := store.WriteFile(ctx, repoName, "main", "file2.txt", "commit 2", []byte("content2\n"))
	if err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	sha2, err := store.WriteFile(ctx, repoName, "main", "file3.txt", "commit 3", []byte("content3\n"))
	if err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// Create tags using CreateTagIfNotExists
	if err := store.CreateTagIfNotExists(ctx, repoName, "v1.0", "v1.0 tag", sha1); err != nil {
		t.Fatalf("CreateTagIfNotExists failed: %v", err)
	}
	if err := store.CreateTagIfNotExists(ctx, repoName, "v2.0", "v2.0 tag", sha2); err != nil {
		t.Fatalf("CreateTagIfNotExists failed: %v", err)
	}

	t.Run("normal range between two tags", func(t *testing.T) {
		log, err := store.LogBetweenTags(ctx, repoName, "v1.0", "v2.0")
		if err != nil {
			t.Fatalf("LogBetweenTags failed: %v", err)
		}
		if log == "" {
			t.Error("expected non-empty log")
		}
		// v1.0 tag points to commit 2 (sha1), v2.0 tag points to commit 3 (sha2)
		// Range v1.0..v2.0 means commits after v1.0 up to v2.0, which is just commit 3
		if !contains(log, "commit 3") {
			t.Errorf("log should contain commit 3, got: %s", log)
		}
	})

	t.Run("missing from tag (empty string)", func(t *testing.T) {
		log, err := store.LogBetweenTags(ctx, repoName, "", "v2.0")
		if err != nil {
			t.Fatalf("LogBetweenTags failed: %v", err)
		}
		if log == "" {
			t.Error("expected non-empty log")
		}
		// Should contain all commits up to v2.0
		t.Logf("Log with empty from: %s", log)
	})

	t.Run("reversed tag order", func(t *testing.T) {
		log, err := store.LogBetweenTags(ctx, repoName, "v2.0", "v1.0")
		if err != nil {
			t.Fatalf("LogBetweenTags failed: %v", err)
		}
		// Reversed order should return empty or error depending on git behavior
		t.Logf("Reversed tag order returned: %s", log)
	})

	t.Run("non-existent from tag", func(t *testing.T) {
		_, err := store.LogBetweenTags(ctx, repoName, "v0.0", "v2.0")
		if err == nil {
			t.Error("expected error for non-existent tag")
		}
	})

	t.Run("non-existent to tag", func(t *testing.T) {
		_, err := store.LogBetweenTags(ctx, repoName, "v1.0", "v9.9")
		if err == nil {
			t.Error("expected error for non-existent tag")
		}
	})

	t.Run("nonexistent repo returns error", func(t *testing.T) {
		_, err := store.LogBetweenTags(ctx, "nonexistent/repo", "v1.0", "v2.0")
		if err == nil {
			t.Error("expected error for nonexistent repo")
		}
	})
}

// TestStore_SearchCommits_Filters tests commit search filter qualifiers
func TestStore_SearchCommits_Filters(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-filters-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	repoName := "user/repo-filters"

	// Initialize repo
	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Create test commits
	sha1, err := store.WriteFile(ctx, repoName, "main", "file1.txt", "initial commit", []byte("content1\n"))
	if err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	sha2, err := store.WriteFile(ctx, repoName, "main", "file2.txt", "feature: add file2", []byte("content2\n"))
	if err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	sha3, err := store.WriteFile(ctx, repoName, "main", "file3.txt", "fix: update file3", []byte("content3\n"))
	if err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	t.Run("hash filter matches prefix", func(t *testing.T) {
		// Filter by first 7 chars of sha2
		hashPrefix := sha2[:7]
		filters := &gitstore.CommitSearchFilters{Hash: hashPrefix}
		results, err := store.SearchCommits(ctx, repoName, "", filters)
		if err != nil {
			t.Fatalf("SearchCommits failed: %v", err)
		}
		if len(results) != 1 {
			t.Errorf("expected 1 result for hash filter, got %d", len(results))
		}
		if len(results) > 0 && results[0].SHA != sha2 {
			t.Errorf("expected SHA %s, got %s", sha2, results[0].SHA)
		}
	})

	t.Run("hash filter no match returns empty", func(t *testing.T) {
		filters := &gitstore.CommitSearchFilters{Hash: "nonexistent123456"}
		results, err := store.SearchCommits(ctx, repoName, "", filters)
		if err != nil {
			t.Fatalf("SearchCommits failed: %v", err)
		}
		if len(results) != 0 {
			t.Errorf("expected 0 results for non-matching hash, got %d", len(results))
		}
	})

	t.Run("parent filter matches child commit", func(t *testing.T) {
		// sha3's parent should be sha2
		filters := &gitstore.CommitSearchFilters{Parent: sha2[:7]}
		results, err := store.SearchCommits(ctx, repoName, "", filters)
		if err != nil {
			t.Fatalf("SearchCommits failed: %v", err)
		}
		// sha3 should have sha2 as parent
		if len(results) == 0 {
			t.Error("expected to find child commit with parent filter")
		}
	})

	t.Run("tree filter matches commit", func(t *testing.T) {
		// Get tree SHA from sha3 commit
		allResults, err := store.SearchCommits(ctx, repoName, "", nil)
		if err != nil {
			t.Fatalf("SearchCommits failed: %v", err)
		}
		var targetTree string
		for _, r := range allResults {
			if r.SHA == sha3 {
				targetTree = r.TreeSHA
				break
			}
		}
		if targetTree == "" {
			t.Skip("could not find tree SHA for test")
		}
		filters := &gitstore.CommitSearchFilters{Tree: targetTree[:7]}
		results, err := store.SearchCommits(ctx, repoName, "", filters)
		if err != nil {
			t.Fatalf("SearchCommits failed: %v", err)
		}
		if len(results) == 0 {
			t.Error("expected to find commit with tree filter")
		}
	})

	t.Run("committer filter matches", func(t *testing.T) {
		// All commits use "gh-server" as committer
		filters := &gitstore.CommitSearchFilters{Committer: "gh-server"}
		results, err := store.SearchCommits(ctx, repoName, "", filters)
		if err != nil {
			t.Fatalf("SearchCommits failed: %v", err)
		}
		if len(results) == 0 {
			t.Error("expected to find commits with matching committer")
		}
	})

	t.Run("committer filter no match returns empty", func(t *testing.T) {
		filters := &gitstore.CommitSearchFilters{Committer: "nonexistent-committer"}
		results, err := store.SearchCommits(ctx, repoName, "", filters)
		if err != nil {
			t.Fatalf("SearchCommits failed: %v", err)
		}
		if len(results) != 0 {
			t.Errorf("expected 0 results for non-matching committer, got %d", len(results))
		}
	})

	t.Run("author-date filter with >= operator", func(t *testing.T) {
		// Get the date of sha1
		allResults, err := store.SearchCommits(ctx, repoName, "", nil)
		if err != nil {
			t.Fatalf("SearchCommits failed: %v", err)
		}
		var targetDate string
		for _, r := range allResults {
			if r.SHA == sha1 {
				targetDate = r.Date[:10] // YYYY-MM-DD
				break
			}
		}
		if targetDate == "" {
			t.Skip("could not find date for test")
		}
		// Filter for commits on or after this date
		filters := &gitstore.CommitSearchFilters{AuthorDate: ">=" + targetDate}
		results, err := store.SearchCommits(ctx, repoName, "", filters)
		if err != nil {
			t.Fatalf("SearchCommits failed: %v", err)
		}
		if len(results) == 0 {
			t.Error("expected to find commits with author-date filter")
		}
	})

	t.Run("committer-date filter with range", func(t *testing.T) {
		// Use a wide date range that should match all commits
		filters := &gitstore.CommitSearchFilters{CommitterDate: "2020-01-01..2030-12-31"}
		results, err := store.SearchCommits(ctx, repoName, "", filters)
		if err != nil {
			t.Fatalf("SearchCommits failed: %v", err)
		}
		if len(results) == 0 {
			t.Error("expected to find commits with committer-date range filter")
		}
	})

	t.Run("merge filter false returns only non-merge commits", func(t *testing.T) {
		// All commits created so far are non-merge commits
		filters := &gitstore.CommitSearchFilters{Merge: boolPtr(false)}
		results, err := store.SearchCommits(ctx, repoName, "", filters)
		if err != nil {
			t.Fatalf("SearchCommits failed: %v", err)
		}
		if len(results) == 0 {
			t.Error("expected to find non-merge commits")
		}
		// Verify none are merge commits (have more than 1 parent)
		for _, r := range results {
			if len(r.ParentSHAs) > 1 {
				t.Errorf("expected non-merge commit, but %s has %d parents", r.SHA, len(r.ParentSHAs))
			}
		}
	})
}

// boolPtr returns a pointer to a bool value
func boolPtr(b bool) *bool {
	return &b
}
