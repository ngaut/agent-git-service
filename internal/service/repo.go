// Package service holds the business logic for all GitHub entities.
// Handlers call service functions; service functions talk to DB and gitstore.
package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/embedding"
	"github.com/ngaut/agent-git-service/internal/gitstore"
	"github.com/ngaut/agent-git-service/internal/wikicatalog"
)

// Repository lookup convention:
// Public-facing methods accept repoFullName (e.g. "owner/repo") since that
// is what API callers provide. Internal helpers use numeric repo IDs for
// efficiency. This dual approach is by design.

// Service groups the shared dependencies injected into every service method.
// All methods access the database through s.DBForCtx(ctx) which returns the
// per-request DB when available, falling back to s.DB.
type Service struct {
	Ctx            context.Context
	DB             *gorm.DB
	Git            *gitstore.Store
	WikiCatalog    *wikicatalog.Catalog
	WikiBlob       *wikicatalog.BlobStore
	BaseURL        string
	AttachmentRoot string
	Embedder       embedding.Embedder
	AllowAnyToken  bool
	OIDC           OIDCProvider
	// AttachmentScanner is an optional hook for virus scanning or policy checks
	// before an attachment is written to disk.
	AttachmentScanner func(ctx context.Context, filename, contentType string, content []byte) error

	WorkflowExecEnabled bool
	WorkflowExecImage   string
	WorkflowExecTimeout time.Duration
	WorkflowExecCPUs    string
	WorkflowExecMemory  string
	WorkflowExecPids    int
	WorkflowExecNoFile  int
	WorkflowExecTmpfs   string

	// Wg tracks background goroutines (embeddings, workflow execution,
	// post-push sync) so main can wait for them to finish during shutdown.
	Wg sync.WaitGroup

	// embedSemOnce and embedSem limit concurrent external embedding API
	// calls to prevent rate-limit errors or socket exhaustion during bulk
	// operations. The semaphore is lazily initialized on first use.
	embedSemOnce sync.Once
	embedSem     chan struct{}

	// vectorInitMu and vectorInitDBs track per-DB vector column initialization
	// to ensure tenant DBs get embedding columns before writes.
	vectorInitMu  sync.Mutex
	vectorInitDBs map[*gorm.DB]bool

	// workflowSyncMu serializes workflow syncs per repository so stale snapshot
	// cleanup cannot race with a newer push's sync.
	workflowSyncMuOnce sync.Once
	workflowSyncMu     map[string]*sync.Mutex
	workflowSyncMapMu  sync.Mutex

	wikiMigrationSyncMuOnce sync.Once
	wikiMigrationSyncMu     map[string]*sync.Mutex
	wikiMigrationSyncMapMu  sync.Mutex

	wikiBgMigrationMuOnce sync.Once
	wikiBgMigrationMu     map[string]struct{}
	wikiBgMigrationMapMu  sync.RWMutex

	wikiBgCompactionMuOnce sync.Once
	wikiBgCompactionMu     map[string]string
	wikiBgCompactionMapMu  sync.RWMutex

	workflowStepRunner workflowStepRunner

	// tokenTouchCache deduplicates TouchToken DB writes in-memory.
	tokenTouchCache sync.Map

	// PresenceHub manages ephemeral user presence state
	PresenceHub *PresenceHub

	// typingHub exposes ephemeral issue typing state and subscriptions.
	typingHubOnce sync.Once
	typingHub     *TypingHub

	wikiBacklinksMu    sync.RWMutex
	wikiBacklinksCache map[string]map[string]wikiBacklinkCacheEntry

	webhookWorkersOnce sync.Once
	webhookJobs        chan webhookJob

	// testWikiMigrationAfterSnapshot is a test-only hook used to
	// coordinate concurrent migration callers after they have loaded the
	// migrated-commit snapshot but before they replay any git commits.
	testWikiMigrationAfterSnapshot func(repoFullName string)

	// testWikiBackgroundMigrationStarted is a test-only hook fired when a
	// repo-scoped background wiki migration is claimed and scheduled.
	testWikiBackgroundMigrationStarted func(repoFullName string)

	// testWikiCompactRefUpdateFailure lets tests force the compact ref update
	// path to fail after the catalog transaction commits.
	testWikiCompactRefUpdateFailure func(repoFullName, commitSHA string) error

	// testWikiCompactionJobStarted is a test-only hook fired after the async
	// compaction worker marks a job running.
	testWikiCompactionJobStarted func(jobID string)

	// testWikiCompactionJobContinue is a test-only hook that can block the
	// async compaction worker until tests allow it to proceed.
	testWikiCompactionJobContinue func(jobID string)
}

type tenantRepoKey struct {
	db     *sql.DB
	repoID uint
	repo   string
}

func (s *Service) workflowSyncMuInit() {
	s.workflowSyncMu = make(map[string]*sync.Mutex)
}

func (s *Service) getWorkflowSyncMu(repoFullName string) *sync.Mutex {
	s.workflowSyncMuOnce.Do(s.workflowSyncMuInit)

	s.workflowSyncMapMu.Lock()
	defer s.workflowSyncMapMu.Unlock()

	mu, ok := s.workflowSyncMu[repoFullName]
	if !ok {
		mu = &sync.Mutex{}
		s.workflowSyncMu[repoFullName] = mu
	}
	return mu
}

func (s *Service) wikiMigrationSyncMuInit() {
	s.wikiMigrationSyncMu = make(map[string]*sync.Mutex)
}

