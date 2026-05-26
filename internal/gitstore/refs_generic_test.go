package gitstore_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/ngaut/agent-git-service/internal/gitstore"
)

func TestLookupRef_CustomNamespace(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-lookup-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repoName := "user/lookup"
	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatal(err)
	}
	sha, _ := store.HeadSHA(ctx, repoName, "main")

	// Create a custom-namespace ref via UpdateRef (the server's own create
	// path — CAS is covered in refs_cas_test.go).
	if err := store.UpdateRef(ctx, repoName, "refs/locks/issue-42", sha); err != nil {
		t.Fatalf("UpdateRef: %v", err)
	}

	got, err := store.LookupRef(ctx, repoName, "refs/locks/issue-42")
	if err != nil {
		t.Fatalf("LookupRef existing: %v", err)
	}
	if got != sha {
		t.Errorf("LookupRef returned %q, want %q", got, sha)
	}

	// Missing ref must return ErrRefNotFound, not a wrapped git error.
	_, err = store.LookupRef(ctx, repoName, "refs/locks/nonexistent")
	if !errors.Is(err, gitstore.ErrRefNotFound) {
		t.Errorf("LookupRef missing: got %v, want ErrRefNotFound", err)
	}
}

func TestListRefsWithPrefix(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-listprefix-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repoName := "user/listprefix"
	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatal(err)
	}
	sha, _ := store.HeadSHA(ctx, repoName, "main")

	// Three custom refs spread across two namespaces.
	refs := []string{"refs/locks/a", "refs/locks/b", "refs/experiment/x"}
	for _, rf := range refs {
		if err := store.UpdateRef(ctx, repoName, rf, sha); err != nil {
			t.Fatalf("UpdateRef %s: %v", rf, err)
		}
	}

	// Prefix "refs/locks" must return 2 entries, not the experiment one.
	got, err := store.ListRefsWithPrefix(ctx, repoName, "refs/locks")
	if err != nil {
		t.Fatalf("ListRefsWithPrefix: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("prefix refs/locks: got %d refs, want 2: %+v", len(got), got)
	}
	for _, rf := range got {
		if rf.SHA != sha {
			t.Errorf("ref %s SHA=%q, want %q", rf.Ref, rf.SHA, sha)
		}
	}

	// Empty prefix lists everything (heads/main + the three customs).
	all, err := store.ListRefsWithPrefix(ctx, repoName, "")
	if err != nil {
		t.Fatalf("ListRefsWithPrefix empty: %v", err)
	}
	if len(all) < 4 {
		t.Errorf("empty prefix: got %d refs, want >= 4 (heads/main + 3 custom)", len(all))
	}

	// Nonexistent prefix returns empty, not an error.
	none, err := store.ListRefsWithPrefix(ctx, repoName, "refs/nowhere")
	if err != nil {
		t.Fatalf("ListRefsWithPrefix nonexistent: %v", err)
	}
	if len(none) != 0 {
		t.Errorf("nonexistent prefix: got %d refs, want 0", len(none))
	}
}

// Regression: issue #1297 — matching-refs (and ListRefsWithPrefix) must
// follow GitHub's character-prefix semantics, not git for-each-ref's
// "literal up to a slash" rule. A prefix that ends mid-component (e.g.
// "refs/heads/octoswarm/fix-") must still match every ref that starts
// with that string.
func TestListRefsWithPrefix_MidComponent_Issue1297(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-listprefix-mid-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repoName := "user/listprefix-mid"
	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatal(err)
	}
	sha, _ := store.HeadSHA(ctx, repoName, "main")

	for _, rf := range []string{
		"refs/heads/octoswarm/fix-1-real",
		"refs/heads/octoswarm/fix-2",
		"refs/heads/octoswarm/other",
		"refs/heads/octoswarm-clone",
	} {
		if err := store.UpdateRef(ctx, repoName, rf, sha); err != nil {
			t.Fatalf("UpdateRef %s: %v", rf, err)
		}
	}

	// Mid-component prefix: must catch the two "fix-*" refs and reject
	// the "other" sibling.
	got, err := store.ListRefsWithPrefix(ctx, repoName, "refs/heads/octoswarm/fix-")
	if err != nil {
		t.Fatalf("ListRefsWithPrefix mid-component: %v", err)
	}
	names := map[string]bool{}
	for _, rf := range got {
		names[rf.Ref] = true
	}
	if !names["refs/heads/octoswarm/fix-1-real"] || !names["refs/heads/octoswarm/fix-2"] {
		t.Errorf("mid-component prefix missing expected refs; got %v", names)
	}
	if names["refs/heads/octoswarm/other"] {
		t.Errorf("mid-component prefix leaked refs/heads/octoswarm/other")
	}
	if names["refs/heads/octoswarm-clone"] {
		t.Errorf("mid-component prefix leaked refs/heads/octoswarm-clone (no slash boundary)")
	}

	// Whole-component prefix that crosses no slash: must also match
	// across the boundary AND match a sibling without a slash.
	got, err = store.ListRefsWithPrefix(ctx, repoName, "refs/heads/octoswarm")
	if err != nil {
		t.Fatalf("ListRefsWithPrefix whole-component: %v", err)
	}
	names = map[string]bool{}
	for _, rf := range got {
		names[rf.Ref] = true
	}
	if !names["refs/heads/octoswarm/fix-1-real"] || !names["refs/heads/octoswarm/fix-2"] ||
		!names["refs/heads/octoswarm/other"] || !names["refs/heads/octoswarm-clone"] {
		t.Errorf("whole-component prefix missing refs; got %v", names)
	}
}

// Regression: issue #1297 Bug 2 — Compare must accept refs whose names
// contain "/" and "-" (the previous IsValidRev regex silently rejected
// them, causing the handler to fall back to ahead=0/behind=0) and must
// expose a non-empty MergeBaseSHA so the REST handler can populate
// merge_base_commit.
func TestCompare_RefWithSlashAndHyphen_Issue1297(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-compare-1297-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repoName := "user/compare-1297"
	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatal(err)
	}
	mainSHA, _ := store.HeadSHA(ctx, repoName, "main")
	if err := store.UpdateRef(ctx, repoName, "refs/heads/octoswarm/fix-2", mainSHA); err != nil {
		t.Fatalf("UpdateRef: %v", err)
	}

	got, err := store.Compare(ctx, repoName, "main", "octoswarm/fix-2")
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if got.MergeBaseSHA != mainSHA {
		t.Errorf("MergeBaseSHA: got %q, want %q", got.MergeBaseSHA, mainSHA)
	}
}
