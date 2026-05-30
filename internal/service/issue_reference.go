package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	issueReferenceSourceIssueBody       = "issue_body"
	issueReferenceSourcePullRequestBody = "pull_request_body"
	issueReferenceSourceIssueComment    = "issue_comment"
	issueReferenceSourceWikiPage        = "wiki_page"
)

type issueReferenceSource struct {
	SourceType               string
	SourceRepositoryID       uint
	SourceRepositoryFullName string
	SourceIssueNumber        *int
	SourcePRNumber           *int
	SourceCommentID          *uint
	SourceWikiSlug           *string
	Body                     string
	CreatedAt                time.Time
}

func (s *Service) syncIssueBodyReferences(ctx context.Context, issue db.Issue) error {
	repoFullName := issue.Repository.FullName
	if repoFullName == "" {
		var rep db.Repository
		if err := s.DBForCtx(ctx).Select("id", "full_name").First(&rep, issue.RepositoryID).Error; err != nil {
			return err
		}
		repoFullName = rep.FullName
	}
	number := issue.Number
	return s.syncIssueReferencesForSource(ctx, issueReferenceSource{
		SourceType:               issueReferenceSourceIssueBody,
		SourceRepositoryID:       issue.RepositoryID,
		SourceRepositoryFullName: repoFullName,
		SourceIssueNumber:        &number,
		Body:                     string(issue.Body),
		CreatedAt:                issueReferenceEventTime(issue.CreatedAt, issue.UpdatedAt),
	})
}

func (s *Service) syncPullRequestBodyReferences(ctx context.Context, pr db.PullRequest) error {
	repoFullName := pr.Repository.FullName
	if repoFullName == "" {
		var rep db.Repository
		if err := s.DBForCtx(ctx).Select("id", "full_name").First(&rep, pr.RepositoryID).Error; err != nil {
			return err
		}
		repoFullName = rep.FullName
	}
	number := pr.Number
	return s.syncIssueReferencesForSource(ctx, issueReferenceSource{
		SourceType:               issueReferenceSourcePullRequestBody,
		SourceRepositoryID:       pr.RepositoryID,
		SourceRepositoryFullName: repoFullName,
		SourcePRNumber:           &number,
		Body:                     string(pr.Body),
		CreatedAt:                issueReferenceEventTime(pr.CreatedAt, pr.UpdatedAt),
	})
}

func (s *Service) syncIssueCommentReferences(ctx context.Context, comment db.IssueComment) error {
	repoFullName := comment.Repository.FullName
	if repoFullName == "" {
		var rep db.Repository
		if err := s.DBForCtx(ctx).Select("id", "full_name").First(&rep, comment.RepositoryID).Error; err != nil {
			return err
		}
		repoFullName = rep.FullName
	}
	source := issueReferenceSource{
		SourceType:               issueReferenceSourceIssueComment,
		SourceRepositoryID:       comment.RepositoryID,
		SourceRepositoryFullName: repoFullName,
		SourceCommentID:          &comment.ID,
		Body:                     string(comment.Body),
		CreatedAt:                issueReferenceEventTime(comment.CreatedAt, comment.UpdatedAt),
	}
	ownerKind, err := s.issueReferenceOwnerKind(ctx, comment.RepositoryID, comment.IssueNumber)
	if err != nil {
		return err
	}
	switch ownerKind {
	case issueReferenceSourceIssueBody:
		number := comment.IssueNumber
		source.SourceIssueNumber = &number
	case issueReferenceSourcePullRequestBody:
		number := comment.IssueNumber
		source.SourcePRNumber = &number
	default:
		return s.deleteIssueReferencesForSource(ctx, source).Error
	}
	return s.syncIssueReferencesForSource(ctx, source)
}

func (s *Service) syncWikiPageReferences(ctx context.Context, repo db.Repository, slug, body string, createdAt time.Time) error {
	if repo.ID == 0 || repo.FullName == "" || slug == "" {
		return nil
	}
	sourceSlug := slug
	err := s.syncIssueReferencesForSource(ctx, issueReferenceSource{
		SourceType:               issueReferenceSourceWikiPage,
		SourceRepositoryID:       repo.ID,
		SourceRepositoryFullName: repo.FullName,
		SourceWikiSlug:           &sourceSlug,
		Body:                     body,
		CreatedAt:                createdAt,
	})
	if isMissingTableErr(err) {
		return nil
	}
	return err
}

