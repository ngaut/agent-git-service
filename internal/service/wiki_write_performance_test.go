package service_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/gitstore"
	applog "github.com/ngaut/agent-git-service/internal/logging"
	"github.com/ngaut/agent-git-service/internal/service"
	"github.com/ngaut/agent-git-service/internal/testharness"
	"github.com/ngaut/agent-git-service/internal/wikicatalog"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

type wikiWriteQueryRecorder struct {
	gormlogger.Interface

	mu         sync.Mutex
	statements []string
}

func newWikiWriteQueryRecorder() *wikiWriteQueryRecorder {
	return &wikiWriteQueryRecorder{Interface: gormlogger.Discard}
}

func (l *wikiWriteQueryRecorder) LogMode(level gormlogger.LogLevel) gormlogger.Interface {
	l.Interface = l.Interface.LogMode(level)
	return l
}

func (l *wikiWriteQueryRecorder) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	sql, _ := fc()
	l.mu.Lock()
	l.statements = append(l.statements, sql)
	l.mu.Unlock()
	l.Interface.Trace(ctx, begin, func() (string, int64) { return sql, 0 }, err)
}

func (l *wikiWriteQueryRecorder) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.statements...)
}

func TestPutWikiPageRecordsRequestTiming(t *testing.T) {
	svc, cleanup := testharness.NewService(t, testharness.ServiceConfig{})
	defer cleanup()

	owner := db.User{Login: "wiki-timing-owner", Name: "Wiki Timing Owner", Type: db.TypeUser}
	if err := svc.DB.Create(&owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	full := owner.Login + "/wiki-timing"
	if _, err := svc.CreateRepo(context.Background(), service.CreateRepoInput{
		OwnerLogin: owner.Login,
		Name:       "wiki-timing",
	}); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	ctx := applog.WithRequestContext(context.Background())
	body := "# Timing\n\nA page used to verify request timing.\n"
	if _, err := svc.PutWikiPage(ctx, full, "timing", body, "create timing", ""); err != nil {
		t.Fatalf("PutWikiPage: %v", err)
	}
	svc.Wg.Wait()

	values := make(map[string]slog.Value)
	for _, attr := range applog.SnapshotAttrs(ctx) {
		values[attr.Key] = attr.Value.Resolve()
	}
	if got := values["wiki_write_operation"]; got.Kind() != slog.KindString || got.String() != "put" {
		t.Fatalf("wiki_write_operation = %v, want put", got)
	}
	if got := values["wiki_write_body_bytes"]; got.Kind() != slog.KindInt64 || got.Int64() != int64(len(body)) {
		t.Fatalf("wiki_write_body_bytes = %v, want %d", got, len(body))
	}

	durationKeys := []string{
		"wiki_write_total_ms",
		"wiki_write_repo_lookup_ms",
		"wiki_write_lock_repo_lookup_ms",
		"wiki_write_catalog_lock_wait_ms",
		"wiki_write_git_lock_wait_ms",
		"wiki_write_critical_section_ms",
		"wiki_write_repo_init_ms",
		"wiki_write_reconcile_ms",
		"wiki_write_mutations_ms",
		"wiki_write_preflight_ms",
		"wiki_write_git_prepare_ms",
		"wiki_write_git_prepare_build_ms",
		"wiki_write_git_prepare_persist_ms",
		"wiki_write_git_persist_barrier_wait_ms",
		"wiki_write_catalog_apply_total_ms",
		"wiki_write_catalog_blob_upload_ms",
		"wiki_write_catalog_transaction_ms",
		"wiki_write_catalog_transaction_body_ms",
		"wiki_write_catalog_transaction_boundary_ms",
		"wiki_write_catalog_changeset_insert_ms",
		"wiki_write_catalog_head_cas_ms",
		"wiki_write_catalog_changes_ms",
		"wiki_write_catalog_page_write_ms",
		"wiki_write_catalog_revision_insert_ms",
		"wiki_write_catalog_blob_refs_ms",
		"wiki_write_catalog_outlinks_ms",
		"wiki_write_catalog_inbound_links_ms",
		"wiki_write_catalog_pending_blob_cleanup_ms",
		"wiki_write_catalog_commit_barrier_ms",
		"wiki_write_catalog_post_commit_ms",
		"wiki_write_git_publish_ms",
		"wiki_write_post_commit_enqueue_ms",
		"wiki_write_reference_queue_wait_ms",
		"wiki_write_reference_effects_total_ms",
		"wiki_write_reference_sync_ms",
		"wiki_write_search_enqueue_ms",
		"wiki_write_post_commit_wait_ms",
		"wiki_write_labels_ms",
	}
	for _, key := range durationKeys {
		got, ok := values[key]
		if !ok {
			t.Errorf("%s missing from request log attributes", key)
			continue
		}
		if got.Kind() != slog.KindInt64 || got.Int64() < 0 {
			t.Errorf("%s = %v, want a non-negative integer", key, got)
		}
	}
	transactionMillis := values["wiki_write_catalog_transaction_ms"].Int64()
	transactionPartsMillis := values["wiki_write_catalog_transaction_body_ms"].Int64() +
		values["wiki_write_catalog_transaction_boundary_ms"].Int64()
	if delta := transactionMillis - transactionPartsMillis; delta < 0 || delta > 1 {
		t.Fatalf(
			"catalog transaction timing = %dms, body + boundary = %dms",
			transactionMillis,
			transactionPartsMillis,
		)
	}
	for _, key := range []string{"wiki_write_reference_queue_depth", "wiki_write_search_queue_depth"} {
		got, ok := values[key]
		if !ok {
			t.Errorf("%s missing from request log attributes", key)
			continue
		}
		if got.Kind() != slog.KindInt64 || got.Int64() < 1 {
			t.Errorf("%s = %v, want an integer >= 1", key, got)
		}
	}
}

func TestPutWikiPageHealthyWriteDoesNotStandaloneReadRepairObligation(t *testing.T) {
	svc, cleanup := testharness.NewService(t, testharness.ServiceConfig{})
	defer cleanup()
	ctx := context.Background()

	owner := db.User{Login: "wiki-repair-budget-owner", Name: "Wiki Repair Budget Owner", Type: db.TypeUser}
	if err := svc.DB.Create(&owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	full := owner.Login + "/wiki-repair-budget"
	if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: owner.Login,
		Name:       "wiki-repair-budget",
	}); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	if _, err := svc.PutWikiPage(ctx, full, "first", "# First\n", "create first", ""); err != nil {
		t.Fatalf("seed PutWikiPage: %v", err)
	}
	svc.Wg.Wait()

	recorder := newWikiWriteQueryRecorder()
	recordingDB := svc.DB.Session(&gorm.Session{Logger: recorder})
	svc.DB = recordingDB
	svc.WikiCatalog.DB = recordingDB

	if _, err := svc.PutWikiPage(ctx, full, "second", "# Second\n", "create second", ""); err != nil {
		t.Fatalf("PutWikiPage: %v", err)
	}
	svc.Wg.Wait()

	statements := recorder.snapshot()
	var repairStatements, standaloneRepairReads int
	for _, statement := range statements {
		if !strings.Contains(statement, "wiki_git_repair_obligations") {
			continue
		}
		repairStatements++
		normalized := strings.TrimSpace(statement)
		if strings.HasPrefix(normalized, "SELECT * FROM `wiki_git_repair_obligations`") ||
			strings.HasPrefix(normalized, "SELECT * FROM wiki_git_repair_obligations") {
			standaloneRepairReads++
		}
	}
	if repairStatements == 0 {
		t.Fatalf("healthy write did not record folded repair-obligation validation:\n%s", strings.Join(statements, "\n"))
	}
	if standaloneRepairReads != 0 {
		t.Fatalf("healthy write performed %d standalone repair-obligation reads:\n%s",
			standaloneRepairReads,
			strings.Join(statements, "\n"),
		)
	}
}

