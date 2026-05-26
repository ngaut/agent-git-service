// wiki-migrate is the operator-run backfill tool that replays the
// legacy git-backed wiki repos into the wikicatalog tables, in
// preparation for cutting REST traffic over to the catalog-backed
// read/write paths.
//
// It is idempotent: each commit lands keyed by its original git SHA
// and reruns skip commits already present in wiki_changesets, so the
// tool can be re-invoked safely if a previous run was interrupted.
//
// Usage:
//
//	wiki-migrate            # migrate every repo with has_wiki=true
//	wiki-migrate --repo X   # migrate just one repo (e.g. "owner/name")
//
// Required environment variables match the main server: DB_DSN,
// GIT_REPO_DIR, etc. See internal/config for the full list.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"gh-server/internal/config"
	"gh-server/internal/db"
	"gh-server/internal/gitstore"
	"gh-server/internal/service"
	"gh-server/internal/wikicatalog"
)

func main() {
	repoFlag := flag.String("repo", "", "migrate only this owner/name (default: every repo with has_wiki=true)")
	skipIncompatible := flag.Bool("skip-incompatible-slugs", false, "drop pages whose slug still cannot be represented after legacy parsing")
	flag.Parse()

	cfg, err := config.New()
	if err != nil {
		slog.Error("config", "err", err)
		os.Exit(2)
	}

	database, err := db.Init(cfg.DBdsn)
	if err != nil {
		slog.Error("db init", "err", err)
		os.Exit(2)
	}

	var gitOpts []gitstore.Option
	if cfg.ControlPlaneDSN != "" {
		gitOpts = append(gitOpts, gitstore.WithTenantIsolation(), gitstore.WithDefaultTenant("default"))
	}
	store, err := gitstore.New(cfg.GitRepoDir, gitOpts...)
	if err != nil {
		slog.Error("gitstore", "err", err)
		os.Exit(2)
	}

	dataRoot := cfg.GitRepoDir
	if strings.TrimSpace(dataRoot) == "" {
		dataRoot = "."
	}
	wikiBlob := wikicatalog.NewBlobStore(dataRoot)
	wikiCat := wikicatalog.New(database, wikiBlob)
	svc := &service.Service{
		DB:          database,
		Git:         store,
		WikiCatalog: wikiCat,
		WikiBlob:    wikiBlob,
	}
	wikiCat.DBFor = svc.DBForCtx
	// Run the post-commit hook synchronously so the search index stays
	// in step with the catalog as migration progresses; without this
	// the indexer would lag the catalog by the depth of the migration.
	wikiCat.OnChangeSetCommitted = svc.WikiCatalogPostCommit

	opts := service.WikiMigrationOptions{SkipIncompatibleSlugs: *skipIncompatible}
	ctx := context.Background()

	if *repoFlag != "" {
		stats, err := svc.MigrateWiki(ctx, *repoFlag, opts)
		printRepo(*repoFlag, stats)
		if err != nil {
			slog.Error("migrate repo", "repo", *repoFlag, "err", err)
			os.Exit(1)
		}
		return
	}

	report, err := svc.MigrateAllWikis(ctx, opts)
	for name, stats := range report.ByRepo {
		printRepo(name, stats)
	}
	if err != nil {
		slog.Error("migrate all", "err", err)
		os.Exit(1)
	}
	fmt.Printf("migration complete: %d repos\n", len(report.ByRepo))
}

func printRepo(name string, stats service.RepoMigrationStats) {
	fmt.Printf("%-48s  git_commits=%-6d new=%-6d skipped=%-6d pages=%d\n",
		name, stats.GitCommits, stats.NewCommits, stats.SkippedExist, stats.Pages)
}
