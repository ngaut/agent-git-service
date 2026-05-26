package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"gh-server/internal/db"
	"gh-server/internal/gitstore"
	"gh-server/internal/wikicatalog"
)

const (
	wikiCompactCommitName  = "gh-server"
	wikiCompactCommitEmail = "gh-server@localhost"
)

type WikiCompactResult struct {
	PreviousHead    string
	NewHead         string
	CompactedBefore time.Time
	Pages           int
	CommitsRemoved  int
}

func (s *Service) CompactWikiHistory(ctx context.Context, repoFullName string) (WikiCompactResult, error) {
	if s.WikiCatalog == nil {
		return WikiCompactResult{}, errors.New("wiki catalog unavailable")
	}
	if err := s.ensureWikiCatalogCurrent(ctx, repoFullName); err != nil {
		return WikiCompactResult{}, err
	}
	rep, err := s.getRepoBase(ctx, repoFullName)
	if err != nil {
		return WikiCompactResult{}, err
	}

	var result WikiCompactResult
	err = s.withWikiCatalogWriteLock(ctx, repoFullName, func() error {
		now := time.Now().UTC()

		var livePages []db.WikiPage
		if err := s.DBForCtx(ctx).
			Where("repository_id = ? AND deleted_at IS NULL", rep.ID).
			Order("page_id ASC").
			Find(&livePages).Error; err != nil {
			return err
		}
		if len(livePages) == 0 {
			return ErrNotFound
		}

		var repoHead db.WikiRepoHead
		if err := s.DBForCtx(ctx).Where("repository_id = ?", rep.ID).Take(&repoHead).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}

		var previousChangeset db.WikiChangeset
		if err := s.DBForCtx(ctx).Where("changeset_id = ?", repoHead.HeadChangesetID).Take(&previousChangeset).Error; err != nil {
			return err
		}

		var revisionRows []db.WikiPageRevision
		pageIDs := make([]uint64, 0, len(livePages))
		for _, page := range livePages {
			pageIDs = append(pageIDs, page.PageID)
		}
		if err := s.DBForCtx(ctx).
			Where("page_id IN ?", pageIDs).
			Order("page_id ASC, revision_id DESC").
			Find(&revisionRows).Error; err != nil {
			return err
		}
		if len(revisionRows) == 0 {
			return ErrNotFound
		}

		nextRevisionByPage := make(map[uint64]uint64, len(livePages))
		for _, rev := range revisionRows {
			if _, ok := nextRevisionByPage[rev.PageID]; !ok {
				nextRevisionByPage[rev.PageID] = rev.RevisionID + 1
			}
		}

		newCommitSHA, err := s.createWikiCompactCommitObject(ctx, repoFullName, now)
		if err != nil {
			return err
		}

		newChangeset := db.WikiChangeset{
			RepositoryID:   rep.ID,
			Message:        db.LargeText(fmt.Sprintf("Compact wiki history at %s", now.Format(time.RFC3339))),
			AuthorID:       s.resolveWikiAuthor(ctx),
			CommittedAt:    now,
			PageCount:      len(livePages),
			Source:         string(wikicatalog.SourceAdmin),
			SynthCommitSHA: newCommitSHA,
			SynthFormatVer: synthProjectionMaterialized,
		}

		return s.DBForCtx(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Create(&newChangeset).Error; err != nil {
				return err
			}
			if err := tx.Model(&db.WikiRepoHead{}).
				Where("repository_id = ? AND head_changeset_id = ?", rep.ID, repoHead.HeadChangesetID).
				Updates(map[string]any{
					"head_changeset_id": newChangeset.ChangesetID,
					"updated_at":        now,
				}).Error; err != nil {
				return err
			}

			if err := tx.Where("page_id IN ?", pageIDs).Delete(&db.WikiPageRevision{}).Error; err != nil {
				return err
			}

			for _, page := range livePages {
				nextRevisionID, ok := nextRevisionByPage[page.PageID]
				if !ok {
					nextRevisionID = page.HeadRevisionID + 1
				}
				newRevision := db.WikiPageRevision{
					PageID:      page.PageID,
					RevisionID:  nextRevisionID,
					ChangesetID: newChangeset.ChangesetID,
					BlobSHA:     page.HeadBlobSHA,
					BodySize:    page.BodySize,
					BodyInline:  page.BodyInline,
					SlugAtRev:   page.Slug,
					CommitSHA:   newCommitSHA,
					Op:          "update",
					AuthorID:    newChangeset.AuthorID,
					CommittedAt: now,
				}
				if err := tx.Create(&newRevision).Error; err != nil {
					return err
				}
				if err := tx.Model(&db.WikiPage{}).
					Where("page_id = ?", page.PageID).
					Updates(map[string]any{
						"head_revision_id":  newRevision.RevisionID,
						"head_changeset_id": newChangeset.ChangesetID,
						"last_author_id":    newChangeset.AuthorID,
						"updated_at":        now,
					}).Error; err != nil {
					return err
				}
			}
			if err := tx.Where("repository_id = ? AND changeset_id <> ?", rep.ID, newChangeset.ChangesetID).Delete(&db.WikiChangeset{}).Error; err != nil {
				return err
			}

			result = WikiCompactResult{
				PreviousHead:    previousChangeset.SynthCommitSHA,
				NewHead:         newCommitSHA,
				CompactedBefore: now,
				Pages:           len(livePages),
				CommitsRemoved:  len(revisionRows) - len(livePages),
			}
			return nil
		})
	})
	if err != nil {
		return WikiCompactResult{}, err
	}
	if err := s.updateWikiCompactRef(ctx, repoFullName, result.NewHead); err != nil {
		if repairErr := s.repairWikiCatalogFromGit(ctx, repoFullName); repairErr != nil {
			return WikiCompactResult{}, fmt.Errorf("compact wiki history: update git ref: %w (catalog repair failed: %v)", err, repairErr)
		}
		return WikiCompactResult{}, err
	}
	s.invalidateWikiBacklinks(repoFullName)
	return result, nil
}

