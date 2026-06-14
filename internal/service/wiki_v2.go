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
	"github.com/ngaut/agent-git-service/internal/gitstore"
	"github.com/ngaut/agent-git-service/internal/wikiv2"
)

// WikiV2KickResult reports the persisted reconcile request marker for one repo.
type WikiV2KickResult struct {
	RepositoryID     uint
	IndexedCommitSHA string
	RequestedAt      time.Time
}

// WikiV2StateResult reports the currently persisted derived-index state for one repo.
type WikiV2StateResult struct {
	RepositoryID         uint
	IndexedCommitSHA     string
	IndexedAt            *time.Time
	ReconcileRequestedAt *time.Time
	ReconcilerLeaseUntil *time.Time
	PageCount            int
}

type wikiV2SnapshotReplaceResult struct {
	Applied          bool
	CurrentHeadSHA   string
	CurrentPageCount int
}

type wikiV2PageSnapshot struct {
	row  db.WikiPageIndex
	path string
	body string
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
		replaceResult, err := s.replaceWikiV2Snapshot(ctx, full, rep.ID, "", nil, nil, nil, reconciledAt)
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
	snapshots := make([]wikiV2PageSnapshot, 0, len(paths))
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
		snapshots = append(snapshots, wikiV2PageSnapshot{
			row:  rows[len(rows)-1],
			path: path,
			body: string(body),
		})
	}

	backlinks := buildWikiV2Backlinks(rep.ID, reconciledAt, snapshots)
	history, err := s.buildWikiV2History(ctx, full, rep.ID, snapshots, reconciledAt)
	if err != nil {
		return wikiv2.ReconcileResult{}, err
	}

	replaceResult, err := s.replaceWikiV2Snapshot(ctx, full, rep.ID, headSHA, rows, backlinks, history, reconciledAt)
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