func TestPutWikiPageOverlapsGitPersistenceWithCatalogApply(t *testing.T) {
	svc, cleanup := testharness.NewService(t, testharness.ServiceConfig{})
	defer cleanup()
	ctx := context.Background()

	owner := db.User{Login: "wiki-persist-overlap-owner", Name: "Wiki Persist Overlap Owner", Type: db.TypeUser}
	if err := svc.DB.Create(&owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	full := owner.Login + "/wiki-persist-overlap"
	if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: owner.Login,
		Name:       "wiki-persist-overlap",
	}); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	persistStarted := make(chan struct{})
	releasePersist := make(chan struct{})
	catalogStarted := make(chan struct{})
	var persistOnce sync.Once
	var releaseOnce sync.Once
	var catalogOnce sync.Once
	service.SetTestWikiPreparedPersistForTest(svc, func(repoFullName, commitSHA string) error {
		if repoFullName == full && commitSHA != "" {
			persistOnce.Do(func() {
				close(persistStarted)
				<-releasePersist
			})
		}
		return nil
	})
	defer service.SetTestWikiPreparedPersistForTest(svc, nil)
	defer releaseOnce.Do(func() { close(releasePersist) })

	const callbackName = "test:observe-wiki-catalog-apply-overlap"
	if err := svc.DB.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Table == "wiki_changesets" {
			catalogOnce.Do(func() { close(catalogStarted) })
		}
	}); err != nil {
		t.Fatalf("register catalog callback: %v", err)
	}
	defer svc.DB.Callback().Create().Remove(callbackName)

	writeDone := make(chan error, 1)
	go func() {
		_, err := svc.PutWikiPage(ctx, full, "home", "# Home\n", "create home", "")
		writeDone <- err
	}()

	select {
	case <-persistStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("Git object persistence did not start")
	}
	select {
	case <-catalogStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("catalog apply did not start while Git object persistence was blocked")
	}
	select {
	case err := <-writeDone:
		t.Fatalf("PutWikiPage returned before Git object persistence completed: %v", err)
	default:
	}
	if _, err := svc.Git.HeadSHA(ctx, full+".wiki", "master"); err == nil {
		t.Fatal("wiki ref published before Git object persistence completed")
	}

	releaseOnce.Do(func() { close(releasePersist) })
	select {
	case err := <-writeDone:
		if err != nil {
			t.Fatalf("PutWikiPage: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("PutWikiPage did not finish")
	}
	svc.Wg.Wait()
}

func TestPutWikiPagePersistsNextWriteWhilePriorPublishIsBlocked(t *testing.T) {
	svc, cleanup := testharness.NewService(t, testharness.ServiceConfig{})
	defer cleanup()
	ctx := context.Background()

	owner := db.User{Login: "wiki-publish-overlap-owner", Name: "Wiki Publish Overlap Owner", Type: db.TypeUser}
	if err := svc.DB.Create(&owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	full := owner.Login + "/wiki-publish-overlap"
	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: owner.Login,
		Name:       "wiki-publish-overlap",
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	firstPublishStarted := make(chan struct{})
	releaseFirstPublish := make(chan struct{})
	secondSnapshotCaptured := make(chan struct{})
	secondPersistStarted := make(chan struct{})
	var publishCalls atomic.Int32
	var snapshotCalls atomic.Int32
	var persistCalls atomic.Int32
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(releaseFirstPublish) })

	service.SetTestWikiPreparedPublishFailureForTest(svc, func(repoFullName, commitSHA string) error {
		if repoFullName == full && commitSHA != "" && publishCalls.Add(1) == 1 {
			close(firstPublishStarted)
			<-releaseFirstPublish
		}
		return nil
	})
	defer service.SetTestWikiPreparedPublishFailureForTest(svc, nil)
	service.SetTestWikiRESTSnapshotForTest(svc, func(repoFullName string) {
		if repoFullName == full && snapshotCalls.Add(1) == 2 {
			close(secondSnapshotCaptured)
		}
	})
	defer service.SetTestWikiRESTSnapshotForTest(svc, nil)
	service.SetTestWikiPreparedPersistForTest(svc, func(repoFullName, commitSHA string) error {
		if repoFullName == full && commitSHA != "" && persistCalls.Add(1) == 2 {
			close(secondPersistStarted)
		}
		return nil
	})
	defer service.SetTestWikiPreparedPersistForTest(svc, nil)

	firstDone := make(chan error, 1)
	go func() {
		_, err := svc.PutWikiPage(ctx, full, "first", "# First\n", "create first", "")
		firstDone <- err
	}()
	select {
	case <-firstPublishStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("first Git ref publication did not start")
	}

	secondDone := make(chan error, 1)
	go func() {
		_, err := svc.PutWikiPage(ctx, full, "second", "# Second\n", "create second", "")
		secondDone <- err
	}()
	select {
	case <-secondSnapshotCaptured:
	case <-time.After(5 * time.Second):
		t.Fatal("second catalog snapshot did not overlap the blocked Git ref publication")
	}

	select {
	case <-secondPersistStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("second Git persistence did not overlap the blocked first publication")
	}
	select {
	case err := <-secondDone:
		t.Fatalf("second PutWikiPage returned before the first ref was published: %v", err)
	default:
	}
	var changesetCount int64
	if err := svc.DB.Model(&db.WikiChangeset{}).
		Where("repository_id = ?", repo.ID).
		Count(&changesetCount).Error; err != nil {
		t.Fatalf("count changesets: %v", err)
	}
	if changesetCount != 1 {
		t.Fatalf("changeset count while first publish is blocked = %d, want 1", changesetCount)
	}

	releaseOnce.Do(func() { close(releaseFirstPublish) })
	for name, done := range map[string]<-chan error{
		"first":  firstDone,
		"second": secondDone,
	} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("%s PutWikiPage: %v", name, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s PutWikiPage did not finish", name)
		}
	}
	svc.Wg.Wait()

	for slug, want := range map[string]string{
		"first.md":  "# First\n",
		"second.md": "# Second\n",
	} {
		got, err := svc.Git.ReadFile(ctx, full+".wiki", slug)
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", slug, err)
		}
		if string(got) != want {
			t.Fatalf("ReadFile(%s) = %q, want %q", slug, got, want)
		}
	}
	var changesets []db.WikiChangeset
	if err := svc.DB.
		Where("repository_id = ?", repo.ID).
		Order("changeset_id ASC").
		Find(&changesets).Error; err != nil {
		t.Fatalf("load changesets: %v", err)
	}
	if len(changesets) != 2 {
		t.Fatalf("changeset count = %d, want 2", len(changesets))
	}
	for _, changeset := range changesets {
		if changeset.SynthFormatVer != 1 {
			t.Fatalf("changeset %d synth format = %d, want 1", changeset.ChangesetID, changeset.SynthFormatVer)
		}
	}
}

