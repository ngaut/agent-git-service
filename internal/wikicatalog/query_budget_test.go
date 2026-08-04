package wikicatalog

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"

	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

type catalogQueryRecorder struct {
	gormlogger.Interface

	mu         sync.Mutex
	statements []string
}

func newCatalogQueryRecorder() *catalogQueryRecorder {
	return &catalogQueryRecorder{Interface: gormlogger.Discard}
}

func (l *catalogQueryRecorder) LogMode(level gormlogger.LogLevel) gormlogger.Interface {
	l.Interface = l.Interface.LogMode(level)
	return l
}

func (l *catalogQueryRecorder) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	sql, _ := fc()
	l.mu.Lock()
	l.statements = append(l.statements, sql)
	l.mu.Unlock()
	l.Interface.Trace(ctx, begin, func() (string, int64) { return sql, 0 }, err)
}

func (l *catalogQueryRecorder) reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.statements = nil
}

func (l *catalogQueryRecorder) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.statements...)
}

func TestPreparedChangeSet_SinglePageQueryBudget(t *testing.T) {
	cat, repoID, gdb := applyTestEnv(t)
	recorder := newCatalogQueryRecorder()
	cat.DB = gdb.Session(&gorm.Session{Logger: recorder})
	ctx := context.Background()

	const slug = "generated/perf/page-00001"
	create := ChangeSetRequest{
		RepositoryID: repoID,
		Source:       SourceREST,
		Message:      "create",
		Changes:      []Change{{Op: OpUpsert, Slug: slug, Body: []byte("body")}},
	}
	applyPreparedForQueryBudget(t, ctx, cat, create)
	assertPreparedQueryBudget(t, recorder.snapshot())

	if _, err := cat.ApplyChangeSet(ctx, ChangeSetRequest{
		RepositoryID: repoID,
		Source:       SourceREST,
		Message:      "delete",
		Changes:      []Change{{Op: OpDelete, Slug: slug}},
	}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	recorder.reset()

	create.Message = "restore"
	result := applyPreparedForQueryBudget(t, ctx, cat, create)
	if got := result.Changes[0].UpsertDisposition; got != UpsertDispositionRestore {
		t.Fatalf("restore disposition = %v, want restore", got)
	}
	assertPreparedQueryBudget(t, recorder.snapshot())
}

func applyPreparedForQueryBudget(
	t *testing.T,
	ctx context.Context,
	cat *Catalog,
	req ChangeSetRequest,
) ChangeSetResult {
	t.Helper()
	prepared, err := cat.PrepareChangeSet(ctx, req)
	if err != nil {
		t.Fatalf("PrepareChangeSet: %v", err)
	}
	result, err := cat.ApplyPreparedChangeSet(ctx, prepared, strings.Repeat("a", 40))
	if err != nil {
		t.Fatalf("ApplyPreparedChangeSet: %v", err)
	}
	return result
}

func assertPreparedQueryBudget(t *testing.T, statements []string) {
	t.Helper()
	const maxQueries = 5
	if len(statements) > maxQueries {
		t.Fatalf("prepared single-page write used %d queries, budget %d:\n%s",
			len(statements), maxQueries, strings.Join(statements, "\n"))
	}

	var standalonePageReads, pagePrefixSnapshots, repairObligationSnapshots int
	for _, statement := range statements {
		if strings.HasPrefix(statement, "SELECT * FROM `wiki_pages`") {
			standalonePageReads++
		}
		if strings.Contains(statement, "FROM wiki_pages AS prefix_page") {
			pagePrefixSnapshots++
		}
		if strings.Contains(statement, "FROM wiki_git_repair_obligations AS repair") {
			repairObligationSnapshots++
		}
		if strings.Contains(strings.ToUpper(statement), "FOR UPDATE") {
			t.Fatalf("prepared write locked and reread its captured head:\n%s",
				strings.Join(statements, "\n"))
		}
		if strings.Contains(statement, "INSERT INTO `wiki_blob_refs`") ||
			strings.Contains(statement, "UPDATE `wiki_blob_refs`") {
			t.Fatalf("inline write touched wiki_blob_refs:\n%s", strings.Join(statements, "\n"))
		}
		if strings.HasPrefix(statement, "UPDATE `wiki_page_links`") {
			t.Fatalf("write without unresolved inbound links ran resolver:\n%s",
				strings.Join(statements, "\n"))
		}
	}
	if standalonePageReads != 0 {
		t.Fatalf("transaction repeated %d standalone page reads:\n%s",
			standalonePageReads, strings.Join(statements, "\n"))
	}
	if pagePrefixSnapshots != 1 {
		t.Fatalf("page prefix snapshots = %d, want one:\n%s",
			pagePrefixSnapshots, strings.Join(statements, "\n"))
	}
	if repairObligationSnapshots != 1 {
		t.Fatalf("repair obligation snapshots = %d, want one folded into preflight:\n%s",
			repairObligationSnapshots, strings.Join(statements, "\n"))
	}
}

func TestPreparedChangeSet_ResolvedOutlinkQueryBudget(t *testing.T) {
	cat, repoID, gdb := applyTestEnv(t)
	recorder := newCatalogQueryRecorder()
	cat.DB = gdb.Session(&gorm.Session{Logger: recorder})
	ctx := context.Background()

	target := applyPreparedForQueryBudget(t, ctx, cat, ChangeSetRequest{
		RepositoryID: repoID,
		Source:       SourceREST,
		Message:      "target",
		Changes: []Change{{
			Op:   OpUpsert,
			Slug: "generated/perf/page-00000",
			Body: []byte("target"),
		}},
	})
	recorder.reset()

	result := applyPreparedForQueryBudget(t, ctx, cat, ChangeSetRequest{
		RepositoryID: repoID,
		Source:       SourceREST,
		Message:      "source",
		Changes: []Change{{
			Op:   OpUpsert,
			Slug: "generated/perf/page-00001",
			Body: []byte("[[generated/perf/page-00000]]"),
		}},
	})
	statements := recorder.snapshot()
	if len(statements) != 6 {
		t.Fatalf("prepared linked sibling used %d queries, want 6:\n%s",
			len(statements), strings.Join(statements, "\n"))
	}
	for _, statement := range statements {
		if strings.HasPrefix(statement, "SELECT `page_id`,`slug` FROM `wiki_pages`") {
			t.Fatalf("transaction repeated outlink target lookup:\n%s",
				strings.Join(statements, "\n"))
		}
	}

	var link db.WikiPageLink
	if err := gdb.Where("src_page_id = ?", result.Changes[0].PageID).Take(&link).Error; err != nil {
		t.Fatalf("read created link: %v", err)
	}
	if link.DstPageID == nil {
		t.Fatal("prepared outlink target was not resolved")
	}
	if *link.DstPageID != target.Changes[0].PageID {
		t.Fatalf("prepared outlink target = %d, want page %d",
			*link.DstPageID, target.Changes[0].PageID)
	}
}

func TestPreparedChangeSet_RejectsStaleHeadSnapshot(t *testing.T) {
	cat, repoID, gdb := applyTestEnv(t)
	ctx := context.Background()

	prepared, err := cat.PrepareChangeSet(ctx, ChangeSetRequest{
		RepositoryID: repoID,
		Source:       SourceREST,
		Message:      "stale",
		Changes:      []Change{{Op: OpUpsert, Slug: "stale", Body: []byte("stale")}},
	})
	if err != nil {
		t.Fatalf("PrepareChangeSet: %v", err)
	}
	winner, err := cat.ApplyChangeSet(ctx, ChangeSetRequest{
		RepositoryID: repoID,
		Source:       SourceREST,
		Message:      "winner",
		Changes:      []Change{{Op: OpUpsert, Slug: "winner", Body: []byte("winner")}},
	})
	if err != nil {
		t.Fatalf("winning ApplyChangeSet: %v", err)
	}

	if _, err := cat.ApplyPreparedChangeSet(ctx, prepared, strings.Repeat("b", 40)); !errors.Is(err, ErrCASLost) {
		t.Fatalf("stale ApplyPreparedChangeSet error = %v, want ErrCASLost", err)
	}
	var count int64
	if err := gdb.Model(&db.WikiPage{}).
		Where("repository_id = ? AND slug = ?", repoID, "stale").
		Count(&count).Error; err != nil {
		t.Fatalf("count stale page: %v", err)
	}
	if count != 0 {
		t.Fatalf("stale prepared write created %d pages, want zero", count)
	}
	if err := gdb.Model(&db.WikiChangeset{}).
		Where("repository_id = ?", repoID).
		Count(&count).Error; err != nil {
		t.Fatalf("count changesets: %v", err)
	}
	if count != 1 {
		t.Fatalf("stale prepared write left %d changesets, want winner only", count)
	}
	var head db.WikiRepoHead
	if err := gdb.Take(&head, "repository_id = ?", repoID).Error; err != nil {
		t.Fatalf("load repository head: %v", err)
	}
	if head.HeadChangesetID != winner.ChangesetID {
		t.Fatalf("repository head = %d, want winner %d", head.HeadChangesetID, winner.ChangesetID)
	}
}

func TestPreparedChangeSet_RejectsRepairObligationCreatedAfterSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name     string
		seedHead bool
	}{
		{name: "empty catalog", seedHead: false},
		{name: "existing head", seedHead: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cat, repoID, gdb := applyTestEnv(t)
			ctx := context.Background()

			var seed ChangeSetResult
			if tc.seedHead {
				var err error
				seed, err = cat.ApplyChangeSet(ctx, ChangeSetRequest{
					RepositoryID: repoID,
					Source:       SourceREST,
					Message:      "seed",
					Changes:      []Change{{Op: OpUpsert, Slug: "seed", Body: []byte("seed")}},
				})
				if err != nil {
					t.Fatalf("seed ApplyChangeSet: %v", err)
				}
			}

			prepared, err := cat.PrepareChangeSet(ctx, ChangeSetRequest{
				RepositoryID: repoID,
				Source:       SourceREST,
				Message:      "stale repair",
				Changes:      []Change{{Op: OpUpsert, Slug: "stale-repair", Body: []byte("stale repair")}},
			})
			if err != nil {
				t.Fatalf("PrepareChangeSet: %v", err)
			}
			now := time.Now().UTC()
			if err := gdb.Create(&db.WikiGitRepairObligation{
				RepositoryID: repoID,
				HeadSHA:      strings.Repeat("a", 40),
				CreatedAt:    now,
				UpdatedAt:    now,
			}).Error; err != nil {
				t.Fatalf("create repair obligation: %v", err)
			}

			if _, err := cat.ApplyPreparedChangeSet(ctx, prepared, strings.Repeat("b", 40)); !errors.Is(err, ErrCASLost) {
				t.Fatalf("stale repair ApplyPreparedChangeSet error = %v, want ErrCASLost", err)
			}
			var count int64
			if err := gdb.Model(&db.WikiPage{}).
				Where("repository_id = ? AND slug = ?", repoID, "stale-repair").
				Count(&count).Error; err != nil {
				t.Fatalf("count stale repair page: %v", err)
			}
			if count != 0 {
				t.Fatalf("stale repair prepared write created %d pages, want zero", count)
			}
			if err := gdb.Model(&db.WikiChangeset{}).
				Where("repository_id = ?", repoID).
				Count(&count).Error; err != nil {
				t.Fatalf("count changesets: %v", err)
			}
			var wantChangesets int64
			if tc.seedHead {
				wantChangesets = 1
			}
			if count != wantChangesets {
				t.Fatalf("changesets = %d, want %d", count, wantChangesets)
			}
			var head db.WikiRepoHead
			err = gdb.Take(&head, "repository_id = ?", repoID).Error
			if !tc.seedHead {
				if !errors.Is(err, gorm.ErrRecordNotFound) {
					t.Fatalf("empty catalog head error = %v, want ErrRecordNotFound", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("load repository head: %v", err)
			}
			if head.HeadChangesetID != seed.ChangesetID {
				t.Fatalf("repository head = %d, want seed %d", head.HeadChangesetID, seed.ChangesetID)
			}
		})
	}
}