func (s *Service) createWikiCompactCommitObject(ctx context.Context, repoFullName string, committedAt time.Time) (string, error) {
	if s.Git == nil {
		return "", errors.New("git store unavailable")
	}
	if err := s.ensureWikiRepo(ctx, repoFullName); err != nil {
		return "", err
	}
	full := wikiRepoFullName(repoFullName)
	headSHA, err := s.Git.HeadSHA(ctx, full, wikiDefaultBranch)
	if err != nil {
		return "", err
	}
	headCommit, err := s.Git.GetGitCommitObject(ctx, full, headSHA)
	if err != nil {
		return "", err
	}
	commit, err := s.Git.CreateCommitObject(ctx, full, gitstore.CreateCommitOptions{
		Message: fmt.Sprintf("Compact wiki history at %s", committedAt.Format(time.RFC3339)),
		TreeSHA: headCommit.TreeSHA,
		Author: gitstore.GitSignature{
			Name:  wikiCompactCommitName,
			Email: wikiCompactCommitEmail,
			Date:  committedAt.Format(time.RFC3339),
		},
		Committer: gitstore.GitSignature{
			Name:  wikiCompactCommitName,
			Email: wikiCompactCommitEmail,
			Date:  committedAt.Format(time.RFC3339),
		},
	})
	if err != nil {
		return "", err
	}
	return commit.SHA, nil
}

func (s *Service) updateWikiCompactRef(ctx context.Context, repoFullName, commitSHA string) error {
	if s.testWikiCompactRefUpdateFailure != nil {
		if err := s.testWikiCompactRefUpdateFailure(repoFullName, commitSHA); err != nil {
			return err
		}
	}
	if s.Git == nil {
		return errors.New("git store unavailable")
	}
	full := wikiRepoFullName(repoFullName)
	return s.Git.UpdateRefSafe(ctx, full, "refs/heads/"+wikiDefaultBranch, commitSHA, true)
}

func (s *Service) repairWikiCatalogFromGit(ctx context.Context, repoFullName string) error {
	_, err := s.MigrateWiki(ctx, repoFullName, WikiMigrationOptions{})
	return err
}
