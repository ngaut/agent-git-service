package wikicatalog

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"

	"gorm.io/gorm"
)

// applyBenchEnv mirrors applyTestEnv but accepts testing.TB so it can
// be used from benchmarks.
func applyBenchEnv(tb testing.TB) (*Catalog, uint, *gorm.DB) {
	tb.Helper()
	gdb, cleanup := wikicatalogTemplatePool(tb).Open(tb)
	tb.Cleanup(cleanup)
	user := db.User{Login: "alice", Type: "User", Email: "a@example.com"}
	if err := gdb.Create(&user).Error; err != nil {
		tb.Fatalf("seed user: %v", err)
	}
	repo := db.Repository{OwnerID: user.ID, Name: "wiki", FullName: "alice/wiki", DefaultBranch: "main"}
	if err := gdb.Create(&repo).Error; err != nil {
		tb.Fatalf("seed repo: %v", err)
	}
	store := NewBlobStore(tb.TempDir())
	cat := New(gdb, store)
	cat.Now = func() time.Time { return time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC) }
	return cat, repo.ID, gdb
}

// preloadPages bulk-loads n pages into the catalog in chunks of up to
// 2000 changes per changeset, staying safely below
// MaxChangesPerChangeset. Bodies are small enough (under
// MaxBodyInlineBytes) that they all inline — the goal here is to
// populate the index/page rows, not to exercise the CAS filesystem
// path.
func preloadPages(tb testing.TB, cat *Catalog, repoID uint, n int) {
	tb.Helper()
	const chunk = 2000 // stay safely under MaxChangesPerChangeset
	for off := 0; off < n; off += chunk {
		end := off + chunk
		if end > n {
			end = n
		}
		changes := make([]Change, 0, end-off)
		for i := off; i < end; i++ {
			changes = append(changes, Change{
				Op:   OpUpsert,
				Slug: fmt.Sprintf("page-%05d", i),
				Body: []byte(fmt.Sprintf("# Page %05d\n\nbody for page %d\n", i, i)),
			})
		}
		if _, err := cat.ApplyChangeSet(context.Background(), ChangeSetRequest{
			RepositoryID: repoID,
			Source:       SourceMigration, // skip the post-commit hook noise
			Message:      fmt.Sprintf("bulk load %d-%d", off, end),
			Changes:      changes,
		}); err != nil {
			tb.Fatalf("preload changeset %d-%d: %v", off, end, err)
		}
	}
}

// BenchmarkApplyChangeSet_BulkLoad measures the cost of loading N
// pages into the catalog in a single changeset. This is the path
// MigrateWiki uses; the user's "1.5s/page" pain came from the legacy
// per-page git commit path, so this benchmark exercises the catalog's
// bulk-write path on the TiDB test backend. The scaling shape (linear in N,
// no super-linear drift) is what this benchmark guards.
func BenchmarkApplyChangeSet_BulkLoad(b *testing.B) {
	for _, n := range []int{100, 1000, 3000} {
		b.Run(fmt.Sprintf("N=%d", n), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				cat, repoID, _ := applyBenchEnv(b)
				changes := make([]Change, 0, n)
				for j := 0; j < n; j++ {
					changes = append(changes, Change{
						Op:   OpUpsert,
						Slug: fmt.Sprintf("page-%05d", j),
						Body: []byte(fmt.Sprintf("# Page %05d\n\nbody\n", j)),
					})
				}
				b.StartTimer()
				if _, err := cat.ApplyChangeSet(context.Background(), ChangeSetRequest{
					RepositoryID: repoID,
					Source:       SourceMigration,
					Message:      "bulk",
					Changes:      changes,
				}); err != nil {
					b.Fatalf("apply: %v", err)
				}
			}
		})
	}
}

