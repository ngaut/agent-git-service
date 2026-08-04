package gitstore_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ngaut/agent-git-service/internal/gitstore"
)

func TestPrepareCommitFilesAtPublishesWithParentCAS(t *testing.T) {
	ctx := context.Background()
	store, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	const full = "owner/prepared-wiki"
	if err := store.Init(ctx, full, "master", false); err != nil {
		t.Fatalf("Init: %v", err)
	}

	first, err := store.PrepareCommitFilesAt(ctx, full, "master", "first", []gitstore.FileMutation{{
		Path:    "home.md",
		Content: []byte("# Home\n"),
	}}, time.Now())
	if err != nil {
		t.Fatalf("PrepareCommitFilesAt(first): %v", err)
	}
	if _, err := store.HeadSHA(ctx, full, "master"); err == nil {
		t.Fatal("prepared commit became visible before publish")
	}
	if err := store.PublishPreparedCommit(ctx, full, "master", first); err != nil {
		t.Fatalf("PublishPreparedCommit(first): %v", err)
	}

	winner, err := store.PrepareCommitFilesAt(ctx, full, "master", "winner", []gitstore.FileMutation{{
		Path:    "winner.md",
		Content: []byte("winner\n"),
	}}, time.Now())
	if err != nil {
		t.Fatalf("PrepareCommitFilesAt(winner): %v", err)
	}
	stale, err := store.PrepareCommitFilesAt(ctx, full, "master", "stale", []gitstore.FileMutation{{
		Path:    "stale.md",
		Content: []byte("stale\n"),
	}}, time.Now())
	if err != nil {
		t.Fatalf("PrepareCommitFilesAt(stale): %v", err)
	}
	if err := store.PublishPreparedCommit(ctx, full, "master", winner); err != nil {
		t.Fatalf("PublishPreparedCommit(winner): %v", err)
	}
	if err := store.PublishPreparedCommit(ctx, full, "master", stale); !errors.Is(err, gitstore.ErrRefChanged) {
		t.Fatalf("PublishPreparedCommit(stale) error = %v, want ErrRefChanged", err)
	}
}

func TestBuildCommitFilesAtRequiresPersistenceBeforePublish(t *testing.T) {
	ctx := context.Background()
	store, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	const full = "owner/built-wiki"
	if err := store.Init(ctx, full, "master", false); err != nil {
		t.Fatalf("Init: %v", err)
	}

	prepared, err := store.BuildCommitFilesAt(ctx, full, "master", "first", []gitstore.FileMutation{{
		Path:    "home.md",
		Content: []byte("# Home\n"),
	}}, time.Now())
	if err != nil {
		t.Fatalf("BuildCommitFilesAt: %v", err)
	}
	if err := store.PublishPreparedCommit(ctx, full, "master", prepared); err == nil {
		t.Fatal("built commit published before its objects were persisted")
	}
	if _, err := store.HeadSHA(ctx, full, "master"); err == nil {
		t.Fatal("built commit became visible before persistence")
	}

	if err := store.PersistPreparedCommit(ctx, prepared); err != nil {
		t.Fatalf("PersistPreparedCommit: %v", err)
	}
	if err := store.PublishPreparedCommit(ctx, full, "master", prepared); err != nil {
		t.Fatalf("PublishPreparedCommit: %v", err)
	}
	head, err := store.HeadSHA(ctx, full, "master")
	if err != nil {
		t.Fatalf("HeadSHA: %v", err)
	}
	if head != prepared.SHA {
		t.Fatalf("head = %s, want %s", head, prepared.SHA)
	}
}