func TestPutWikiPageRollsBackCatalogWhenGitPersistenceFails(t *testing.T) {
	svc, cleanup := testharness.NewService(t, testharness.ServiceConfig{})
	defer cleanup()
	ctx := context.Background()

	owner := db.User{Login: "wiki-persist-failure-owner", Name: "Wiki Persist Failure Owner", Type: db.TypeUser}
	if err := svc.DB.Create(&owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	full := owner.Login + "/wiki-persist-failure"
	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: owner.Login,
		Name:       "wiki-persist-failure",
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	persistErr := errors.New("forced object persistence failure")
	service.SetTestWikiPreparedPersistForTest(svc, func(repoFullName, commitSHA string) error {
		if repoFullName == full && commitSHA != "" {
			return persistErr
		}
		return nil
	})
	if _, err := svc.PutWikiPage(ctx, full, "home", "# Home\n", "create home", ""); !errors.Is(err, persistErr) {
		t.Fatalf("PutWikiPage error = %v, want %v", err, persistErr)
	}
	service.SetTestWikiPreparedPersistForTest(svc, nil)

	if _, err := svc.Git.HeadSHA(ctx, full+".wiki", "master"); err == nil {
		t.Fatal("wiki ref became visible despite object persistence failure")
	}
	for name, model := range map[string]any{
		"changesets": &db.WikiChangeset{},
		"heads":      &db.WikiRepoHead{},
		"pages":      &db.WikiPage{},
		"revisions":  &db.WikiPageRevision{},
	} {
		var count int64
		query := svc.DB.Model(model)
		switch model.(type) {
		case *db.WikiChangeset, *db.WikiRepoHead, *db.WikiPage:
			query = query.Where("repository_id = ?", repo.ID)
		}
		if err := query.Count(&count).Error; err != nil {
			t.Fatalf("count %s: %v", name, err)
		}
		if count != 0 {
			t.Fatalf("%s after persistence failure = %d, want 0", name, count)
		}
	}

	if _, err := svc.PutWikiPage(ctx, full, "home", "# Home\n", "create home", ""); err != nil {
		t.Fatalf("PutWikiPage after persistence recovery: %v", err)
	}
	svc.Wg.Wait()
	commits, err := svc.Git.CountCommits(ctx, full+".wiki", nil)
	if err != nil {
		t.Fatalf("CountCommits: %v", err)
	}
	if commits != 1 {
		t.Fatalf("commit count = %d, want 1", commits)
	}
}

func TestPutWikiPagePersistsPublishedGitSHAWithoutBackfill(t *testing.T) {
	svc, cleanup := testharness.NewService(t, testharness.ServiceConfig{})
	defer cleanup()
	ctx := context.Background()

	owner := db.User{Login: "wiki-sha-owner", Name: "Wiki SHA Owner", Type: db.TypeUser}
	if err := svc.DB.Create(&owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	full := owner.Login + "/wiki-sha"
	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: owner.Login,
		Name:       "wiki-sha",
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	var changesetUpdates atomic.Int64
	var revisionUpdates atomic.Int64
	const callbackName = "test:count-wiki-publish-backfill-updates"
	if err := svc.DB.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Schema == nil {
			return
		}
		switch tx.Statement.Schema.Table {
		case "wiki_changesets":
			changesetUpdates.Add(1)
		case "wiki_page_revisions":
			revisionUpdates.Add(1)
		}
	}); err != nil {
		t.Fatalf("register revision update callback: %v", err)
	}
	defer svc.DB.Callback().Update().Remove(callbackName)

	if _, err := svc.PutWikiPage(ctx, full, "home", "# Home\n", "create home", ""); err != nil {
		t.Fatalf("PutWikiPage: %v", err)
	}
	svc.Wg.Wait()
	if got := revisionUpdates.Load(); got != 0 {
		t.Fatalf("revision update statements after prepared publish = %d, want 0", got)
	}
	if got := changesetUpdates.Load(); got != 0 {
		t.Fatalf("changeset update statements after prepared publish = %d, want 0", got)
	}

	head, err := svc.Git.HeadSHA(ctx, full+".wiki", "master")
	if err != nil {
		t.Fatalf("HeadSHA: %v", err)
	}
	var changeset db.WikiChangeset
	if err := svc.DB.
		Where("repository_id = ?", repo.ID).
		Order("changeset_id DESC").
		First(&changeset).Error; err != nil {
		t.Fatalf("load changeset: %v", err)
	}
	if changeset.SynthCommitSHA != head {
		t.Fatalf("changeset SHA = %s, Git HEAD = %s", changeset.SynthCommitSHA, head)
	}
	if changeset.SynthFormatVer != 1 {
		t.Fatalf("synth format = %d, want materialized format 1", changeset.SynthFormatVer)
	}

	var revision db.WikiPageRevision
	if err := svc.DB.
		Where("changeset_id = ?", changeset.ChangesetID).
		First(&revision).Error; err != nil {
		t.Fatalf("load revision: %v", err)
	}
	if revision.CommitSHA != head {
		t.Fatalf("revision SHA = %s, Git HEAD = %s", revision.CommitSHA, head)
	}
}