func (s *Service) issueReferenceOwnerKind(ctx context.Context, repoID uint, number int) (string, error) {
	var count int64
	if err := s.DBForCtx(ctx).Model(&db.Issue{}).
		Where("repository_id = ? AND number = ?", repoID, number).
		Count(&count).Error; err != nil {
		return "", err
	}
	if count > 0 {
		return issueReferenceSourceIssueBody, nil
	}
	if err := s.DBForCtx(ctx).Model(&db.PullRequest{}).
		Where("repository_id = ? AND number = ?", repoID, number).
		Count(&count).Error; err != nil {
		return "", err
	}
	if count > 0 {
		return issueReferenceSourcePullRequestBody, nil
	}
	return "", nil
}

func (s *Service) syncIssueReferencesForSource(ctx context.Context, source issueReferenceSource) error {
	if source.SourceRepositoryID == 0 || source.SourceRepositoryFullName == "" {
		return nil
	}
	matches := ParseIssueReferences(source.Body, source.SourceRepositoryFullName)
	refs, err := s.resolveIssueReferenceMatches(ctx, source, matches)
	if err != nil {
		return err
	}
	return s.DBForCtx(ctx).Transaction(func(tx *gorm.DB) error {
		txCtx := ContextWithDB(ctx, tx)
		if err := s.deleteIssueReferencesForSource(txCtx, source).Error; err != nil {
			return err
		}
		if len(refs) == 0 {
			return nil
		}
		for i := range refs {
			if !source.CreatedAt.IsZero() {
				refs[i].CreatedAt = source.CreatedAt
			}
		}
		return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&refs).Error
	})
}

func (s *Service) resolveIssueReferenceMatches(ctx context.Context, source issueReferenceSource, matches []IssueReferenceMatch) ([]db.IssueReference, error) {
	if len(matches) == 0 {
		return nil, nil
	}
	refs := make([]db.IssueReference, 0, len(matches))
	seen := make(map[string]struct{}, len(matches))
	for _, match := range matches {
		targetRepo, err := s.lookupIssueReferenceTargetRepo(ctx, match.RepositoryFullName)
		if err != nil {
			if errors.Is(err, ErrNotFound) || errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			return nil, err
		}
		if !s.issueReferenceTargetExists(ctx, targetRepo.ID, match.Number) {
			continue
		}
		if source.SourceRepositoryID == targetRepo.ID {
			if source.SourceIssueNumber != nil && *source.SourceIssueNumber == match.Number {
				continue
			}
			if source.SourcePRNumber != nil && *source.SourcePRNumber == match.Number {
				continue
			}
		}
		key := fmt.Sprintf("%d#%d", targetRepo.ID, match.Number)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		refs = append(refs, db.IssueReference{
			SourceType:         source.SourceType,
			SourceRepositoryID: source.SourceRepositoryID,
			SourceIssueNumber:  source.SourceIssueNumber,
			SourcePRNumber:     source.SourcePRNumber,
			SourceCommentID:    source.SourceCommentID,
			SourceWikiSlug:     source.SourceWikiSlug,
			TargetRepositoryID: targetRepo.ID,
			TargetNumber:       match.Number,
			RawReference:       match.RawReference,
		})
	}
	return refs, nil
}

func (s *Service) lookupIssueReferenceTargetRepo(ctx context.Context, fullName string) (db.Repository, error) {
	return s.lookupRepo(ctx, fullName, func() *gorm.DB {
		return s.DBForCtx(ctx).Select("id", "full_name", "owner_id", "private", "visibility")
	})
}

func (s *Service) issueReferenceTargetExists(ctx context.Context, repoID uint, number int) bool {
	var count int64
	if err := s.DBForCtx(ctx).Model(&db.Issue{}).
		Where("repository_id = ? AND number = ?", repoID, number).
		Count(&count).Error; err != nil {
		slog.WarnContext(ctx, "issue reference target issue lookup failed", "repo_id", repoID, "number", number, "error", err)
		return false
	}
	if count > 0 {
		return true
	}
	if err := s.DBForCtx(ctx).Model(&db.PullRequest{}).
		Where("repository_id = ? AND number = ?", repoID, number).
		Count(&count).Error; err != nil {
		slog.WarnContext(ctx, "issue reference target pull request lookup failed", "repo_id", repoID, "number", number, "error", err)
		return false
	}
	return count > 0
}