// BenchmarkApplyChangeSet_SingleUpdateAtFill measures the latency of
// one upsert against an existing slug when the catalog already holds N
// pages. The user reported "writes degrade to 1.5s/page as pages
// accumulate"; this benchmark asks: does steady-state per-write cost
// grow with N? Each iteration upserts the same target slug so the
// catalog's live-page count stays exactly N for the entire run — there
// is no fill drift across b.N iterations.
//
// Sub-benchmark N=0 is an outlier: the first iteration must create the
// target slug because there is no preload, so iteration 1 measures a
// create and iterations 2..b.N measure an update at fill 1. With
// typical b.N (hundreds), the amortised reading is dominated by update
// cost.
func BenchmarkApplyChangeSet_SingleUpdateAtFill(b *testing.B) {
	for _, n := range []int{0, 1000, 3000, 10000} {
		b.Run(fmt.Sprintf("N=%d", n), func(b *testing.B) {
			cat, repoID, _ := applyBenchEnv(b)
			preloadPages(b, cat, repoID, n)
			// Pick a slug that already exists in the preload so every
			// iteration is an update, not a create. For N=0 there is
			// no preload, so iteration 1 creates it.
			target := "hot-target"
			if n > 0 {
				target = fmt.Sprintf("page-%05d", n/2)
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := cat.ApplyChangeSet(context.Background(), ChangeSetRequest{
					RepositoryID: repoID,
					Source:       SourceREST,
					Message:      "one",
					Changes: []Change{{
						Op:   OpUpsert,
						Slug: target,
						Body: []byte(fmt.Sprintf("# rev %d\n", i)),
					}},
				}); err != nil {
					b.Fatalf("apply at i=%d: %v", i, err)
				}
			}
		})
	}
}

// BenchmarkListPagesByRepo measures the indexed catalog query that
// will back the sidebar list after the M3 cutover. It returns the slug
// list for the whole repo, ordered for stable rendering, and is the
// catalog-backed replacement for the legacy git-log walk that the user
// observed taking 55 s at 3 000 pages.
//
// The benchmark queries GORM directly because the catalog does not yet
// export a Read/List API — that lands with M3. The query shape mirrors
// what those handlers will issue; if the API surface lands later, this
// benchmark should be rewritten to call it. The numbers therefore time
// the index path, not the production end-to-end HTTP path.
//
// This benchmark guards the scaling shape: linear in N (every live page is in
// the result set), no super-linear drift.
func BenchmarkListPagesByRepo(b *testing.B) {
	for _, n := range []int{100, 1000, 3000, 10000} {
		b.Run(fmt.Sprintf("N=%d", n), func(b *testing.B) {
			cat, repoID, gdb := applyBenchEnv(b)
			preloadPages(b, cat, repoID, n)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				var slugs []string
				if err := gdb.Model(&db.WikiPage{}).
					Where("repository_id = ? AND deleted_at IS NULL", repoID).
					Order("slug").
					Pluck("slug", &slugs).Error; err != nil {
					b.Fatalf("list: %v", err)
				}
				if len(slugs) != n {
					b.Fatalf("got %d slugs, want %d", len(slugs), n)
				}
			}
		})
	}
}

// TestCatalogListQuery_UsesIndexedPlan keeps a deterministic regression check
// on the underlying indexed repo-wide slug query. Rather than asserting
// wall-clock latency in the default unit test lane, it verifies TiDB still
// takes a repository_id + slug index path for the list query shape that the
// future ListWikiPages cutover will rely on.
func TestCatalogListQuery_UsesTiDBIndexedPlan(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping indexed-plan integration check in -short mode")
	}
	_, repoID, gdb := applyTestEnv(t)
	seedWikiPagesForPlan(t, gdb, repoID, 10_000)

	var slugs []string
	if err := gdb.Model(&db.WikiPage{}).
		Where("repository_id = ? AND deleted_at IS NULL", repoID).
		Order("slug").
		Pluck("slug", &slugs).Error; err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(slugs) != 10_000 {
		t.Fatalf("got %d slugs, want 10000", len(slugs))
	}

	plan := explainPlanText(t, gdb, "EXPLAIN SELECT slug FROM wiki_pages WHERE repository_id = ? AND deleted_at IS NULL ORDER BY slug", repoID)
	upperPlan := strings.ToUpper(plan)
	if strings.Contains(upperPlan, "IDX_WIKI_PAGES_REPO_PREFIX") ||
		strings.Contains(upperPlan, "IDX_WIKI_PAGES_REPO_SLUG") {
		return
	}
	t.Fatalf("expected TiDB to use a (repository_id, slug) index for repo-wide slug listing, got plan:\n%s", plan)
}

