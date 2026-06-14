package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"
	"gorm.io/gorm"
)

// IssueListPageFilter groups DB-pageable filters for the REST issues list.
type IssueListPageFilter struct {
	RepoFullName  string
	State         string
	Labels        string
	Kind          string
	TitlePrefix   string
	Sort          string
	Direction     string
	Milestone     string
	Since         string
	Page          int
	PerPage       int
	OmitIssueBody bool
}

// IssueListPageItem is an ordered issue-or-PR row returned by ListIssuesForRESTPage.
type IssueListPageItem struct {
	Issue       *db.Issue
	PullRequest *db.PullRequest
	Comments    int64
}

// IssueListPage is one REST issues page plus the total number of matching rows.
type IssueListPage struct {
	Items []IssueListPageItem
	Total int64
}

type issueListPageRow struct {
	Kind     string
	ID       uint
	Number   int
	Comments int64
}

// ListIssuesForRESTPage returns one DB-paginated /issues page across issues and PRs.
func (s *Service) ListIssuesForRESTPage(ctx context.Context, filter IssueListPageFilter) (IssueListPage, error) {
	rep, err := s.getRepoForIssueListPage(ctx, filter.RepoFullName)
	if err != nil {
		return IssueListPage{}, err
	}
	normalized, err := normalizeIssueListPageFilter(filter)
	if err != nil {
		return IssueListPage{}, err
	}
	labelNames, labelIDsByName, noLabelResults, err := s.resolveIssueListPageLabelIDs(ctx, rep.ID, filter.Labels)
	if err != nil {
		return IssueListPage{}, err
	}
	if noLabelResults {
		return IssueListPage{}, nil
	}

	database := s.DBForCtx(ctx)
	pageSQL, pageArgs := buildIssueListPageQuery(database, rep.ID, normalized, labelNames, labelIDsByName, true, normalized.sort == "comments", normalized.perPage+1)
	var rows []issueListPageRow
	if err := database.Raw(pageSQL, pageArgs...).Scan(&rows).Error; err != nil {
		return IssueListPage{}, err
	}
	hasMore := len(rows) > normalized.perPage
	if hasMore {
		rows = rows[:normalized.perPage]
	}
	total, err := s.issueListPageTotal(ctx, rep.ID, normalized, labelNames, labelIDsByName, len(rows), hasMore)
	if err != nil {
		return IssueListPage{}, err
	}
	if total == 0 {
		return IssueListPage{}, nil
	}
	items, err := s.hydrateIssueListPageItems(ctx, rep, rows, filter.OmitIssueBody)
	if err != nil {
		return IssueListPage{}, err
	}
	return IssueListPage{Items: items, Total: total}, nil
}

