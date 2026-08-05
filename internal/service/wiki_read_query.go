package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"github.com/ngaut/agent-git-service/internal/db"
)

type wikiPageListSource uint8

const (
	wikiPageListSourceCatalog wikiPageListSource = iota + 1
	wikiPageListSourceV2
)

// ListWikiPagesPaginated returns a stable slug-ordered page of wiki summaries
// plus the total number of rows matching opts. A non-positive limit preserves
// ListWikiPages' unbounded behavior for aggregate callers.
func (s *Service) ListWikiPagesPaginated(ctx context.Context, repoFullName string, opts ListWikiPagesOptions, offset, limit int) ([]WikiPageSummary, int, error) {
	pages, total, _, err := s.listWikiPagesPaginated(ctx, repoFullName, opts, offset, limit)
	return pages, total, err
}

// ListWikiPagesForCatalog returns the unbounded aggregate page set and whether
// it can safely synthesize the live tree without changing projection fallback.
func (s *Service) ListWikiPagesForCatalog(ctx context.Context, repoFullName string, opts ListWikiPagesOptions) ([]WikiPageSummary, bool, error) {
	pages, _, source, err := s.listWikiPagesPaginated(ctx, repoFullName, opts, 0, 0)
	return pages, source == wikiPageListSourceCatalog && len(pages) > 0, err
}

func (s *Service) listWikiPagesPaginated(ctx context.Context, repoFullName string, opts ListWikiPagesOptions, offset, limit int) ([]WikiPageSummary, int, wikiPageListSource, error) {
	rep, err := s.getRepoBase(ctx, repoFullName)
	if err != nil {
		return nil, 0, 0, err
	}
	if opts.Path != "" {
		if err := validateWikiSlug(opts.Path); err != nil {
			return nil, 0, 0, err
		}
	}
	if offset < 0 {
		offset = 0
	}

	headSHA, v2Current, err := s.loadCurrentWikiV2HeadSHA(ctx, repoFullName, rep.ID)
	if err != nil {
		return nil, 0, 0, err
	}
	if v2Current {
		pages, total, err := s.listWikiPagesFromV2Query(ctx, rep.ID, headSHA, opts, offset, limit)
		return pages, total, wikiPageListSourceV2, err
	}
	if s.WikiCatalog == nil {
		return nil, 0, 0, errors.New("wiki catalog unavailable")
	}
	if err := s.ensureWikiCatalogCurrent(ctx, repoFullName); err != nil {
		return nil, 0, 0, err
	}
	pages, total, err := s.listWikiPagesFromCatalogQuery(ctx, rep.ID, opts, offset, limit)
	return pages, total, wikiPageListSourceCatalog, err
}

// WikiNavigationVersion returns the live snapshot identity used by tree and
// tree-only catalog responses. Catalog state wins because REST writes can be
// durable before their asynchronous Git ref publication completes.
func (s *Service) WikiNavigationVersion(ctx context.Context, repoFullName, dirPath string) (string, error) {
	dirPath = strings.Trim(strings.TrimSpace(dirPath), "/")
	if dirPath != "" {
		if err := validateWikiSlug(dirPath); err != nil {
			return "", err
		}
	}
	rep, err := s.getRepoBase(ctx, repoFullName)
	if err != nil {
		return "", err
	}
	if s.WikiCatalog != nil {
		if err := s.ensureWikiCatalogCurrent(ctx, repoFullName); err != nil {
			return "", err
		}
		var row struct {
			SynthCommitSHA string
		}
		err := s.DBForCtx(ctx).
			Table("wiki_repo_heads AS heads").
			Select("changesets.synth_commit_sha").
			Joins("JOIN wiki_changesets AS changesets ON changesets.changeset_id = heads.head_changeset_id").
			Where("heads.repository_id = ?", rep.ID).
			Take(&row).Error
		if err == nil && strings.TrimSpace(row.SynthCommitSHA) != "" {
			return "catalog:" + strings.ToLower(strings.TrimSpace(row.SynthCommitSHA)), nil
		}
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) && !isMissingTableErr(err) {
			return "", err
		}
	}
	if headSHA, ok, err := s.loadCurrentWikiV2HeadSHA(ctx, repoFullName, rep.ID); err != nil {
		return "", err
	} else if ok {
		return "v2:" + strings.ToLower(strings.TrimSpace(headSHA)), nil
	}
	if s.Git != nil {
		full := wikiRepoFullName(repoFullName)
		if s.Git.Exists(ctx, full) && !s.Git.IsEmpty(ctx, full) {
			headSHA, err := s.Git.ResolveContentCommit(ctx, full, wikiDefaultBranch)
			if err != nil {
				return "", err
			}
			return "git:" + strings.ToLower(strings.TrimSpace(headSHA)), nil
		}
	}
	return "empty", nil
}