// GetWikiV2State returns the current persisted derived-index state without changing wiki behavior.
func (s *Service) GetWikiV2State(ctx context.Context, repoFullName string) (WikiV2StateResult, error) {
	rep, err := s.getRepoBase(ctx, repoFullName)
	if err != nil {
		return WikiV2StateResult{}, err
	}
	state, err := s.loadWikiV2State(ctx, rep.ID)
	if err != nil {
		return WikiV2StateResult{}, err
	}
	var pageCount int64
	if err := s.DBForCtx(ctx).Model(&db.WikiPageIndex{}).Where("repository_id = ?", rep.ID).Count(&pageCount).Error; err != nil {
		return WikiV2StateResult{}, err
	}
	return WikiV2StateResult{
		RepositoryID:         rep.ID,
		IndexedCommitSHA:     state.IndexedCommitSHA,
		IndexedAt:            state.IndexedAt,
		ReconcileRequestedAt: state.ReconcileRequestedAt,
		ReconcilerLeaseUntil: state.ReconcilerLeaseUntil,
		PageCount:            int(pageCount),
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

func (s *Service) replaceWikiV2Snapshot(ctx context.Context, repoFullName string, repoID uint, headSHA string, rows []db.WikiPageIndex, backlinks []db.WikiBacklink, history []db.WikiPageHistory, indexedAt time.Time) (wikiV2SnapshotReplaceResult, error) {
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
		if err := tx.Where("repository_id = ?", repoID).Delete(&db.WikiBacklink{}).Error; err != nil {
			return err
		}
		if err := tx.Where("repository_id = ?", repoID).Delete(&db.WikiPageHistory{}).Error; err != nil {
			return err
		}
		if len(rows) > 0 {
			if err := tx.CreateInBatches(rows, 100).Error; err != nil {
				return err
			}
		}
		if len(backlinks) > 0 {
			if err := tx.CreateInBatches(backlinks, 100).Error; err != nil {
				return err
			}
		}
		if len(history) > 0 {
			if err := tx.CreateInBatches(history, 100).Error; err != nil {
				return err
			}
		}

		state := db.WikiIndexState{
			RepositoryID:        repoID,
			IndexedCommitSHA:    candidateHeadSHA,
			BacklinksIndexedSHA: candidateHeadSHA,
			IndexedAt:           &indexedAt,
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "repository_id"}},
			DoUpdates: clause.Assignments(map[string]any{
				"indexed_commit_sha":     state.IndexedCommitSHA,
				"backlinks_indexed_sha":  state.BacklinksIndexedSHA,
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

func buildWikiV2Backlinks(repoID uint, updatedAt time.Time, snapshots []wikiV2PageSnapshot) []db.WikiBacklink {
	pages := make(map[string]struct{}, len(snapshots))
	topLevelPages := make(map[string]struct{}, len(snapshots))
	for _, snapshot := range snapshots {
		slug := snapshot.row.Slug
		pages[slug] = struct{}{}
		if !strings.Contains(slug, "/") {
			topLevelPages[slug] = struct{}{}
		}
	}

	backlinks := make([]db.WikiBacklink, 0)
	seen := make(map[string]struct{})
	for _, snapshot := range snapshots {
		for _, match := range extractWikiLinkMatches(snapshot.body) {
			resolvedTarget, ok := resolveWikiBacklinkTarget(match, pages, topLevelPages)
			if !ok {
				continue
			}
			key := snapshot.row.Slug + "\x00" + resolvedTarget
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			backlinks = append(backlinks, db.WikiBacklink{
				RepositoryID: repoID,
				SrcSlug:      snapshot.row.Slug,
				DstSlug:      resolvedTarget,
				Resolved:     true,
				UpdatedAt:    updatedAt,
			})
		}
	}
	sort.Slice(backlinks, func(i, j int) bool {
		if backlinks[i].DstSlug == backlinks[j].DstSlug {
			return backlinks[i].SrcSlug < backlinks[j].SrcSlug
		}
		return backlinks[i].DstSlug < backlinks[j].DstSlug
	})
	return backlinks
}

func (s *Service) buildWikiV2History(ctx context.Context, wikiRepoFullName string, repoID uint, snapshots []wikiV2PageSnapshot, fallback time.Time) ([]db.WikiPageHistory, error) {
	history := make([]db.WikiPageHistory, 0, len(snapshots)*2)
	for _, snapshot := range snapshots {
		commits, err := s.Git.ListAllCommits(ctx, wikiRepoFullName, &gitstore.ListCommitsOptions{Path: snapshot.path})
		if err != nil {
			return nil, fmt.Errorf("wiki v2 reconcile: load history for %s: %w", snapshot.path, err)
		}
		for idx, commit := range commits {
			authorID, err := s.lookupWikiV2AuthorID(ctx, commit.Email)
			if err != nil {
				return nil, err
			}
			committerID, err := s.lookupWikiV2AuthorID(ctx, commit.CommitterEmail)
			if err != nil {
				return nil, err
			}
			bodySize := 0
			if body, err := s.Git.ReadFileAtRef(ctx, wikiRepoFullName, snapshot.path, commit.SHA); err == nil {
				bodySize = len(body)
			}
			parentSHA := ""
			if len(commit.ParentSHAs) > 0 {
				parentSHA = commit.ParentSHAs[0]
			}
			history = append(history, db.WikiPageHistory{
				RepositoryID:    repoID,
				Slug:            snapshot.row.Slug,
				CommitSHA:       strings.ToLower(strings.TrimSpace(commit.SHA)),
				ParentCommitSHA: strings.ToLower(strings.TrimSpace(parentSHA)),
				PathSequence:    len(commits) - idx,
				AuthorID:        authorID,
				CommitterID:     committerID,
				Message:         strings.TrimSpace(commit.Message),
				BodySize:        bodySize,
				CommittedAt:     parseWikiV2CommitTime(commit.CommitterDate, fallback),
			})
		}
	}
	sort.Slice(history, func(i, j int) bool {
		if history[i].Slug == history[j].Slug {
			if history[i].CommittedAt.Equal(history[j].CommittedAt) {
				if history[i].PathSequence == history[j].PathSequence {
					return history[i].CommitSHA > history[j].CommitSHA
				}
				return history[i].PathSequence > history[j].PathSequence
			}
			return history[i].CommittedAt.After(history[j].CommittedAt)
		}
		return history[i].Slug < history[j].Slug
	})
	return history, nil
}