func (s *Service) getRepoForIssueListPage(ctx context.Context, fullName string) (db.Repository, error) {
	if cached, ok := repoCacheGet(ctx, fullName); ok {
		return cached, nil
	}
	rep, err := s.lookupRepo(ctx, fullName, func() *gorm.DB {
		return s.DBForCtx(ctx).Preload("Owner")
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
	return rep, nil
}

func (s *Service) issueListPageTotal(
	ctx context.Context,
	repoID uint,
	filter normalizedIssueListPageFilter,
	labelNames []string,
	labelIDsByName map[string][]uint,
	rowCount int,
	hasMore bool,
) (int64, error) {
	if !hasMore {
		if rowCount == 0 && filter.page > 1 {
			return s.countIssueListPageRows(ctx, repoID, filter, labelNames, labelIDsByName)
		}
		offset := (filter.page - 1) * filter.perPage
		return int64(offset + rowCount), nil
	}
	return s.countIssueListPageRows(ctx, repoID, filter, labelNames, labelIDsByName)
}

func (s *Service) countIssueListPageRows(ctx context.Context, repoID uint, filter normalizedIssueListPageFilter, labelNames []string, labelIDsByName map[string][]uint) (int64, error) {
	database := s.DBForCtx(ctx)
	countSQL, countArgs := buildIssueListPageQuery(database, repoID, filter, labelNames, labelIDsByName, false, false, 0)
	var total int64
	if err := database.Raw(countSQL, countArgs...).Scan(&total).Error; err != nil {
		return 0, err
	}
	return total, nil
}

type normalizedIssueListPageFilter struct {
	state       string
	kind        string
	titlePrefix string
	sort        string
	direction   string
	milestone   string
	since       *time.Time
	page        int
	perPage     int
}

func normalizeIssueListPageFilter(filter IssueListPageFilter) (normalizedIssueListPageFilter, error) {
	state := strings.TrimSpace(filter.State)
	if state == "" {
		state = db.StateOpen
	}
	kind := strings.ToLower(strings.TrimSpace(filter.Kind))
	switch kind {
	case "", "all":
		kind = "all"
	case "issue", "pull":
	default:
		return normalizedIssueListPageFilter{}, fmt.Errorf("%w: kind must be one of: issue, pull, all", ErrValidation)
	}
	sortKey := strings.ToLower(strings.TrimSpace(filter.Sort))
	switch sortKey {
	case "", "created":
		sortKey = "created"
	case "updated", "comments":
	default:
		sortKey = "created"
	}
	direction := strings.ToLower(strings.TrimSpace(filter.Direction))
	if direction != "asc" && direction != "desc" {
		direction = "desc"
	}
	page := filter.Page
	if page < 1 {
		page = 1
	}
	perPage := filter.PerPage
	if perPage < 1 {
		perPage = defaultListLimit
	}
	if perPage > defaultListLimit {
		perPage = defaultListLimit
	}
	var since *time.Time
	if rawSince := strings.TrimSpace(filter.Since); rawSince != "" {
		parsed, err := time.Parse(time.RFC3339Nano, rawSince)
		if err != nil {
			return normalizedIssueListPageFilter{}, fmt.Errorf("%w: since must be ISO 8601", ErrValidation)
		}
		since = &parsed
	}
	return normalizedIssueListPageFilter{
		state:       state,
		kind:        kind,
		titlePrefix: strings.TrimSpace(filter.TitlePrefix),
		sort:        sortKey,
		direction:   direction,
		milestone:   strings.TrimSpace(filter.Milestone),
		since:       since,
		page:        page,
		perPage:     perPage,
	}, nil
}

func (s *Service) resolveIssueListPageLabelIDs(ctx context.Context, repoID uint, rawLabels string) ([]string, map[string][]uint, bool, error) {
	labelNames := splitIssueListPageLabels(rawLabels)
	if len(labelNames) == 0 {
		return nil, nil, false, nil
	}
	wanted := make(map[string]struct{}, len(labelNames))
	for _, name := range labelNames {
		wanted[name] = struct{}{}
	}
	var labels []struct {
		ID   uint
		Name string
	}
	if err := s.DBForCtx(ctx).Model(&db.Label{}).
		Select("id", "name").
		Where("repository_id = ?", repoID).
		Find(&labels).Error; err != nil {
		return nil, nil, false, err
	}
	labelIDsByName := make(map[string][]uint, len(wanted))
	for _, label := range labels {
		key := strings.ToLower(label.Name)
		if _, ok := wanted[key]; ok {
			labelIDsByName[key] = append(labelIDsByName[key], label.ID)
		}
	}
	for _, name := range labelNames {
		if len(labelIDsByName[name]) == 0 {
			return labelNames, labelIDsByName, true, nil
		}
	}
	return labelNames, labelIDsByName, false, nil
}

func splitIssueListPageLabels(raw string) []string {
	parts := strings.Split(raw, ",")
	names := make([]string, 0, len(parts))
	for _, part := range parts {
		name := strings.ToLower(strings.TrimSpace(part))
		if name != "" {
			names = append(names, name)
		}
	}
	return names
}

func buildIssueListPageQuery(database *gorm.DB, repoID uint, filter normalizedIssueListPageFilter, labelNames []string, labelIDsByName map[string][]uint, paginate bool, includeComments bool, limit int) (string, []any) {
	parts := make([]string, 0, 2)
	args := make([]any, 0)
	if filter.kind == "all" || filter.kind == "issue" {
		issueSQL, issueArgs := buildIssueListPageEntitySQL(database, "issue", "issues", repoID, filter, labelNames, labelIDsByName, includeComments)
		parts = append(parts, issueSQL)
		args = append(args, issueArgs...)
	}
	if filter.kind == "all" || filter.kind == "pull" {
		prSQL, prArgs := buildIssueListPageEntitySQL(database, "pr", "pull_requests", repoID, filter, labelNames, labelIDsByName, includeComments)
		parts = append(parts, prSQL)
		args = append(args, prArgs...)
	}
	unionSQL := strings.Join(parts, " UNION ALL ")
	if !paginate {
		return "SELECT COUNT(*) FROM (" + unionSQL + ") AS combined", args
	}
	sortColumn := "created_at"
	switch filter.sort {
	case "updated":
		sortColumn = "updated_at"
	case "comments":
		sortColumn = "comments"
	}
	direction := strings.ToUpper(filter.direction)
	offset := (filter.page - 1) * filter.perPage
	if limit < 1 {
		limit = filter.perPage
	}
	args = append(args, limit, offset)
	pageSQL := fmt.Sprintf(
		"SELECT kind, id, number, comments FROM (%s) AS combined ORDER BY %s %s, number %s LIMIT ? OFFSET ?",
		unionSQL, sortColumn, direction, direction,
	)
	if includeComments {
		return pageSQL, args
	}
	args = append([]any{repoID}, args...)
	return "SELECT kind, id, number, " +
		"(SELECT COUNT(*) FROM issue_comments ic WHERE ic.repository_id = ? AND ic.issue_number = page.number) AS comments " +
		"FROM (" + pageSQL + ") AS page", args
}

func buildIssueListPageEntitySQL(database *gorm.DB, kind, table string, repoID uint, filter normalizedIssueListPageFilter, labelNames []string, labelIDsByName map[string][]uint, includeComments bool) (string, []any) {
	where := []string{table + ".repository_id = ?"}
	args := []any{repoID}
	if table == "issues" {
		if filter.state != "all" {
			where = append(where, table+".state = ?")
			args = append(args, filter.state)
		}
	} else {
		switch filter.state {
		case db.StateClosed:
			where = append(where, "("+table+".state = ? OR "+table+".merged = ?)")
			args = append(args, db.StateClosed, true)
		case "all":
		default:
			where = append(where, table+".state = ? AND "+table+".merged = ?")
			args = append(args, db.StateOpen, false)
		}
	}
	if filter.since != nil {
		where = append(where, table+".updated_at >= ?")
		args = append(args, *filter.since)
	}
	if filter.titlePrefix != "" {
		where = append(where, "LOWER("+table+".title) LIKE ?"+sqlLikeEscapeClause(database))
		args = append(args, strings.ToLower(escapeSQLLike(filter.titlePrefix))+"%")
	}
	where, args = appendIssueListPageMilestoneWhere(where, args, table, repoID, filter.milestone)
	where, args = appendIssueListPageLabelWhere(where, args, table, labelNames, labelIDsByName)

	commentsExpr := "0"
	if includeComments {
		commentsExpr = fmt.Sprintf(
			"(SELECT COUNT(*) FROM issue_comments ic WHERE ic.repository_id = %s.repository_id AND ic.issue_number = %s.number)",
			table, table,
		)
	}
	return fmt.Sprintf(
		"SELECT '%s' AS kind, %s.id AS id, %s.number AS number, %s.created_at AS created_at, %s.updated_at AS updated_at, %s AS comments FROM %s WHERE %s",
		kind, table, table, table, table, commentsExpr, table, strings.Join(where, " AND "),
	), args
}

func escapeSQLLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}

func appendIssueListPageMilestoneWhere(where []string, args []any, table string, repoID uint, rawMilestone string) ([]string, []any) {
	milestone := strings.ToLower(strings.TrimSpace(rawMilestone))
	switch milestone {
	case "":
		return where, args
	case "*":
		return append(where, table+".milestone_id IS NOT NULL"), args
	case "none":
		return append(where, table+".milestone_id IS NULL"), args
	default:
		if num, err := strconv.Atoi(rawMilestone); err == nil {
			where = append(where, table+".milestone_id IN (SELECT id FROM milestones WHERE repository_id = ? AND (number = ? OR LOWER(title) = LOWER(?)))")
			args = append(args, repoID, num, rawMilestone)
			return where, args
		}
		where = append(where, table+".milestone_id IN (SELECT id FROM milestones WHERE repository_id = ? AND LOWER(title) = LOWER(?))")
		args = append(args, repoID, rawMilestone)
		return where, args
	}
}

func appendIssueListPageLabelWhere(where []string, args []any, table string, labelNames []string, labelIDsByName map[string][]uint) ([]string, []any) {
	if len(labelNames) == 0 {
		return where, args
	}
	labelTable := "issue_labels"
	labelFK := "issue_id"
	if table == "pull_requests" {
		labelTable = "pr_labels"
		labelFK = "pull_request_id"
	}
	for _, labelName := range labelNames {
		ids := labelIDsByName[labelName]
		placeholders := make([]string, 0, len(ids))
		for _, id := range ids {
			placeholders = append(placeholders, "?")
			args = append(args, id)
		}
		where = append(where, fmt.Sprintf(
			"EXISTS (SELECT 1 FROM %s labels_filter WHERE labels_filter.%s = %s.id AND labels_filter.label_id IN (%s))",
			labelTable, labelFK, table, strings.Join(placeholders, ","),
		))
	}
	return where, args
}

func (s *Service) hydrateIssueListPageItems(ctx context.Context, repo db.Repository, rows []issueListPageRow, omitIssueBody bool) ([]IssueListPageItem, error) {
	issueIDs := make([]uint, 0, len(rows))
	prIDs := make([]uint, 0, len(rows))
	for _, row := range rows {
		switch row.Kind {
		case "issue":
			issueIDs = append(issueIDs, row.ID)
		case "pr":
			prIDs = append(prIDs, row.ID)
		}
	}

	issuesByID := make(map[uint]db.Issue, len(issueIDs))
	if len(issueIDs) > 0 {
		var issues []db.Issue
		q := preloadIssueForRESTList(s.DBForCtx(ctx))
		if omitIssueBody {
			q = q.Omit("Body")
		}
		if err := q.Where("issues.id IN ?", issueIDs).Find(&issues).Error; err != nil {
			return nil, err
		}
		for _, issue := range issues {
			issue.Repository = repo
			issuesByID[issue.ID] = issue
		}
	}

	prsByID := make(map[uint]db.PullRequest, len(prIDs))
	if len(prIDs) > 0 {
		var prs []db.PullRequest
		if err := preloadPRForRESTIssueList(s.DBForCtx(ctx)).Where("pull_requests.id IN ?", prIDs).Find(&prs).Error; err != nil {
			return nil, err
		}
		for _, pr := range prs {
			pr.Repository = repo
			prsByID[pr.ID] = pr
		}
	}

	items := make([]IssueListPageItem, 0, len(rows))
	for _, row := range rows {
		switch row.Kind {
		case "issue":
			issue, ok := issuesByID[row.ID]
			if !ok {
				continue
			}
			items = append(items, IssueListPageItem{Issue: &issue, Comments: row.Comments})
		case "pr":
			pr, ok := prsByID[row.ID]
			if !ok {
				continue
			}
			items = append(items, IssueListPageItem{PullRequest: &pr, Comments: row.Comments})
		}
	}
	return items, nil
}
