package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/ngaut/agent-git-service/internal/db"
)

const wikiReceivePackRepairObligationOwnerTTL = time.Hour

func (s *Service) recordWikiGitRepairObligationLocked(ctx context.Context, repo db.Repository, inProgress bool) error {
	return s.recordWikiGitRepairObligationWithOwnerLocked(ctx, repo, inProgress, "", nil)
}

func (s *Service) recordWikiGitRepairObligationWithOwnerLocked(
	ctx context.Context,
	repo db.Repository,
	inProgress bool,
	ownerToken string,
	ownerExpiresAt *time.Time,
) error {
	obligation, err := s.buildWikiGitRepairObligationLocked(ctx, repo, inProgress, ownerToken, ownerExpiresAt)
	if err != nil {
		return err
	}
	return s.DBForCtx(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "repository_id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"head_sha":         obligation.HeadSHA,
			"branch_missing":   obligation.BranchMissing,
			"in_progress":      obligation.InProgress,
			"owner_token":      obligation.OwnerToken,
			"owner_expires_at": obligation.OwnerExpiresAt,
			"updated_at":       obligation.UpdatedAt,
		}),
	}).Create(&obligation).Error
}

func (s *Service) buildWikiGitRepairObligationLocked(
	ctx context.Context,
	repo db.Repository,
	inProgress bool,
	ownerToken string,
	ownerExpiresAt *time.Time,
) (db.WikiGitRepairObligation, error) {
	full := wikiRepoFullName(repo.FullName)
	headSHA, err := s.Git.HeadSHA(ctx, full, wikiDefaultBranch)
	branchMissing := false
	if err != nil {
		if !errors.Is(err, plumbing.ErrReferenceNotFound) {
			return db.WikiGitRepairObligation{}, fmt.Errorf("read wiki Git head for repair obligation: %w", err)
		}
		branchMissing = true
	}

	now := time.Now().UTC()
	return db.WikiGitRepairObligation{
		RepositoryID:   repo.ID,
		HeadSHA:        strings.ToLower(strings.TrimSpace(headSHA)),
		BranchMissing:  branchMissing,
		InProgress:     inProgress,
		OwnerToken:     strings.TrimSpace(ownerToken),
		OwnerExpiresAt: ownerExpiresAt,
		CreatedAt:      now,
		UpdatedAt:      now,
	}, nil
}

func (s *Service) claimWikiReceivePackRepairObligationLocked(
	ctx context.Context,
	repo db.Repository,
	ownerToken string,
	ownerExpiresAt *time.Time,
) error {
	obligation, err := s.buildWikiGitRepairObligationLocked(ctx, repo, true, ownerToken, ownerExpiresAt)
	if err != nil {
		return err
	}
	result := s.DBForCtx(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&obligation)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 0 {
		return nil
	}
	existing, exists, err := s.loadWikiGitRepairObligation(ctx, repo.ID)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("wiki receive-pack repair obligation for %s could not be claimed", repo.FullName)
	}
	if wikiGitRepairObligationOwnerActive(existing, time.Now().UTC()) {
		return fmt.Errorf(
			"wiki receive-pack repair obligation for %s is still owned by an active receive-pack until %s",
			repo.FullName,
			existing.OwnerExpiresAt.UTC().Format(time.RFC3339),
		)
	}
	return fmt.Errorf("wiki Git repair obligation for %s must be repaired before receive-pack", repo.FullName)
}

// BeginWikiReceivePackRepairObligationLocked durably marks a wiki
// receive-pack as in progress before git-http-backend is allowed to mutate the
// wiki ref. The caller must already own the wiki catalog serialization lock and
// the backing Git repository lock.
func (s *Service) BeginWikiReceivePackRepairObligationLocked(ctx context.Context, repoFullName string) (string, error) {
	repo, err := s.LookupRepoIdentity(ctx, repoFullName)
	if err != nil {
		return "", err
	}
	ownerToken, err := newWikiReceivePackRepairOwnerToken()
	if err != nil {
		return "", fmt.Errorf("create wiki receive-pack repair owner for %s: %w", repoFullName, err)
	}
	expiresAt := time.Now().UTC().Add(wikiReceivePackRepairObligationOwnerTTL)
	if err := s.claimWikiReceivePackRepairObligationLocked(ctx, repo, ownerToken, &expiresAt); err != nil {
		return "", fmt.Errorf("record wiki receive-pack repair obligation for %s: %w", repoFullName, err)
	}
	return ownerToken, nil
}

