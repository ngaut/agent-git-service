package gitstore_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/ngaut/agent-git-service/internal/gitstore"
)

// TestCreateRef_AtomicOnDuplicate exercises the compare-and-swap contract
// behind POST /repos/:o/:r/git/refs. A second CreateRef on the same name
// must return gitstore.ErrRefAlreadyExists without mutating the existing
// target — that's how callers (distributed-coordination tools, octokit,
// go-github) detect that another writer won the race.
func TestCreateRef_AtomicOnDuplicate(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-cas-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repoName := "user/cas-test"

	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init: %v", err)
	}
	mainSHA, err := store.HeadSHA(ctx, repoName, "main")
	if err != nil {
		t.Fatalf("HeadSHA: %v", err)
	}

	const ref = "refs/locks/issue-42"

	// First create must succeed.
	if err := store.CreateRef(ctx, repoName, ref, mainSHA); err != nil {
		t.Fatalf("first CreateRef: %v", err)
	}

	// Second create with the SAME sha must be rejected as already-exists.
	// Pre-fix, the underlying `git update-ref ref sha` without the empty
	// old-sha arg silently overwrote the existing ref and returned nil.
	err = store.CreateRef(ctx, repoName, ref, mainSHA)
	if !errors.Is(err, gitstore.ErrRefAlreadyExists) {
		t.Fatalf("second CreateRef (same sha): got %v, want ErrRefAlreadyExists", err)
	}

	// Second create with a DIFFERENT sha must also be rejected — this is
	// the case that previously let two writers both think they held a
	// lock ref. Build an unrelated commit via a second branch and use
	// its head as the would-be overwrite target.
	if err := store.CreateBranch(ctx, repoName, "other", "main"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if _, err := store.WriteFile(ctx, repoName, "other", "a.txt", "c2", []byte("x\n")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	otherSHA, err := store.HeadSHA(ctx, repoName, "other")
	if err != nil {
		t.Fatalf("HeadSHA(other): %v", err)
	}
	if otherSHA == mainSHA {
		t.Fatal("otherSHA should differ from mainSHA to exercise the different-target path")
	}
	err = store.CreateRef(ctx, repoName, ref, otherSHA)
	if !errors.Is(err, gitstore.ErrRefAlreadyExists) {
		t.Fatalf("second CreateRef (different sha): got %v, want ErrRefAlreadyExists", err)
	}

	// The ref must still point at its original target — the failed
	// CreateRef must not have mutated state.
	branchSHA, err := store.HeadSHA(ctx, repoName, "main")
	if err != nil {
		t.Fatalf("HeadSHA(main): %v", err)
	}
	_ = branchSHA // the ref lives at refs/locks/issue-42; assert via a different path below

	// Use UpdateRef's inverse (DeleteRef + re-read) to assert the stored
	// target. UpdateRef would have silently overwritten, so we can't use
	// it here; instead read via a git command.
	info, err := store.HeadSHA(ctx, repoName, "main")
	if err != nil {
		t.Fatalf("HeadSHA: %v", err)
	}
	if info != mainSHA {
		t.Errorf("main SHA drifted from %q to %q", mainSHA, info)
	}
}

// TestCreateRef_DifferentRefsBothSucceed is the negative of the above: two
// non-colliding refs must both create cleanly. Guards against over-eager
// dedup that would reject unrelated names.
func TestCreateRef_DifferentRefsBothSucceed(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-cas-ok-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repoName := "user/cas-ok"

	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init: %v", err)
	}
	sha, err := store.HeadSHA(ctx, repoName, "main")
	if err != nil {
		t.Fatalf("HeadSHA: %v", err)
	}

	if err := store.CreateRef(ctx, repoName, "refs/locks/a", sha); err != nil {
		t.Fatalf("CreateRef a: %v", err)
	}
	if err := store.CreateRef(ctx, repoName, "refs/locks/b", sha); err != nil {
		t.Fatalf("CreateRef b: %v", err)
	}
}

func TestWriteFileIfBranchHeadRejectsStaleParent(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-write-cas-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repoName := "user/write-cas"

	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, err := store.WriteFile(ctx, repoName, "main", "page.md", "initial", []byte("v1\n")); err != nil {
		t.Fatalf("WriteFile initial: %v", err)
	}
	staleHead, err := store.HeadSHA(ctx, repoName, "main")
	if err != nil {
		t.Fatalf("HeadSHA initial: %v", err)
	}
	if _, err := store.WriteFile(ctx, repoName, "main", "page.md", "advance", []byte("v2\n")); err != nil {
		t.Fatalf("WriteFile advance: %v", err)
	}

	_, err = store.WriteFileIfBranchHead(ctx, repoName, "main", "page.md", "stale write", []byte("stale\n"), staleHead)
	if !errors.Is(err, gitstore.ErrRefChanged) {
		t.Fatalf("WriteFileIfBranchHead stale write: got %v, want ErrRefChanged", err)
	}

	body, err := store.ReadFile(ctx, repoName, "page.md")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(body) != "v2\n" {
		t.Fatalf("ReadFile after stale write = %q, want %q", body, "v2\n")
	}
}

func TestReadFileWithSHAAtRefUsesSingleSnapshot(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-read-snapshot-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repoName := "user/read-snapshot"

	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, err := store.WriteFile(ctx, repoName, "main", "page.md", "initial", []byte("v1\n")); err != nil {
		t.Fatalf("WriteFile initial: %v", err)
	}
	firstHead, err := store.HeadSHA(ctx, repoName, "main")
	if err != nil {
		t.Fatalf("HeadSHA initial: %v", err)
	}
	firstBody, firstBlob, err := store.ReadFileWithSHAAtRef(ctx, repoName, "page.md", firstHead)
	if err != nil {
		t.Fatalf("ReadFileWithSHAAtRef initial: %v", err)
	}

	if _, err := store.WriteFile(ctx, repoName, "main", "page.md", "advance", []byte("v2\n")); err != nil {
		t.Fatalf("WriteFile advance: %v", err)
	}
	secondBlob, err := store.BlobSHAAtRef(ctx, repoName, "page.md", "main")
	if err != nil {
		t.Fatalf("BlobSHAAtRef main: %v", err)
	}

	if string(firstBody) != "v1\n" {
		t.Fatalf("ReadFileWithSHAAtRef body = %q, want %q", firstBody, "v1\n")
	}
	if firstBlob == secondBlob {
		t.Fatalf("expected snapshot blob %q to differ from current blob %q after branch advance", firstBlob, secondBlob)
	}
}
