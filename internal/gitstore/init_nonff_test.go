package gitstore_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ngaut/agent-git-service/internal/gitstore"
)

// TestInit_InstallsNonFFRejectHook is the unit-level guard for the fix to
// #1234: every bare repo the server creates must have a pre-receive hook
// that rejects non-fast-forward pushes on non-standard ref namespaces.
func TestInit_InstallsNonFFRejectHook(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gitstore-nonff-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repoName := "user/nonff-hook"
	if err := store.Init(ctx, repoName, "main", false); err != nil {
		t.Fatalf("Init: %v", err)
	}

	root, err := store.RepoRoot(ctx)
	if err != nil {
		t.Fatalf("RepoRoot: %v", err)
	}
	repoDir := filepath.Join(root, repoName+".git")
	if _, err := os.Stat(repoDir); err != nil {
		repoDir = filepath.Join(root, repoName)
	}
	hook := filepath.Join(repoDir, "hooks", "pre-receive")
	info, err := os.Stat(hook)
	if err != nil {
		t.Fatalf("pre-receive hook not found: %v", err)
	}
	if info.Mode()&0o100 == 0 {
		t.Errorf("pre-receive hook is not executable: mode=%v", info.Mode())
	}
	body, err := os.ReadFile(hook)
	if err != nil {
		t.Fatalf("read hook: %v", err)
	}
	// Sanity-check the hook's logic is the one we installed.
	for _, need := range []string{"refs/heads/*", "refs/tags/*", "merge-base --is-ancestor", "non-fast-forward"} {
		if !strings.Contains(string(body), need) {
			t.Errorf("pre-receive hook missing expected fragment %q", need)
		}
	}
}

// TestReceive_RejectsNonFastForwardOnCustomRef drives the hook end-to-end:
// clone the bare repo, build two SIBLING commits on separate branches, push
// the first to a custom ref (refs/locks/*), then push the second over the
// same ref without --force. The pre-receive hook must reject with
// "non-fast-forward"; pre-fix the second push succeeded silently.
func TestReceive_RejectsNonFastForwardOnCustomRef(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	tmpDir, err := os.MkdirTemp("", "gitstore-nonff-push-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repoName := "user/nonff-push"
	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init: %v", err)
	}
	root, _ := store.RepoRoot(ctx)
	bareDir := filepath.Join(root, repoName+".git")
	if _, err := os.Stat(bareDir); err != nil {
		bareDir = filepath.Join(root, repoName)
	}

	// Clone the bare repo to a working copy. `git clone` takes paths, not
	// -C, so bypass the shared runGit helper here.
	workDir := filepath.Join(tmpDir, "work")
	if out, err := exec.Command("git", "clone", bareDir, workDir).CombinedOutput(); err != nil {
		t.Fatalf("git clone: %v\n%s", err, out)
	}

	runGit(t, workDir, "config", "user.name", "tester")
	runGit(t, workDir, "config", "user.email", "tester@example.com")

	runGit(t, workDir, "checkout", "-b", "c1")
	runGit(t, workDir, "commit", "--allow-empty", "-m", "c1")
	c1 := runGit(t, workDir, "rev-parse", "HEAD")
	runGit(t, workDir, "checkout", "main")
	runGit(t, workDir, "checkout", "-b", "c2")
	runGit(t, workDir, "commit", "--allow-empty", "-m", "c2")
	c2 := runGit(t, workDir, "rev-parse", "HEAD")

	// Sanity: c2 must not be a descendant of c1.
	cmd := exec.Command("git", "-C", workDir, "merge-base", "--is-ancestor", c1, c2)
	if err := cmd.Run(); err == nil {
		t.Fatal("test setup: c2 is an ancestor of c1 — need two true siblings")
	}

	// Push c1 to a fresh custom ref — must succeed (ref creation is allowed).
	runGit(t, workDir, "push", bareDir, "c1:refs/locks/nonff-test")

	// Push c2 over the same ref WITHOUT --force — hook must reject.
	cmd = exec.Command("git", "-C", workDir, "push", bareDir, "c2:refs/locks/nonff-test")
	combined, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("non-FF push unexpectedly succeeded; server accepted sibling commit over existing ref\noutput: %s", combined)
	}
	if !strings.Contains(string(combined), "non-fast-forward") {
		t.Errorf("rejection reason missing from server output; want non-fast-forward error, got:\n%s", combined)
	}
}

// TestReceive_AllowsNonFastForwardOnHeads is the negative-space test: the
// hook must NOT intervene for refs/heads/*, so existing PR-rebase flows
// and other server-initiated branch rewrites keep working.
func TestReceive_AllowsNonFastForwardOnHeads(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	tmpDir, err := os.MkdirTemp("", "gitstore-nonff-heads-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repoName := "user/nonff-heads"
	if err := store.Init(ctx, repoName, "main", true); err != nil {
		t.Fatalf("Init: %v", err)
	}
	root, _ := store.RepoRoot(ctx)
	bareDir := filepath.Join(root, repoName+".git")
	if _, err := os.Stat(bareDir); err != nil {
		bareDir = filepath.Join(root, repoName)
	}

	workDir := filepath.Join(tmpDir, "work")
	if out, err := exec.Command("git", "clone", bareDir, workDir).CombinedOutput(); err != nil {
		t.Fatalf("git clone: %v\n%s", err, out)
	}
	runGit(t, workDir, "config", "user.name", "tester")
	runGit(t, workDir, "config", "user.email", "tester@example.com")

	runGit(t, workDir, "checkout", "-b", "feature")
	runGit(t, workDir, "commit", "--allow-empty", "-m", "v1")
	runGit(t, workDir, "push", bareDir, "feature:refs/heads/feature")

	// Force a new history: reset to main, commit again on feature.
	runGit(t, workDir, "checkout", "main")
	runGit(t, workDir, "branch", "-D", "feature")
	runGit(t, workDir, "checkout", "-b", "feature")
	runGit(t, workDir, "commit", "--allow-empty", "-m", "v2-diff-history")

	// --force push of a non-FF onto refs/heads/feature — the hook lets
	// standard refs fall through, so this should succeed (or at worst be
	// blocked by some other policy, but not by our hook's "non-fast-forward"
	// wording).
	cmd := exec.Command("git", "-C", workDir, "push", "--force", bareDir, "feature:refs/heads/feature")
	combined, err := cmd.CombinedOutput()
	if err != nil && strings.Contains(string(combined), "non-fast-forward push") {
		t.Errorf("hook rejected refs/heads/feature non-FF push; must only apply to custom namespaces\noutput: %s", combined)
	}
}