func TestPutWikiPageRunsReferenceSyncAndSearchEnqueueConcurrently(t *testing.T) {
	svc, cleanup := testharness.NewService(t, testharness.ServiceConfig{})
	defer cleanup()
	ctx := context.Background()

	owner := db.User{Login: "wiki-effects-overlap-owner", Name: "Wiki Effects Overlap Owner", Type: db.TypeUser}
	if err := svc.DB.Create(&owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	full := owner.Login + "/wiki-effects-overlap"
	if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: owner.Login,
		Name:       "wiki-effects-overlap",
	}); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	if _, err := svc.PutWikiPage(ctx, full, "home", "# Home\n", "create home", ""); err != nil {
		t.Fatalf("seed PutWikiPage: %v", err)
	}
	svc.Wg.Wait()

	referenceStarted := make(chan struct{})
	releaseReference := make(chan struct{})
	searchStarted := make(chan struct{})
	var referenceOnce sync.Once
	var searchOnce sync.Once
	var releaseOnce sync.Once
	const deleteCallback = "test:block-wiki-reference-delete"
	if err := svc.DB.Callback().Delete().Before("gorm:delete").Register(deleteCallback, func(tx *gorm.DB) {
		if tx.Statement.Schema == nil || tx.Statement.Schema.Table != "issue_references" {
			return
		}
		referenceOnce.Do(func() {
			close(referenceStarted)
			<-releaseReference
		})
	}); err != nil {
		t.Fatalf("register reference callback: %v", err)
	}
	defer svc.DB.Callback().Delete().Remove(deleteCallback)
	defer releaseOnce.Do(func() { close(releaseReference) })

	const createCallback = "test:observe-wiki-search-task-create"
	if err := svc.DB.Callback().Create().Before("gorm:create").Register(createCallback, func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Table == "wiki_search_projection_tasks" {
			searchOnce.Do(func() { close(searchStarted) })
		}
	}); err != nil {
		t.Fatalf("register search callback: %v", err)
	}
	defer svc.DB.Callback().Create().Remove(createCallback)

	writeDone := make(chan error, 1)
	go func() {
		_, err := svc.PutWikiPage(ctx, full, "home", "# Home updated\n", "update home", "")
		writeDone <- err
	}()

	select {
	case <-referenceStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("reference sync did not start")
	}
	select {
	case <-searchStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("search enqueue did not overlap blocked reference sync")
	}

	releaseOnce.Do(func() { close(releaseReference) })
	select {
	case err := <-writeDone:
		if err != nil {
			t.Fatalf("PutWikiPage: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("PutWikiPage did not finish")
	}
	svc.Wg.Wait()
}

func TestPutWikiPageStoresRecoverableGitSHAWhenPublishFails(t *testing.T) {
	svc, cleanup := testharness.NewService(t, testharness.ServiceConfig{})
	defer cleanup()
	ctx := context.Background()

	owner := db.User{Login: "wiki-publish-owner", Name: "Wiki Publish Owner", Type: db.TypeUser}
	if err := svc.DB.Create(&owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	full := owner.Login + "/wiki-publish"
	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: owner.Login,
		Name:       "wiki-publish",
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	publishErr := errors.New("forced publish failure")
	service.SetTestWikiPreparedPublishFailureForTest(svc, func(repoFullName, commitSHA string) error {
		if repoFullName != full || commitSHA == "" {
			return nil
		}
		return publishErr
	})
	defer service.SetTestWikiPreparedPublishFailureForTest(svc, nil)

	if _, err := svc.PutWikiPage(ctx, full, "home", "# Home\n", "create home", ""); !errors.Is(err, publishErr) {
		t.Fatalf("PutWikiPage error = %v, want %v", err, publishErr)
	}

	if _, err := svc.Git.HeadSHA(ctx, full+".wiki", "master"); err == nil {
		t.Fatal("wiki HEAD became visible despite publish failure")
	}

	var changeset db.WikiChangeset
	if err := svc.DB.
		Where("repository_id = ?", repo.ID).
		Order("changeset_id DESC").
		First(&changeset).Error; err != nil {
		t.Fatalf("load changeset: %v", err)
	}
	if changeset.SynthFormatVer != 1 {
		t.Fatalf("synth format = %d, want real Git SHA format 1", changeset.SynthFormatVer)
	}
	if changeset.SynthCommitSHA == "" {
		t.Fatal("changeset synth SHA not persisted")
	}

	var revision db.WikiPageRevision
	if err := svc.DB.
		Where("changeset_id = ?", changeset.ChangesetID).
		First(&revision).Error; err != nil {
		t.Fatalf("load revision: %v", err)
	}
	if revision.CommitSHA != changeset.SynthCommitSHA {
		t.Fatalf("revision SHA = %s, want recoverable Git SHA %s", revision.CommitSHA, changeset.SynthCommitSHA)
	}
}

func TestPutWikiPageRecoversInterruptedProjectionBeforeNextWrite(t *testing.T) {
	svc, cleanup := testharness.NewService(t, testharness.ServiceConfig{})
	defer cleanup()
	ctx := context.Background()

	owner := db.User{Login: "wiki-recover-owner", Name: "Wiki Recover Owner", Type: db.TypeUser}
	if err := svc.DB.Create(&owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	full := owner.Login + "/wiki-recover"
	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: owner.Login,
		Name:       "wiki-recover",
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	publishErr := errors.New("forced publish failure")
	failPublish := true
	service.SetTestWikiPreparedPublishFailureForTest(svc, func(repoFullName, commitSHA string) error {
		if repoFullName == full && commitSHA != "" && failPublish {
			failPublish = false
			return publishErr
		}
		return nil
	})
	defer service.SetTestWikiPreparedPublishFailureForTest(svc, nil)

	var effectsMu sync.Mutex
	var effectSlugs []string
	service.SetTestWikiPostCommitEffectsForTest(svc, func(repoFullName string, result wikicatalog.ChangeSetResult) {
		if repoFullName != full {
			return
		}
		effectsMu.Lock()
		defer effectsMu.Unlock()
		for _, change := range result.Changes {
			effectSlugs = append(effectSlugs, change.Slug)
		}
	})
	defer service.SetTestWikiPostCommitEffectsForTest(svc, nil)

	if _, err := svc.PutWikiPage(ctx, full, "first", "# First\n", "create first", ""); !errors.Is(err, publishErr) {
		t.Fatalf("first PutWikiPage error = %v, want %v", err, publishErr)
	}
	if _, err := svc.PutWikiPage(ctx, full, "second", "# Second\n", "create second", ""); err != nil {
		t.Fatalf("second PutWikiPage: %v", err)
	}
	svc.Wg.Wait()

	for slug, want := range map[string]string{
		"first.md":  "# First\n",
		"second.md": "# Second\n",
	} {
		got, err := svc.Git.ReadFile(ctx, full+".wiki", slug)
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", slug, err)
		}
		if string(got) != want {
			t.Fatalf("ReadFile(%s) = %q, want %q", slug, got, want)
		}
	}

	var changesets []db.WikiChangeset
	if err := svc.DB.
		Where("repository_id = ?", repo.ID).
		Order("changeset_id ASC").
		Find(&changesets).Error; err != nil {
		t.Fatalf("load changesets: %v", err)
	}
	if len(changesets) != 2 {
		t.Fatalf("changeset count = %d, want 2", len(changesets))
	}
	for _, changeset := range changesets {
		if changeset.SynthFormatVer != 1 {
			t.Fatalf("changeset %d synth format = %d, want 1", changeset.ChangesetID, changeset.SynthFormatVer)
		}
	}

	effectsMu.Lock()
	gotEffects := append([]string(nil), effectSlugs...)
	effectsMu.Unlock()
	if len(gotEffects) != 2 || gotEffects[0] != "first" || gotEffects[1] != "second" {
		t.Fatalf("post-commit effect order = %v, want [first second]", gotEffects)
	}
}

func TestPutWikiPageReplaysRecoveredEffectsAfterMissingPreparedObjectRebuild(t *testing.T) {
	svc, cleanup := testharness.NewService(t, testharness.ServiceConfig{})
	defer cleanup()
	ctx := context.Background()

	owner := db.User{Login: "wiki-lost-object-owner", Name: "Wiki Lost Object Owner", Type: db.TypeUser}
	if err := svc.DB.Create(&owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	full := owner.Login + "/wiki-lost-object"
	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: owner.Login,
		Name:       "wiki-lost-object",
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: full,
		Title:        "Referenced issue",
		Body:         "Issue referenced from recovered wiki effects.",
		AuthorLogin:  owner.Login,
	})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	publishErr := errors.New("forced publish failure")
	service.SetTestWikiPreparedPublishFailureForTest(svc, func(repoFullName, commitSHA string) error {
		if repoFullName == full && commitSHA != "" {
			return publishErr
		}
		return nil
	})
	if _, err := svc.PutWikiPage(ctx, full, "first", fmt.Sprintf("# First\n\nSee #%d\n", issue.Number), "create first", ""); !errors.Is(err, publishErr) {
		t.Fatalf("first PutWikiPage error = %v, want %v", err, publishErr)
	}
	service.SetTestWikiPreparedPublishFailureForTest(svc, nil)

	if err := svc.Git.Delete(ctx, full+".wiki"); err != nil {
		t.Fatalf("delete wiki repo after failed publish: %v", err)
	}
	if _, err := svc.PutWikiPage(ctx, full, "second", "# Second\n", "create second", ""); err != nil {
		t.Fatalf("second PutWikiPage: %v", err)
	}
	svc.Wg.Wait()

	for slug, want := range map[string]string{
		"first.md":  fmt.Sprintf("# First\n\nSee #%d\n", issue.Number),
		"second.md": "# Second\n",
	} {
		got, err := svc.Git.ReadFile(ctx, full+".wiki", slug)
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", slug, err)
		}
		if string(got) != want {
			t.Fatalf("ReadFile(%s) = %q, want %q", slug, got, want)
		}
	}

	var refs int64
	if err := svc.DB.Model(&db.IssueReference{}).
		Where("source_repository_id = ? AND source_wiki_slug = ? AND target_number = ?", repo.ID, "first", issue.Number).
		Count(&refs).Error; err != nil {
		t.Fatalf("count wiki issue references: %v", err)
	}
	if refs != 1 {
		t.Fatalf("wiki issue reference count = %d, want 1", refs)
	}

	var searchDocs, searchTasks int64
	if err := svc.DB.Model(&db.WikiSearchDocument{}).
		Where("repository_id = ? AND slug = ?", repo.ID, "first").
		Count(&searchDocs).Error; err != nil {
		t.Fatalf("count wiki search documents: %v", err)
	}
	if err := svc.DB.Model(&db.WikiSearchProjectionTask{}).
		Where("repository_id = ? AND slug = ?", repo.ID, "first").
		Count(&searchTasks).Error; err != nil {
		t.Fatalf("count wiki search projection tasks: %v", err)
	}
	if searchDocs+searchTasks == 0 {
		t.Fatal("recovered wiki effects did not enqueue or complete search projection for first")
	}
}

