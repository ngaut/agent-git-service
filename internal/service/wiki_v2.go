package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/wikiv2"
)

// WikiV2KickResult reports the persisted reconcile request marker for one repo.
type WikiV2KickResult struct {
	RepositoryID     uint
	IndexedCommitSHA string
	RequestedAt      time.Time
}

type wikiV2SnapshotReplaceResult struct {
	Applied          bool
	CurrentHeadSHA   string
	CurrentPageCount int
}

// KickWikiV2Reconcile persists a manual reconcile request without changing any
// existing wiki route behavior.
func (s *Service) KickWikiV2Reconcile(ctx context.Context, repoFullName string) (WikiV2KickResult, error) {
	rep, err := s.getRepoBase(ctx, repoFullName)
	if err != nil {
		return WikiV2KickResult{}, err
	}
	requestedAt := time.Now().UTC()
	var result WikiV2KickResult
	if err := s.DBForCtx(ctx).Transaction(func(tx *gorm.DB) error {
		var row db.WikiIndexState
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&row, "repository_id = ?", rep.ID).Error
		switch {
		case err == nil:
			row.ReconcileRequestedAt = &requestedAt
			row.UpdatedAt = requestedAt
			if err := tx.Model(&row).Select("reconcile_requested_at", "updated_at").Updates(row).Error; err != nil {
				return err
			}
		case errors.Is(err, gorm.ErrRecordNotFound):
			row = db.WikiIndexState{
				RepositoryID:         rep.ID,
				ReconcileRequestedAt: &requestedAt,
				UpdatedAt:            requestedAt,
			}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		default:
			return err
		}
		result = WikiV2KickResult{
			RepositoryID:     rep.ID,
			IndexedCommitSHA: row.IndexedCommitSHA,
			RequestedAt:      requestedAt,
		}
		return nil
	}); err != nil {
		return WikiV2KickResult{}, err
	}

	return result, nil
}

// ReconcileWikiV2 rebuilds the current derived wiki index from git.
func (s *Service) ReconcileWikiV2(ctx context.Context, repoFullName string) (wikiv2.ReconcileResult, error) {
	rep, err := s.getRepoBase(ctx, repoFullName)
	if err != nil {
		return wikiv2.ReconcileResult{}, err
	}
	if s.Git == nil {
		return wikiv2.ReconcileResult{}, errors.New("wiki v2 reconcile: git store unavailable")
	}

	full := wikiRepoFullName(repoFullName)
	reconciledAt := time.Now().UTC()
	if !s.Git.Exists(ctx, full) || s.Git.IsEmpty(ctx, full) {
		replaceResult, err := s.replaceWikiV2Snapshot(ctx, full, rep.ID, "", nil, reconciledAt)
		if err != nil {
			return wikiv2.ReconcileResult{}, err
		}
		return wikiv2.ReconcileResult{
			RepositoryID:     rep.ID,
			IndexedCommitSHA: replaceResult.CurrentHeadSHA,
			PageCount:        replaceResult.CurrentPageCount,
			Reconciled:       replaceResult.Applied,
		}, nil
	}

	headSHA, err := s.Git.ResolveContentCommit(ctx, full, wikiDefaultBranch)
	if err != nil {
		return wikiv2.ReconcileResult{}, fmt.Errorf("wiki v2 reconcile: resolve head: %w", err)
	}
	paths, err := s.Git.ListTreeFilesAtRef(ctx, full, headSHA)
	if err != nil {
		return wikiv2.ReconcileResult{}, fmt.Errorf("wiki v2 reconcile: list tree: %w", err)
	}
	sort.Strings(paths)

	rows := make([]db.WikiPageIndex, 0, len(paths))
	for _, path := range paths {
		slug := wikiPathToSlug(path)
		if slug == "" {
			continue
		}
		body, blobSHA, err := s.Git.ReadFileWithSHAAtRef(ctx, full, path, headSHA)
		if err != nil {
			return wikiv2.ReconcileResult{}, fmt.Errorf("wiki v2 reconcile: read %s: %w", path, err)
		}
		pageCommit, err := s.Git.CommitForPathAtRef(ctx, full, headSHA, path)
		if err != nil {
			return wikiv2.ReconcileResult{}, fmt.Errorf("wiki v2 reconcile: load page commit for %s: %w", path, err)
		}
		updatedAt := parseWikiV2CommitTime(pageCommit.Committer.Date, reconciledAt)
		lastAuthorID, err := s.lookupWikiV2AuthorID(ctx, pageCommit.Author.Email)
		if err != nil {
			return wikiv2.ReconcileResult{}, err
		}
		rows = append(rows, db.WikiPageIndex{
			RepositoryID:  rep.ID,
			Slug:          slug,
			HeadBlobSHA:   blobSHA,
			HeadCommitSHA: headSHA,
			Title:         titleFromSlug(slug),
			Size:          len(body),
			UpdatedAt:     updatedAt,
			LastAuthorID:  lastAuthorID,
		})
	}

	replaceResult, err := s.replaceWikiV2Snapshot(ctx, full, rep.ID, headSHA, rows, reconciledAt)
	if err != nil {
		return wikiv2.ReconcileResult{}, err
	}
	return wikiv2.ReconcileResult{
		RepositoryID:     rep.ID,
		IndexedCommitSHA: replaceResult.CurrentHeadSHA,
		PageCount:        replaceResult.CurrentPageCount,
		Reconciled:       replaceResult.Applied,
	}, nil
}