func (s *Service) getWikiMigrationSyncMu(key tenantRepoKey) *sync.Mutex {
	s.wikiMigrationSyncMuOnce.Do(s.wikiMigrationSyncMuInit)

	s.wikiMigrationSyncMapMu.Lock()
	defer s.wikiMigrationSyncMapMu.Unlock()

	muKey := s.tenantRepoMutexKey(key)
	mu, ok := s.wikiMigrationSyncMu[muKey]
	if !ok {
		mu = &sync.Mutex{}
		s.wikiMigrationSyncMu[muKey] = mu
	}
	return mu
}

func (s *Service) wikiBgMigrationMuInit() {
	s.wikiBgMigrationMu = make(map[string]struct{})
}

func (s *Service) wikiBgCompactionMuInit() {
	s.wikiBgCompactionMu = make(map[string]string)
}

func (s *Service) claimWikiBackgroundMigration(key tenantRepoKey) bool {
	s.wikiBgMigrationMuOnce.Do(s.wikiBgMigrationMuInit)

	s.wikiBgMigrationMapMu.Lock()
	defer s.wikiBgMigrationMapMu.Unlock()

	muKey := s.tenantRepoMutexKey(key)
	if _, ok := s.wikiBgMigrationMu[muKey]; ok {
		return false
	}
	s.wikiBgMigrationMu[muKey] = struct{}{}
	return true
}

func (s *Service) releaseWikiBackgroundMigration(key tenantRepoKey) {
	s.wikiBgMigrationMuOnce.Do(s.wikiBgMigrationMuInit)

	s.wikiBgMigrationMapMu.Lock()
	defer s.wikiBgMigrationMapMu.Unlock()
	delete(s.wikiBgMigrationMu, s.tenantRepoMutexKey(key))
}

func (s *Service) claimWikiBackgroundCompaction(key tenantRepoKey, jobID string) bool {
	s.wikiBgCompactionMuOnce.Do(s.wikiBgCompactionMuInit)

	s.wikiBgCompactionMapMu.Lock()
	defer s.wikiBgCompactionMapMu.Unlock()

	muKey := s.tenantRepoMutexKey(key)
	if _, ok := s.wikiBgCompactionMu[muKey]; ok {
		return false
	}
	s.wikiBgCompactionMu[muKey] = jobID
	return true
}

func (s *Service) releaseWikiBackgroundCompaction(key tenantRepoKey, jobID string) {
	s.wikiBgCompactionMuOnce.Do(s.wikiBgCompactionMuInit)

	s.wikiBgCompactionMapMu.Lock()
	defer s.wikiBgCompactionMapMu.Unlock()

	muKey := s.tenantRepoMutexKey(key)
	if activeJobID, ok := s.wikiBgCompactionMu[muKey]; ok && activeJobID == jobID {
		delete(s.wikiBgCompactionMu, muKey)
	}
}

func (s *Service) isWikiBackgroundMigrationRunning(key tenantRepoKey) bool {
	s.wikiBgMigrationMuOnce.Do(s.wikiBgMigrationMuInit)

	s.wikiBgMigrationMapMu.RLock()
	defer s.wikiBgMigrationMapMu.RUnlock()
	_, ok := s.wikiBgMigrationMu[s.tenantRepoMutexKey(key)]
	return ok
}

func (s *Service) wikiRepoKey(ctx context.Context, repo db.Repository) tenantRepoKey {
	key := tenantRepoKey{
		repoID: repo.ID,
		repo:   repo.FullName,
	}
	targetDB := s.DB
	if tenantDB, ok := DBFromContext(ctx); ok && tenantDB != nil {
		targetDB = tenantDB
	}
	if targetDB != nil {
		if sqlDB, err := s.sqlDBHandle(targetDB); err == nil {
			key.db = sqlDB
		}
	}
	return key
}

func (s *Service) tenantRepoMutexKey(key tenantRepoKey) string {
	return fmt.Sprintf("%p:%d:%s", key.db, key.repoID, key.repo)
}

func (s *Service) sqlDBHandle(dbh interface{ DB() (*sql.DB, error) }) (*sql.DB, error) {
	return dbh.DB()
}

// DBForCtx returns the per-request DB when one was injected via
// ContextWithDB (multi-agent mode), or falls back to s.DB (single-DB mode).
func (s *Service) DBForCtx(ctx context.Context) *gorm.DB {
	if ctx == nil {
		return s.DB.WithContext(context.Background())
	}
	if db, ok := DBFromContext(ctx); ok {
		return db.WithContext(ctx)
	}
	return s.DB.WithContext(ctx)
}

// ServerCtx returns the server-lifecycle context, falling back to
// context.Background() when Ctx is nil (e.g. in tests).
func (s *Service) ServerCtx() context.Context {
	if s.Ctx != nil {
		return s.Ctx
	}
	return context.Background()
}

// HTMLBaseURL returns the base URL with https:// scheme for user-facing URLs.
// GitHub always uses https:// for browser URLs; the CLI tests assert this.
func (s *Service) HTMLBaseURL() string {
	return strings.Replace(s.BaseURL, "http://", "https://", 1)
}

