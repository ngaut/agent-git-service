package gitstore_test

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"gh-server/internal/gitstore"
)

func TestGitStore_Pass13(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-pass13-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	repoName := "test/repo-stubs"

	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// 1. Create a base commit writing a file 'hello.txt'
	contentBase := []byte("line 1\nline 2\nline 3\n")
	baseSHA, err := store.WriteFile(ctx, repoName, "main", "hello.txt", "base commit", contentBase)
	if err != nil {
		t.Fatal(err)
	}

	// 2. Create a head commit modifying 'hello.txt'
	contentHead := []byte("line 1\nline 2 modified\nline 3\n")
	headSHA, err := store.WriteFile(ctx, repoName, "main", "hello.txt", "head commit", contentHead)
	if err != nil {
		t.Fatal(err)
	}

	// --- Test GetDiffHunk ---
	hunk, err := store.GetDiffHunk(ctx, repoName, baseSHA, headSHA, "hello.txt", 2)
	if err != nil {
		t.Fatalf("GetDiffHunk failed: %v", err)
	}
	if hunk == "" {
		t.Error("Expected hunk, got empty string")
	}
	if !strings.Contains(hunk, "@@ -1,3 +1,3 @@") || !strings.Contains(hunk, "+line 2 modified") {
		t.Errorf("Unexpected hunk content: %s", hunk)
	}

	// --- Test Archive ---
	var buf bytes.Buffer
	err = store.Archive(ctx, repoName, "zip", "main", "repo-1.0.0", &buf)
	if err != nil {
		t.Fatalf("Archive failed: %v", err)
	}
	if buf.Len() == 0 {
		t.Error("Archive wrote 0 bytes")
	}
	bReader := bytes.NewReader(buf.Bytes())
	zReader, err := zip.NewReader(bReader, int64(buf.Len()))
	if err != nil {
		t.Fatalf("Failed to open zip archive: %v", err)
	}
	foundHello := false
	for _, f := range zReader.File {
		if f.Name == "repo-1.0.0/hello.txt" {
			foundHello = true
			break
		}
	}
	if !foundHello {
		t.Error("hello.txt not found in archive")
	}

	// --- Test SimulateMerge ---
	// Let's create a feat.txt on a new branch to simulate a PR merge
	if err := store.CreateBranch(ctx, repoName, "feature", "main"); err != nil {
		t.Fatalf("CreateBranch failed: %v", err)
	}
	contentFeat := []byte("feature rules\n")
	featSHA, err := store.WriteFile(ctx, repoName, "feature", "feat.txt", "feat commit", contentFeat)
	if err != nil {
		t.Fatal(err)
	}

	// headSHA is on main, featSHA is on feature. Merge featSHA into headSHA
	mergeSHA, err := store.SimulateMerge(ctx, repoName, headSHA, featSHA)
	if err != nil {
		t.Fatalf("SimulateMerge failed: %v", err)
	}
	if mergeSHA == "" || mergeSHA == headSHA || mergeSHA == featSHA {
		t.Errorf("Invalid simulated merge SHA: %s", mergeSHA)
	}
}