func (s *Service) loadWikiV2State(ctx context.Context, repoID uint) (wikiv2.IndexState, error) {
	var row db.WikiIndexState
	if err := s.DBForCtx(ctx).First(&row, "repository_id = ?", repoID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return wikiv2.IndexState{}, nil
		}
		return wikiv2.IndexState{}, err
	}
	return wikiv2.IndexState{
		IndexedCommitSHA:     row.IndexedCommitSHA,
		IndexedAt:            row.IndexedAt,
		ReconcileRequestedAt: row.ReconcileRequestedAt,
		ReconcilerLeaseUntil: row.ReconcilerLeaseUntil,
	}, nil
}

func (s *Service) replaceWikiV2Snapshot(ctx context.Context, repoFullName string, repoID uint, headSHA string, rows []db.WikiPageIndex, indexedAt time.Time) (wikiV2SnapshotReplaceResult, error) {
	var result wikiV2SnapshotReplaceResult
	err := s.DBForCtx(ctx).Transaction(func(tx *gorm.DB) error {
		var current db.WikiIndexState
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, "repository_id = ?", repoID).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		liveHeadSHA := ""
		if s.Git.Exists(ctx, repoFullName) && !s.Git.IsEmpty(ctx, repoFullName) {
			resolvedHeadSHA, err := s.Git.ResolveContentCommit(ctx, repoFullName, wikiDefaultBranch)
			if err != nil {
				return fmt.Errorf("wiki v2 reconcile: resolve live head: %w", err)
			}
			liveHeadSHA = strings.ToLower(strings.TrimSpace(resolvedHeadSHA))
		}
		candidateHeadSHA := strings.ToLower(strings.TrimSpace(headSHA))
		if liveHeadSHA != candidateHeadSHA {
			result = wikiV2SnapshotReplaceResult{
				Applied:          false,
				CurrentHeadSHA:   liveHeadSHA,
				CurrentPageCount: countCurrentWikiV2Rows(tx, repoID),
			}
			return nil
		}

		if err := tx.Where("repository_id = ?", repoID).Delete(&db.WikiPageIndex{}).Error; err != nil {
			return err
		}
		if len(rows) > 0 {
			if err := tx.CreateInBatches(rows, 100).Error; err != nil {
				return err
			}
		}

		state := db.WikiIndexState{
			RepositoryID:     repoID,
			IndexedCommitSHA: candidateHeadSHA,
			IndexedAt:        &indexedAt,
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "repository_id"}},
			DoUpdates: clause.Assignments(map[string]any{
				"indexed_commit_sha":     state.IndexedCommitSHA,
				"indexed_at":             state.IndexedAt,
				"reconcile_requested_at": nil,
				"reconciler_lease_until": nil,
				"updated_at":             indexedAt,
			}),
		}).Create(&state).Error; err != nil {
			return err
		}
		result = wikiV2SnapshotReplaceResult{
			Applied:          true,
			CurrentHeadSHA:   candidateHeadSHA,
			CurrentPageCount: len(rows),
		}
		return nil
	})
	return result, err
}

func (s *Service) lookupWikiV2AuthorID(ctx context.Context, email string) (*uint, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return nil, nil
	}
	var user db.User
	if err := s.DBForCtx(ctx).Select("id").Where("LOWER(email) = ?", email).First(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &user.ID, nil
}

func parseWikiV2CommitTime(raw string, fallback time.Time) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.UTC()
	}
	if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return t.UTC()
	}
	return fallback
}

func countCurrentWikiV2Rows(tx *gorm.DB, repoID uint) int {
	var rowCount int64
	if err := tx.Model(&db.WikiPageIndex{}).Where("repository_id = ?", repoID).Count(&rowCount).Error; err != nil {
		return 0
	}
	return int(rowCount)
}