// CreateRepoInput holds parameters for creating a repository.
type CreateRepoInput struct {
	OwnerLogin          string
	Name                string
	Description         string
	Visibility          string
	Private             bool
	HasIssues           bool
	HasIssuesSet        bool
	HasWiki             bool
	HasWikiSet          bool
	HasProjects         *bool
	HasDownloads        *bool
	HasDiscussions      *bool
	Homepage            string
	IsTemplate          bool
	License             string
	AddReadme           bool
	DefaultBranch       string
	AutoInit            bool
	AllowMergeCommit    *bool
	AllowSquashMerge    *bool
	AllowRebaseMerge    *bool
	AllowAutoMerge      *bool
	AllowUpdateBranch   *bool
	DeleteBranchOnMerge *bool
	RequireOrgAdmin     bool
	SkipOrgBootstrap    bool
}

// CreateRepo creates a repository in DB and initializes its git storage.
func (s *Service) CreateRepo(ctx context.Context, in CreateRepoInput) (db.Repository, error) {
	owner, err := s.GetUser(ctx, in.OwnerLogin)
	if err != nil {
		return db.Repository{}, fmt.Errorf("service: create repo: owner: %w", err)
	}
	if owner.Type == db.TypeOrganization && in.RequireOrgAdmin {
		if err := s.requireOrgAdminForRepoTarget(ctx, owner.ID); err != nil {
			return db.Repository{}, err
		}
	}
	if in.DefaultBranch == "" {
		in.DefaultBranch = "main"
	}
	visibility, private, err := normalizeRepositoryVisibility(in.Visibility, in.Private)
	if err != nil {
		return db.Repository{}, err
	}
	in.Private = private
	hasProjects := true
	if in.HasProjects != nil {
		hasProjects = *in.HasProjects
	}
	hasDownloads := true
	if in.HasDownloads != nil {
		hasDownloads = *in.HasDownloads
	}
	hasDiscussions := false
	if in.HasDiscussions != nil {
		hasDiscussions = *in.HasDiscussions
	}
	hasIssues := true
	if in.HasIssuesSet {
		hasIssues = in.HasIssues
	} else if in.HasIssues {
		hasIssues = true
	}
	hasWiki := true
	if in.HasWikiSet {
		hasWiki = in.HasWiki
	} else if in.HasWiki {
		hasWiki = true
	}

	var rep db.Repository
	const maxNameRetries = 10
	for attempt := 0; attempt < maxNameRetries; attempt++ {
		fullName := owner.Login + "/" + in.Name
		rep = db.Repository{
			Name:                in.Name,
			FullName:            fullName,
			Description:         in.Description,
			Private:             in.Private,
			Visibility:          visibility,
			OwnerID:             owner.ID,
			DefaultBranch:       in.DefaultBranch,
			License:             in.License,
			IsTemplate:          in.IsTemplate,
			HasIssues:           hasIssues,
			HasWiki:             hasWiki,
			HasProjects:         hasProjects,
			HasDownloads:        hasDownloads,
			HasDiscussions:      hasDiscussions,
			Homepage:            in.Homepage,
			AllowMergeCommit:    true,
			AllowSquashMerge:    true,
			AllowRebaseMerge:    true,
			AllowAutoMerge:      false,
			AllowUpdateBranch:   false,
			DeleteBranchOnMerge: false,
		}
		if in.AllowMergeCommit != nil {
			rep.AllowMergeCommit = *in.AllowMergeCommit
		}
		if in.AllowSquashMerge != nil {
			rep.AllowSquashMerge = *in.AllowSquashMerge
		}
		if in.AllowRebaseMerge != nil {
			rep.AllowRebaseMerge = *in.AllowRebaseMerge
		}
		if in.AllowAutoMerge != nil {
			rep.AllowAutoMerge = *in.AllowAutoMerge
		}
		if in.AllowUpdateBranch != nil {
			rep.AllowUpdateBranch = *in.AllowUpdateBranch
		}
		if in.DeleteBranchOnMerge != nil {
			rep.DeleteBranchOnMerge = *in.DeleteBranchOnMerge
		}
		now := time.Now().UTC()
		rep.CreatedAt = now
		rep.UpdatedAt = now
		values := map[string]any{
			"name":                   rep.Name,
			"full_name":              rep.FullName,
			"description":            rep.Description,
			"owner_id":               rep.OwnerID,
			"parent_id":              rep.ParentID,
			"private":                rep.Private,
			"visibility":             rep.Visibility,
			"fork":                   rep.Fork,
			"default_branch":         rep.DefaultBranch,
			"language":               rep.Language,
			"license":                rep.License,
			"archived":               rep.Archived,
			"disabled":               rep.Disabled,
			"is_template":            rep.IsTemplate,
			"is_mirror":              rep.IsMirror,
			"has_wiki":               rep.HasWiki,
			"has_issues":             rep.HasIssues,
			"has_projects":           rep.HasProjects,
			"has_downloads":          rep.HasDownloads,
			"has_discussions":        rep.HasDiscussions,
			"homepage":               rep.Homepage,
			"allow_merge_commit":     rep.AllowMergeCommit,
			"allow_squash_merge":     rep.AllowSquashMerge,
			"allow_rebase_merge":     rep.AllowRebaseMerge,
			"allow_auto_merge":       rep.AllowAutoMerge,
			"allow_update_branch":    rep.AllowUpdateBranch,
			"delete_branch_on_merge": rep.DeleteBranchOnMerge,
			"open_issue_count":       rep.OpenIssueCount,
			"topics":                 rep.Topics,
			"pushed_at":              rep.PushedAt,
			"created_at":             rep.CreatedAt,
			"updated_at":             rep.UpdatedAt,
		}
		if err := s.DBForCtx(ctx).Model(&db.Repository{}).Create(values).Error; err != nil {
			if errors.Is(err, gorm.ErrDuplicatedKey) || strings.Contains(err.Error(), "UNIQUE constraint") {
				return db.Repository{}, fmt.Errorf("%w: repository %s already exists", ErrConflict, fullName)
			}
			return db.Repository{}, fmt.Errorf("service: create repo: db: %w", err)
		}
		if err := s.DBForCtx(ctx).First(&rep, "full_name = ?", fullName).Error; err != nil {
			return db.Repository{}, fmt.Errorf("service: create repo: reload: %w", err)
		}
		if owner.Type == db.TypeOrganization {
			if in.SkipOrgBootstrap {
				if err := s.DBForCtx(ctx).Transaction(func(tx *gorm.DB) error {
					return s.ensureOrgRepoGovernanceTx(ctx, tx, owner.ID, rep.ID)
				}); err != nil {
					return db.Repository{}, fmt.Errorf("service: create repo: org governance sync: %w", err)
				}
			} else if viewer, ok := UserFromContext(ctx); ok && viewer.ID != 0 {
				if viewer.ID != owner.ID {
					collab := db.Collaborator{
						RepositoryID: rep.ID,
						UserID:       viewer.ID,
						Permission:   "admin",
					}
					if err := upsertCollaboratorTx(s.DBForCtx(ctx), &collab); err != nil {
						return db.Repository{}, fmt.Errorf("service: create repo: bootstrap collaborator: %w", err)
					}
					if err := syncOutsideCollaboratorForOrgTx(s.DBForCtx(ctx), owner.ID, viewer.ID); err != nil {
						return db.Repository{}, fmt.Errorf("service: create repo: bootstrap outside collaborator sync: %w", err)
					}
				}

				if err := s.DBForCtx(ctx).Transaction(func(tx *gorm.DB) error {
					principalIDs := []uint{viewer.ID}
					if viewer.UserKind == db.UserKindAgent {
						humanID, ok, err := boundHumanIDForAgentQuery(tx, viewer.ID)
						if err != nil {
							return err
						}
						if ok {
							principalIDs = append(principalIDs, humanID)
						}
					}

					adminsTeam, err := ensureAdminsPrincipalsTx(tx, owner.ID, principalIDs...)
					if err != nil {
						return err
					}
					return ensureAdminsTeamRepoTx(tx, adminsTeam.ID, rep.ID)
				}); err != nil {
					return db.Repository{}, fmt.Errorf("service: create repo: admins team repo: %w", err)
				}
			}
		}
		break
	}
	if rep.ID == 0 {
		return db.Repository{}, fmt.Errorf("%w: failed to allocate repo name after retries", ErrConflict)
	}

	fullName := rep.FullName
	seedGit := in.AddReadme || in.AutoInit
	if err := s.Git.Init(ctx, fullName, in.DefaultBranch, seedGit); err != nil {
		// Non-fatal, the repo object still gets returned.
		slog.Error("CreateRepo: git init", "repo", fullName, "error", err)
	}
	_ = s.Git.SetupConfig(ctx, fullName, s.BaseURL)

	if err := preloadRepoFull(s.DBForCtx(ctx)).First(&rep, rep.ID).Error; err != nil {
		return rep, wrapErr(err)
	}
	return rep, nil
}