// RefreshWikiReceivePackRepairObligationOwner extends the active receive-pack
// owner lease while git-http-backend is still serving the request. The update
// is token-scoped so it cannot revive or steal another receive-pack owner.
func (s *Service) RefreshWikiReceivePackRepairObligationOwner(ctx context.Context, repoFullName, ownerToken string) error {
	repo, err := s.LookupRepoIdentity(ctx, repoFullName)
	if err != nil {
		return err
	}
	expiresAt := time.Now().UTC().Add(wikiReceivePackRepairObligationOwnerTTL)
	if err := s.refreshWikiGitRepairObligationOwner(ctx, repo.ID, ownerToken, expiresAt); err != nil {
		return fmt.Errorf("refresh wiki receive-pack repair obligation for %s: %w", repoFullName, err)
	}
	return nil
}

// ClearWikiReceivePackRepairObligationLocked removes a receive-pack repair
// obligation when the backend made no ref change or when ingest completed. The
// caller must already own the wiki catalog serialization lock and the backing
// Git repository lock.
func (s *Service) ClearWikiReceivePackRepairObligationLocked(ctx context.Context, repoFullName, ownerToken string) error {
	repo, err := s.LookupRepoIdentity(ctx, repoFullName)
	if err != nil {
		return err
	}
	if err := s.clearWikiGitRepairObligationForOwner(ctx, repo.ID, ownerToken); err != nil {
		return fmt.Errorf("clear wiki receive-pack repair obligation for %s: %w", repoFullName, err)
	}
	return nil
}

