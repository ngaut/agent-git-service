package search

import (
	"context"
	"strings"

	"gh-server/internal/db"

	"gorm.io/gorm"
)

// RepoSearchDeps groups dependencies for repository search.
type RepoSearchDeps struct {
	DBForCtx         func(context.Context) *gorm.DB
	UserFromContext  func(context.Context) (db.User, bool)
	DefaultListLimit int
}

// SearchRepos performs a GitHub-style repository search using qualifiers.
// Returns an empty slice (never nil) when query is blank or no results.
func SearchRepos(ctx context.Context, deps RepoSearchDeps, query string) ([]db.Repository, error) {
	var repos []db.Repository

	sq := ParseSearchQuery(query)
	textQuery := strings.TrimSpace(strings.Join(sq.FreeText, " "))
	if textQuery == "" && !HasRepoSearchFilters(sq) {
		return []db.Repository{}, nil
	}

	q := deps.DBForCtx(ctx).Model(&db.Repository{}).Preload("Owner")

	userFilter := sq.User
	if userFilter == "" {
		userFilter = sq.Org
	}

	if userFilter != "" {
		q = q.Where("repositories.owner_id IN (?)",
			deps.DBForCtx(ctx).Model(&db.User{}).
				Select("id").
				Where("LOWER(login) = LOWER(?)", userFilter),
		)
	}

	if sq.Language != "" {
		q = q.Where("LOWER(repositories.language) = ?", strings.ToLower(sq.Language))
	}
	if sq.Visibility != "" {
		q = applyRepoVisibilityFilter(q, sq.Visibility)
	}
	if len(sq.Topic) > 0 {
		if cond, args := topicMatchAllCondition(sq.Topic); cond != "" {
			q = q.Where(cond, args...)
		}
	}
	if sq.Archived != nil {
		q = q.Where("repositories.archived = ?", *sq.Archived)
	}
	if sq.Fork != "" {
		switch strings.ToLower(strings.TrimSpace(sq.Fork)) {
		case "only":
			q = q.Where("repositories.fork = ?", true)
		case "false":
			q = q.Where("repositories.fork = ?", false)
		case "true":
			// include forks; no additional filter
		}
	}
	if sq.License != "" {
		q = q.Where("LOWER(repositories.license) = ?", strings.ToLower(sq.License))
	}

	if dr, ok := parseDateRange(sq.Created); ok {
		q = applyDateRange(q, "repositories.created_at", dr)
	}
	if dr, ok := parseDateRange(sq.Updated); ok {
		q = applyDateRange(q, "repositories.updated_at", dr)
	}
	if dr, ok := parseDateRange(sq.Pushed); ok {
		q = applyDateRange(q, repoPushedExpr, dr)
	}

	starRange, starOK := parseNumericRange(sq.Stars)
	forkRange, forkOK := parseNumericRange(sq.Forks)
	sortKey, sortDir := repoSortSpec(sq.Sort, sq.Order)

	needStarCounts := starOK || sortKey == "stars"
	needForkCounts := forkOK || sortKey == "forks"
	if needStarCounts {
		q = q.Joins(repoStarCountsJoin)
	}
	if needForkCounts {
		q = q.Joins(repoForkCountsJoin)
	}
	if starOK {
		q = applyNumericRange(q, repoStarCountExpr, starRange)
	}
	if forkOK {
		q = applyNumericRange(q, repoForkCountExpr, forkRange)
	}

	if textQuery != "" {
		like := "%" + escapeLike(textQuery) + "%"
		q = q.Where("repositories.name LIKE ? OR repositories.description LIKE ?", like, like)
	}

	if orderClause := repoSortClause(sortKey, sortDir); orderClause != "" {
		q = q.Order(orderClause)
	}

	limit := deps.DefaultListLimit
	if textQuery == "" {
		limit = 1000
	}
	err := q.Limit(limit).Find(&repos).Error
	return repos, err
}

const (
	repoStarCountsJoin = "LEFT JOIN (SELECT repository_id, COUNT(*) AS stars_count FROM stars GROUP BY repository_id) star_counts ON star_counts.repository_id = repositories.id"
	repoForkCountsJoin = "LEFT JOIN (SELECT parent_id AS repository_id, COUNT(*) AS forks_count FROM repositories WHERE parent_id IS NOT NULL GROUP BY parent_id) fork_counts ON fork_counts.repository_id = repositories.id"
	repoStarCountExpr  = "COALESCE(star_counts.stars_count, 0)"
	repoForkCountExpr  = "COALESCE(fork_counts.forks_count, 0)"
	repoPushedExpr     = "COALESCE(repositories.pushed_at, repositories.created_at)"
)

func applyRepoVisibilityFilter(q *gorm.DB, visibility string) *gorm.DB {
	vis := strings.TrimSpace(strings.ToLower(visibility))
	if vis == "" {
		return q
	}
	return q.Where(
		"LOWER(COALESCE(NULLIF(repositories.visibility, ''), CASE WHEN repositories.private THEN 'private' ELSE 'public' END)) = ?",
		vis,
	)
}