func normalizeRepositoryVisibility(raw string, private bool) (string, bool, error) {
	visibility := strings.ToLower(strings.TrimSpace(raw))
	switch visibility {
	case "":
		if private {
			return "private", true, nil
		}
		return "public", false, nil
	case "public":
		return "public", false, nil
	case "private":
		return "private", true, nil
	case "internal":
		return "internal", true, nil
	default:
		return "", false, fmt.Errorf("%w: visibility must be public, private, or internal", ErrValidation)
	}
}

func (s *Service) requireOrgAdminForRepoTarget(ctx context.Context, orgID uint) error {
	viewer, ok := UserFromContext(ctx)
	if !ok || viewer.ID == 0 {
		return ErrUnauthorized
	}

	allowed, err := s.IsOrgAdmin(ctx, orgID, viewer.ID)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrForbidden
	}
	return nil
}

func (s *Service) ensureOrgRepoGovernanceTx(ctx context.Context, tx *gorm.DB, orgID, repoID uint) error {
	adminsTeam, err := ensureAdminsTeamTx(tx, orgID)
	if err != nil {
		return err
	}
	if err := ensureAdminsTeamRepoTx(tx, adminsTeam.ID, repoID); err != nil {
		return err
	}

	viewer, ok := UserFromContext(ctx)
	if !ok || viewer.ID == 0 {
		return nil
	}

	principalIDs := []uint{viewer.ID}
	if viewer.UserKind == db.UserKindAgent {
		humanID, ok, err := boundHumanIDForAgentQuery(tx, viewer.ID)
		if err != nil {
			return err
		}
		if ok && humanID != 0 && humanID != viewer.ID {
			principalIDs = append(principalIDs, humanID)
		}
	}

	seen := make(map[uint]struct{}, len(principalIDs))
	for _, principalID := range principalIDs {
		if principalID == 0 {
			continue
		}
		if _, exists := seen[principalID]; exists {
			continue
		}
		seen[principalID] = struct{}{}

		var count int64
		if err := tx.Model(&db.OrganizationMember{}).
			Where("organization_id = ? AND user_id = ? AND role = ?", orgID, principalID, db.OrganizationRoleOwner).
			Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			continue
		}
		if err := ensureAdminsTeamMemberTx(tx, adminsTeam.ID, principalID); err != nil {
			return err
		}
	}

	return nil
}