func TestPutWikiPageRebuildsWhenPreparedChildObjectMissingButParentRefRemains(t *testing.T) {
	testPutWikiPageRebuildsWhenPreparedChildObjectMissingButParentRefRemains(t, removeLooseGitObject)
}

func TestPutWikiPageRebuildsWhenPreparedChildTreeMissingButParentRefRemains(t *testing.T) {
	testPutWikiPageRebuildsWhenPreparedChildObjectMissingButParentRefRemains(t, removePreparedCommitTreeObject)
}

func testPutWikiPageRebuildsWhenPreparedChildObjectMissingButParentRefRemains(
	t *testing.T,
	removePreparedObject func(*testing.T, string, string),
) {
	svc, cleanup := testharness.NewService(t, testharness.ServiceConfig{})
	defer cleanup()
	ctx := context.Background()

	owner := db.User{Login: "wiki-lost-child-owner", Name: "Wiki Lost Child Owner", Type: db.TypeUser}
	if err := svc.DB.Create(&owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	full := owner.Login + "/wiki-lost-child"
	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: owner.Login,
		Name:       "wiki-lost-child",
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	issue, err := svc.CreateIssue(ctx, service.CreateIssueInput{
		RepoFullName: full,
		Title:        "Referenced issue",
		Body:         "Issue referenced from recovered child wiki effects.",
		AuthorLogin:  owner.Login,
	})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	if _, err := svc.PutWikiPage(ctx, full, "parent", "# Parent\n", "create parent", ""); err != nil {
		t.Fatalf("parent PutWikiPage: %v", err)
	}
	parentHead, err := svc.Git.HeadSHA(ctx, full+".wiki", "master")
	if err != nil {
		t.Fatalf("HeadSHA(parent): %v", err)
	}

	publishErr := errors.New("forced child publish failure")
	var childCommit string
	service.SetTestWikiPreparedPublishFailureForTest(svc, func(repoFullName, commitSHA string) error {
		if repoFullName == full && commitSHA != "" {
			childCommit = commitSHA
			return publishErr
		}
		return nil
	})
	if _, err := svc.PutWikiPage(
		ctx,
		full,
		"child",
		fmt.Sprintf("# Child\n\nSee #%d\n", issue.Number),
		"create child",
		"",
	); !errors.Is(err, publishErr) {
		t.Fatalf("child PutWikiPage error = %v, want %v", err, publishErr)
	}
	service.SetTestWikiPreparedPublishFailureForTest(svc, nil)
	if childCommit == "" {
		t.Fatal("test hook did not capture the prepared child commit")
	}
	failedHead, err := svc.Git.HeadSHA(ctx, full+".wiki", "master")
	if err != nil {
		t.Fatalf("HeadSHA(after failed child): %v", err)
	}
	if failedHead != parentHead {
		t.Fatalf("head after failed child = %s, want parent %s", failedHead, parentHead)
	}

	repoPath, err := svc.Git.GetRepoPath(ctx, full+".wiki")
	if err != nil {
		t.Fatalf("GetRepoPath: %v", err)
	}
	removePreparedObject(t, repoPath, childCommit)

	if _, err := svc.PutWikiPage(ctx, full, "next", "# Next\n", "create next", ""); err != nil {
		t.Fatalf("next PutWikiPage: %v", err)
	}
	svc.Wg.Wait()

	for slug, want := range map[string]string{
		"parent.md": "# Parent\n",
		"child.md":  fmt.Sprintf("# Child\n\nSee #%d\n", issue.Number),
		"next.md":   "# Next\n",
	} {
		got, err := svc.Git.ReadFile(ctx, full+".wiki", slug)
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", slug, err)
		}
		if string(got) != want {
			t.Fatalf("ReadFile(%s) = %q, want %q", slug, got, want)
		}
	}

	var refs int64
	if err := svc.DB.Model(&db.IssueReference{}).
		Where("source_repository_id = ? AND source_wiki_slug = ? AND target_number = ?", repo.ID, "child", issue.Number).
		Count(&refs).Error; err != nil {
		t.Fatalf("count child issue references: %v", err)
	}
	if refs != 1 {
		t.Fatalf("child wiki issue reference count = %d, want 1", refs)
	}

	var searchDocs, searchTasks int64
	if err := svc.DB.Model(&db.WikiSearchDocument{}).
		Where("repository_id = ? AND slug = ?", repo.ID, "child").
		Count(&searchDocs).Error; err != nil {
		t.Fatalf("count child wiki search documents: %v", err)
	}
	if err := svc.DB.Model(&db.WikiSearchProjectionTask{}).
		Where("repository_id = ? AND slug = ?", repo.ID, "child").
		Count(&searchTasks).Error; err != nil {
		t.Fatalf("count child wiki search projection tasks: %v", err)
	}
	if searchDocs+searchTasks == 0 {
		t.Fatal("recovered child wiki effects did not enqueue or complete search projection")
	}
}