func (s *Service) listWikiPagesFromCatalogQuery(ctx context.Context, repoID uint, opts ListWikiPagesOptions, offset, limit int) ([]WikiPageSummary, int, error) {
	query := s.DBForCtx(ctx).
		Model(&db.WikiPage{}).
		Where("wiki_pages.repository_id = ? AND wiki_pages.deleted_at IS NULL", repoID)
	query = applyWikiPathFilterQuery(query, "wiki_pages.slug", opts.Path, opts.Recursive)

	var noResults bool
	var err error
	query, noResults, err = s.applyWikiLabelFilterQuery(ctx, query, repoID, "wiki_pages.slug", opts)
	if err != nil {
		return nil, 0, err
	}
	if noResults {
		return []WikiPageSummary{}, 0, nil
	}

	var total int64
	if limit > 0 {
		if err := query.Count(&total).Error; err != nil {
			return nil, 0, fmt.Errorf("count wiki pages: %w", err)
		}
	}

	var pages []db.WikiPage
	pageQuery := query.
		Select("wiki_pages.page_id", "wiki_pages.repository_id", "wiki_pages.slug", "wiki_pages.head_blob_sha", "wiki_pages.body_size", "wiki_pages.last_author_id", "wiki_pages.updated_at").
		Preload("LastAuthor").
		Order("wiki_pages.slug ASC")
	if limit > 0 {
		pageQuery = pageQuery.Offset(offset).Limit(limit)
	}
	if err := pageQuery.Find(&pages).Error; err != nil {
		return nil, 0, fmt.Errorf("list wiki pages: %w", err)
	}
	if limit <= 0 {
		total = int64(len(pages))
	}

	slugs := make([]string, 0, len(pages))
	for _, page := range pages {
		slugs = append(slugs, page.Slug)
	}
	labelsBySlug, err := s.wikiLabelsForSlugs(ctx, repoID, slugs)
	if err != nil {
		return nil, 0, err
	}

	out := make([]WikiPageSummary, 0, len(pages))
	for _, page := range pages {
		out = append(out, WikiPageSummary{
			Slug:       page.Slug,
			Title:      titleFromSlug(page.Slug),
			SHA:        page.HeadBlobSHA,
			Size:       int64(page.BodySize),
			UpdatedAt:  page.UpdatedAt,
			LastAuthor: page.LastAuthor,
			Labels:     labelsBySlug[page.Slug],
		})
	}
	return out, int(total), nil
}