func (s *Service) lookupRepo(ctx context.Context, fullName string, newQuery func() *gorm.DB) (db.Repository, error) {
	var rep db.Repository
	if err := newQuery().First(&rep, "full_name = ?", fullName).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// Resolve historical repo URLs (rename/transfer/claim/merge) via repo_redirects.
			var redir db.RepoRedirect
			if rerr := s.DBForCtx(ctx).First(&redir, "old_full_name = ?", fullName).Error; rerr == nil {
				if err := newQuery().First(&rep, "id = ?", redir.RepoID).Error; err != nil {
					return rep, wrapErrf(err, "repository %s", fullName)
				}
			} else if !errors.Is(rerr, gorm.ErrRecordNotFound) {
				return rep, rerr
			} else {
				return rep, wrapErrf(err, "repository %s", fullName)
			}
		} else {
			return rep, wrapErrf(err, "repository %s", fullName)
		}
	}
	return rep, nil
}

// GetRepo fetches a repository by owner/name.  Results are cached
// per-request via ContextWithRepoCache to avoid redundant DB queries
// when multiple service methods need the same repo in one handler.
func (s *Service) GetRepo(ctx context.Context, fullName string) (db.Repository, error) {
	if cached, ok := repoCacheGet(ctx, fullName); ok {
		return cached, nil
	}
	rep, err := s.lookupRepo(ctx, fullName, func() *gorm.DB {
		return preloadRepoFull(s.DBForCtx(ctx))
	})
	if err != nil {
		return rep, err
	}
	if viewer, ok := UserFromContext(ctx); ok && viewer.ID != 0 {
		perm, err := s.HasRepoAccess(ctx, rep.ID, viewer.ID)
		if err != nil {
			return db.Repository{}, err
		}
		if !perm.AtLeast(RepoPermissionRead) && !s.isPublicRepo(ctx, rep.ID) {
			return db.Repository{}, ErrNotFound
		}
		repoPermissionCacheSet(ctx, rep.ID, perm)
	} else if err := s.requireRepoPermission(ctx, rep.ID, RepoPermissionRead); err != nil {
		return db.Repository{}, err
	}
	repoCacheSet(ctx, rep)
	return rep, nil
}

// CachedRepoPermission returns the per-request repo permission computed earlier
// in the request lifecycle, when available.
func (s *Service) CachedRepoPermission(ctx context.Context, repoID uint) (RepoPermission, bool) {
	return repoPermissionCacheGet(ctx, repoID)
}

// LoadRepoAggregates returns the repo counters used in REST responses,
// caching the result for the lifetime of the request.
func (s *Service) LoadRepoAggregates(ctx context.Context, repoID uint) RepoAggregates {
	if cached, ok := repoAggregatesCacheGet(ctx, repoID); ok {
		return cached
	}

	var row struct {
		ForksCount      int `gorm:"column:forks_count"`
		OpenIssuesCount int `gorm:"column:open_issues_count"`
		StargazersCount int `gorm:"column:stargazers_count"`
	}
	const query = `
SELECT
	(SELECT COUNT(*) FROM repositories WHERE parent_id = ?) AS forks_count,
	(SELECT COUNT(*) FROM issues WHERE repository_id = ? AND state = ?) AS open_issues_count,
	(SELECT COUNT(*) FROM stars WHERE repository_id = ?) AS stargazers_count
`
	if err := s.DBForCtx(ctx).Raw(query, repoID, repoID, db.StateOpen, repoID).Scan(&row).Error; err != nil {
		slog.Warn("LoadRepoAggregates", "repo_id", repoID, "error", err)
		row.ForksCount = s.ForkCount(ctx, repoID)
		row.OpenIssuesCount = s.CountIssuesByRepoID(ctx, repoID)
		row.StargazersCount = s.StarCount(ctx, repoID)
	}

	aggregates := RepoAggregates{
		ForksCount:      row.ForksCount,
		OpenIssuesCount: row.OpenIssuesCount,
		StargazersCount: row.StargazersCount,
	}
	repoAggregatesCacheSet(ctx, repoID, aggregates)
	return aggregates
}

// RepoDiskUsageKB returns a repository's disk usage in kilobytes, caching the
// result for the lifetime of the current request.
func (s *Service) RepoDiskUsageKB(ctx context.Context, repo db.Repository) int {
	if cached, ok := repoDiskUsageCacheGet(ctx, repo.ID); ok {
		return cached
	}
	diskUsageKB := s.GitDiskUsageKB(ctx, repo.FullName)
	repoDiskUsageCacheSet(ctx, repo.ID, diskUsageKB)
	return diskUsageKB
}

// getRepoBase fetches just the repository identity fields needed for
// repo-scoped mutations and permission checks.
func (s *Service) getRepoBase(ctx context.Context, fullName string) (db.Repository, error) {
	rep, err := s.lookupRepo(ctx, fullName, func() *gorm.DB {
		return s.DBForCtx(ctx).Select("id", "full_name", "owner_id")
	})
	if err != nil {
		return rep, err
	}
	if err := s.requireRepoPermission(ctx, rep.ID, RepoPermissionRead); err != nil {
		return db.Repository{}, err
	}
	return rep, nil
}