func TestPutWikiPageRebuildRemovesDeletedPageWhenPreparedChildObjectMissingButParentRefRemains(t *testing.T) {
	svc, cleanup := testharness.NewService(t, testharness.ServiceConfig{})
	defer cleanup()
	ctx := context.Background()

	owner := db.User{Login: "wiki-lost-delete-owner", Name: "Wiki Lost Delete Owner", Type: db.TypeUser}
	if err := svc.DB.Create(&owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	full := owner.Login + "/wiki-lost-delete"
	if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: owner.Login,
		Name:       "wiki-lost-delete",
	}); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	if _, err := svc.PutWikiPage(ctx, full, "old", "# Old\n", "create old", ""); err != nil {
		t.Fatalf("old PutWikiPage: %v", err)
	}
	parentHead, err := svc.Git.HeadSHA(ctx, full+".wiki", "master")
	if err != nil {
		t.Fatalf("HeadSHA(parent): %v", err)
	}

	publishErr := errors.New("forced delete publish failure")
	var deleteCommit string
	service.SetTestWikiPreparedPublishFailureForTest(svc, func(repoFullName, commitSHA string) error {
		if repoFullName == full && commitSHA != "" {
			deleteCommit = commitSHA
			return publishErr
		}
		return nil
	})
	if err := svc.DeleteWikiPage(ctx, full, "old", "delete old"); !errors.Is(err, publishErr) {
		t.Fatalf("DeleteWikiPage error = %v, want %v", err, publishErr)
	}
	service.SetTestWikiPreparedPublishFailureForTest(svc, nil)
	if deleteCommit == "" {
		t.Fatal("test hook did not capture the prepared delete commit")
	}
	failedHead, err := svc.Git.HeadSHA(ctx, full+".wiki", "master")
	if err != nil {
		t.Fatalf("HeadSHA(after failed delete): %v", err)
	}
	if failedHead != parentHead {
		t.Fatalf("head after failed delete = %s, want parent %s", failedHead, parentHead)
	}

	repoPath, err := svc.Git.GetRepoPath(ctx, full+".wiki")
	if err != nil {
		t.Fatalf("GetRepoPath: %v", err)
	}
	removeLooseGitObject(t, repoPath, deleteCommit)

	if _, err := svc.PutWikiPage(ctx, full, "next", "# Next\n", "create next", ""); err != nil {
		t.Fatalf("next PutWikiPage: %v", err)
	}
	svc.Wg.Wait()

	paths := wikiTreePathSet(t, svc, ctx, full)
	if _, ok := paths["old.md"]; ok {
		t.Fatalf("recovered wiki tree still contains deleted path old.md: %v", paths)
	}
	if _, ok := paths["next.md"]; !ok {
		t.Fatalf("recovered wiki tree missing next.md: %v", paths)
	}
}

func TestPutWikiPageRebuildsEmptySnapshotWhenPreparedLastPageDeleteObjectMissing(t *testing.T) {
	svc, cleanup := testharness.NewService(t, testharness.ServiceConfig{})
	defer cleanup()
	ctx := context.Background()

	owner := db.User{Login: "wiki-lost-last-delete-owner", Name: "Wiki Lost Last Delete Owner", Type: db.TypeUser}
	if err := svc.DB.Create(&owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	full := owner.Login + "/wiki-lost-last-delete"
	if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: owner.Login,
		Name:       "wiki-lost-last-delete",
	}); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	if _, err := svc.PutWikiPage(ctx, full, "old", "# Old\n", "create old", ""); err != nil {
		t.Fatalf("old PutWikiPage: %v", err)
	}

	publishErr := errors.New("forced last delete publish failure")
	var deleteCommit string
	service.SetTestWikiPreparedPublishFailureForTest(svc, func(repoFullName, commitSHA string) error {
		if repoFullName == full && commitSHA != "" {
			deleteCommit = commitSHA
			return publishErr
		}
		return nil
	})
	if err := svc.DeleteWikiPage(ctx, full, "old", "delete old"); !errors.Is(err, publishErr) {
		t.Fatalf("DeleteWikiPage error = %v, want %v", err, publishErr)
	}
	service.SetTestWikiPreparedPublishFailureForTest(svc, nil)
	if deleteCommit == "" {
		t.Fatal("test hook did not capture the prepared delete commit")
	}
	if err := svc.Git.Delete(ctx, full+".wiki"); err != nil {
		t.Fatalf("delete wiki repo after failed delete publish: %v", err)
	}

	if _, err := svc.PutWikiPage(ctx, full, "next", "# Next\n", "create next", ""); err != nil {
		t.Fatalf("next PutWikiPage: %v", err)
	}
	svc.Wg.Wait()

	paths := wikiTreePathSet(t, svc, ctx, full)
	if _, ok := paths["old.md"]; ok {
		t.Fatalf("recovered wiki tree still contains deleted path old.md: %v", paths)
	}
	if _, ok := paths["next.md"]; !ok {
		t.Fatalf("recovered wiki tree missing next.md: %v", paths)
	}
	repoPath, err := svc.Git.GetRepoPath(ctx, full+".wiki")
	if err != nil {
		t.Fatalf("GetRepoPath: %v", err)
	}
	out, err := exec.Command("git", "--git-dir", repoPath, "rev-list", "--count", "master").Output()
	if err != nil {
		t.Fatalf("git rev-list --count master: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "2" {
		t.Fatalf("master commit count = %s, want 2 recovery+next commits", got)
	}
}

func TestPutWikiPageRebuildRemovesRenamedPageWhenPreparedChildObjectMissingButParentRefRemains(t *testing.T) {
	svc, cleanup := testharness.NewService(t, testharness.ServiceConfig{})
	defer cleanup()
	ctx := context.Background()

	owner := db.User{Login: "wiki-lost-rename-owner", Name: "Wiki Lost Rename Owner", Type: db.TypeUser}
	if err := svc.DB.Create(&owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	full := owner.Login + "/wiki-lost-rename"
	if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: owner.Login,
		Name:       "wiki-lost-rename",
	}); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	page, err := svc.PutWikiPage(ctx, full, "old-name", "# Old Name\n", "create old-name", "")
	if err != nil {
		t.Fatalf("old-name PutWikiPage: %v", err)
	}
	parentHead, err := svc.Git.HeadSHA(ctx, full+".wiki", "master")
	if err != nil {
		t.Fatalf("HeadSHA(parent): %v", err)
	}

	publishErr := errors.New("forced rename publish failure")
	var renameCommit string
	service.SetTestWikiPreparedPublishFailureForTest(svc, func(repoFullName, commitSHA string) error {
		if repoFullName == full && commitSHA != "" {
			renameCommit = commitSHA
			return publishErr
		}
		return nil
	})
	if _, err := svc.MoveWikiPage(ctx, full, "old-name", "new-name", page.SHA, "rename old-name"); !errors.Is(err, publishErr) {
		t.Fatalf("MoveWikiPage error = %v, want %v", err, publishErr)
	}
	service.SetTestWikiPreparedPublishFailureForTest(svc, nil)
	if renameCommit == "" {
		t.Fatal("test hook did not capture the prepared rename commit")
	}
	failedHead, err := svc.Git.HeadSHA(ctx, full+".wiki", "master")
	if err != nil {
		t.Fatalf("HeadSHA(after failed rename): %v", err)
	}
	if failedHead != parentHead {
		t.Fatalf("head after failed rename = %s, want parent %s", failedHead, parentHead)
	}

	repoPath, err := svc.Git.GetRepoPath(ctx, full+".wiki")
	if err != nil {
		t.Fatalf("GetRepoPath: %v", err)
	}
	removeLooseGitObject(t, repoPath, renameCommit)

	if _, err := svc.PutWikiPage(ctx, full, "next", "# Next\n", "create next", ""); err != nil {
		t.Fatalf("next PutWikiPage: %v", err)
	}
	svc.Wg.Wait()

	paths := wikiTreePathSet(t, svc, ctx, full)
	if _, ok := paths["old-name.md"]; ok {
		t.Fatalf("recovered wiki tree still contains renamed path old-name.md: %v", paths)
	}
	if _, ok := paths["new-name.md"]; !ok {
		t.Fatalf("recovered wiki tree missing new-name.md: %v", paths)
	}
	got, err := svc.Git.ReadFile(ctx, full+".wiki", "new-name.md")
	if err != nil {
		t.Fatalf("ReadFile(new-name.md): %v", err)
	}
	if string(got) != "# Old Name\n" {
		t.Fatalf("ReadFile(new-name.md) = %q, want %q", got, "# Old Name\n")
	}
}

