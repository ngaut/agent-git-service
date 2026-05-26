package service

// Catalog-backed helpers for the wiki REST surface.
//
// These functions exist alongside the legacy git-walk helpers in
// wiki.go during the cutover. Once every REST entry point uses these
// helpers, the legacy paths get deleted in a follow-up cleanup pass.

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/wikicatalog"
)

// wikiPageBody reads a page's body. Returns the inline copy when the
// page row carries one (≤ MaxBodyInlineBytes) and falls back to the
// content-addressed blob store otherwise. The caller owns the
// returned slice — the catalog does not mutate it.
func (s *Service) wikiPageBody(ctx context.Context, page db.WikiPage) ([]byte, error) {
	if len(page.BodyInline) > 0 {
		return page.BodyInline, nil
	}
	if page.HeadBlobSHA == "" {
		return nil, nil
	}
	if s.WikiBlob == nil {
		return nil, errors.New("wiki blob store unavailable")
	}
	return s.WikiBlob.Get(ctx, page.HeadBlobSHA)
}

// loadLiveWikiPage fetches a single live (non-deleted) catalog page by
// canonical slug, preloading LastAuthor for response shaping.
// Returns ErrNotFound translated for the service boundary.
func (s *Service) loadLiveWikiPage(ctx context.Context, repoID uint, slug string) (db.WikiPage, error) {
	ci, err := wikicatalog.CanonicalV1(slug)
	if err != nil {
		return db.WikiPage{}, ErrNotFound
	}
	var page db.WikiPage
	err = s.DBForCtx(ctx).
		Preload("LastAuthor").
		Where("repository_id = ? AND slug_ci_v1 = ? AND deleted_at IS NULL", repoID, ci).
		Take(&page).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return db.WikiPage{}, ErrNotFound
	}
	return page, err
}

// translateCatalogError maps wikicatalog typed errors back onto the
// legacy service-boundary error types so REST handlers and tests can
// stay unchanged through the cutover.
//
// fromMove distinguishes the move endpoints (which translate stale
// IfMatch into wikiMoveConflictError) from the put endpoint (which
// translates it into WikiConflictError).
func (s *Service) translateCatalogError(ctx context.Context, repoID uint, repoFullName string, err error, fromMove bool) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, wikicatalog.ErrPageNotFound) {
		return ErrNotFound
	}
	if errors.Is(err, wikicatalog.ErrCASLost) {
		// CAS loss with no ExpectedParent pin is internal-only; surface
		// generically. With an ExpectedParent set, callers translate
		// directly without going through this helper.
		return fmt.Errorf("wiki: head changed: %w", err)
	}
	var conflict *wikicatalog.ConflictError
	if errors.As(err, &conflict) {
		switch conflict.Code {
		case wikicatalog.ConflictCodeStale:
			if fromMove {
				return &wikiMoveConflictError{
					code:    wikiMoveCodeStale,
					message: fmt.Sprintf("%s: source page %q is stale", wikiMoveCodeStale, conflict.Slug),
				}
			}
			// Look up the current page so callers see the live state
			// that beat their IfMatch.
			current, lookupErr := s.loadLiveWikiPage(ctx, repoID, conflict.Slug)
			if lookupErr != nil {
				return &WikiConflictError{ExpectedSHA: conflict.ExpectedSHA, CurrentPage: nil}
			}
			body, _ := s.wikiPageBody(ctx, current)
			page := WikiPage{
				Slug:       current.Slug,
				Title:      wikicatalog.TitleFromSlug(current.Slug),
				Body:       string(body),
				SHA:        current.HeadBlobSHA,
				UpdatedAt:  current.UpdatedAt,
				LastAuthor: current.LastAuthor,
			}
			return &WikiConflictError{ExpectedSHA: conflict.ExpectedSHA, CurrentPage: &page}
		case wikicatalog.ConflictCodeDestinationTake:
			return &wikiMoveConflictError{
				code:    wikiMoveCodeDestTaken,
				message: fmt.Sprintf("%s: destination page %q already exists", wikiMoveCodeDestTaken, conflict.Destination),
			}
		case wikicatalog.ConflictCodePrefix:
			if fromMove {
				return &wikiMoveConflictError{
					code:    wikiMoveCodePrefix,
					message: fmt.Sprintf("%s: wiki slug %q conflicts with existing page %q", wikiMoveCodePrefix, conflict.Slug, conflict.CollidesWith),
				}
			}
			return fmt.Errorf("%w: wiki slug %q conflicts with existing page %q", ErrConflict, conflict.Slug, conflict.CollidesWith)
		}
	}
	return err
}

// wikiPageFromCatalog projects a catalog row plus its body and label
// set into the legacy WikiPage shape that REST handlers and tests
// already understand.
func (s *Service) wikiPageFromCatalog(page db.WikiPage, body []byte, labels []db.Label) WikiPage {
	return WikiPage{
		Slug:       page.Slug,
		Title:      wikicatalog.TitleFromSlug(page.Slug),
		Body:       string(body),
		SHA:        page.HeadBlobSHA,
		UpdatedAt:  page.UpdatedAt,
		LastAuthor: page.LastAuthor,
		Labels:     labels,
	}
}

// resolveWikiAuthor looks up the catalog-side author id for a REST
// caller. The catalog records AuthorID on each changeset; for runtime
// REST writes we resolve the authenticated user from context and
// pass the resulting id into ApplyChangeSet. When no authenticated
// user is in context we fall back to the user (if any) whose email
// matches the default git committer identity — this keeps the
// LastAuthor field populated for system-driven writes the same way
// the legacy git-walking code resolved it from commit metadata.
func (s *Service) resolveWikiAuthor(ctx context.Context) *uint {
	if user, ok := UserFromContext(ctx); ok && user.ID != 0 {
		out := user.ID
		return &out
	}
	const defaultGitEmail = "gh-server@localhost"
	var u db.User
	if err := s.DBForCtx(ctx).Select("id").
		Where("LOWER(email) = ?", defaultGitEmail).
		Take(&u).Error; err == nil && u.ID != 0 {
		out := u.ID
		return &out
	}
	return nil
}