// LookupRepoIdentity resolves a repository and redirects without loading
// associations or checking permissions. Callers must enforce authorization.
func (s *Service) LookupRepoIdentity(ctx context.Context, fullName string) (db.Repository, error) {
	return s.lookupRepo(ctx, fullName, func() *gorm.DB {
		return s.DBForCtx(ctx).Select("id", "full_name", "owner_id", "default_branch", "private")
	})
}

// GetRepoByID fetches a repository by its numeric DB ID (as a string, e.g. "42").
func (s *Service) GetRepoByID(ctx context.Context, idStr string) (db.Repository, error) {
	var rep db.Repository
	err := preloadRepoFull(s.DBForCtx(ctx)).First(&rep, "id = ?", idStr).Error
	if err != nil {
		return rep, wrapErr(err)
	}
	if err := s.requireRepoPermission(ctx, rep.ID, RepoPermissionRead); err != nil {
		return db.Repository{}, err
	}
	return rep, nil
}

// DeleteRepo removes a repository from the DB and git store.
func (s *Service) DeleteRepo(ctx context.Context, fullName string) error {
	rep, err := s.GetRepo(ctx, fullName)
	if err != nil {
		return err
	}
	if err := s.requireRepoPermission(ctx, rep.ID, RepoPermissionAdmin); err != nil {
		return err
	}
	fullName = rep.FullName
	var attachmentPaths []string
	if err := s.DBForCtx(ctx).Model(&db.Attachment{}).Where("repository_id = ?", rep.ID).Pluck("stored_path", &attachmentPaths).Error; err != nil {
		return err
	}
	if err := s.DBForCtx(ctx).Transaction(func(tx *gorm.DB) error {
		return s.deleteRepoCascade(tx, rep.ID, fullName)
	}); err != nil {
		return err
	}
	s.cleanupAttachmentPaths(attachmentPaths)
	if err := s.removeRepoAttachmentDir(rep.ID); err != nil {
		return err
	}
	RepoCacheInvalidate(ctx, rep.ID)
	if s.Git != nil {
		return s.Git.Delete(ctx, fullName)
	}
	return nil
}

