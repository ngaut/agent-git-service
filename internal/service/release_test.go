package service_test

import (
	"context"
	"strings"
	"testing"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/service"
)

// setupReleaseTest creates a site_admin user + repo for release tests.
func setupReleaseTest(t *testing.T, svc *service.Service, login, repoName string) {
	t.Helper()
	svc.DB.Create(&db.User{Login: login, Name: login, Type: db.TypeUser, SiteAdmin: true})
	_, err := svc.CreateRepo(context.Background(), service.CreateRepoInput{OwnerLogin: login, Name: repoName, AutoInit: true})
	if err != nil {
		t.Fatalf("setupReleaseTest failed: %v", err)
	}
}

// setupReleaseTestWithCommit creates a site_admin user + repo with an initial commit.
func setupReleaseTestWithCommit(t *testing.T, svc *service.Service, login, repoName string) {
	t.Helper()
	svc.DB.Create(&db.User{Login: login, Name: login, Type: db.TypeUser, SiteAdmin: true})
	_, err := svc.CreateRepo(context.Background(), service.CreateRepoInput{
		OwnerLogin: login,
		Name:       repoName,
		AutoInit:   true,
	})
	if err != nil {
		t.Fatalf("setupReleaseTestWithCommit failed: %v", err)
	}
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func assertReleaseNoteLine(t *testing.T, line, msg, sha string) {
	t.Helper()
	prefix := "* " + msg + " ("
	if !strings.HasPrefix(line, prefix) || !strings.HasSuffix(line, ")") {
		t.Fatalf("unexpected release note line: %q", line)
	}
	hash := strings.TrimSuffix(strings.TrimPrefix(line, prefix), ")")
	if !strings.HasPrefix(hash, shortSHA(sha)) {
		t.Fatalf("expected hash prefix %q for %q, got %q", shortSHA(sha), msg, hash)
	}
}

func TestReleaseFlow(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupReleaseTest(t, svc, "reluser", "relrepo")

	// Create release
	rel, err := svc.CreateRelease(ctx, "reluser/relrepo", service.CreateReleaseInput{
		TagName: "v1.0.0",
		Name:    "First Release",
		Body:    "Release notes",
	})
	if err != nil {
		t.Fatalf("CreateRelease failed: %v", err)
	}
	if rel.TagName != "v1.0.0" {
		t.Errorf("expected tag v1.0.0, got %s", rel.TagName)
	}
	if rel.PublishedAt == nil {
		t.Error("expected published_at to be set")
	}

	// Get by ID
	got, err := svc.GetRelease(ctx, rel.ID)
	if err != nil {
		t.Fatalf("GetRelease failed: %v", err)
	}
	if got.Name != "First Release" {
		t.Errorf("expected name 'First Release', got %s", got.Name)
	}

	// Get by tag
	gotByTag, err := svc.GetReleaseByTag(ctx, "reluser/relrepo", "v1.0.0")
	if err != nil {
		t.Fatalf("GetReleaseByTag failed: %v", err)
	}
	if gotByTag.ID != rel.ID {
		t.Errorf("expected id %d, got %d", rel.ID, gotByTag.ID)
	}

	// Get latest
	latest, err := svc.GetLatestRelease(ctx, "reluser/relrepo")
	if err != nil {
		t.Fatalf("GetLatestRelease failed: %v", err)
	}
	if latest.ID != rel.ID {
		t.Errorf("expected latest to be id %d, got %d", rel.ID, latest.ID)
	}

	// Duplicate tag should fail
	_, err = svc.CreateRelease(ctx, "reluser/relrepo", service.CreateReleaseInput{TagName: "v1.0.0"})
	if err == nil {
		t.Error("expected error for duplicate tag, got nil")
	}
}

func TestReleaseListAndUpdate(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupReleaseTest(t, svc, "reluser2", "relrepo2")

	_, err := svc.CreateRelease(ctx, "reluser2/relrepo2", service.CreateReleaseInput{TagName: "v1", Name: "R1"})
	if err != nil {
		t.Fatalf("CreateRelease(v1) failed: %v", err)
	}
	_, err = svc.CreateRelease(ctx, "reluser2/relrepo2", service.CreateReleaseInput{TagName: "v2", Name: "R2"})
	if err != nil {
		t.Fatalf("CreateRelease(v2) failed: %v", err)
	}

	// List
	all, err := svc.ListReleases(ctx, "reluser2/relrepo2")
	if err != nil {
		t.Fatalf("ListReleases failed: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 releases, got %d", len(all))
	}

	// Update
	newName := "Updated R1"
	newBody := "Updated body"
	isDraft := true
	updated, err := svc.UpdateRelease(ctx, all[len(all)-1].ID, service.UpdateReleaseInput{
		Name:  &newName,
		Body:  &newBody,
		Draft: &isDraft,
	})
	if err != nil {
		t.Fatalf("UpdateRelease failed: %v", err)
	}
	if updated.Name != "Updated R1" {
		t.Errorf("expected name 'Updated R1', got %s", updated.Name)
	}
	if !updated.Draft {
		t.Error("expected draft to be true")
	}
}

func TestReleaseDelete(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupReleaseTest(t, svc, "reluser3", "relrepo3")

	rel, err := svc.CreateRelease(ctx, "reluser3/relrepo3", service.CreateReleaseInput{TagName: "v1"})
	if err != nil {
		t.Fatalf("CreateRelease failed: %v", err)
	}

	if err := svc.DeleteRelease(ctx, rel.ID); err != nil {
		t.Fatalf("DeleteRelease failed: %v", err)
	}

	_, err = svc.GetRelease(ctx, rel.ID)
	if err == nil {
		t.Error("expected error after delete")
	}
}

func TestDeleteReleaseWithAssets(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupReleaseTest(t, svc, "reluser5", "relrepo5")

	rel, err := svc.CreateRelease(ctx, "reluser5/relrepo5", service.CreateReleaseInput{TagName: "v1"})
	if err != nil {
		t.Fatalf("CreateRelease failed: %v", err)
	}

	// Upload an asset to the release
	_, err = svc.UploadReleaseAsset(ctx, rel.ID, "app.zip", "", "application/zip",
		strings.NewReader("zip content"))
	if err != nil {
		t.Fatalf("UploadReleaseAsset failed: %v", err)
	}

	// Delete the release (should cascade-delete assets)
	if err := svc.DeleteRelease(ctx, rel.ID); err != nil {
		t.Fatalf("DeleteRelease with assets failed: %v", err)
	}

	// Verify release is gone
	_, err = svc.GetRelease(ctx, rel.ID)
	if err == nil {
		t.Error("expected error fetching deleted release")
	}

	// Verify assets are gone
	assets, err := svc.ListReleaseAssets(ctx, rel.ID)
	if err != nil {
		t.Fatalf("ListReleaseAssets failed: %v", err)
	}
	if len(assets) != 0 {
		t.Errorf("expected 0 assets after release delete, got %d", len(assets))
	}
}

func TestReleaseAssets(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupReleaseTest(t, svc, "reluser4", "relrepo4")

	rel, err := svc.CreateRelease(ctx, "reluser4/relrepo4", service.CreateReleaseInput{TagName: "v1"})
	if err != nil {
		t.Fatalf("CreateRelease failed: %v", err)
	}

	// Upload asset
	asset, err := svc.UploadReleaseAsset(ctx, rel.ID, "binary.tar.gz", "Linux amd64", "application/gzip",
		strings.NewReader("fake binary content"))
	if err != nil {
		t.Fatalf("UploadReleaseAsset failed: %v", err)
	}
	if asset.Name != "binary.tar.gz" {
		t.Errorf("expected name binary.tar.gz, got %s", asset.Name)
	}
	if asset.Size != int64(len("fake binary content")) {
		t.Errorf("expected size %d, got %d", len("fake binary content"), asset.Size)
	}

	// Get asset
	got, err := svc.GetReleaseAsset(ctx, asset.ID)
	if err != nil {
		t.Fatalf("GetReleaseAsset failed: %v", err)
	}
	if got.ContentType != "application/gzip" {
		t.Errorf("expected content type application/gzip, got %s", got.ContentType)
	}

	// List assets
	assets, err := svc.ListReleaseAssets(ctx, rel.ID)
	if err != nil {
		t.Fatalf("ListReleaseAssets failed: %v", err)
	}
	if len(assets) != 1 {
		t.Errorf("expected 1 asset, got %d", len(assets))
	}

	// Delete asset
	if err := svc.DeleteReleaseAsset(ctx, asset.ID); err != nil {
		t.Fatalf("DeleteReleaseAsset failed: %v", err)
	}
	assets2, _ := svc.ListReleaseAssets(ctx, rel.ID)
	if len(assets2) != 0 {
		t.Errorf("expected 0 assets after delete, got %d", len(assets2))
	}
}

func TestCreateRelease_CreatesGitTag(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupReleaseTestWithCommit(t, svc, "tagu1", "tagrepo1")

	_, err := svc.CreateRelease(ctx, "tagu1/tagrepo1", service.CreateReleaseInput{
		TagName: "v1.0.0",
		Name:    "Release 1",
	})
	if err != nil {
		t.Fatalf("CreateRelease failed: %v", err)
	}

	// Verify the git tag exists
	tags, err := svc.Git.ListTags(ctx, "tagu1/tagrepo1")
	if err != nil {
		t.Fatalf("ListTags failed: %v", err)
	}
	found := false
	for _, tag := range tags {
		if tag.Name == "v1.0.0" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected git tag v1.0.0 to exist after CreateRelease")
	}
}

func TestCreateRelease_ExistingTagNotMoved(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupReleaseTestWithCommit(t, svc, "tagu2", "tagrepo2")

	// Get the HEAD SHA
	headSHA, err := svc.Git.HeadSHA(ctx, "tagu2/tagrepo2", "main")
	if err != nil {
		t.Fatalf("HeadSHA failed: %v", err)
	}

	// Pre-create the tag manually
	if err := svc.Git.UpdateRef(ctx, "tagu2/tagrepo2", "refs/tags/v2.0.0", headSHA); err != nil {
		t.Fatalf("UpdateRef failed: %v", err)
	}

	// Create a release with the same tag
	_, err = svc.CreateRelease(ctx, "tagu2/tagrepo2", service.CreateReleaseInput{
		TagName: "v2.0.0",
		Name:    "Release 2",
	})
	if err != nil {
		t.Fatalf("CreateRelease failed: %v", err)
	}

	// Verify the tag still points to the same SHA
	tags, err := svc.Git.ListTags(ctx, "tagu2/tagrepo2")
	if err != nil {
		t.Fatalf("ListTags failed: %v", err)
	}
	for _, tag := range tags {
		if tag.Name == "v2.0.0" {
			if tag.SHA != headSHA[:len(tag.SHA)] {
				t.Errorf("tag SHA changed: expected prefix of %s, got %s", headSHA, tag.SHA)
			}
			return
		}
	}
	t.Error("tag v2.0.0 not found")
}

func TestCreateRelease_RollbackOnDBFailure(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupReleaseTestWithCommit(t, svc, "tagu3", "tagrepo3")

	// Create a release to occupy the tag name in DB
	_, err := svc.CreateRelease(ctx, "tagu3/tagrepo3", service.CreateReleaseInput{
		TagName: "v3.0.0",
		Name:    "Release 3",
	})
	if err != nil {
		t.Fatalf("first CreateRelease failed: %v", err)
	}

	// Delete the git tag but keep the DB row so next create hits DB conflict
	if err := svc.Git.DeleteRef(ctx, "tagu3/tagrepo3", "refs/tags/v3.0.0"); err != nil {
		t.Fatalf("DeleteRef failed: %v", err)
	}

	// Attempt to create a release with the same tag - this should fail at DB level
	// because the DB row still exists (duplicate check catches it before tag creation)
	_, err = svc.CreateRelease(ctx, "tagu3/tagrepo3", service.CreateReleaseInput{
		TagName: "v3.0.0",
		Name:    "Release 3 Again",
	})
	if err == nil {
		t.Fatal("expected error for duplicate tag")
	}

	// The git tag should NOT have been re-created since the duplicate check
	// happens before tag creation
	tags, err := svc.Git.ListTags(ctx, "tagu3/tagrepo3")
	if err != nil {
		t.Fatalf("ListTags failed: %v", err)
	}
	for _, tag := range tags {
		if tag.Name == "v3.0.0" {
			t.Error("tag v3.0.0 should not exist after rollback")
		}
	}
}

func TestCreateRelease_TargetCommitish_SHA(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupReleaseTestWithCommit(t, svc, "tagu4", "tagrepo4")

	headSHA, err := svc.Git.HeadSHA(ctx, "tagu4/tagrepo4", "main")
	if err != nil {
		t.Fatalf("HeadSHA failed: %v", err)
	}

	_, err = svc.CreateRelease(ctx, "tagu4/tagrepo4", service.CreateReleaseInput{
		TagName:         "v4.0.0",
		Name:            "Release 4",
		TargetCommitish: headSHA,
	})
	if err != nil {
		t.Fatalf("CreateRelease failed: %v", err)
	}

	tags, err := svc.Git.ListTags(ctx, "tagu4/tagrepo4")
	if err != nil {
		t.Fatalf("ListTags failed: %v", err)
	}
	found := false
	for _, tag := range tags {
		if tag.Name == "v4.0.0" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected tag v4.0.0 to be created with SHA target")
	}
}

func TestCreateRelease_TargetCommitish_Branch(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupReleaseTestWithCommit(t, svc, "tagu5", "tagrepo5")

	// Create a branch
	if err := svc.Git.CreateBranch(ctx, "tagu5/tagrepo5", "feature", "main"); err != nil {
		t.Fatalf("CreateBranch failed: %v", err)
	}

	_, err := svc.CreateRelease(ctx, "tagu5/tagrepo5", service.CreateReleaseInput{
		TagName:         "v5.0.0",
		Name:            "Release 5",
		TargetCommitish: "feature",
	})
	if err != nil {
		t.Fatalf("CreateRelease failed: %v", err)
	}

	tags, err := svc.Git.ListTags(ctx, "tagu5/tagrepo5")
	if err != nil {
		t.Fatalf("ListTags failed: %v", err)
	}
	found := false
	for _, tag := range tags {
		if tag.Name == "v5.0.0" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected tag v5.0.0 to be created from branch target")
	}
}

func TestCreateRelease_InvalidTargetCommitish(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupReleaseTestWithCommit(t, svc, "tagu6", "tagrepo6")

	_, err := svc.CreateRelease(ctx, "tagu6/tagrepo6", service.CreateReleaseInput{
		TagName:         "v6.0.0",
		Name:            "Release 6",
		TargetCommitish: "nonexistent-branch",
	})
	if err == nil {
		t.Fatal("expected error for invalid target_commitish")
	}

	// Verify no tag was created
	tags, err := svc.Git.ListTags(ctx, "tagu6/tagrepo6")
	if err != nil {
		t.Fatalf("ListTags failed: %v", err)
	}
	for _, tag := range tags {
		if tag.Name == "v6.0.0" {
			t.Error("tag should not exist after invalid target_commitish")
		}
	}

	// Verify no DB row was created
	_, err = svc.GetReleaseByTag(ctx, "tagu6/tagrepo6", "v6.0.0")
	if err == nil {
		t.Error("expected no DB row for failed release creation")
	}
}

func TestGenerateReleaseNotes(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupReleaseTestWithCommit(t, svc, "notesuser", "notesrepo")
	full := "notesuser/notesrepo"

	initialSHA, err := svc.Git.HeadSHA(ctx, full, "main")
	if err != nil {
		t.Fatalf("HeadSHA failed: %v", err)
	}
	if err := svc.Git.CreateTagIfNotExists(ctx, full, "v1.0.0", "Release v1.0.0", initialSHA); err != nil {
		t.Fatalf("CreateTagIfNotExists(v1.0.0) failed: %v", err)
	}

	commit1, err := svc.Git.WriteFile(ctx, full, "main", "feature.txt", "Add feature A", []byte("feature"))
	if err != nil {
		t.Fatalf("WriteFile(feature) failed: %v", err)
	}
	commit2, err := svc.Git.WriteFile(ctx, full, "main", "fix.txt", "Fix bug B", []byte("fix"))
	if err != nil {
		t.Fatalf("WriteFile(fix) failed: %v", err)
	}
	if err := svc.Git.CreateTagIfNotExists(ctx, full, "v1.1.0", "Release v1.1.0", commit2); err != nil {
		t.Fatalf("CreateTagIfNotExists(v1.1.0) failed: %v", err)
	}

	name, notes, err := svc.GenerateReleaseNotes(ctx, full, "v1.1.0", "v1.0.0")
	if err != nil {
		t.Fatalf("GenerateReleaseNotes failed: %v", err)
	}
	if name != "v1.1.0" {
		t.Fatalf("expected name v1.1.0, got %s", name)
	}

	lines := strings.Split(strings.TrimSuffix(notes, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines in release notes, got %d: %q", len(lines), notes)
	}
	if lines[0] != "## What's Changed" {
		t.Fatalf("unexpected header line: %q", lines[0])
	}
	assertReleaseNoteLine(t, lines[1], "Fix bug B", commit2)
	assertReleaseNoteLine(t, lines[2], "Add feature A", commit1)
}

func TestGenerateReleaseNotes_MissingTag(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	setupReleaseTestWithCommit(t, svc, "notesuser2", "notesrepo2")
	full := "notesuser2/notesrepo2"

	headSHA, err := svc.Git.HeadSHA(ctx, full, "main")
	if err != nil {
		t.Fatalf("HeadSHA failed: %v", err)
	}
	if err := svc.Git.CreateTagIfNotExists(ctx, full, "v1.0.0", "Release v1.0.0", headSHA); err != nil {
		t.Fatalf("CreateTagIfNotExists(v1.0.0) failed: %v", err)
	}

	name, notes, err := svc.GenerateReleaseNotes(ctx, full, "v1.0.0", "v0.9.0")
	if err != nil {
		t.Fatalf("GenerateReleaseNotes failed: %v", err)
	}
	if name != "v1.0.0" {
		t.Fatalf("expected name v1.0.0, got %s", name)
	}
	expected := "## What's Changed\nNo changes.\n"
	if notes != expected {
		t.Fatalf("unexpected notes body: %q", notes)
	}
}