func (s *Service) listWikiPagesFromV2Query(ctx context.Context, repoID uint, headSHA string, opts ListWikiPagesOptions, offset, limit int) ([]WikiPageSummary, int, error) {
	query := s.DBForCtx(ctx).
		Model(&db.WikiPageIndex{}).
		Where("wiki_page_index.repository_id = ? AND LOWER(wiki_page_index.head_commit_sha) = LOWER(?)", repoID, headSHA)
	query = applyWikiPathFilterQuery(query, "wiki_page_index.slug", opts.Path, opts.Recursive)

	var noResults bool
	var err error
	query, noResults, err = s.applyWikiLabelFilterQuery(ctx, query, repoID, "wiki_page_index.slug", opts)
	if err != nil {
		return nil, 0, err
	}
	if noResults {
		return []WikiPageSummary{}, 0, nil
	}

	var total int64
	if limit > 0 {
		if err := query.Count(&total).Error; err != nil {
			return nil, 0, fmt.Errorf("count wiki v2 pages: %w", err)
		}
	}

	var rows []db.WikiPageIndex
	pageQuery := query.
		Select("wiki_page_index.repository_id", "wiki_page_index.slug", "wiki_page_index.head_blob_sha", "wiki_page_index.title", "wiki_page_index.size", "wiki_page_index.updated_at", "wiki_page_index.last_author_id").
		Preload("LastAuthor").
		Order("wiki_page_index.slug ASC")
	if limit > 0 {
		pageQuery = pageQuery.Offset(offset).Limit(limit)
	}
	if err := pageQuery.Find(&rows).Error; err != nil {
		return nil, 0, fmt.Errorf("list wiki v2 pages: %w", err)
	}
	if limit <= 0 {
		total = int64(len(rows))
	}

	slugs := make([]string, 0, len(rows))
	for _, row := range rows {
		slugs = append(slugs, row.Slug)
	}
	labelsBySlug, err := s.wikiLabelsForSlugs(ctx, repoID, slugs)
	if err != nil {
		return nil, 0, err
	}

	out := make([]WikiPageSummary, 0, len(rows))
	for _, row := range rows {
		out = append(out, WikiPageSummary{
			Slug:       row.Slug,
			Title:      row.Title,
			SHA:        row.HeadBlobSHA,
			Size:       int64(row.Size),
			UpdatedAt:  row.UpdatedAt,
			LastAuthor: row.LastAuthor,
			Labels:     labelsBySlug[row.Slug],
		})
	}
	return out, int(total), nil
}

func applyWikiPathFilterQuery(query *gorm.DB, slugColumn, path string, recursive bool) *gorm.DB {
	if path == "" {
		if recursive {
			return query
		}
		return query.Where("LENGTH(" + slugColumn + ") - LENGTH(REPLACE(" + slugColumn + ", '/', '')) = 0")
	}

	lower := path + "/"
	upper := path + "0"
	query = query.Where(slugColumn+" >= ? AND "+slugColumn+" < ?", lower, upper)
	if recursive {
		return query
	}
	depth := strings.Count(path, "/") + 1
	return query.Where("LENGTH("+slugColumn+") - LENGTH(REPLACE("+slugColumn+", '/', '')) = ?", depth)
}

func (s *Service) applyWikiLabelFilterQuery(ctx context.Context, query *gorm.DB, repoID uint, slugColumn string, opts ListWikiPagesOptions) (*gorm.DB, bool, error) {
	for _, labelName := range uniqueLabelNames(opts.Labels) {
		label, err := s.repoLabelByName(ctx, repoID, labelName)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				return query, true, nil
			}
			return nil, false, err
		}
		query = query.Where(
			"EXISTS (SELECT 1 FROM wiki_page_labels AS required_wiki_labels WHERE required_wiki_labels.repository_id = ? AND required_wiki_labels.slug = "+slugColumn+" AND required_wiki_labels.label_id = ?)",
			repoID,
			label.ID,
		)
	}

	excluded, err := s.resolveRepoLabels(ctx, repoID, opts.ExcludeLabels)
	if err != nil {
		return nil, false, err
	}
	if len(excluded) == 0 {
		return query, false, nil
	}
	ids := make([]uint, 0, len(excluded))
	for _, label := range excluded {
		ids = append(ids, label.ID)
	}
	query = query.Where(
		"NOT EXISTS (SELECT 1 FROM wiki_page_labels AS excluded_wiki_labels WHERE excluded_wiki_labels.repository_id = ? AND excluded_wiki_labels.slug = "+slugColumn+" AND excluded_wiki_labels.label_id IN ?)",
		repoID,
		ids,
	)
	return query, false, nil
}