func TestBuildCommitFilesAtParentChainsPersistedCommitsBeforePublication(t *testing.T) {
	ctx := context.Background()
	store, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	const full = "owner/pipelined-wiki"
	if err := store.Init(ctx, full, "master", false); err != nil {
		t.Fatalf("Init: %v", err)
	}

	parent, err := store.BuildCommitFilesAt(ctx, full, "master", "parent", []gitstore.FileMutation{{
		Path:    "guides/first.md",
		Content: []byte("first\n"),
	}}, time.Now())
	if err != nil {
		t.Fatalf("BuildCommitFilesAt(parent): %v", err)
	}
	if err := store.PersistPreparedCommit(ctx, parent); err != nil {
		t.Fatalf("PersistPreparedCommit(parent): %v", err)
	}

	child, err := store.BuildCommitFilesAtParent(
		ctx,
		full,
		"master",
		parent.SHA,
		"child",
		[]gitstore.FileMutation{{Path: "guides/second.md", Content: []byte("second\n")}},
		time.Now(),
	)
	if err != nil {
		t.Fatalf("BuildCommitFilesAtParent(child): %v", err)
	}
	if child.ParentSHA != parent.SHA {
		t.Fatalf("child parent = %s, want %s", child.ParentSHA, parent.SHA)
	}
	if err := store.PersistPreparedCommit(ctx, child); err != nil {
		t.Fatalf("PersistPreparedCommit(child): %v", err)
	}
	if _, err := store.HeadSHA(ctx, full, "master"); err == nil {
		t.Fatal("persisted commit chain became visible before publication")
	}

	if err := store.PublishPreparedCommit(ctx, full, "master", parent); err != nil {
		t.Fatalf("PublishPreparedCommit(parent): %v", err)
	}
	if err := store.PublishPreparedCommit(ctx, full, "master", child); err != nil {
		t.Fatalf("PublishPreparedCommit(child): %v", err)
	}
	for path, want := range map[string]string{
		"guides/first.md":  "first\n",
		"guides/second.md": "second\n",
	} {
		got, err := store.ReadFile(ctx, full, path)
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", path, err)
		}
		if string(got) != want {
			t.Fatalf("ReadFile(%s) = %q, want %q", path, got, want)
		}
	}
}

func TestBuildCommitFilesAtParentRejectsInvalidSHA(t *testing.T) {
	store, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = store.BuildCommitFilesAtParent(
		context.Background(),
		"owner/invalid-parent",
		"master",
		"not-a-sha",
		"child",
		[]gitstore.FileMutation{{Path: "home.md", Content: []byte("home\n")}},
		time.Now(),
	)
	if err == nil || !strings.Contains(err.Error(), "invalid parent commit SHA") {
		t.Fatalf("BuildCommitFilesAtParent error = %v, want invalid SHA", err)
	}
}

func TestPublishPreparedCommitChecksTrustedObjectStillExists(t *testing.T) {
	ctx := context.Background()
	store, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	const full = "owner/missing-prepared-object"
	if err := store.Init(ctx, full, "master", false); err != nil {
		t.Fatalf("Init: %v", err)
	}

	prepared, err := store.PrepareCommitFilesAt(ctx, full, "master", "first", []gitstore.FileMutation{{
		Path:    "home.md",
		Content: []byte("# Home\n"),
	}}, time.Now())
	if err != nil {
		t.Fatalf("PrepareCommitFilesAt: %v", err)
	}
	repoPath, err := store.GetRepoPath(ctx, full)
	if err != nil {
		t.Fatalf("GetRepoPath: %v", err)
	}
	objectPath := filepath.Join(repoPath, "objects", prepared.SHA[:2], prepared.SHA[2:])
	if err := os.Remove(objectPath); err != nil {
		t.Fatalf("remove prepared commit object: %v", err)
	}

	err = store.PublishPreparedCommit(ctx, full, "master", prepared)
	if err == nil || !strings.Contains(err.Error(), "check prepared commit") {
		t.Fatalf("PublishPreparedCommit error = %v, want trusted object check failure", err)
	}
	if _, err := store.HeadSHA(ctx, full, "master"); err == nil {
		t.Fatal("missing prepared object became branch head")
	}
}

func TestPublishPreparedCommitFallsBackWhenMetadataChanges(t *testing.T) {
	ctx := context.Background()
	store, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	const full = "owner/modified-prepared-metadata"
	if err := store.Init(ctx, full, "master", false); err != nil {
		t.Fatalf("Init: %v", err)
	}
	first, err := store.CommitFiles(ctx, full, "master", "first", []gitstore.FileMutation{{
		Path:    "first.md",
		Content: []byte("first\n"),
	}})
	if err != nil {
		t.Fatalf("CommitFiles(first): %v", err)
	}
	prepared, err := store.PrepareCommitFilesAt(ctx, full, "master", "second", []gitstore.FileMutation{{
		Path:    "second.md",
		Content: []byte("second\n"),
	}}, time.Now())
	if err != nil {
		t.Fatalf("PrepareCommitFilesAt(second): %v", err)
	}

	prepared.SHA = first
	err = store.PublishPreparedCommit(ctx, full, "master", prepared)
	if err == nil || !strings.Contains(err.Error(), "metadata says") {
		t.Fatalf("PublishPreparedCommit error = %v, want full parent validation failure", err)
	}
	head, err := store.HeadSHA(ctx, full, "master")
	if err != nil {
		t.Fatalf("HeadSHA: %v", err)
	}
	if head != first {
		t.Fatalf("head = %s, want %s", head, first)
	}
}