func TestPutWikiPageRebuildPreservesIgnoredGitPathsWhenPreparedChildObjectMissing(t *testing.T) {
	svc, cleanup := testharness.NewService(t, testharness.ServiceConfig{})
	defer cleanup()
	ctx := context.Background()

	owner := db.User{Login: "wiki-lost-asset-owner", Name: "Wiki Lost Asset Owner", Type: db.TypeUser}
	if err := svc.DB.Create(&owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	full := owner.Login + "/wiki-lost-asset"
	if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: owner.Login,
		Name:       "wiki-lost-asset",
	}); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	if err := svc.Git.Init(ctx, full+".wiki", "master", false); err != nil {
		t.Fatalf("init wiki repo: %v", err)
	}
	seedHead, err := svc.Git.CommitFiles(ctx, full+".wiki", "master", "receive-pack seed", []gitstore.FileMutation{
		{Path: "home.md", Content: []byte("# Home\n")},
		{Path: "logo.png", Content: []byte("png bytes")},
		{Path: ".hidden.md", Content: []byte("hidden markdown")},
	})
	if err != nil {
		t.Fatalf("seed wiki git: %v", err)
	}
	if _, err := svc.IngestWikiGitAfterReceivePackLocked(ctx, full, service.WikiGitIngestOptions{}); err != nil {
		t.Fatalf("ingest receive-pack seed: %v", err)
	}
	if _, err := svc.GetWikiPage(ctx, full, "home"); err != nil {
		t.Fatalf("GetWikiPage(home): %v", err)
	}

	publishErr := errors.New("forced child publish failure")
	var childCommit string
	service.SetTestWikiPreparedPublishFailureForTest(svc, func(repoFullName, commitSHA string) error {
		if repoFullName == full && commitSHA != "" {
			childCommit = commitSHA
			return publishErr
		}
		return nil
	})
	if _, err := svc.PutWikiPage(ctx, full, "child", "# Child\n", "create child", ""); !errors.Is(err, publishErr) {
		t.Fatalf("child PutWikiPage error = %v, want %v", err, publishErr)
	}
	service.SetTestWikiPreparedPublishFailureForTest(svc, nil)
	if childCommit == "" {
		t.Fatal("test hook did not capture the prepared child commit")
	}
	failedHead, err := svc.Git.HeadSHA(ctx, full+".wiki", "master")
	if err != nil {
		t.Fatalf("HeadSHA(after failed child): %v", err)
	}
	if failedHead != seedHead {
		t.Fatalf("head after failed child = %s, want seed %s", failedHead, seedHead)
	}

	repoPath, err := svc.Git.GetRepoPath(ctx, full+".wiki")
	if err != nil {
		t.Fatalf("GetRepoPath: %v", err)
	}
	removeLooseGitObject(t, repoPath, childCommit)

	if _, err := svc.PutWikiPage(ctx, full, "next", "# Next\n", "create next", ""); err != nil {
		t.Fatalf("next PutWikiPage: %v", err)
	}
	svc.Wg.Wait()

	for path, want := range map[string]string{
		"home.md":    "# Home\n",
		"child.md":   "# Child\n",
		"next.md":    "# Next\n",
		"logo.png":   "png bytes",
		".hidden.md": "hidden markdown",
	} {
		got, err := svc.Git.ReadFile(ctx, full+".wiki", path)
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", path, err)
		}
		if string(got) != want {
			t.Fatalf("ReadFile(%s) = %q, want %q", path, got, want)
		}
	}
}

func wikiTreePathSet(t *testing.T, svc *service.Service, ctx context.Context, repoFullName string) map[string]struct{} {
	t.Helper()
	paths, err := svc.Git.ListTreeFilesAtRef(ctx, repoFullName+".wiki", "master")
	if err != nil {
		t.Fatalf("ListTreeFilesAtRef: %v", err)
	}
	out := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		out[path] = struct{}{}
	}
	return out
}

func removeLooseGitObject(t *testing.T, repoPath, sha string) {
	t.Helper()
	if len(sha) != 40 {
		t.Fatalf("prepared commit SHA length = %d, want 40", len(sha))
	}
	objectPath := filepath.Join(repoPath, "objects", sha[:2], sha[2:])
	if err := os.Remove(objectPath); err != nil {
		t.Fatalf("remove loose git object %s: %v", sha, err)
	}
}

func removePreparedCommitTreeObject(t *testing.T, repoPath, commitSHA string) {
	t.Helper()
	out, err := exec.Command("git", "--git-dir", repoPath, "show", "-s", "--format=%T", commitSHA).Output()
	if err != nil {
		t.Fatalf("read prepared commit tree: %v", err)
	}
	treeSHA := strings.TrimSpace(string(out))
	removeLooseGitObject(t, repoPath, treeSHA)
}