// HasRepoSearchFilters reports whether repo-specific qualifiers were provided.
func HasRepoSearchFilters(sq SearchQualifiers) bool {
	if sq.User != "" || sq.Org != "" || len(sq.Topic) > 0 || sq.Language != "" || sq.Fork != "" || sq.License != "" || sq.Visibility != "" {
		return true
	}
	if sq.Archived != nil {
		return true
	}
	if sq.Created != "" || sq.Updated != "" || sq.Pushed != "" {
		return true
	}
	if sq.Size != "" || sq.Topics != "" {
		return true
	}
	if sq.Stars != "" || sq.Forks != "" {
		return true
	}
	return false
}

func topicMatchCondition(topic string) (string, []any) {
	topic = strings.TrimSpace(topic)
	if topic == "" {
		return "", nil
	}
	esc := escapeLike(topic)
	return "repositories.topics = ? OR repositories.topics LIKE ? OR repositories.topics LIKE ? OR repositories.topics LIKE ?",
		[]any{topic, esc + ",%", "%," + esc, "%," + esc + ",%"}
}

func topicMatchAllCondition(topics []string) (string, []any) {
	if len(topics) == 0 {
		return "", nil
	}
	clauses := make([]string, 0, len(topics))
	args := make([]any, 0, len(topics)*4)
	for _, topic := range topics {
		cond, condArgs := topicMatchCondition(topic)
		if cond == "" {
			continue
		}
		clauses = append(clauses, "("+cond+")")
		args = append(args, condArgs...)
	}
	if len(clauses) == 0 {
		return "", nil
	}
	return strings.Join(clauses, " AND "), args
}

func repoSortSpec(sort, order string) (string, string) {
	sort = strings.TrimSpace(strings.ToLower(sort))
	order = strings.TrimSpace(strings.ToLower(order))
	if sort == "" {
		return "", ""
	}
	if strings.HasSuffix(sort, "-asc") {
		return strings.TrimSuffix(sort, "-asc"), "asc"
	}
	if strings.HasSuffix(sort, "-desc") {
		return strings.TrimSuffix(sort, "-desc"), "desc"
	}
	if order != "asc" && order != "desc" {
		order = "desc"
	}
	return sort, order
}

func repoSortClause(key, dir string) string {
	if key == "" {
		return ""
	}
	if dir == "" {
		dir = "desc"
	}
	switch key {
	case "stars":
		return repoStarCountExpr + " " + dir
	case "forks":
		return repoForkCountExpr + " " + dir
	case "updated":
		return "repositories.updated_at " + dir
	case "created":
		return "repositories.created_at " + dir
	case "pushed":
		return repoPushedExpr + " " + dir
	default:
		return ""
	}
}

// SearchReposGQL handles GraphQL search(type: REPOSITORY) queries.
// The query string uses GitHub search syntax: "user:X language:Y topic:Z sort:updated-desc".
func SearchReposGQL(ctx context.Context, deps RepoSearchDeps, query string) ([]db.Repository, error) {
	var repos []db.Repository
	q := deps.DBForCtx(ctx).Preload("Owner").Preload("Labels")

	sq := ParseSearchQuery(query)

	userFilter := sq.User
	if userFilter == "" {
		userFilter = sq.Org
	}

	if userFilter != "" {
		q = q.Where("repositories.owner_id IN (?)",
			deps.DBForCtx(ctx).Model(&db.User{}).
				Select("id").
				Where("login = ?", userFilter),
		)
	}

	if sq.Language != "" {
		q = q.Where("LOWER(repositories.language) = ?", strings.ToLower(sq.Language))
	}
	if sq.Visibility != "" {
		q = applyRepoVisibilityFilter(q, sq.Visibility)
	}
	if len(sq.Topic) > 0 {
		if cond, args := topicMatchAllCondition(sq.Topic); cond != "" {
			q = q.Where(cond, args...)
		}
	}
	if sq.Archived != nil {
		q = q.Where("repositories.archived = ?", *sq.Archived)
	}
	if sq.Fork != "" {
		switch strings.ToLower(strings.TrimSpace(sq.Fork)) {
		case "only":
			q = q.Where("repositories.fork = ?", true)
		case "false":
			q = q.Where("repositories.fork = ?", false)
		case "true":
			// include forks; no additional filter
		}
	}

	sortClause := "repositories.updated_at DESC"
	switch sq.Sort {
	case "stars-desc", "stars":
		sortClause = "repositories.updated_at DESC" // real sort by stars would need a join
	case "updated-asc":
		sortClause = "repositories.updated_at ASC"
	case "created-desc":
		sortClause = "repositories.created_at DESC"
	case "created-asc":
		sortClause = "repositories.created_at ASC"
	}

	textParts := strings.Join(sq.FreeText, " ")
	if textParts != "" {
		q = q.Where("repositories.name LIKE ? OR repositories.description LIKE ?", "%"+escapeLike(textParts)+"%", "%"+escapeLike(textParts)+"%")
	}

	err := q.Order(sortClause).Limit(deps.DefaultListLimit).Find(&repos).Error
	return repos, err
}