func TestPublishPreparedCommitWithoutPrivateProvenanceUsesFullValidation(t *testing.T) {
	ctx := context.Background()
	store, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	const full = "owner/recovered-prepared-commit"
	if err := store.Init(ctx, full, "master", false); err != nil {
		t.Fatalf("Init: %v", err)
	}
	first, err := store.CommitFiles(ctx, full, "master", "first", []gitstore.FileMutation{{
		Path:    "first.md",
		Content: []byte("first\n"),
	}})
	if err != nil {
		t.Fatalf("CommitFiles(first): %v", err)
	}
	prepared, err := store.PrepareCommitFilesAt(ctx, full, "master", "second", []gitstore.FileMutation{{
		Path:    "second.md",
		Content: []byte("second\n"),
	}}, time.Now())
	if err != nil {
		t.Fatalf("PrepareCommitFilesAt(second): %v", err)
	}

	recovered := gitstore.PreparedCommit{SHA: prepared.SHA}
	if err := store.PublishPreparedCommit(ctx, full, "master", recovered); err != nil {
		t.Fatalf("PublishPreparedCommit(recovered): %v", err)
	}
	head, err := store.HeadSHA(ctx, full, "master")
	if err != nil {
		t.Fatalf("HeadSHA: %v", err)
	}
	if head != prepared.SHA {
		t.Fatalf("head = %s, want %s (parent %s)", head, prepared.SHA, first)
	}
}

func TestPublishPreparedCommitWithoutPrivateProvenanceRejectsMissingTree(t *testing.T) {
	ctx := context.Background()
	store, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	const full = "owner/recovered-missing-tree"
	if err := store.Init(ctx, full, "master", false); err != nil {
		t.Fatalf("Init: %v", err)
	}
	prepared, err := store.PrepareCommitFilesAt(ctx, full, "master", "first", []gitstore.FileMutation{{
		Path:    "first.md",
		Content: []byte("first\n"),
	}}, time.Now())
	if err != nil {
		t.Fatalf("PrepareCommitFilesAt: %v", err)
	}
	repoPath, err := store.GetRepoPath(ctx, full)
	if err != nil {
		t.Fatalf("GetRepoPath: %v", err)
	}
	treeSHA := gitShowField(t, repoPath, prepared.SHA, "%T")
	if err := os.Remove(filepath.Join(repoPath, "objects", treeSHA[:2], treeSHA[2:])); err != nil {
		t.Fatalf("remove prepared tree object: %v", err)
	}

	err = store.PublishPreparedCommit(ctx, full, "master", gitstore.PreparedCommit{SHA: prepared.SHA})
	if err == nil || !strings.Contains(err.Error(), "validate prepared commit") {
		t.Fatalf("PublishPreparedCommit error = %v, want tree closure validation failure", err)
	}
	if _, err := store.HeadSHA(ctx, full, "master"); err == nil {
		t.Fatal("commit with missing tree became branch head")
	}
}

func TestPublishPreparedCommitFastPathDetectsExternalRefChange(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	storeA, err := gitstore.New(root)
	if err != nil {
		t.Fatalf("New(storeA): %v", err)
	}
	const full = "owner/external-ref-change"
	if err := storeA.Init(ctx, full, "master", false); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, err := storeA.CommitFiles(ctx, full, "master", "first", []gitstore.FileMutation{{
		Path:    "first.md",
		Content: []byte("first\n"),
	}}); err != nil {
		t.Fatalf("CommitFiles(first): %v", err)
	}
	prepared, err := storeA.PrepareCommitFilesAt(ctx, full, "master", "stale", []gitstore.FileMutation{{
		Path:    "stale.md",
		Content: []byte("stale\n"),
	}}, time.Now())
	if err != nil {
		t.Fatalf("PrepareCommitFilesAt(stale): %v", err)
	}

	storeB, err := gitstore.New(root)
	if err != nil {
		t.Fatalf("New(storeB): %v", err)
	}
	winner, err := storeB.CommitFiles(ctx, full, "master", "winner", []gitstore.FileMutation{{
		Path:    "winner.md",
		Content: []byte("winner\n"),
	}})
	if err != nil {
		t.Fatalf("CommitFiles(winner): %v", err)
	}

	if err := storeA.PublishPreparedCommit(ctx, full, "master", prepared); !errors.Is(err, gitstore.ErrRefChanged) {
		t.Fatalf("PublishPreparedCommit(stale) error = %v, want ErrRefChanged", err)
	}
	head, err := storeA.HeadSHA(ctx, full, "master")
	if err != nil {
		t.Fatalf("HeadSHA: %v", err)
	}
	if head != winner {
		t.Fatalf("head = %s, want external winner %s", head, winner)
	}
}

