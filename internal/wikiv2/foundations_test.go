package wikiv2

import (
	"context"
	"errors"
	"testing"

	"github.com/ngaut/agent-git-service/internal/gitstore"
)

func TestPlanPageUpsertAndDelete(t *testing.T) {
	upsert, err := PlanPageUpsert("guides/setup", []byte("# Setup\n"), "seed page", "abc")
	if err != nil {
		t.Fatalf("PlanPageUpsert: %v", err)
	}
	if upsert.Ref != DefaultRef {
		t.Fatalf("Ref = %q, want %q", upsert.Ref, DefaultRef)
	}
	if len(upsert.Mutations) != 1 || upsert.Mutations[0].Path != "guides/setup.md" || upsert.Mutations[0].Delete {
		t.Fatalf("unexpected upsert mutations: %+v", upsert.Mutations)
	}

	del, err := PlanPageDelete("guides/setup", "delete page", "")
	if err != nil {
		t.Fatalf("PlanPageDelete: %v", err)
	}
	if len(del.Mutations) != 1 || del.Mutations[0].Path != "guides/setup.md" || !del.Mutations[0].Delete {
		t.Fatalf("unexpected delete mutations: %+v", del.Mutations)
	}
}

func TestAdvanceRefCASIsIdempotent(t *testing.T) {
	ctx := context.Background()
	store, repoName := setupRefCASTestRepo(t)

	sha1, err := store.HeadSHA(ctx, repoName, DefaultBranch)
	if err != nil {
		t.Fatalf("HeadSHA 1: %v", err)
	}
	sha2, err := store.WriteFile(ctx, repoName, DefaultBranch, "guides/setup.md", "update page", []byte("# Setup v2\n"))
	if err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	branchRef := DefaultRef
	if err := store.UpdateRefCAS(ctx, repoName, branchRef, sha1, sha2); err != nil {
		t.Fatalf("reset branch for CAS test: %v", err)
	}

	first, err := AdvanceRefCAS(ctx, store, repoName, branchRef, sha1, sha2)
	if err != nil {
		t.Fatalf("AdvanceRefCAS first: %v", err)
	}
	if !first.Updated || first.PreviousSHA != sha1 || first.CurrentSHA != sha2 {
		t.Fatalf("first result = %+v", first)
	}

	second, err := AdvanceRefCAS(ctx, store, repoName, branchRef, sha1, sha2)
	if err != nil {
		t.Fatalf("AdvanceRefCAS second: %v", err)
	}
	if second.Updated {
		t.Fatalf("second result should be idempotent no-op, got %+v", second)
	}
	if second.CurrentSHA != sha2 {
		t.Fatalf("second CurrentSHA = %q, want %q", second.CurrentSHA, sha2)
	}
}

func TestAdvanceRefCASRejectsEmptyTargetSHA(t *testing.T) {
	ctx := context.Background()
	store, repoName := setupRefCASTestRepo(t)

	if _, err := AdvanceRefCAS(ctx, store, repoName, DefaultRef, "", ""); !errors.Is(err, gitstore.ErrInvalidSHA) {
		t.Fatalf("AdvanceRefCAS empty new SHA error = %v, want %v", err, gitstore.ErrInvalidSHA)
	}
}

func setupRefCASTestRepo(t *testing.T) (*gitstore.Store, string) {
	t.Helper()

	root := t.TempDir()
	store, err := gitstore.New(root)
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	repoName := "alice/wiki-v2-ref-cas"
	if err := store.Init(context.Background(), repoName, DefaultBranch, false); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, err := store.WriteFile(context.Background(), repoName, DefaultBranch, "guides/setup.md", "seed page", []byte("# Setup\n")); err != nil {
		t.Fatalf("seed WriteFile: %v", err)
	}
	return store, repoName
}