func (s *Service) refreshWikiGitRepairObligationOwner(ctx context.Context, repoID uint, ownerToken string, expiresAt time.Time) error {
	ownerToken = strings.TrimSpace(ownerToken)
	if ownerToken == "" {
		return errors.New("wiki receive-pack repair owner token is empty")
	}
	now := time.Now().UTC()
	result := s.DBForCtx(ctx).
		Model(&db.WikiGitRepairObligation{}).
		Where("repository_id = ? AND owner_token = ? AND in_progress = ?", repoID, ownerToken, true).
		Updates(map[string]any{
			"owner_expires_at": expiresAt.UTC(),
			"updated_at":       now,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 0 {
		return nil
	}
	obligation, exists, err := s.loadWikiGitRepairObligation(ctx, repoID)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	if strings.TrimSpace(obligation.OwnerToken) == ownerToken && !obligation.InProgress {
		return nil
	}
	return fmt.Errorf("wiki receive-pack repair obligation is owned by a different token")
}

func (s *Service) loadWikiGitRepairObligation(ctx context.Context, repoID uint) (db.WikiGitRepairObligation, bool, error) {
	var obligation db.WikiGitRepairObligation
	err := s.DBForCtx(ctx).First(&obligation, "repository_id = ?", repoID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return db.WikiGitRepairObligation{}, false, nil
	}
	if err != nil {
		return db.WikiGitRepairObligation{}, false, err
	}
	return obligation, true, nil
}

func (s *Service) clearWikiGitRepairObligationForOwner(ctx context.Context, repoID uint, ownerToken string) error {
	ownerToken = strings.TrimSpace(ownerToken)
	if ownerToken == "" {
		return s.clearWikiGitRepairObligationIfUnownedOrExpired(ctx, repoID)
	}
	result := s.DBForCtx(ctx).
		Where("repository_id = ? AND owner_token = ?", repoID, ownerToken).
		Delete(&db.WikiGitRepairObligation{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 0 {
		return nil
	}
	_, exists, err := s.loadWikiGitRepairObligation(ctx, repoID)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	return fmt.Errorf("wiki receive-pack repair obligation is owned by a different token")
}

func (s *Service) clearWikiGitRepairObligationIfUnownedOrExpired(ctx context.Context, repoID uint) error {
	obligation, exists, err := s.loadWikiGitRepairObligation(ctx, repoID)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	if wikiGitRepairObligationOwnerActive(obligation, time.Now().UTC()) {
		return fmt.Errorf("wiki receive-pack repair obligation is still owned by an active receive-pack")
	}
	cleared, err := s.clearLoadedWikiGitRepairObligation(ctx, obligation)
	if err != nil {
		return err
	}
	if cleared {
		return nil
	}
	return s.rejectChangedWikiGitRepairObligation(ctx, db.Repository{ID: repoID}, "clear wiki Git repair obligation")
}

func (s *Service) clearConsumedWikiGitRepairObligation(
	ctx context.Context,
	repo db.Repository,
	obligation db.WikiGitRepairObligation,
	operation string,
) error {
	cleared, err := s.clearLoadedWikiGitRepairObligation(ctx, obligation)
	if err != nil {
		return fmt.Errorf("%s for %s: %w", operation, repo.FullName, err)
	}
	if cleared {
		return nil
	}
	return s.rejectChangedWikiGitRepairObligation(ctx, repo, operation)
}

func (s *Service) clearLoadedWikiGitRepairObligation(ctx context.Context, obligation db.WikiGitRepairObligation) (bool, error) {
	query := s.DBForCtx(ctx).
		Where("repository_id = ?", obligation.RepositoryID).
		Where("head_sha = ?", obligation.HeadSHA).
		Where("branch_missing = ?", obligation.BranchMissing).
		Where("in_progress = ?", obligation.InProgress).
		Where("owner_token = ?", obligation.OwnerToken).
		Where("created_at = ?", obligation.CreatedAt).
		Where("updated_at = ?", obligation.UpdatedAt)
	if obligation.OwnerExpiresAt == nil {
		query = query.Where("owner_expires_at IS NULL")
	} else {
		query = query.Where("owner_expires_at = ?", *obligation.OwnerExpiresAt)
	}
	result := query.Delete(&db.WikiGitRepairObligation{})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected != 0, nil
}

func (s *Service) rejectChangedWikiGitRepairObligation(ctx context.Context, repo db.Repository, operation string) error {
	obligation, exists, err := s.loadWikiGitRepairObligation(ctx, repo.ID)
	if err != nil {
		return fmt.Errorf("%s: reload changed wiki Git repair obligation: %w", operation, err)
	}
	if !exists {
		return nil
	}
	if wikiGitRepairObligationOwnerActive(obligation, time.Now().UTC()) {
		repoName := repo.FullName
		if repoName == "" {
			repoName = fmt.Sprintf("repository %d", repo.ID)
		}
		return fmt.Errorf(
			"wiki receive-pack repair obligation for %s changed to an active receive-pack owner until %s",
			repoName,
			obligation.OwnerExpiresAt.UTC().Format(time.RFC3339),
		)
	}
	return fmt.Errorf("%s: wiki Git repair obligation changed while it was being consumed", operation)
}

func (s *Service) rejectActiveWikiGitRepairObligationOwner(
	ctx context.Context,
	repo db.Repository,
	ownerToken string,
) error {
	obligation, exists, err := s.loadWikiGitRepairObligation(ctx, repo.ID)
	if err != nil {
		return err
	}
	if !exists || !wikiGitRepairObligationOwnerActive(obligation, time.Now().UTC()) {
		return nil
	}
	if strings.TrimSpace(ownerToken) == strings.TrimSpace(obligation.OwnerToken) {
		return nil
	}
	return fmt.Errorf(
		"wiki receive-pack repair obligation for %s is still owned by an active receive-pack until %s",
		repo.FullName,
		obligation.OwnerExpiresAt.UTC().Format(time.RFC3339),
	)
}

func (s *Service) consumeWikiGitRepairObligationLocked(ctx context.Context, repo db.Repository) (bool, error) {
	obligation, exists, err := s.loadWikiGitRepairObligation(ctx, repo.ID)
	if err != nil {
		return false, fmt.Errorf("load wiki Git repair obligation for %s: %w", repo.FullName, err)
	}
	if !exists {
		return false, nil
	}
	if s.testWikiGitRepairObligationLoaded != nil {
		s.testWikiGitRepairObligationLoaded(repo.FullName, obligation)
	}

	if wikiGitRepairObligationOwnerActive(obligation, time.Now().UTC()) {
		return false, fmt.Errorf(
			"wiki receive-pack repair obligation for %s is still owned by an active receive-pack until %s",
			repo.FullName,
			obligation.OwnerExpiresAt.UTC().Format(time.RFC3339),
		)
	}

	if obligation.InProgress {
		unchanged, err := s.wikiGitRepairObligationMatchesCurrentHead(ctx, repo, obligation)
		if err != nil {
			return false, err
		}
		if unchanged {
			if err := s.clearConsumedWikiGitRepairObligation(ctx, repo, obligation, "clear unchanged wiki Git repair obligation"); err != nil {
				return false, err
			}
			return true, nil
		}
	}

	full := wikiRepoFullName(repo.FullName)
	if _, err := s.Git.HeadSHA(ctx, full, wikiDefaultBranch); err != nil {
		if !errors.Is(err, plumbing.ErrReferenceNotFound) {
			return false, fmt.Errorf("read wiki Git head for repair obligation on %s: %w", repo.FullName, err)
		}
		if err := s.resetWikiCatalogRepo(ctx, repo.ID); err != nil {
			return false, fmt.Errorf("reset wiki catalog after authoritative branch deletion for %s: %w", repo.FullName, err)
		}
		if err := s.pruneWikiPageLabelsForMissingPages(ctx, repo.ID); err != nil {
			return false, fmt.Errorf("prune wiki labels after authoritative branch deletion for %s: %w", repo.FullName, err)
		}
	} else if _, err := s.ingestOneWikiGitLockedWithMissingBranchPolicy(
		ctx,
		repo,
		WikiGitIngestOptions{},
		true,
	); err != nil {
		return false, fmt.Errorf("ingest authoritative wiki Git repair for %s: %w", repo.FullName, err)
	}

	if err := s.clearConsumedWikiGitRepairObligation(ctx, repo, obligation, "clear wiki Git repair obligation"); err != nil {
		return false, err
	}
	return true, nil
}

func wikiGitRepairObligationOwnerActive(obligation db.WikiGitRepairObligation, now time.Time) bool {
	if !obligation.InProgress || strings.TrimSpace(obligation.OwnerToken) == "" || obligation.OwnerExpiresAt == nil {
		return false
	}
	return obligation.OwnerExpiresAt.After(now)
}

func newWikiReceivePackRepairOwnerToken() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func (s *Service) wikiGitRepairObligationMatchesCurrentHead(
	ctx context.Context,
	repo db.Repository,
	obligation db.WikiGitRepairObligation,
) (bool, error) {
	full := wikiRepoFullName(repo.FullName)
	headSHA, err := s.Git.HeadSHA(ctx, full, wikiDefaultBranch)
	if err != nil {
		if errors.Is(err, plumbing.ErrReferenceNotFound) {
			return obligation.BranchMissing, nil
		}
		return false, fmt.Errorf("read wiki Git head for repair obligation on %s: %w", repo.FullName, err)
	}
	if obligation.BranchMissing {
		return false, nil
	}
	return strings.EqualFold(
		strings.TrimSpace(obligation.HeadSHA),
		strings.TrimSpace(headSHA),
	), nil
}