func TestChangeSetSnapshotCapturesHeadProjectionState(t *testing.T) {
	cat, repoID, gdb := applyTestEnv(t)
	ctx := context.Background()

	request := ChangeSetRequest{
		RepositoryID: repoID,
		Source:       SourceREST,
		Message:      "next",
		Changes:      []Change{{Op: OpUpsert, Slug: "next", Body: []byte("next")}},
	}
	empty, err := cat.SnapshotChangeSet(ctx, request)
	if err != nil {
		t.Fatalf("SnapshotChangeSet(empty): %v", err)
	}
	if state := empty.HeadProjection(); state.Exists {
		t.Fatalf("empty projection state = %+v, want no head", state)
	}

	pending, err := cat.ApplyChangeSet(ctx, ChangeSetRequest{
		RepositoryID: repoID,
		Source:       SourceREST,
		Message:      "pending",
		Changes:      []Change{{Op: OpUpsert, Slug: "pending", Body: []byte("pending")}},
	})
	if err != nil {
		t.Fatalf("ApplyChangeSet(pending): %v", err)
	}

	snapshot, err := cat.SnapshotChangeSet(ctx, request)
	if err != nil {
		t.Fatalf("SnapshotChangeSet(pending): %v", err)
	}
	state := snapshot.HeadProjection()
	if !state.Exists ||
		state.ChangesetID != pending.ChangesetID ||
		state.CommitSHA != pending.CommitSHA ||
		state.Source != SourceREST ||
		state.SynthFormatVersion != 0 ||
		state.PendingProjectionCount != 1 {
		t.Fatalf("pending projection state = %+v, want changeset %d with one pending projection",
			state, pending.ChangesetID)
	}

	if err := gdb.Model(&db.WikiChangeset{}).
		Where("changeset_id = ?", pending.ChangesetID).
		Update("synth_format_ver", 1).Error; err != nil {
		t.Fatalf("mark projection materialized: %v", err)
	}
	materialized, err := cat.SnapshotChangeSet(ctx, request)
	if err != nil {
		t.Fatalf("SnapshotChangeSet(materialized): %v", err)
	}
	state = materialized.HeadProjection()
	if state.SynthFormatVersion != 1 || state.PendingProjectionCount != 0 {
		t.Fatalf("materialized projection state = %+v, want format 1 and no pending projections", state)
	}
	if _, err := cat.ValidateChangeSetSnapshot(ctx, materialized); err != nil {
		t.Fatalf("ValidateChangeSetSnapshot: %v", err)
	}

	headOnly, err := cat.SnapshotChangeSet(ctx, ChangeSetRequest{
		RepositoryID: repoID,
		Source:       SourceGit,
		Message:      "head-only snapshot",
	})
	if err != nil {
		t.Fatalf("SnapshotChangeSet(head only): %v", err)
	}
	if got := headOnly.HeadProjection(); got != state {
		t.Fatalf("head-only projection state = %+v, want %+v", got, state)
	}
}