func TestPutWikiPageCatchesUpUningestedGitBeforeWriting(t *testing.T) {
	svc, cleanup := testharness.NewService(t, testharness.ServiceConfig{})
	defer cleanup()
	ctx := context.Background()

	owner := db.User{Login: "wiki-catchup-owner", Name: "Wiki Catchup Owner", Type: db.TypeUser}
	if err := svc.DB.Create(&owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	full := owner.Login + "/wiki-catchup"
	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: owner.Login,
		Name:       "wiki-catchup",
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	if err := svc.Git.Init(ctx, full+".wiki", "master", false); err != nil {
		t.Fatalf("init wiki repo: %v", err)
	}
	if _, err := svc.Git.CommitFiles(ctx, full+".wiki", "master", "direct push", []gitstore.FileMutation{{
		Path:    "pushed.md",
		Content: []byte("# Pushed\n"),
	}}); err != nil {
		t.Fatalf("seed direct push: %v", err)
	}

	if _, err := svc.PutWikiPage(ctx, full, "rest", "# Rest\n", "create rest", ""); err != nil {
		t.Fatalf("PutWikiPage: %v", err)
	}
	svc.Wg.Wait()

	for slug := range map[string]struct{}{"pushed": {}, "rest": {}} {
		if _, err := svc.GetWikiPage(ctx, full, slug); err != nil {
			t.Fatalf("GetWikiPage(%s): %v", slug, err)
		}
	}

	var changesets []db.WikiChangeset
	if err := svc.DB.
		Where("repository_id = ?", repo.ID).
		Order("changeset_id ASC").
		Find(&changesets).Error; err != nil {
		t.Fatalf("load changesets: %v", err)
	}
	if len(changesets) != 2 {
		t.Fatalf("changeset count = %d, want direct push plus REST write", len(changesets))
	}
	if changesets[0].Source != string(wikicatalog.SourceGit) || changesets[1].Source != string(wikicatalog.SourceREST) {
		t.Fatalf("changeset sources = [%s %s], want [git rest]", changesets[0].Source, changesets[1].Source)
	}
}

func TestPutWikiPageCatchesUpGitCommitAheadOfPreparedCatalogHead(t *testing.T) {
	svc, cleanup := testharness.NewService(t, testharness.ServiceConfig{})
	defer cleanup()
	ctx := context.Background()

	owner := db.User{Login: "wiki-existing-catchup-owner", Name: "Wiki Existing Catchup Owner", Type: db.TypeUser}
	if err := svc.DB.Create(&owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	full := owner.Login + "/wiki-existing-catchup"
	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: owner.Login,
		Name:       "wiki-existing-catchup",
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	if _, err := svc.PutWikiPage(ctx, full, "first", "# First\n", "create first", ""); err != nil {
		t.Fatalf("seed PutWikiPage: %v", err)
	}
	directSHA, err := svc.Git.CommitFiles(ctx, full+".wiki", "master", "direct push", []gitstore.FileMutation{{
		Path:    "pushed.md",
		Content: []byte("# Pushed\n"),
	}})
	if err != nil {
		t.Fatalf("direct Git commit: %v", err)
	}

	if _, err := svc.PutWikiPage(ctx, full, "second", "# Second\n", "create second", ""); err != nil {
		t.Fatalf("PutWikiPage after direct Git commit: %v", err)
	}
	svc.Wg.Wait()

	for slug := range map[string]struct{}{"first": {}, "pushed": {}, "second": {}} {
		if _, err := svc.GetWikiPage(ctx, full, slug); err != nil {
			t.Fatalf("GetWikiPage(%s): %v", slug, err)
		}
	}

	var changesets []db.WikiChangeset
	if err := svc.DB.
		Where("repository_id = ?", repo.ID).
		Order("changeset_id ASC").
		Find(&changesets).Error; err != nil {
		t.Fatalf("load changesets: %v", err)
	}
	if len(changesets) != 3 {
		t.Fatalf("changeset count = %d, want REST + direct Git + REST", len(changesets))
	}
	if got := []string{changesets[0].Source, changesets[1].Source, changesets[2].Source}; got[0] != string(wikicatalog.SourceREST) ||
		got[1] != string(wikicatalog.SourceGit) ||
		got[2] != string(wikicatalog.SourceREST) {
		t.Fatalf("changeset sources = %v, want [rest git rest]", got)
	}
	if changesets[1].SynthCommitSHA != directSHA {
		t.Fatalf("ingested direct SHA = %s, want %s", changesets[1].SynthCommitSHA, directSHA)
	}
}

func TestWikiPostCommitEffectsRunOutsideGitWriteLock(t *testing.T) {
	svc, cleanup := testharness.NewService(t, testharness.ServiceConfig{})
	defer cleanup()
	ctx := context.Background()

	owner := db.User{Login: "wiki-lock-owner", Name: "Wiki Lock Owner", Type: db.TypeUser}
	if err := svc.DB.Create(&owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	full := owner.Login + "/wiki-lock"
	repo, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: owner.Login,
		Name:       "wiki-lock",
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	effectsStarted := make(chan struct{})
	releaseEffects := make(chan struct{})
	var startOnce sync.Once
	var releaseOnce sync.Once
	service.SetTestWikiPostCommitEffectsForTest(svc, func(repoFullName string, _ wikicatalog.ChangeSetResult) {
		if repoFullName != full {
			return
		}
		startOnce.Do(func() {
			close(effectsStarted)
			<-releaseEffects
		})
	})
	defer releaseOnce.Do(func() { close(releaseEffects) })

	firstDone := make(chan error, 1)
	go func() {
		_, writeErr := svc.PutWikiPage(ctx, full, "first", "# First\n", "create first", "")
		firstDone <- writeErr
	}()
	select {
	case <-effectsStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("first post-commit effects did not start")
	}

	secondDone := make(chan error, 1)
	go func() {
		_, writeErr := svc.PutWikiPage(ctx, full, "second", "# Second\n", "create second", "")
		secondDone <- writeErr
	}()

	deadline := time.Now().Add(5 * time.Second)
	for {
		var count int64
		if err := svc.DB.Model(&db.WikiChangeset{}).
			Where("repository_id = ?", repo.ID).
			Count(&count).Error; err != nil {
			t.Fatalf("count changesets: %v", err)
		}
		if count == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("changeset count = %d, second write still blocked by post-commit effects", count)
		}
		time.Sleep(10 * time.Millisecond)
	}

	releaseOnce.Do(func() { close(releaseEffects) })
	for name, done := range map[string]<-chan error{
		"first":  firstDone,
		"second": secondDone,
	} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("%s PutWikiPage: %v", name, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s PutWikiPage did not finish", name)
		}
	}
	svc.Wg.Wait()
}

func TestReceivePackIngestBlocksRESTWriteUntilCatalogCatchup(t *testing.T) {
	svc, cleanup := testharness.NewService(t, testharness.ServiceConfig{})
	defer cleanup()
	ctx := context.Background()

	owner := db.User{Login: "wiki-receive-pack-owner", Name: "Wiki Receive Pack Owner", Type: db.TypeUser}
	if err := svc.DB.Create(&owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	full := owner.Login + "/wiki-receive-pack"
	if _, err := svc.CreateRepo(ctx, service.CreateRepoInput{
		OwnerLogin: owner.Login,
		Name:       "wiki-receive-pack",
	}); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	if err := svc.Git.Init(ctx, full+".wiki", "master", false); err != nil {
		t.Fatalf("init wiki repo: %v", err)
	}
	if _, err := svc.Git.CommitFiles(ctx, full+".wiki", "master", "direct push", []gitstore.FileMutation{{
		Path:    "pushed.md",
		Content: []byte("# Pushed\n"),
	}}); err != nil {
		t.Fatalf("seed direct push: %v", err)
	}

	ingestStarted := make(chan struct{})
	releaseIngest := make(chan struct{})
	svc.SetWikiGitIngestAfterSnapshotHookForTest(func(repoFullName string) {
		if repoFullName != full {
			return
		}
		select {
		case <-ingestStarted:
		default:
			close(ingestStarted)
		}
		<-releaseIngest
	})
	defer svc.SetWikiGitIngestAfterSnapshotHookForTest(nil)

	ingestDone := make(chan error, 1)
	go func() {
		err := svc.WithWikiCatalogWriteLockForReceivePack(ctx, full, func() error {
			return svc.Git.WithRepoLock(ctx, full+".wiki", func() error {
				_, err := svc.IngestWikiGitAfterReceivePackLocked(ctx, full, service.WikiGitIngestOptions{})
				return err
			})
		})
		ingestDone <- err
	}()
	select {
	case <-ingestStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("receive-pack ingest did not start")
	}

	writeDone := make(chan error, 1)
	go func() {
		_, err := svc.PutWikiPage(ctx, full, "rest", "# Rest\n", "create rest", "")
		writeDone <- err
	}()

	select {
	case err := <-writeDone:
		t.Fatalf("REST write finished before receive-pack ingest completed: %v", err)
	case <-time.After(250 * time.Millisecond):
	}

	close(releaseIngest)
	select {
	case err := <-ingestDone:
		if err != nil {
			t.Fatalf("IngestWikiGitAfterReceivePack: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("receive-pack ingest did not finish")
	}

	select {
	case err := <-writeDone:
		if err != nil {
			t.Fatalf("PutWikiPage: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("REST write did not finish after receive-pack ingest")
	}

	svc.Wg.Wait()
	pushed, err := svc.GetWikiPage(ctx, full, "pushed")
	if err != nil {
		t.Fatalf("GetWikiPage(pushed): %v", err)
	}
	if pushed.Title != "Pushed" {
		t.Fatalf("pushed page title = %q, want Pushed", pushed.Title)
	}
}