func gitShowField(t *testing.T, repoPath, ref, format string) string {
	t.Helper()
	out, err := exec.Command("git", "--git-dir", repoPath, "show", "-s", "--format="+format, ref).Output()
	if err != nil {
		t.Fatalf("git show %s: %v", ref, err)
	}
	return strings.TrimSpace(string(out))
}

func TestCommitFilesAtWritesCompatibleLinearHistory(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := gitstore.New(root)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	const full = "owner/wiki"
	if err := store.Init(ctx, full, "master", false); err != nil {
		t.Fatalf("Init: %v", err)
	}

	firstAt := time.Date(2026, 7, 23, 10, 11, 12, 0, time.FixedZone("test", 8*60*60))
	firstSHA, err := store.CommitFilesAt(ctx, full, "master", "first commit", []gitstore.FileMutation{
		{Path: "home.md", Content: []byte("# Home\n")},
		{Path: "guides/setup.md", Content: []byte("# Setup\n\nv1\n")},
	}, firstAt)
	if err != nil {
		t.Fatalf("CommitFilesAt(first): %v", err)
	}

	secondAt := firstAt.Add(time.Minute)
	secondSHA, err := store.CommitFilesAt(ctx, full, "master", "second commit", []gitstore.FileMutation{
		{Path: "home.md", Delete: true},
		{Path: "guides/setup.md", Content: []byte("# Setup\n\nv2\n")},
		{Path: "guides/advanced.md", Content: []byte("# Advanced\n")},
	}, secondAt)
	if err != nil {
		t.Fatalf("CommitFilesAt(second): %v", err)
	}

	commits, err := store.ListCommits(ctx, full, 10, nil)
	if err != nil {
		t.Fatalf("ListCommits: %v", err)
	}
	if len(commits) != 2 {
		t.Fatalf("len(commits) = %d, want 2", len(commits))
	}
	if commits[0].SHA != secondSHA || commits[1].SHA != firstSHA {
		t.Fatalf("commit order = [%s, %s], want [%s, %s]", commits[0].SHA, commits[1].SHA, secondSHA, firstSHA)
	}

	paths, err := store.ListTreeFilesAtRef(ctx, full, secondSHA)
	if err != nil {
		t.Fatalf("ListTreeFilesAtRef: %v", err)
	}
	sort.Strings(paths)
	wantPaths := []string{"guides/advanced.md", "guides/setup.md"}
	if fmt.Sprint(paths) != fmt.Sprint(wantPaths) {
		t.Fatalf("paths = %v, want %v", paths, wantPaths)
	}
	body, err := store.ReadFileAtRef(ctx, full, "guides/setup.md", secondSHA)
	if err != nil {
		t.Fatalf("ReadFileAtRef: %v", err)
	}
	if string(body) != "# Setup\n\nv2\n" {
		t.Fatalf("setup body = %q", body)
	}

	repoPath, err := store.GetRepoPath(ctx, full)
	if err != nil {
		t.Fatalf("GetRepoPath: %v", err)
	}
	if out, err := exec.CommandContext(ctx, "git", "--git-dir", repoPath, "fsck", "--strict").CombinedOutput(); err != nil {
		t.Fatalf("git fsck --strict: %v\n%s", err, out)
	}
	raw, err := exec.CommandContext(ctx, "git", "--git-dir", repoPath, "cat-file", "commit", secondSHA).Output()
	if err != nil {
		t.Fatalf("git cat-file commit: %v", err)
	}
	if !strings.Contains(string(raw), "parent "+firstSHA+"\n") {
		t.Fatalf("second commit missing parent %s:\n%s", firstSHA, raw)
	}
	if !strings.Contains(string(raw), "second commit\n") {
		t.Fatalf("second commit missing message:\n%s", raw)
	}
}