// deleteRepoCascade removes all records related to a repository in ordered phases.
func (s *Service) deleteRepoCascade(tx *gorm.DB, repoID uint, fullName string) error {
	del := func(stmt *gorm.DB) error { return stmt.Error }

	orgID, err := repoOrganizationIDTx(tx, repoID)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	var collaboratorUserIDs []uint
	if orgID != 0 {
		collaboratorUserIDs, err = repoCollaboratorUserIDsTx(tx, repoID)
		if err != nil {
			return err
		}
	}

	// Lock the repo row to prevent concurrent inserts/updates of FK-bound child rows
	// during cascade delete (important for TiDB/MySQL FK enforcement).
	if tx.Dialector.Name() != "sqlite" {
		if err := del(tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").First(&db.Repository{}, repoID)); err != nil {
			return err
		}
	}

	// Phase 1: Detach forks.
	if err := del(tx.Model(&db.Repository{}).Where("parent_id = ?", repoID).
		Updates(map[string]any{"parent_id": nil, "fork": false})); err != nil {
		return err
	}

	// Phase 2: Workflow child records (jobs, artifacts, runs, workflows, action cache).
	if err := del(tx.Where("run_id IN (?)", tx.Model(&db.WorkflowRun{}).Select("id").Where("repository_id = ?", repoID)).Delete(&db.WorkflowRunJob{})); err != nil {
		return err
	}
	if err := del(tx.Where("run_id IN (?)", tx.Model(&db.WorkflowRun{}).Select("id").Where("repository_id = ?", repoID)).Delete(&db.Artifact{})); err != nil {
		return err
	}
	if err := del(tx.Where("repository_id = ?", repoID).Delete(&db.WorkflowRun{})); err != nil {
		return err
	}
	if err := del(tx.Where("repository_id = ?", repoID).Delete(&db.Workflow{})); err != nil {
		return err
	}
	if err := del(tx.Where("repository_id = ?", repoID).Delete(&db.ActionCache{})); err != nil {
		return err
	}

	// Phase 3: PR child records (review comments, reviews, review requests).
	prIDs := tx.Model(&db.PullRequest{}).Select("id").Where("repository_id = ?", repoID)
	crossPRIDs := tx.Model(&db.PullRequest{}).Select("id").Where("head_repository_id = ?", repoID)
	// PRReviewComment must be deleted before PullRequestReview (FK dependency)
	if err := del(tx.Where("pull_request_id IN (?)", prIDs).Delete(&db.PRReviewComment{})); err != nil {
		return err
	}
	if err := del(tx.Where("pull_request_id IN (?)", crossPRIDs).Delete(&db.PRReviewComment{})); err != nil {
		return err
	}
	if err := del(tx.Where("pull_request_id IN (?)", prIDs).Delete(&db.PullRequestReview{})); err != nil {
		return err
	}
	if err := del(tx.Where("pull_request_id IN (?)", prIDs).Delete(&db.ReviewRequest{})); err != nil {
		return err
	}
	if err := del(tx.Where("pull_request_id IN (?)", crossPRIDs).Delete(&db.PullRequestReview{})); err != nil {
		return err
	}
	if err := del(tx.Where("pull_request_id IN (?)", crossPRIDs).Delete(&db.ReviewRequest{})); err != nil {
		return err
	}

	// Phase 4: Many2many join tables.
	if err := tx.Exec("DELETE FROM pr_labels WHERE pull_request_id IN (SELECT id FROM pull_requests WHERE head_repository_id = ?)", repoID).Error; err != nil {
		return err
	}
	if err := tx.Exec("DELETE FROM issue_labels WHERE issue_id IN (SELECT id FROM issues WHERE repository_id = ?)", repoID).Error; err != nil {
		return err
	}
	if err := tx.Exec("DELETE FROM pr_labels WHERE pull_request_id IN (SELECT id FROM pull_requests WHERE repository_id = ?)", repoID).Error; err != nil {
		return err
	}
	if err := tx.Exec("DELETE FROM wiki_page_labels WHERE repository_id = ?", repoID).Error; err != nil {
		return err
	}

	// Phase 5: PRs and linked records.
	if err := del(tx.Where("head_repository_id = ?", repoID).Delete(&db.PullRequest{})); err != nil {
		return err
	}
	if err := del(tx.Where("repository_id = ?", repoID).Delete(&db.LinkedBranch{})); err != nil {
		return err
	}
	if err := del(tx.Where("repository_id = ?", repoID).Delete(&db.PullRequest{})); err != nil {
		return err
	}

	// Phase 6: Issue-related records.
	// Keep milestones last in this phase: issues.milestone_id has an FK to milestones.id.
	if err := del(tx.Where("repository_id = ?", repoID).Delete(&db.IssueComment{})); err != nil {
		return err
	}
	if err := del(tx.Where("repository_id = ?", repoID).Delete(&db.Attachment{})); err != nil {
		return err
	}
	// Issue events reference issues via FK, so remove them before deleting issues.
	if err := del(tx.Where("issue_id IN (?)", tx.Model(&db.Issue{}).Select("id").Where("repository_id = ?", repoID)).Delete(&db.IssueEvent{})); err != nil {
		return err
	}
	if err := del(tx.Where("repository_id = ?", repoID).Delete(&db.Issue{})); err != nil {
		return err
	}
	if err := del(tx.Where("repository_id = ?", repoID).Delete(&db.Milestone{})); err != nil {
		return err
	}

	// Phase 7: Other records (labels, keys, releases, variables, secrets, rulesets, autolinks, stars).
	// Deployment statuses before deployments (FK dependency).
	if err := del(tx.Where("repository_id = ?", repoID).Delete(&db.Notification{})); err != nil {
		return err
	}
	if err := del(tx.Where("deployment_id IN (?)", tx.Model(&db.Deployment{}).Select("id").Where("repository_id = ?", repoID)).Delete(&db.DeploymentStatus{})); err != nil {
		return err
	}
	if err := del(tx.Where("repository_id = ?", repoID).Delete(&db.Deployment{})); err != nil {
		return err
	}
	if err := del(tx.Where("repository_id = ?", repoID).Delete(&db.BranchProtection{})); err != nil {
		return err
	}
	if err := del(tx.Where("repository_id = ?", repoID).Delete(&db.Collaborator{})); err != nil {
		return err
	}
	if err := del(tx.Where("repository_id = ?", repoID).Delete(&db.TeamRepository{})); err != nil {
		return err
	}
	if err := del(tx.Where("repository_id = ?", repoID).Delete(&db.CommitStatus{})); err != nil {
		return err
	}
	if err := del(tx.Where("repository_id = ?", repoID).Delete(&db.WikiCompactionJob{})); err != nil {
		return err
	}
	if err := del(tx.Where("repository_id = ?", repoID).Delete(&db.WikiSearchDocument{})); err != nil {
		return err
	}
	if err := del(tx.Where("repository_id = ?", repoID).Delete(&db.DependabotAlert{})); err != nil {
		return err
	}
	if err := del(tx.Where("repository_id = ?", repoID).Delete(&db.RepositoryInvitation{})); err != nil {
		return err
	}
	if err := del(tx.Where("repository_id = ?", repoID).Delete(&db.Webhook{})); err != nil {
		return err
	}
	if err := del(tx.Where("repository_id = ?", repoID).Delete(&db.Label{})); err != nil {
		return err
	}
	if err := del(tx.Where("repository_id = ?", repoID).Delete(&db.DeployKey{})); err != nil {
		return err
	}
	// Release assets before releases
	if err := del(tx.Where("release_id IN (?)", tx.Model(&db.Release{}).Select("id").Where("repository_id = ?", repoID)).Delete(&db.ReleaseAsset{})); err != nil {
		return err
	}
	if err := del(tx.Where("repository_id = ?", repoID).Delete(&db.Release{})); err != nil {
		return err
	}
	if err := del(tx.Where("repository_id = ?", repoID).Delete(&db.Variable{})); err != nil {
		return err
	}
	if err := del(tx.Where("repository_id = ?", repoID).Delete(&db.Secret{})); err != nil {
		return err
	}
	if err := del(tx.Where("repository_id = ?", repoID).Delete(&db.Ruleset{})); err != nil {
		return err
	}
	if err := del(tx.Where("repository_full_name = ?", fullName).Delete(&db.Autolink{})); err != nil {
		return err
	}
	if err := del(tx.Where("repository_id = ?", repoID).Delete(&db.Star{})); err != nil {
		return err
	}
	// Remove historical redirects before deleting the repo row itself.
	if err := tx.Exec("DELETE FROM repo_redirects WHERE repo_id = ?", repoID).Error; err != nil {
		return err
	}

	// Phase 8: Delete repo itself.
	if err := tx.Delete(&db.Repository{ID: repoID}).Error; err != nil {
		return err
	}
	if orgID != 0 {
		for _, userID := range collaboratorUserIDs {
			if err := syncOutsideCollaboratorForOrgTx(tx, orgID, userID); err != nil {
				return err
			}
		}
	}
	return nil
}