func TestChangeSetSnapshotCapturesGitRepairObligationWithoutHead(t *testing.T) {
	cat, repoID, gdb := applyTestEnv(t)
	ctx := context.Background()

	if err := gdb.Create(&db.WikiGitRepairObligation{
		RepositoryID:  repoID,
		BranchMissing: true,
		InProgress:    true,
	}).Error; err != nil {
		t.Fatalf("create repair obligation: %v", err)
	}

	request := ChangeSetRequest{
		RepositoryID: repoID,
		Source:       SourceREST,
		Message:      "create",
		Changes:      []Change{{Op: OpUpsert, Slug: "home", Body: []byte("home")}},
	}
	for name, req := range map[string]ChangeSetRequest{
		"page snapshot": request,
		"head only": {
			RepositoryID: repoID,
			Source:       SourceGit,
			Message:      "head only",
		},
	} {
		t.Run(name, func(t *testing.T) {
			snapshot, err := cat.SnapshotChangeSet(ctx, req)
			if err != nil {
				t.Fatalf("SnapshotChangeSet: %v", err)
			}
			state := snapshot.HeadProjection()
			if state.Exists || !state.GitRepairObligationExists {
				t.Fatalf("projection state = %+v, want no catalog head with repair obligation", state)
			}
		})
	}

	if err := gdb.Delete(&db.WikiGitRepairObligation{}, "repository_id = ?", repoID).Error; err != nil {
		t.Fatalf("delete repair obligation: %v", err)
	}
	snapshot, err := cat.SnapshotChangeSet(ctx, request)
	if err != nil {
		t.Fatalf("SnapshotChangeSet after delete: %v", err)
	}
	if state := snapshot.HeadProjection(); state.GitRepairObligationExists {
		t.Fatalf("projection state after repair clear = %+v, want no obligation", state)
	}
}
