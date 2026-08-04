package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/wikicatalog"
	"gorm.io/gorm"
)

func TestRecoverPendingWikiReferenceEffectsRebuildsFromCatalog(t *testing.T) {
	gdb, cleanup := openMigratedServiceTestDB(t)
	defer cleanup()
	ctx := context.Background()

	owner := db.User{Login: "wiki-reference-recovery", Name: "Wiki Reference Recovery", Type: db.TypeUser}
	if err := gdb.Create(&owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	repo := db.Repository{
		OwnerID:       owner.ID,
		Name:          "wiki-reference-recovery",
		FullName:      owner.Login + "/wiki-reference-recovery",
		DefaultBranch: "main",
	}
	if err := gdb.Create(&repo).Error; err != nil {
		t.Fatalf("create repository: %v", err)
	}
	if err := gdb.Create(&db.Issue{
		RepositoryID: repo.ID,
		Number:       1,
		Title:        "Referenced issue",
		AuthorID:     owner.ID,
	}).Error; err != nil {
		t.Fatalf("create issue: %v", err)
	}

	blobStore := wikicatalog.NewBlobStore(t.TempDir())
	catalog := wikicatalog.New(gdb, blobStore)
	svc := &Service{DB: gdb, WikiCatalog: catalog, WikiBlob: blobStore}
	result, err := catalog.ApplyChangeSet(ctx, wikicatalog.ChangeSetRequest{
		RepositoryID: repo.ID,
		Source:       wikicatalog.SourceREST,
		Message:      "create page before interruption",
		Changes:      []wikicatalog.Change{{Op: wikicatalog.OpUpsert, Slug: "home", Body: []byte("See #1")}},
	})
	if err != nil {
		t.Fatalf("ApplyChangeSet: %v", err)
	}
	if !result.ReferenceEffectsPending {
		t.Fatal("expected committed changeset to retain pending reference effects")
	}

	recovered, err := svc.recoverPendingWikiReferenceEffects(ctx)
	if err != nil {
		t.Fatalf("recoverPendingWikiReferenceEffects: %v", err)
	}
	if recovered != 1 {
		t.Fatalf("recovered repositories = %d, want 1", recovered)
	}

	var references int64
	if err := gdb.Model(&db.IssueReference{}).
		Where("source_type = ? AND source_repository_id = ? AND source_wiki_slug = ?", issueReferenceSourceWikiPage, repo.ID, "home").
		Count(&references).Error; err != nil {
		t.Fatalf("count recovered references: %v", err)
	}
	if references != 1 {
		t.Fatalf("recovered references = %d, want 1", references)
	}
	var head db.WikiRepoHead
	if err := gdb.First(&head, "repository_id = ?", repo.ID).Error; err != nil {
		t.Fatalf("load repo head: %v", err)
	}
	if head.ReferenceEffectsThroughChangesetID != nil {
		t.Fatalf("reference recovery cursor after recovery = %v, want nil", *head.ReferenceEffectsThroughChangesetID)
	}
}

func TestRebuildWikiReferencesSkipsStaleCursorAfterCompletion(t *testing.T) {
	gdb, cleanup := openMigratedServiceTestDB(t)
	defer cleanup()
	ctx := context.Background()

	owner := db.User{Login: "wiki-reference-stale", Name: "Wiki Reference Stale", Type: db.TypeUser}
	if err := gdb.Create(&owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	repo := db.Repository{
		OwnerID:       owner.ID,
		Name:          "wiki-reference-stale",
		FullName:      owner.Login + "/wiki-reference-stale",
		DefaultBranch: "main",
	}
	if err := gdb.Create(&repo).Error; err != nil {
		t.Fatalf("create repository: %v", err)
	}
	if err := gdb.Create(&db.Issue{
		RepositoryID: repo.ID,
		Number:       1,
		Title:        "Referenced issue",
		AuthorID:     owner.ID,
	}).Error; err != nil {
		t.Fatalf("create issue: %v", err)
	}

	blobStore := wikicatalog.NewBlobStore(t.TempDir())
	catalog := wikicatalog.New(gdb, blobStore)
	body := strings.Repeat("x", wikicatalog.MaxBodyInlineBytes+1) + "\nSee #1"
	result, err := catalog.ApplyChangeSet(ctx, wikicatalog.ChangeSetRequest{
		RepositoryID: repo.ID,
		Source:       wikicatalog.SourceREST,
		Message:      "create page with reference",
		Changes:      []wikicatalog.Change{{Op: wikicatalog.OpUpsert, Slug: "home", Body: []byte(body)}},
	})
	if err != nil {
		t.Fatalf("ApplyChangeSet: %v", err)
	}
	if len(result.Changes) != 1 || result.Changes[0].BlobSHA == "" {
		t.Fatalf("unexpected change result: %#v", result.Changes)
	}

	svc := &Service{DB: gdb, WikiCatalog: catalog, WikiBlob: blobStore}
	done, err := svc.enqueueWikiPostCommitEffects(ctx, repo, result)
	if err != nil {
		t.Fatalf("enqueue post-commit effects: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("run post-commit effects: %v", err)
	}
	svc.Wg.Wait()
	assertWikiIssueReferenceCount(t, gdb, repo.ID, "home", 1, 1)

	if err := blobStore.Delete(ctx, result.Changes[0].BlobSHA); err != nil {
		t.Fatalf("delete page blob: %v", err)
	}
	if err := svc.rebuildWikiReferences(ctx, pendingWikiReferenceRepo{
		RepositoryID:     repo.ID,
		FullName:         repo.FullName,
		ThroughChangeset: result.ChangesetID,
	}); err != nil {
		t.Fatalf("stale rebuildWikiReferences: %v", err)
	}
	assertWikiIssueReferenceCount(t, gdb, repo.ID, "home", 1, 1)

	var head db.WikiRepoHead
	if err := gdb.First(&head, "repository_id = ?", repo.ID).Error; err != nil {
		t.Fatalf("load completed cursor: %v", err)
	}
	if head.ReferenceEffectsThroughChangesetID != nil {
		t.Fatalf("stale rebuild cursor = %v, want nil", *head.ReferenceEffectsThroughChangesetID)
	}
}

func TestRebuildWikiReferencesPropagatesTargetLookupError(t *testing.T) {
	gdb, cleanup := openMigratedServiceTestDB(t)
	defer cleanup()
	ctx := context.Background()

	owner := db.User{Login: "wiki-reference-lookup", Name: "Wiki Reference Lookup", Type: db.TypeUser}
	if err := gdb.Create(&owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	repo := db.Repository{
		OwnerID:       owner.ID,
		Name:          "wiki-reference-lookup",
		FullName:      owner.Login + "/wiki-reference-lookup",
		DefaultBranch: "main",
	}
	if err := gdb.Create(&repo).Error; err != nil {
		t.Fatalf("create repository: %v", err)
	}
	if err := gdb.Create(&db.Issue{
		RepositoryID: repo.ID,
		Number:       1,
		Title:        "Referenced issue",
		AuthorID:     owner.ID,
	}).Error; err != nil {
		t.Fatalf("create issue: %v", err)
	}

	blobStore := wikicatalog.NewBlobStore(t.TempDir())
	catalog := wikicatalog.New(gdb, blobStore)
	result, err := catalog.ApplyChangeSet(ctx, wikicatalog.ChangeSetRequest{
		RepositoryID: repo.ID,
		Source:       wikicatalog.SourceREST,
		Message:      "create page with reference",
		Changes:      []wikicatalog.Change{{Op: wikicatalog.OpUpsert, Slug: "home", Body: []byte("See #1")}},
	})
	if err != nil {
		t.Fatalf("ApplyChangeSet: %v", err)
	}
	slug := "home"
	if err := gdb.Create(&db.IssueReference{
		SourceType:         issueReferenceSourceWikiPage,
		SourceRepositoryID: repo.ID,
		SourceWikiSlug:     &slug,
		TargetRepositoryID: repo.ID,
		TargetNumber:       1,
		RawReference:       "#1",
	}).Error; err != nil {
		t.Fatalf("create existing wiki reference: %v", err)
	}

	lookupErr := errors.New("injected target issue lookup failure")
	const callbackName = "test:fail-wiki-reference-target-issue-lookup"
	if err := gdb.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Table == "issues" {
			tx.AddError(lookupErr)
		}
	}); err != nil {
		t.Fatalf("register query callback: %v", err)
	}
	defer gdb.Callback().Query().Remove(callbackName)

	svc := &Service{DB: gdb, WikiCatalog: catalog, WikiBlob: blobStore}
	err = svc.rebuildWikiReferences(ctx, pendingWikiReferenceRepo{
		RepositoryID:     repo.ID,
		FullName:         repo.FullName,
		ThroughChangeset: result.ChangesetID,
	})
	if !errors.Is(err, lookupErr) {
		t.Fatalf("rebuildWikiReferences error = %v, want injected lookup error", err)
	}
	assertWikiIssueReferenceCount(t, gdb, repo.ID, "home", 1, 1)

	var head db.WikiRepoHead
	if err := gdb.First(&head, "repository_id = ?", repo.ID).Error; err != nil {
		t.Fatalf("load retained cursor: %v", err)
	}
	if head.ReferenceEffectsThroughChangesetID == nil || *head.ReferenceEffectsThroughChangesetID != result.ChangesetID {
		t.Fatalf("cursor after failed rebuild = %v, want %d", head.ReferenceEffectsThroughChangesetID, result.ChangesetID)
	}
}

func TestWikiReferenceRecoveryMarkerSkipsPlainCreateButTracksUpdate(t *testing.T) {
	gdb, cleanup := openMigratedServiceTestDB(t)
	defer cleanup()
	ctx := context.Background()

	owner := db.User{Login: "wiki-reference-marker", Name: "Wiki Reference Marker", Type: db.TypeUser}
	if err := gdb.Create(&owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	repo := db.Repository{
		OwnerID:       owner.ID,
		Name:          "wiki-reference-marker",
		FullName:      owner.Login + "/wiki-reference-marker",
		DefaultBranch: "main",
	}
	if err := gdb.Create(&repo).Error; err != nil {
		t.Fatalf("create repository: %v", err)
	}
	catalog := wikicatalog.New(gdb, wikicatalog.NewBlobStore(t.TempDir()))

	created, err := catalog.ApplyChangeSet(ctx, wikicatalog.ChangeSetRequest{
		RepositoryID: repo.ID,
		Source:       wikicatalog.SourceREST,
		Message:      "plain create",
		Changes:      []wikicatalog.Change{{Op: wikicatalog.OpUpsert, Slug: "home", Body: []byte("plain body")}},
	})
	if err != nil {
		t.Fatalf("create page: %v", err)
	}
	if created.ReferenceEffectsPending {
		t.Fatal("plain page creation should not create a recovery marker")
	}
	var head db.WikiRepoHead
	if err := gdb.First(&head, "repository_id = ?", repo.ID).Error; err != nil {
		t.Fatalf("load head after create: %v", err)
	}
	if head.ReferenceEffectsThroughChangesetID != nil {
		t.Fatalf("plain create reference cursor = %v, want nil", *head.ReferenceEffectsThroughChangesetID)
	}

	updated, err := catalog.ApplyChangeSet(ctx, wikicatalog.ChangeSetRequest{
		RepositoryID: repo.ID,
		Source:       wikicatalog.SourceREST,
		Message:      "plain update",
		Changes:      []wikicatalog.Change{{Op: wikicatalog.OpUpsert, Slug: "home", Body: []byte("updated body")}},
	})
	if err != nil {
		t.Fatalf("update page: %v", err)
	}
	if !updated.ReferenceEffectsPending {
		t.Fatal("page update should retain a marker so removed references can be recovered")
	}
	if err := gdb.First(&head, "repository_id = ?", repo.ID).Error; err != nil {
		t.Fatalf("load head after update: %v", err)
	}
	if head.ReferenceEffectsThroughChangesetID == nil || *head.ReferenceEffectsThroughChangesetID != updated.ChangesetID {
		t.Fatalf("update reference cursor = %v, want %d", head.ReferenceEffectsThroughChangesetID, updated.ChangesetID)
	}
}

func TestWikiReferenceRecoveryCursorPreservesNewerChangeset(t *testing.T) {
	gdb, cleanup := openMigratedServiceTestDB(t)
	defer cleanup()
	ctx := context.Background()

	owner := db.User{Login: "wiki-reference-generation", Name: "Wiki Reference Generation", Type: db.TypeUser}
	if err := gdb.Create(&owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	repo := db.Repository{
		OwnerID:       owner.ID,
		Name:          "wiki-reference-generation",
		FullName:      owner.Login + "/wiki-reference-generation",
		DefaultBranch: "main",
	}
	if err := gdb.Create(&repo).Error; err != nil {
		t.Fatalf("create repository: %v", err)
	}
	catalog := wikicatalog.New(gdb, wikicatalog.NewBlobStore(t.TempDir()))
	first, err := catalog.ApplyChangeSet(ctx, wikicatalog.ChangeSetRequest{
		RepositoryID: repo.ID,
		Source:       wikicatalog.SourceREST,
		Message:      "first reference",
		Changes:      []wikicatalog.Change{{Op: wikicatalog.OpUpsert, Slug: "home", Body: []byte("See #1")}},
	})
	if err != nil {
		t.Fatalf("create page: %v", err)
	}
	second, err := catalog.ApplyChangeSet(ctx, wikicatalog.ChangeSetRequest{
		RepositoryID: repo.ID,
		Source:       wikicatalog.SourceREST,
		Message:      "second reference",
		Changes:      []wikicatalog.Change{{Op: wikicatalog.OpUpsert, Slug: "home", Body: []byte("See #2")}},
	})
	if err != nil {
		t.Fatalf("update page: %v", err)
	}

	svc := &Service{DB: gdb}
	if err := svc.markWikiReferenceEffectsComplete(ctx, repo.ID, first.ChangesetID); err != nil {
		t.Fatalf("complete first changeset: %v", err)
	}
	var head db.WikiRepoHead
	if err := gdb.First(&head, "repository_id = ?", repo.ID).Error; err != nil {
		t.Fatalf("load newer cursor: %v", err)
	}
	if head.ReferenceEffectsThroughChangesetID == nil || *head.ReferenceEffectsThroughChangesetID != second.ChangesetID {
		t.Fatalf("cursor through changeset = %v, want %d", head.ReferenceEffectsThroughChangesetID, second.ChangesetID)
	}
	if err := svc.markWikiReferenceEffectsComplete(ctx, repo.ID, second.ChangesetID); err != nil {
		t.Fatalf("complete second changeset: %v", err)
	}
	if err := gdb.First(&head, "repository_id = ?", repo.ID).Error; err != nil {
		t.Fatalf("load completed cursor: %v", err)
	}
	if head.ReferenceEffectsThroughChangesetID != nil {
		t.Fatalf("completed reference cursor = %v, want nil", *head.ReferenceEffectsThroughChangesetID)
	}
}

func TestWikiReferenceCoalescedCompletionKeepsRecoveryCursor(t *testing.T) {
	gdb, cleanup := openMigratedServiceTestDB(t)
	defer cleanup()
	ctx := context.Background()

	owner := db.User{Login: "wiki-reference-coalesced", Name: "Wiki Reference Coalesced", Type: db.TypeUser}
	if err := gdb.Create(&owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	repo := db.Repository{
		OwnerID:       owner.ID,
		Name:          "wiki-reference-coalesced",
		FullName:      owner.Login + "/wiki-reference-coalesced",
		DefaultBranch: "main",
	}
	if err := gdb.Create(&repo).Error; err != nil {
		t.Fatalf("create repository: %v", err)
	}
	for i := 1; i <= 2; i++ {
		if err := gdb.Create(&db.Issue{
			RepositoryID: repo.ID,
			Number:       i,
			Title:        "Referenced issue",
			AuthorID:     owner.ID,
		}).Error; err != nil {
			t.Fatalf("create issue %d: %v", i, err)
		}
	}

	blobStore := wikicatalog.NewBlobStore(t.TempDir())
	catalog := wikicatalog.New(gdb, blobStore)
	first, err := catalog.ApplyChangeSet(ctx, wikicatalog.ChangeSetRequest{
		RepositoryID: repo.ID,
		Source:       wikicatalog.SourceREST,
		Message:      "first reference",
		Changes:      []wikicatalog.Change{{Op: wikicatalog.OpUpsert, Slug: "alpha", Body: []byte("See #1")}},
	})
	if err != nil {
		t.Fatalf("create alpha page: %v", err)
	}
	if !first.ReferenceEffectsPending {
		t.Fatal("first page should retain pending reference effects")
	}
	if first.ReferenceEffectsCoalesced {
		t.Fatal("first page should not be marked coalesced")
	}
	second, err := catalog.ApplyChangeSet(ctx, wikicatalog.ChangeSetRequest{
		RepositoryID: repo.ID,
		Source:       wikicatalog.SourceREST,
		Message:      "second reference",
		Changes:      []wikicatalog.Change{{Op: wikicatalog.OpUpsert, Slug: "beta", Body: []byte("See #2")}},
	})
	if err != nil {
		t.Fatalf("create beta page: %v", err)
	}
	if !second.ReferenceEffectsPending {
		t.Fatal("second page should retain pending reference effects")
	}
	if !second.ReferenceEffectsCoalesced {
		t.Fatal("second page should be marked coalesced with the earlier pending cursor")
	}

	svc := &Service{DB: gdb, WikiCatalog: catalog, WikiBlob: blobStore}
	done, err := svc.enqueueWikiPostCommitEffects(ctx, repo, second)
	if err != nil {
		t.Fatalf("enqueue second post-commit effects: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("run second post-commit effects: %v", err)
	}
	svc.Wg.Wait()

	var head db.WikiRepoHead
	if err := gdb.First(&head, "repository_id = ?", repo.ID).Error; err != nil {
		t.Fatalf("load coalesced cursor: %v", err)
	}
	if head.ReferenceEffectsThroughChangesetID == nil || *head.ReferenceEffectsThroughChangesetID != second.ChangesetID {
		t.Fatalf("coalesced cursor = %v, want %d", head.ReferenceEffectsThroughChangesetID, second.ChangesetID)
	}
	assertWikiIssueReferenceCount(t, gdb, repo.ID, "alpha", 1, 0)
	assertWikiIssueReferenceCount(t, gdb, repo.ID, "beta", 2, 1)

	recovered, err := svc.recoverPendingWikiReferenceEffects(ctx)
	if err != nil {
		t.Fatalf("recoverPendingWikiReferenceEffects: %v", err)
	}
	if recovered != 1 {
		t.Fatalf("recovered repositories = %d, want 1", recovered)
	}
	if err := gdb.First(&head, "repository_id = ?", repo.ID).Error; err != nil {
		t.Fatalf("load cursor after recovery: %v", err)
	}
	if head.ReferenceEffectsThroughChangesetID != nil {
		t.Fatalf("cursor after recovery = %v, want nil", *head.ReferenceEffectsThroughChangesetID)
	}
	assertWikiIssueReferenceCount(t, gdb, repo.ID, "alpha", 1, 1)
	assertWikiIssueReferenceCount(t, gdb, repo.ID, "beta", 2, 1)
}

func TestRecoveredWikiReferenceCoalescedCompletionKeepsRecoveryCursor(t *testing.T) {
	gdb, cleanup := openMigratedServiceTestDB(t)
	defer cleanup()
	ctx := context.Background()

	owner := db.User{Login: "wiki-reference-recovered", Name: "Wiki Reference Recovered", Type: db.TypeUser}
	if err := gdb.Create(&owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	repo := db.Repository{
		OwnerID:       owner.ID,
		Name:          "wiki-reference-recovered",
		FullName:      owner.Login + "/wiki-reference-recovered",
		DefaultBranch: "main",
	}
	if err := gdb.Create(&repo).Error; err != nil {
		t.Fatalf("create repository: %v", err)
	}
	for i := 1; i <= 2; i++ {
		if err := gdb.Create(&db.Issue{
			RepositoryID: repo.ID,
			Number:       i,
			Title:        "Referenced issue",
			AuthorID:     owner.ID,
		}).Error; err != nil {
			t.Fatalf("create issue %d: %v", i, err)
		}
	}

	blobStore := wikicatalog.NewBlobStore(t.TempDir())
	catalog := wikicatalog.New(gdb, blobStore)
	first, err := catalog.ApplyChangeSet(ctx, wikicatalog.ChangeSetRequest{
		RepositoryID: repo.ID,
		Source:       wikicatalog.SourceREST,
		Message:      "first reference",
		Changes:      []wikicatalog.Change{{Op: wikicatalog.OpUpsert, Slug: "alpha", Body: []byte("See #1")}},
	})
	if err != nil {
		t.Fatalf("create alpha page: %v", err)
	}
	second, err := catalog.ApplyChangeSet(ctx, wikicatalog.ChangeSetRequest{
		RepositoryID: repo.ID,
		Source:       wikicatalog.SourceREST,
		Message:      "second reference",
		Changes:      []wikicatalog.Change{{Op: wikicatalog.OpUpsert, Slug: "beta", Body: []byte("See #2")}},
	})
	if err != nil {
		t.Fatalf("create beta page: %v", err)
	}
	if !first.ReferenceEffectsPending || !second.ReferenceEffectsCoalesced {
		t.Fatalf("unexpected reference pending flags: first=%v second coalesced=%v", first.ReferenceEffectsPending, second.ReferenceEffectsCoalesced)
	}

	var secondChangeset db.WikiChangeset
	if err := gdb.Take(&secondChangeset, "changeset_id = ?", second.ChangesetID).Error; err != nil {
		t.Fatalf("load second changeset: %v", err)
	}
	svc := &Service{DB: gdb, WikiCatalog: catalog, WikiBlob: blobStore}
	done, err := svc.enqueueRecoveredWikiChangesetEffects(ctx, repo, secondChangeset)
	if err != nil {
		t.Fatalf("enqueue recovered second post-commit effects: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("run recovered second post-commit effects: %v", err)
	}
	svc.Wg.Wait()

	var head db.WikiRepoHead
	if err := gdb.First(&head, "repository_id = ?", repo.ID).Error; err != nil {
		t.Fatalf("load recovered coalesced cursor: %v", err)
	}
	if head.ReferenceEffectsThroughChangesetID == nil || *head.ReferenceEffectsThroughChangesetID != second.ChangesetID {
		t.Fatalf("recovered coalesced cursor = %v, want %d", head.ReferenceEffectsThroughChangesetID, second.ChangesetID)
	}
	assertWikiIssueReferenceCount(t, gdb, repo.ID, "alpha", 1, 0)
	assertWikiIssueReferenceCount(t, gdb, repo.ID, "beta", 2, 1)

	recovered, err := svc.recoverPendingWikiReferenceEffects(ctx)
	if err != nil {
		t.Fatalf("recoverPendingWikiReferenceEffects: %v", err)
	}
	if recovered != 1 {
		t.Fatalf("recovered repositories = %d, want 1", recovered)
	}
	if err := gdb.First(&head, "repository_id = ?", repo.ID).Error; err != nil {
		t.Fatalf("load cursor after recovery: %v", err)
	}
	if head.ReferenceEffectsThroughChangesetID != nil {
		t.Fatalf("cursor after recovery = %v, want nil", *head.ReferenceEffectsThroughChangesetID)
	}
	assertWikiIssueReferenceCount(t, gdb, repo.ID, "alpha", 1, 1)
	assertWikiIssueReferenceCount(t, gdb, repo.ID, "beta", 2, 1)
}

func TestRecoverPendingWikiReferenceEffectsContinuesAfterRepoFailure(t *testing.T) {
	gdb, cleanup := openMigratedServiceTestDB(t)
	defer cleanup()
	ctx := context.Background()

	owner := db.User{Login: "wiki-reference-isolation", Name: "Wiki Reference Isolation", Type: db.TypeUser}
	if err := gdb.Create(&owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	failingRepo := db.Repository{
		OwnerID:       owner.ID,
		Name:          "wiki-reference-isolation-failing",
		FullName:      owner.Login + "/wiki-reference-isolation-failing",
		DefaultBranch: "main",
	}
	if err := gdb.Create(&failingRepo).Error; err != nil {
		t.Fatalf("create failing repository: %v", err)
	}
	healthyRepo := db.Repository{
		OwnerID:       owner.ID,
		Name:          "wiki-reference-isolation-healthy",
		FullName:      owner.Login + "/wiki-reference-isolation-healthy",
		DefaultBranch: "main",
	}
	if err := gdb.Create(&healthyRepo).Error; err != nil {
		t.Fatalf("create healthy repository: %v", err)
	}
	for _, repo := range []db.Repository{failingRepo, healthyRepo} {
		if err := gdb.Create(&db.Issue{
			RepositoryID: repo.ID,
			Number:       1,
			Title:        "Referenced issue",
			AuthorID:     owner.ID,
		}).Error; err != nil {
			t.Fatalf("create issue for %s: %v", repo.FullName, err)
		}
	}

	blobStore := wikicatalog.NewBlobStore(t.TempDir())
	catalog := wikicatalog.New(gdb, blobStore)
	failingBody := strings.Repeat("x", wikicatalog.MaxBodyInlineBytes+1) + "\nSee #1"
	failing, err := catalog.ApplyChangeSet(ctx, wikicatalog.ChangeSetRequest{
		RepositoryID: failingRepo.ID,
		Source:       wikicatalog.SourceREST,
		Message:      "failing reference",
		Changes:      []wikicatalog.Change{{Op: wikicatalog.OpUpsert, Slug: "broken", Body: []byte(failingBody)}},
	})
	if err != nil {
		t.Fatalf("create failing page: %v", err)
	}
	if _, err := catalog.ApplyChangeSet(ctx, wikicatalog.ChangeSetRequest{
		RepositoryID: healthyRepo.ID,
		Source:       wikicatalog.SourceREST,
		Message:      "healthy reference",
		Changes:      []wikicatalog.Change{{Op: wikicatalog.OpUpsert, Slug: "home", Body: []byte("See #1")}},
	}); err != nil {
		t.Fatalf("create healthy page: %v", err)
	}
	if err := blobStore.Delete(ctx, failing.Changes[0].BlobSHA); err != nil {
		t.Fatalf("delete failing blob: %v", err)
	}

	svc := &Service{DB: gdb, WikiCatalog: catalog, WikiBlob: blobStore}
	recovered, err := svc.recoverPendingWikiReferenceEffects(ctx)
	if err == nil {
		t.Fatal("recoverPendingWikiReferenceEffects error = nil, want failing repository error")
	}
	if recovered != 1 {
		t.Fatalf("recovered repositories = %d, want 1", recovered)
	}

	var failingHead db.WikiRepoHead
	if err := gdb.First(&failingHead, "repository_id = ?", failingRepo.ID).Error; err != nil {
		t.Fatalf("load failing cursor: %v", err)
	}
	if failingHead.ReferenceEffectsThroughChangesetID == nil || *failingHead.ReferenceEffectsThroughChangesetID != failing.ChangesetID {
		t.Fatalf("failing cursor = %v, want %d", failingHead.ReferenceEffectsThroughChangesetID, failing.ChangesetID)
	}
	var healthyHead db.WikiRepoHead
	if err := gdb.First(&healthyHead, "repository_id = ?", healthyRepo.ID).Error; err != nil {
		t.Fatalf("load healthy cursor: %v", err)
	}
	if healthyHead.ReferenceEffectsThroughChangesetID != nil {
		t.Fatalf("healthy cursor = %v, want nil", *healthyHead.ReferenceEffectsThroughChangesetID)
	}
	assertWikiIssueReferenceCount(t, gdb, healthyRepo.ID, "home", 1, 1)
}

func TestResetWikiCatalogForRepoDeletesWikiIssueReferences(t *testing.T) {
	gdb, cleanup := openMigratedServiceTestDB(t)
	defer cleanup()
	ctx := context.Background()

	owner := db.User{Login: "wiki-reference-reset", Name: "Wiki Reference Reset", Type: db.TypeUser}
	if err := gdb.Create(&owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	repo := db.Repository{
		OwnerID:       owner.ID,
		Name:          "wiki-reference-reset",
		FullName:      owner.Login + "/wiki-reference-reset",
		DefaultBranch: "main",
	}
	if err := gdb.Create(&repo).Error; err != nil {
		t.Fatalf("create repository: %v", err)
	}
	slug := "home"
	if err := gdb.Create(&db.IssueReference{
		SourceType:         issueReferenceSourceWikiPage,
		SourceRepositoryID: repo.ID,
		SourceWikiSlug:     &slug,
		TargetRepositoryID: repo.ID,
		TargetNumber:       1,
		RawReference:       "#1",
	}).Error; err != nil {
		t.Fatalf("create wiki issue reference: %v", err)
	}

	svc := &Service{DB: gdb}
	if err := svc.resetWikiCatalogRepo(ctx, repo.ID); err != nil {
		t.Fatalf("resetWikiCatalogRepo: %v", err)
	}
	var references int64
	if err := gdb.Model(&db.IssueReference{}).
		Where("source_type = ? AND source_repository_id = ?", issueReferenceSourceWikiPage, repo.ID).
		Count(&references).Error; err != nil {
		t.Fatalf("count wiki issue references: %v", err)
	}
	if references != 0 {
		t.Fatalf("wiki issue references after reset = %d, want 0", references)
	}
}

func assertWikiIssueReferenceCount(t *testing.T, gdb *gorm.DB, repoID uint, slug string, targetNumber int, want int64) {
	t.Helper()
	var got int64
	if err := gdb.Model(&db.IssueReference{}).
		Where("source_type = ? AND source_repository_id = ? AND source_wiki_slug = ? AND target_number = ?",
			issueReferenceSourceWikiPage,
			repoID,
			slug,
			targetNumber,
		).
		Count(&got).Error; err != nil {
		t.Fatalf("count wiki issue references for %s: %v", slug, err)
	}
	if got != want {
		t.Fatalf("wiki issue references for %s -> #%d = %d, want %d", slug, targetNumber, got, want)
	}
}
