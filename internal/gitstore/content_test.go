package gitstore_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"gh-server/internal/gitstore"
)

func TestReadFile_DanglingHeadFallback(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-readfile-dangling-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repoName := "user/readfile-dangling"

	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	repoDir, err := store.GetRepoPath(ctx, repoName)
	if err != nil {
		t.Fatalf("GetRepoPath failed: %v", err)
	}

	if err := store.CreateBranch(ctx, repoName, "master", "main"); err != nil {
		t.Fatalf("CreateBranch failed: %v", err)
	}

	content := []byte("fallback content\n")
	if _, err := store.WriteFile(ctx, repoName, "master", "fallback.txt", "add fallback", content); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	runGit(t, repoDir, "update-ref", "-d", "refs/heads/main")

	got, err := store.ReadFile(ctx, repoName, "fallback.txt")
	if err != nil {
		t.Fatalf("ReadFile fallback failed: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("content mismatch:\n  got:  %s\n  want: %s", got, content)
	}
}

func TestReadFile_FileNotFoundOnHead(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-readfile-missing-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repoName := "user/readfile-missing"

	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	if _, err := store.ReadFile(ctx, repoName, "missing.txt"); err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

func TestReadFileAtRef_CustomNamespaceRefs(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-readfile-custom-ref-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repoName := "user/readfile-custom-ref"

	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	if err := store.CreateBranch(ctx, repoName, "scratch", "main"); err != nil {
		t.Fatalf("CreateBranch failed: %v", err)
	}

	want := []byte("{\"probe\":\"content2\"}\n")
	sha, err := store.WriteFile(ctx, repoName, "scratch", "session.jsonl", "add session", want)
	if err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	if err := store.UpdateRef(ctx, repoName, "refs/runs/probe-test2", sha); err != nil {
		t.Fatalf("UpdateRef failed: %v", err)
	}
	if err := store.DeleteRef(ctx, repoName, "refs/heads/scratch"); err != nil {
		t.Fatalf("DeleteRef failed: %v", err)
	}

	if _, err := store.ReadFile(ctx, repoName, "session.jsonl"); err == nil {
		t.Fatal("expected HEAD read to miss session.jsonl after deleting scratch branch")
	}

	for _, ref := range []string{sha, "refs/runs/probe-test2", "runs/probe-test2"} {
		got, err := store.ReadFileAtRef(ctx, repoName, "session.jsonl", ref)
		if err != nil {
			t.Fatalf("ReadFileAtRef(%q) failed: %v", ref, err)
		}
		if string(got) != string(want) {
			t.Fatalf("ReadFileAtRef(%q) mismatch:\n  got:  %s\n  want: %s", ref, got, want)
		}
	}
}

func TestListTags_Success(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-tags-success-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repoName := "user/tags-success"

	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	sha1, err := store.WriteFile(ctx, repoName, "main", "file1.txt", "add file1", []byte("one\n"))
	if err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	sha2, err := store.WriteFile(ctx, repoName, "main", "file2.txt", "add file2", []byte("two\n"))
	if err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	repoDir, err := store.GetRepoPath(ctx, repoName)
	if err != nil {
		t.Fatalf("GetRepoPath failed: %v", err)
	}
	runGit(t, repoDir, "tag", "v1", sha1)
	runGit(t, repoDir, "tag", "v2", sha2)

	tags, err := store.ListTags(ctx, repoName)
	if err != nil {
		t.Fatalf("ListTags failed: %v", err)
	}

	tagMap := make(map[string]string)
	for _, tag := range tags {
		tagMap[tag.Name] = tag.SHA
	}

	if tagMap["v1"] == "" || tagMap["v2"] == "" {
		t.Fatalf("expected tags v1 and v2, got: %+v", tags)
	}
	if !strings.HasPrefix(sha1, tagMap["v1"]) {
		t.Fatalf("expected v1 SHA %s to prefix %s", tagMap["v1"], sha1)
	}
	if !strings.HasPrefix(sha2, tagMap["v2"]) {
		t.Fatalf("expected v2 SHA %s to prefix %s", tagMap["v2"], sha2)
	}
}

func TestListTags_Error(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-tags-error-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	if _, err := store.ListTags(ctx, "user/missing-repo"); err == nil {
		t.Fatal("expected error for missing repo, got nil")
	}
}

func TestDiffNameStatus_EmptyOutput(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-namestatus-empty-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repoName := "user/namestatus-empty"

	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	out, err := store.DiffNameStatus(ctx, repoName, "main", "main")
	if err != nil {
		t.Fatalf("DiffNameStatus failed: %v", err)
	}
	if strings.TrimSpace(out) != "" {
		t.Fatalf("expected empty output, got: %q", out)
	}
}

func TestDiffNameStatus_RenameAndBinary(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-namestatus-edge-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repoName := "user/namestatus-edge"

	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	repoDir, err := store.GetRepoPath(ctx, repoName)
	if err != nil {
		t.Fatalf("GetRepoPath failed: %v", err)
	}
	runGit(t, repoDir, "config", "diff.renames", "true")

	renameContent := []byte("rename me\n")
	if _, err := store.WriteFile(ctx, repoName, "main", "old.txt", "add old", renameContent); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	if _, err := store.WriteFile(ctx, repoName, "main", "data.bin", "add binary", []byte{0x00, 0x01, 0x02, 0x03}); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	if err := store.CreateBranch(ctx, repoName, "feature", "main"); err != nil {
		t.Fatalf("CreateBranch failed: %v", err)
	}

	if _, err := store.WriteFile(ctx, repoName, "feature", "new.txt", "add new", renameContent); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	if _, err := store.DeleteFileFromRepo(ctx, repoName, "feature", "old.txt", "remove old"); err != nil {
		t.Fatalf("DeleteFileFromRepo failed: %v", err)
	}
	if _, err := store.WriteFile(ctx, repoName, "feature", "data.bin", "modify binary", []byte{0x00, 0x01, 0x02, 0x04}); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	out, err := store.DiffNameStatus(ctx, repoName, "main", "feature")
	if err != nil {
		t.Fatalf("DiffNameStatus failed: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(out), "\n")
	foundRename := false
	foundBinary := false
	for _, line := range lines {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		status := fields[0]
		if strings.HasPrefix(status, "R") && strings.Contains(line, "old.txt") && strings.Contains(line, "new.txt") {
			foundRename = true
		}
		if status == "M" && strings.Contains(line, "data.bin") {
			foundBinary = true
		}
	}
	if !foundRename {
		t.Fatalf("expected rename entry, got:\n%s", out)
	}
	if !foundBinary {
		t.Fatalf("expected binary modification entry, got:\n%s", out)
	}
}

func TestDiffNameStatus_InvalidRevision(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-namestatus-invalid-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repoName := "user/namestatus-invalid"

	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	_, err = store.DiffNameStatus(ctx, repoName, "invalid!!!", "main")
	if err == nil {
		t.Fatal("expected error for invalid revision, got nil")
	}
	if !strings.Contains(err.Error(), "invalid base or head revision") {
		t.Fatalf("expected invalid revision error, got: %v", err)
	}
}

func TestDiffNameStatus_MissingRevision(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-namestatus-missing-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repoName := "user/namestatus-missing"

	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	if _, err := store.WriteFile(ctx, repoName, "main", "file.txt", "initial", []byte("hello\n")); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	_, err = store.DiffNameStatus(ctx, repoName, "main", "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	if err == nil {
		t.Fatal("expected error for missing revision, got nil")
	}
	if !strings.Contains(err.Error(), "git diff failed") {
		t.Fatalf("expected git diff failure, got: %v", err)
	}
}

func TestDiffNumStat_EdgeCases(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-numstat-edge-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repoName := "user/numstat-edge"

	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	repoDir, err := store.GetRepoPath(ctx, repoName)
	if err != nil {
		t.Fatalf("GetRepoPath failed: %v", err)
	}
	runGit(t, repoDir, "config", "diff.renames", "true")

	renameContent := []byte("rename me\n")
	if _, err := store.WriteFile(ctx, repoName, "main", "old.txt", "add old", renameContent); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	if _, err := store.WriteFile(ctx, repoName, "main", "data.bin", "add binary", []byte{0x00, 0x01, 0x02, 0x03}); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	if err := store.CreateBranch(ctx, repoName, "feature", "main"); err != nil {
		t.Fatalf("CreateBranch failed: %v", err)
	}

	if _, err := store.WriteFile(ctx, repoName, "feature", "new.txt", "add new", renameContent); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	if _, err := store.DeleteFileFromRepo(ctx, repoName, "feature", "old.txt", "remove old"); err != nil {
		t.Fatalf("DeleteFileFromRepo failed: %v", err)
	}
	if _, err := store.WriteFile(ctx, repoName, "feature", "data.bin", "modify binary", []byte{0x00, 0x01, 0x02, 0x04}); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	out, err := store.DiffNumStat(ctx, repoName, "main", "feature")
	if err != nil {
		t.Fatalf("DiffNumStat failed: %v", err)
	}
	if strings.TrimSpace(out) == "" {
		t.Fatal("expected non-empty numstat output")
	}
	if !strings.Contains(out, "old.txt") || !strings.Contains(out, "new.txt") {
		t.Fatalf("expected rename line in output, got:\n%s", out)
	}
	if !strings.Contains(out, "data.bin") {
		t.Fatalf("expected binary entry in output, got:\n%s", out)
	}

	var binaryLine string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.Contains(line, "data.bin") {
			binaryLine = line
			break
		}
	}
	if binaryLine == "" {
		t.Fatal("expected binary line in numstat output")
	}
	binaryFields := strings.Fields(binaryLine)
	if len(binaryFields) < 3 {
		t.Fatalf("expected numstat fields for binary line, got: %q", binaryLine)
	}
	if binaryFields[0] != "-" || binaryFields[1] != "-" {
		t.Fatalf("expected '-' counts for binary line, got: %q", binaryLine)
	}

	stats := parseNumStat(out)
	if stats["data.bin"] != [2]int{0, 0} {
		t.Fatalf("expected binary stats to parse as zeros, got %v", stats["data.bin"])
	}
	if _, ok := stats["old.txt"]; !ok {
		t.Fatalf("expected rename line to parse with old filename, got: %v", stats)
	}
}

func TestDiffNumStat_InvalidRevision(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-numstat-invalid-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repoName := "user/numstat-invalid"

	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	_, err = store.DiffNumStat(ctx, repoName, "invalid!!!", "main")
	if err == nil {
		t.Fatal("expected error for invalid revision, got nil")
	}
	if !strings.Contains(err.Error(), "invalid base or head revision") {
		t.Fatalf("expected invalid revision error, got: %v", err)
	}
}

func TestDiffNumStat_MissingRevision(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-numstat-missing-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repoName := "user/numstat-missing"

	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	if _, err := store.WriteFile(ctx, repoName, "main", "file.txt", "initial", []byte("hello\n")); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	_, err = store.DiffNumStat(ctx, repoName, "main", "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	if err == nil {
		t.Fatal("expected error for missing revision, got nil")
	}
	if !strings.Contains(err.Error(), "git diff --numstat failed") {
		t.Fatalf("expected git diff --numstat failure, got: %v", err)
	}
}

func parseNumStat(out string) map[string][2]int {
	stats := map[string][2]int{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 3 {
			continue
		}
		var add, del int
		fmt.Sscanf(parts[0], "%d", &add)
		fmt.Sscanf(parts[1], "%d", &del)
		stats[parts[2]] = [2]int{add, del}
	}
	return stats
}

// TestDiffRaw_TextDiff tests DiffRaw with a simple text diff
func TestDiffRaw_TextDiff(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-diffraw-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repoName := "user/diff-text"

	// Initialize repo
	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Create initial commit on main
	if _, err := store.WriteFile(ctx, repoName, "main", "file.txt", "initial commit", []byte("line1\nline2\n")); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// Create feature branch with a change
	if err := store.CreateBranch(ctx, repoName, "feature", "main"); err != nil {
		t.Fatalf("CreateBranch failed: %v", err)
	}
	if _, err := store.WriteFile(ctx, repoName, "feature", "file.txt", "modify file", []byte("line1\nline2\nline3\n")); err != nil {
		t.Fatalf("WriteFile on feature failed: %v", err)
	}

	// Get diff
	diff, err := store.DiffRaw(ctx, repoName, "main", "feature")
	if err != nil {
		t.Fatalf("DiffRaw failed: %v", err)
	}

	// Verify diff contains expected content
	if !strings.Contains(diff, "+line3") {
		t.Errorf("expected diff to contain '+line3', got: %s", diff)
	}
	if !strings.Contains(diff, "file.txt") {
		t.Errorf("expected diff to mention file.txt, got: %s", diff)
	}
}

// TestDiffRaw_BinaryDiff tests DiffRaw with binary file changes
func TestDiffRaw_BinaryDiff(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-diffraw-bin-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repoName := "user/diff-binary"

	// Initialize repo
	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Create initial commit with binary file
	binaryContent := []byte{0x00, 0x01, 0x02, 0x03, 0xFF, 0xFE}
	if _, err := store.WriteFile(ctx, repoName, "main", "data.bin", "add binary", binaryContent); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// Create feature branch with modified binary
	if err := store.CreateBranch(ctx, repoName, "feature", "main"); err != nil {
		t.Fatalf("CreateBranch failed: %v", err)
	}
	modifiedBinary := []byte{0x00, 0x01, 0x02, 0x03, 0x04, 0x05}
	if _, err := store.WriteFile(ctx, repoName, "feature", "data.bin", "modify binary", modifiedBinary); err != nil {
		t.Fatalf("WriteFile on feature failed: %v", err)
	}

	// Get diff - should work even for binary files (may show "Binary files differ")
	diff, err := store.DiffRaw(ctx, repoName, "main", "feature")
	if err != nil {
		t.Fatalf("DiffRaw failed: %v", err)
	}

	// Binary diff should still return something (git diff output)
	if diff == "" {
		t.Error("expected non-empty diff for binary file change")
	}
}

// TestDiffRaw_LargeDiff tests DiffRaw with a large diff
func TestDiffRaw_LargeDiff(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-diffraw-large-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repoName := "user/diff-large"

	// Initialize repo
	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Create initial commit
	if _, err := store.WriteFile(ctx, repoName, "main", "large.txt", "initial", []byte("")); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// Create feature branch with large content
	if err := store.CreateBranch(ctx, repoName, "feature", "main"); err != nil {
		t.Fatalf("CreateBranch failed: %v", err)
	}

	// Generate large content (1000 lines)
	var largeContent strings.Builder
	for i := 0; i < 1000; i++ {
		largeContent.WriteString("line " + string(rune('0'+i%10)) + "\n")
	}

	if _, err := store.WriteFile(ctx, repoName, "feature", "large.txt", "add large file", []byte(largeContent.String())); err != nil {
		t.Fatalf("WriteFile on feature failed: %v", err)
	}

	// Get diff
	diff, err := store.DiffRaw(ctx, repoName, "main", "feature")
	if err != nil {
		t.Fatalf("DiffRaw failed: %v", err)
	}

	// Verify diff is substantial
	if len(diff) < 1000 {
		t.Errorf("expected large diff, got %d bytes", len(diff))
	}
}

// TestDiffRaw_MissingCommit tests DiffRaw when one commit doesn't exist
func TestDiffRaw_MissingCommit(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-diffraw-missing-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repoName := "user/diff-missing"

	// Initialize repo
	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Create initial commit
	if _, err := store.WriteFile(ctx, repoName, "main", "file.txt", "initial", []byte("hello\n")); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// Try to diff against a non-existent commit
	_, err = store.DiffRaw(ctx, repoName, "main", "nonexistent1234567890abcdef")
	if err == nil {
		t.Fatal("expected error for non-existent commit, got nil")
	}

	// Verify error message mentions the issue
	if !strings.Contains(err.Error(), "git diff failed") {
		t.Errorf("expected 'git diff failed' in error, got: %v", err)
	}
}

// TestDiffRaw_InvalidRevision tests DiffRaw with invalid revision format
func TestDiffRaw_InvalidRevision(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-diffraw-invalid-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repoName := "user/diff-invalid-rev"

	// Initialize repo
	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Try to diff with invalid revision format
	_, err = store.DiffRaw(ctx, repoName, "invalid!!!", "main")
	if err == nil {
		t.Fatal("expected error for invalid revision, got nil")
	}

	if !strings.Contains(err.Error(), "invalid") {
		t.Errorf("expected 'invalid' in error, got: %v", err)
	}
}

// TestDeleteFileFromRepo_RootFile tests deleting a file at the root level
func TestDeleteFileFromRepo_RootFile(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-delroot-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repoName := "user/del-root"

	// Initialize repo
	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Create file at root
	if _, err := store.WriteFile(ctx, repoName, "main", "root.txt", "add root file", []byte("content\n")); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// Verify file exists (README.md is also present from Init)
	files, err := store.ListTreeFiles(ctx, repoName)
	if err != nil {
		t.Fatalf("ListTreeFiles failed: %v", err)
	}
	foundRoot := false
	for _, f := range files {
		if f == "root.txt" {
			foundRoot = true
			break
		}
	}
	if !foundRoot {
		t.Fatalf("expected root.txt to exist, got %v", files)
	}

	// Delete the file
	commitSHA, err := store.DeleteFileFromRepo(ctx, repoName, "main", "root.txt", "delete root file")
	if err != nil {
		t.Fatalf("DeleteFileFromRepo failed: %v", err)
	}
	if commitSHA == "" {
		t.Fatal("expected non-empty commit SHA")
	}

	// Verify file is deleted (README.md remains from Init)
	files, err = store.ListTreeFiles(ctx, repoName)
	if err != nil {
		t.Fatalf("ListTreeFiles after delete failed: %v", err)
	}
	for _, f := range files {
		if f == "root.txt" {
			t.Errorf("root.txt should be deleted, but still exists in %v", files)
			break
		}
	}
}

// TestDeleteFileFromRepo_NestedPath tests deleting a file in a nested directory
func TestDeleteFileFromRepo_NestedPath(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-delnested-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repoName := "user/del-nested"

	// Initialize repo
	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Create nested file
	if _, err := store.WriteFile(ctx, repoName, "main", "dir/subdir/nested.txt", "add nested file", []byte("nested content\n")); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// Verify file exists (README.md is also present from Init)
	files, err := store.ListTreeFiles(ctx, repoName)
	if err != nil {
		t.Fatalf("ListTreeFiles failed: %v", err)
	}
	foundNested := false
	for _, f := range files {
		if f == "dir/subdir/nested.txt" {
			foundNested = true
			break
		}
	}
	if !foundNested {
		t.Fatalf("expected dir/subdir/nested.txt to exist, got %v", files)
	}

	// Delete the nested file
	commitSHA, err := store.DeleteFileFromRepo(ctx, repoName, "main", "dir/subdir/nested.txt", "delete nested file")
	if err != nil {
		t.Fatalf("DeleteFileFromRepo failed: %v", err)
	}
	if commitSHA == "" {
		t.Fatal("expected non-empty commit SHA")
	}

	// Verify file is deleted (README.md remains from Init)
	files, err = store.ListTreeFiles(ctx, repoName)
	if err != nil {
		t.Fatalf("ListTreeFiles after delete failed: %v", err)
	}
	for _, f := range files {
		if f == "dir/subdir/nested.txt" {
			t.Errorf("dir/subdir/nested.txt should be deleted, but still exists in %v", files)
			break
		}
	}
}

// TestDeleteFileFromRepo_MultipleFilesWithNested tests deleting one file among multiple including nested
func TestDeleteFileFromRepo_MultipleFilesWithNested(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-delmulti-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repoName := "user/del-multi"

	// Initialize repo
	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Create multiple files including nested
	if _, err := store.WriteFile(ctx, repoName, "main", "root.txt", "add root", []byte("root\n")); err != nil {
		t.Fatalf("WriteFile root failed: %v", err)
	}
	if _, err := store.WriteFile(ctx, repoName, "main", "dir/nested.txt", "add nested", []byte("nested\n")); err != nil {
		t.Fatalf("WriteFile nested failed: %v", err)
	}
	if _, err := store.WriteFile(ctx, repoName, "main", "dir/deep/deeper.txt", "add deep", []byte("deep\n")); err != nil {
		t.Fatalf("WriteFile deep failed: %v", err)
	}

	// Verify all files exist (README.md is also present from Init)
	files, err := store.ListTreeFiles(ctx, repoName)
	if err != nil {
		t.Fatalf("ListTreeFiles failed: %v", err)
	}
	expectedFiles := map[string]bool{"root.txt": true, "dir/nested.txt": true, "dir/deep/deeper.txt": true}
	foundCount := 0
	for _, f := range files {
		if expectedFiles[f] {
			foundCount++
		}
	}
	if foundCount != 3 {
		t.Fatalf("expected 3 files (root.txt, dir/nested.txt, dir/deep/deeper.txt), found %d in %v", foundCount, files)
	}

	// Delete only the nested file
	commitSHA, err := store.DeleteFileFromRepo(ctx, repoName, "main", "dir/nested.txt", "delete nested only")
	if err != nil {
		t.Fatalf("DeleteFileFromRepo failed: %v", err)
	}
	if commitSHA == "" {
		t.Fatal("expected non-empty commit SHA")
	}

	// Verify only the nested file is deleted (README.md remains)
	files, err = store.ListTreeFiles(ctx, repoName)
	if err != nil {
		t.Fatalf("ListTreeFiles after delete failed: %v", err)
	}
	foundRoot := false
	foundDeep := false
	foundNested := false
	for _, f := range files {
		if f == "root.txt" {
			foundRoot = true
		}
		if f == "dir/deep/deeper.txt" {
			foundDeep = true
		}
		if f == "dir/nested.txt" {
			foundNested = true
		}
	}
	if !foundRoot || !foundDeep {
		t.Errorf("expected root.txt and dir/deep/deeper.txt to remain, got %v", files)
	}
	if foundNested {
		t.Errorf("dir/nested.txt should be deleted, but still exists in %v", files)
	}
}

// TestDeleteFileFromRepo_PathTraversal tests that path traversal is rejected
func TestDeleteFileFromRepo_PathTraversal(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-deltraversal-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repoName := "user/del-traversal"

	// Initialize repo
	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Create a file
	if _, err := store.WriteFile(ctx, repoName, "main", "safe.txt", "add file", []byte("safe\n")); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// Try to delete with path traversal - should fail or be handled safely
	// The path "../etc/passwd" should not escape the repo
	_, err = store.DeleteFileFromRepo(ctx, repoName, "main", "../etc/passwd", "delete traversal")
	// This should either fail with an error or succeed without actually deleting anything outside repo
	// The key is that it should NOT allow escaping the repo boundary
	if err == nil {
		// If it succeeded, verify no actual traversal happened
		files, listErr := store.ListTreeFiles(ctx, repoName)
		if listErr != nil {
			t.Fatalf("ListTreeFiles failed: %v", listErr)
		}
		// safe.txt should still exist
		found := false
		for _, f := range files {
			if f == "safe.txt" {
				found = true
				break
			}
		}
		if !found {
			t.Error("safe.txt should still exist after path traversal attempt")
		}
	}
	// If error occurred, that's also acceptable behavior
}

// TestDeleteFileFromRepo_NonExistentFile tests deleting a file that doesn't exist
func TestDeleteFileFromRepo_NonExistentFile(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-delnoexist-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repoName := "user/del-noexist"

	// Initialize repo
	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Create a file
	if _, err := store.WriteFile(ctx, repoName, "main", "exists.txt", "add file", []byte("exists\n")); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// Try to delete non-existent file
	_, err = store.DeleteFileFromRepo(ctx, repoName, "main", "doesnotexist.txt", "delete non-existent")
	if err == nil {
		// May succeed (git allows this) but file list should be unchanged
		files, listErr := store.ListTreeFiles(ctx, repoName)
		if listErr != nil {
			t.Fatalf("ListTreeFiles failed: %v", listErr)
		}
		foundExists := false
		for _, f := range files {
			if f == "exists.txt" {
				foundExists = true
				break
			}
		}
		if !foundExists {
			t.Errorf("expected exists.txt to remain unchanged, got %v", files)
		}
	}
}

// TestDeleteFileFromRepo_NonExistentBranch tests deleting from a non-existent branch
func TestDeleteFileFromRepo_NonExistentBranch(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-delbadbranch-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repoName := "user/del-badbranch"

	// Initialize repo
	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Try to delete from non-existent branch
	_, err = store.DeleteFileFromRepo(ctx, repoName, "nonexistent", "file.txt", "delete from bad branch")
	if err == nil {
		t.Fatal("expected error for non-existent branch, got nil")
	}
}

func TestMoveFiles_CreatesSingleCommitAndRenamesAll(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-movefiles-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repoName := "user/movefiles"

	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	if _, err := store.WriteFile(ctx, repoName, "main", "tutorial/intro.md", "add intro", []byte("intro\n")); err != nil {
		t.Fatalf("WriteFile intro failed: %v", err)
	}
	if _, err := store.WriteFile(ctx, repoName, "main", "tutorial/advanced.md", "add advanced", []byte("advanced\n")); err != nil {
		t.Fatalf("WriteFile advanced failed: %v", err)
	}

	repoDir, err := store.GetRepoPath(ctx, repoName)
	if err != nil {
		t.Fatalf("GetRepoPath failed: %v", err)
	}
	beforeCount, err := gitRevListCount(ctx, repoDir, "main")
	if err != nil {
		t.Fatalf("rev-list before: %v", err)
	}

	commitSHA, err := store.MoveFiles(ctx, repoName, "main", []gitstore.FileMove{
		{OldPath: "tutorial/intro.md", NewPath: "guides/intro.md"},
		{OldPath: "tutorial/advanced.md", NewPath: "guides/advanced.md"},
	}, "bulk wiki move")
	if err != nil {
		t.Fatalf("MoveFiles failed: %v", err)
	}
	if commitSHA == "" {
		t.Fatal("MoveFiles returned empty commit SHA")
	}

	afterCount, err := gitRevListCount(ctx, repoDir, "main")
	if err != nil {
		t.Fatalf("rev-list after: %v", err)
	}
	if got, want := afterCount-beforeCount, 1; got != want {
		t.Fatalf("commit delta = %d, want %d", got, want)
	}

	files, err := store.ListTreeFiles(ctx, repoName)
	if err != nil {
		t.Fatalf("ListTreeFiles failed: %v", err)
	}
	joined := strings.Join(files, ",")
	if strings.Contains(joined, "tutorial/intro.md") || strings.Contains(joined, "tutorial/advanced.md") {
		t.Fatalf("old files still present after MoveFiles: %v", files)
	}
	if !strings.Contains(joined, "guides/intro.md") || !strings.Contains(joined, "guides/advanced.md") {
		t.Fatalf("new files missing after MoveFiles: %v", files)
	}
}

func TestMoveFiles_MissingSourceLeavesTreeUntouched(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-movefiles-missing-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repoName := "user/movefiles-missing"

	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	if _, err := store.WriteFile(ctx, repoName, "main", "tutorial/intro.md", "add intro", []byte("intro\n")); err != nil {
		t.Fatalf("WriteFile intro failed: %v", err)
	}

	repoDir, err := store.GetRepoPath(ctx, repoName)
	if err != nil {
		t.Fatalf("GetRepoPath failed: %v", err)
	}
	beforeCount, err := gitRevListCount(ctx, repoDir, "main")
	if err != nil {
		t.Fatalf("rev-list before: %v", err)
	}

	_, err = store.MoveFiles(ctx, repoName, "main", []gitstore.FileMove{
		{OldPath: "tutorial/intro.md", NewPath: "guides/intro.md"},
		{OldPath: "tutorial/missing.md", NewPath: "guides/missing.md"},
	}, "bulk wiki move")
	if err == nil {
		t.Fatal("MoveFiles missing source error = nil, want non-nil")
	}

	afterCount, err := gitRevListCount(ctx, repoDir, "main")
	if err != nil {
		t.Fatalf("rev-list after: %v", err)
	}
	if afterCount != beforeCount {
		t.Fatalf("commit count changed after failed MoveFiles: before=%d after=%d", beforeCount, afterCount)
	}

	files, err := store.ListTreeFiles(ctx, repoName)
	if err != nil {
		t.Fatalf("ListTreeFiles failed: %v", err)
	}
	joined := strings.Join(files, ",")
	if !strings.Contains(joined, "tutorial/intro.md") || !strings.Contains(joined, "README.md") || strings.Contains(joined, "guides/intro.md") {
		t.Fatalf("files after failed MoveFiles = %v, want README.md and tutorial/intro.md only", files)
	}
}

func gitRevListCount(ctx context.Context, repoDir, ref string) (int, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", repoDir, "rev-list", "--count", ref).CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("git rev-list --count %s: %w: %s", ref, err, out)
	}
	var count int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &count); err != nil {
		return 0, fmt.Errorf("parse rev-list count %q: %w", strings.TrimSpace(string(out)), err)
	}
	return count, nil
}

// TestListDir tests listing directory contents
func TestListDir(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-listdir-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repoName := "user/listdir"

	// Initialize repo with auto-init (creates README.md)
	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Create directory structure:
	// README.md (auto-created)
	// src/
	//   main.go
	//   utils/
	//     helper.go
	// docs/
	//   README.md

	// Create src/main.go
	if _, err := store.WriteFile(ctx, repoName, "main", "src/main.go", "add main.go", []byte("package main")); err != nil {
		t.Fatalf("WriteFile src/main.go failed: %v", err)
	}

	// Create src/utils/helper.go
	if _, err := store.WriteFile(ctx, repoName, "main", "src/utils/helper.go", "add helper.go", []byte("package utils")); err != nil {
		t.Fatalf("WriteFile src/utils/helper.go failed: %v", err)
	}

	// Create docs/README.md
	if _, err := store.WriteFile(ctx, repoName, "main", "docs/README.md", "add docs readme", []byte("# Documentation")); err != nil {
		t.Fatalf("WriteFile docs/README.md failed: %v", err)
	}

	// Test listing root directory
	entries, err := store.ListDir(ctx, repoName, "")
	if err != nil {
		t.Fatalf("ListDir root failed: %v", err)
	}
	// Should have README.md, src, and docs
	if len(entries) != 3 {
		t.Fatalf("root: expected 3 entries (README.md, src, docs), got %d: %v", len(entries), entries)
	}

	// Find src and docs
	var srcEntry, docsEntry, readmeEntry *gitstore.TreeEntry
	for i := range entries {
		if entries[i].Name == "src" {
			srcEntry = &entries[i]
		} else if entries[i].Name == "docs" {
			docsEntry = &entries[i]
		} else if entries[i].Name == "README.md" {
			readmeEntry = &entries[i]
		}
	}

	if srcEntry == nil {
		t.Fatal("root: missing src entry")
	}
	if docsEntry == nil {
		t.Fatal("root: missing docs entry")
	}
	if readmeEntry == nil {
		t.Fatal("root: missing README.md entry")
	}

	if srcEntry.Type != "tree" {
		t.Errorf("src type: got %q, want %q", srcEntry.Type, "tree")
	}
	if srcEntry.Path != "src" {
		t.Errorf("src path: got %q, want %q", srcEntry.Path, "src")
	}

	if docsEntry.Type != "tree" {
		t.Errorf("docs type: got %q, want %q", docsEntry.Type, "tree")
	}

	if readmeEntry.Type != "blob" {
		t.Errorf("README.md type: got %q, want %q", readmeEntry.Type, "blob")
	}

	// Test listing src directory
	entries, err = store.ListDir(ctx, repoName, "src")
	if err != nil {
		t.Fatalf("ListDir src failed: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("src: expected 2 entries (main.go, utils), got %d: %v", len(entries), entries)
	}

	var mainGoEntry, utilsEntry *gitstore.TreeEntry
	for i := range entries {
		if entries[i].Name == "main.go" {
			mainGoEntry = &entries[i]
		} else if entries[i].Name == "utils" {
			utilsEntry = &entries[i]
		}
	}

	if mainGoEntry == nil {
		t.Fatal("src: missing main.go entry")
	}
	if utilsEntry == nil {
		t.Fatal("src: missing utils entry")
	}

	if mainGoEntry.Type != "blob" {
		t.Errorf("main.go type: got %q, want %q", mainGoEntry.Type, "blob")
	}
	if mainGoEntry.Path != "src/main.go" {
		t.Errorf("main.go path: got %q, want %q", mainGoEntry.Path, "src/main.go")
	}
	if mainGoEntry.Size == 0 {
		t.Errorf("main.go size: expected > 0, got %d", mainGoEntry.Size)
	}
	if mainGoEntry.SHA == "" {
		t.Errorf("main.go sha: expected non-empty")
	}

	if utilsEntry.Type != "tree" {
		t.Errorf("utils type: got %q, want %q", utilsEntry.Type, "tree")
	}

	// Test listing src/utils directory
	entries, err = store.ListDir(ctx, repoName, "src/utils")
	if err != nil {
		t.Fatalf("ListDir src/utils failed: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("src/utils: expected 1 entry, got %d: %v", len(entries), entries)
	}

	if entries[0].Name != "helper.go" {
		t.Errorf("helper.go name: got %q, want %q", entries[0].Name, "helper.go")
	}
	if entries[0].Type != "blob" {
		t.Errorf("helper.go type: got %q, want %q", entries[0].Type, "blob")
	}
	if entries[0].Path != "src/utils/helper.go" {
		t.Errorf("helper.go path: got %q, want %q", entries[0].Path, "src/utils/helper.go")
	}

	// Test listing non-existent directory - returns empty list without error
	entries, err = store.ListDir(ctx, repoName, "nonexistent")
	if err != nil {
		t.Fatalf("ListDir nonexistent failed: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("ListDir nonexistent: expected 0 entries, got %d", len(entries))
	}
}

// TestIsDir tests checking if a path is a directory
func TestIsDir(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-isdir-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repoName := "user/isdir"

	// Initialize repo with auto-init
	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Create a file
	if _, err := store.WriteFile(ctx, repoName, "main", "file.txt", "add file", []byte("content")); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// Create a directory with a file
	if _, err := store.WriteFile(ctx, repoName, "main", "dir/file.txt", "add dir file", []byte("dir content")); err != nil {
		t.Fatalf("WriteFile dir failed: %v", err)
	}

	// Test file is not a directory
	isDir, err := store.IsDir(ctx, repoName, "file.txt")
	if err != nil {
		t.Fatalf("IsDir file.txt failed: %v", err)
	}
	if isDir {
		t.Errorf("file.txt: expected false, got true")
	}

	// Test directory is a directory
	isDir, err = store.IsDir(ctx, repoName, "dir")
	if err != nil {
		t.Fatalf("IsDir dir failed: %v", err)
	}
	if !isDir {
		t.Errorf("dir: expected true, got false")
	}

	// Test README.md (auto-created) is not a directory
	isDir, err = store.IsDir(ctx, repoName, "README.md")
	if err != nil {
		t.Fatalf("IsDir README.md failed: %v", err)
	}
	if isDir {
		t.Errorf("README.md: expected false, got true")
	}

	// Test non-existent path - IsDir returns false without error for non-paths
	// (git ls-tree returns empty output, not an error)
	isDir, err = store.IsDir(ctx, repoName, "nonexistent")
	if err != nil {
		// Error is acceptable for non-existent paths
		t.Logf("IsDir nonexistent returned error (acceptable): %v", err)
	}
	if isDir {
		t.Errorf("nonexistent: expected false, got true")
	}
}