func (s *Service) deleteIssueReferencesForSource(ctx context.Context, source issueReferenceSource) *gorm.DB {
	q := s.DBForCtx(ctx).Where("source_type = ? AND source_repository_id = ?", source.SourceType, source.SourceRepositoryID)
	if source.SourceIssueNumber != nil {
		q = q.Where("source_issue_number = ?", *source.SourceIssueNumber)
	} else {
		q = q.Where("source_issue_number IS NULL")
	}
	if source.SourcePRNumber != nil {
		q = q.Where("source_pr_number = ?", *source.SourcePRNumber)
	} else {
		q = q.Where("source_pr_number IS NULL")
	}
	if source.SourceCommentID != nil {
		q = q.Where("source_comment_id = ?", *source.SourceCommentID)
	} else {
		q = q.Where("source_comment_id IS NULL")
	}
	if source.SourceWikiSlug != nil {
		q = q.Where("source_wiki_slug = ?", *source.SourceWikiSlug)
	} else {
		q = q.Where("source_wiki_slug IS NULL")
	}
	return q.Delete(&db.IssueReference{})
}

func (s *Service) deleteIssueReferencesForWikiPage(ctx context.Context, repoID uint, slug string) error {
	err := s.deleteIssueReferencesForSource(ctx, issueReferenceSource{
		SourceType:         issueReferenceSourceWikiPage,
		SourceRepositoryID: repoID,
		SourceWikiSlug:     &slug,
	}).Error
	if isMissingTableErr(err) {
		return nil
	}
	return err
}

func (s *Service) deleteIssueReferencesForIssueNumber(ctx context.Context, repoID uint, number int) error {
	return s.DBForCtx(ctx).Where(
		"(source_repository_id = ? AND (source_issue_number = ? OR source_pr_number = ?)) OR (target_repository_id = ? AND target_number = ?)",
		repoID, number, number, repoID, number,
	).Delete(&db.IssueReference{}).Error
}

// BackfillIssueReferences rebuilds cross-reference edges from all existing
// issue, pull request, and issue comment bodies.
func (s *Service) BackfillIssueReferences(ctx context.Context) error {
	return s.DBForCtx(ctx).Transaction(func(tx *gorm.DB) error {
		txCtx := ContextWithDB(ctx, tx)
		if err := tx.Where("1 = 1").Delete(&db.IssueReference{}).Error; err != nil {
			return err
		}

		var issues []db.Issue
		if err := preloadIssue(tx).Find(&issues).Error; err != nil {
			return err
		}
		for _, issue := range issues {
			if err := s.syncIssueBodyReferences(txCtx, issue); err != nil {
				return err
			}
		}

		var prs []db.PullRequest
		if err := preloadPRFull(tx).Find(&prs).Error; err != nil {
			return err
		}
		for _, pr := range prs {
			if err := s.syncPullRequestBodyReferences(txCtx, pr); err != nil {
				return err
			}
		}

		var comments []db.IssueComment
		if err := preloadIssueComment(tx).Find(&comments).Error; err != nil {
			return err
		}
		for _, comment := range comments {
			if err := s.syncIssueCommentReferences(txCtx, comment); err != nil {
				return err
			}
		}

		var pages []db.WikiPage
		if err := tx.Preload("Repository").
			Where("deleted_at IS NULL").
			Find(&pages).Error; err != nil {
			return err
		}
		for _, page := range pages {
			body, err := s.wikiPageBody(txCtx, page)
			if err != nil {
				return err
			}
			if err := s.syncWikiPageReferences(txCtx, page.Repository, page.Slug, string(body), issueReferenceEventTime(page.CreatedAt, page.UpdatedAt)); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Service) viewerCanReadReferencedSource(ctx context.Context, repoID uint) bool {
	viewer, ok := UserFromContext(ctx)
	if !ok || viewer.ID == 0 {
		if IsAnonRequest(ctx) {
			return s.isPublicRepo(ctx, repoID)
		}
		return true
	}
	perm, err := s.HasRepoAccess(ctx, repoID, viewer.ID)
	if err != nil {
		slog.WarnContext(ctx, "issue reference source permission lookup failed", "repo_id", repoID, "viewer_id", viewer.ID, "error", err)
		return false
	}
	return perm.AtLeast(RepoPermissionRead) || s.isPublicRepo(ctx, repoID)
}

func issueReferenceBodyUpdate(v any, old db.LargeText) (string, bool) {
	switch body := v.(type) {
	case string:
		return body, db.LargeText(body) != old
	case db.LargeText:
		return string(body), body != old
	case *string:
		if body == nil {
			return "", false
		}
		return *body, db.LargeText(*body) != old
	case *db.LargeText:
		if body == nil {
			return "", false
		}
		return string(*body), *body != old
	default:
		return "", false
	}
}

func issueReferenceEventTime(createdAt, updatedAt time.Time) time.Time {
	if !updatedAt.IsZero() {
		return updatedAt
	}
	return createdAt
}