// UpdateRepoInput for PATCH /repos/{owner}/{repo}.
type UpdateRepoInput struct {
	Name                *string
	Description         *string
	Private             *bool
	HasIssues           *bool
	HasWiki             *bool
	HasProjects         *bool
	HasDownloads        *bool
	HasDiscussions      *bool
	Homepage            OptionalStringUpdate
	DefaultBranch       *string
	Archived            *bool
	AllowMergeCommit    *bool
	AllowSquashMerge    *bool
	AllowRebaseMerge    *bool
	AllowAutoMerge      *bool
	AllowUpdateBranch   *bool
	DeleteBranchOnMerge *bool
}

type OptionalStringUpdate struct {
	Set   bool
	Value *string
}

// UpdateRepo applies a partial update to a repository.
// When the Name field changes, it delegates to RenameRepo to atomically
// update FullName and the git directory, then applies remaining fields.
func (s *Service) UpdateRepo(ctx context.Context, fullName string, in UpdateRepoInput) (db.Repository, error) {
	// Load repo first so we can validate permissions before any rename.
	baseRep, err := s.GetRepo(ctx, fullName)
	if err != nil {
		return db.Repository{}, err
	}
	if err := s.requireRepoPermission(ctx, baseRep.ID, RepoPermissionWrite); err != nil {
		return db.Repository{}, err
	}

	// If name is changing, delegate the rename (updates FullName + git dir).
	currentFullName := baseRep.FullName
	if in.Name != nil {
		renamed, err := s.RenameRepo(ctx, currentFullName, *in.Name)
		if err != nil {
			return db.Repository{}, fmt.Errorf("service: update repo: rename: %w", err)
		}
		currentFullName = renamed.FullName
	}

	var rep db.Repository
	if err := s.DBForCtx(ctx).Preload("Owner").First(&rep, "full_name = ?", currentFullName).Error; err != nil {
		return rep, wrapErrf(err, "repository %s", currentFullName)
	}
	if in.Description != nil {
		rep.Description = *in.Description
	}
	if in.Private != nil {
		rep.Private = *in.Private
		switch {
		case !*in.Private:
			rep.Visibility = "public"
		case strings.EqualFold(rep.Visibility, "internal"):
			rep.Visibility = "internal"
		default:
			rep.Visibility = "private"
		}
	}
	if in.HasIssues != nil {
		rep.HasIssues = *in.HasIssues
	}
	if in.HasWiki != nil {
		rep.HasWiki = *in.HasWiki
	}
	if in.HasProjects != nil {
		rep.HasProjects = *in.HasProjects
	}
	if in.HasDownloads != nil {
		rep.HasDownloads = *in.HasDownloads
	}
	if in.HasDiscussions != nil {
		rep.HasDiscussions = *in.HasDiscussions
	}
	if in.Homepage.Set {
		if in.Homepage.Value == nil {
			rep.Homepage = ""
		} else {
			rep.Homepage = *in.Homepage.Value
		}
	}
	if in.DefaultBranch != nil {
		rep.DefaultBranch = *in.DefaultBranch
	}
	if in.Archived != nil {
		rep.Archived = *in.Archived
	}
	if in.AllowMergeCommit != nil {
		rep.AllowMergeCommit = *in.AllowMergeCommit
	}
	if in.AllowSquashMerge != nil {
		rep.AllowSquashMerge = *in.AllowSquashMerge
	}
	if in.AllowRebaseMerge != nil {
		rep.AllowRebaseMerge = *in.AllowRebaseMerge
	}
	if in.AllowAutoMerge != nil {
		rep.AllowAutoMerge = *in.AllowAutoMerge
	}
	if in.AllowUpdateBranch != nil {
		rep.AllowUpdateBranch = *in.AllowUpdateBranch
	}
	if in.DeleteBranchOnMerge != nil {
		rep.DeleteBranchOnMerge = *in.DeleteBranchOnMerge
	}
	if err := s.DBForCtx(ctx).Save(&rep).Error; err != nil {
		return rep, err
	}
	// Invalidate by ID so both old and new names are cleared.
	RepoCacheInvalidate(ctx, rep.ID)
	if err := preloadRepoFull(s.DBForCtx(ctx)).First(&rep, rep.ID).Error; err != nil {
		return rep, wrapErr(err)
	}
	repoCacheSet(ctx, rep)
	return rep, nil
}

// UpdateRepoTopics updates the topics for a repository.
func (s *Service) UpdateRepoTopics(ctx context.Context, fullName, topics string) error {
	rep, err := s.GetRepo(ctx, fullName)
	if err != nil {
		return err
	}
	if err := s.requireRepoPermission(ctx, rep.ID, RepoPermissionWrite); err != nil {
		return err
	}
	res := s.DBForCtx(ctx).Model(&db.Repository{}).Where("id = ?", rep.ID).Update("topics", topics)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}