func TestCommitFilesTreeCacheInvalidatesAfterExternalRefChange(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	storeA, err := gitstore.New(root)
	if err != nil {
		t.Fatalf("New(storeA): %v", err)
	}
	const full = "owner/cache-invalidation"
	if err := storeA.Init(ctx, full, "master", false); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, err := storeA.CommitFiles(ctx, full, "master", "first", []gitstore.FileMutation{{
		Path:    "first.md",
		Content: []byte("first\n"),
	}}); err != nil {
		t.Fatalf("CommitFiles(first): %v", err)
	}

	// A second Store represents a receive-pack or another process. Its write
	// advances the branch without touching storeA's in-memory tree cache.
	storeB, err := gitstore.New(root)
	if err != nil {
		t.Fatalf("New(storeB): %v", err)
	}
	if _, err := storeB.CommitFiles(ctx, full, "master", "external", []gitstore.FileMutation{{
		Path:    "external.md",
		Content: []byte("external\n"),
	}}); err != nil {
		t.Fatalf("CommitFiles(external): %v", err)
	}
	if _, err := storeA.CommitFiles(ctx, full, "master", "after external", []gitstore.FileMutation{{
		Path:    "after.md",
		Content: []byte("after\n"),
	}}); err != nil {
		t.Fatalf("CommitFiles(after external): %v", err)
	}

	paths, err := storeA.ListTreeFilesAtRef(ctx, full, "master")
	if err != nil {
		t.Fatalf("ListTreeFilesAtRef: %v", err)
	}
	sort.Strings(paths)
	want := []string{"after.md", "external.md", "first.md"}
	if fmt.Sprint(paths) != fmt.Sprint(want) {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
}

func TestCommitFilesTreeCacheRecreatesPrunedDirectory(t *testing.T) {
	ctx := context.Background()
	store, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	const full = "owner/cache-prune"
	if err := store.Init(ctx, full, "master", false); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, err := store.CommitFiles(ctx, full, "master", "create nested", []gitstore.FileMutation{{
		Path:    "guides/first.md",
		Content: []byte("first\n"),
	}}); err != nil {
		t.Fatalf("CommitFiles(create): %v", err)
	}
	if _, err := store.CommitFiles(ctx, full, "master", "delete nested", []gitstore.FileMutation{{
		Path:   "guides/first.md",
		Delete: true,
	}}); err != nil {
		t.Fatalf("CommitFiles(delete): %v", err)
	}
	if _, err := store.CommitFiles(ctx, full, "master", "recreate nested", []gitstore.FileMutation{{
		Path:    "guides/second.md",
		Content: []byte("second\n"),
	}}); err != nil {
		t.Fatalf("CommitFiles(recreate): %v", err)
	}

	paths, err := store.ListTreeFilesAtRef(ctx, full, "master")
	if err != nil {
		t.Fatalf("ListTreeFilesAtRef: %v", err)
	}
	if fmt.Sprint(paths) != fmt.Sprint([]string{"guides/second.md"}) {
		t.Fatalf("paths = %v, want [guides/second.md]", paths)
	}
}

func BenchmarkCommitFilesSequential(b *testing.B) {
	for _, commitCount := range []int{100, 1000, 3000} {
		b.Run(fmt.Sprintf("%d", commitCount), func(b *testing.B) {
			b.ReportAllocs()
			b.ReportMetric(float64(commitCount), "commits/op")
			ctx := context.Background()
			for iteration := 0; iteration < b.N; iteration++ {
				b.StopTimer()
				store, err := gitstore.New(b.TempDir())
				if err != nil {
					b.Fatalf("New: %v", err)
				}
				full := fmt.Sprintf("owner/wiki-%d", iteration)
				if err := store.Init(ctx, full, "master", false); err != nil {
					b.Fatalf("Init: %v", err)
				}

				b.StartTimer()
				for commit := 0; commit < commitCount; commit++ {
					path := fmt.Sprintf("generated/page-%05d.md", commit)
					body := []byte(fmt.Sprintf("# Page %05d\n", commit))
					if _, err := store.CommitFiles(ctx, full, "master", "write "+path, []gitstore.FileMutation{{
						Path:    path,
						Content: body,
					}}); err != nil {
						b.Fatalf("CommitFiles(%s): %v", path, err)
					}
				}
				b.StopTimer()
			}
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*commitCount), "ns/commit")
		})
	}
}