func seedWikiPagesForPlan(t testing.TB, gdb *gorm.DB, repoID uint, n int) {
	t.Helper()

	now := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)
	pages := make([]db.WikiPage, 0, n)
	for i := 0; i < n; i++ {
		slug := fmt.Sprintf("page-%05d", i)
		pages = append(pages, db.WikiPage{
			RepositoryID:    repoID,
			Slug:            slug,
			Title:           slug,
			HeadBlobSHA:     fmt.Sprintf("%040d", i),
			BodySize:        len(slug),
			BodyInline:      []byte(slug),
			HeadRevisionID:  uint64(i + 1),
			HeadChangesetID: uint64(i + 1),
			CreatedAt:       now,
			UpdatedAt:       now,
		})
	}
	if err := gdb.CreateInBatches(pages, 500).Error; err != nil {
		t.Fatalf("seed wiki pages: %v", err)
	}
}

func explainPlanText(t testing.TB, gdb *gorm.DB, query string, args ...any) string {
	t.Helper()
	rows, err := gdb.Raw(query, args...).Rows()
	if err != nil {
		t.Fatalf("explain query: %v", err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		t.Fatalf("explain columns: %v", err)
	}
	var lines []string
	for rows.Next() {
		values := make([]sql.NullString, len(cols))
		scan := make([]any, len(cols))
		for i := range values {
			scan[i] = &values[i]
		}
		if err := rows.Scan(scan...); err != nil {
			t.Fatalf("scan explain row: %v", err)
		}
		parts := make([]string, 0, len(cols))
		for i, value := range values {
			if value.Valid {
				parts = append(parts, cols[i]+"="+value.String)
			}
		}
		lines = append(lines, strings.Join(parts, " "))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("explain rows: %v", err)
	}
	if len(lines) == 0 {
		t.Fatal("explain returned no rows")
	}
	return strings.Join(lines, "\n")
}

// BenchmarkReadPageBySlug measures the indexed point lookup that will
// back single-page reads after the M3 cutover. It exercises the unique
// index on (repository_id, slug) — the lookup that will sit on
// the read hot path.
//
// As with BenchmarkListPagesByRepo, the catalog does not yet export a
// Read API and this benchmark issues a GORM query in the shape the future
// handler will use. The scaling shape this benchmark guards is O(log N) on a
// B-tree index — flat per-lookup cost regardless of N.
func BenchmarkReadPageBySlug(b *testing.B) {
	for _, n := range []int{100, 1000, 3000, 10000} {
		b.Run(fmt.Sprintf("N=%d", n), func(b *testing.B) {
			cat, repoID, gdb := applyBenchEnv(b)
			preloadPages(b, cat, repoID, n)
			// Pick a slug near the middle so neither bound is favoured.
			target := fmt.Sprintf("page-%05d", n/2)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				var page db.WikiPage
				if err := gdb.Where("repository_id = ? AND slug = ? AND deleted_at IS NULL",
					repoID, target).Take(&page).Error; err != nil {
					b.Fatalf("read %q at N=%d: %v", target, n, err)
				}
				if page.Slug != target {
					b.Fatalf("slug mismatch: got %q want %q", page.Slug, target)
				}
			}
		})
	}
}
