package search

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"

	"gorm.io/gorm"
)

// escapeLike escapes SQL LIKE wildcards (% and _) in user-supplied input.
// This prevents users from injecting wildcards that match unintended rows.
// Usage: q.Where("col LIKE ?", "%"+escapeLike(input)+"%")
func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "%", `\%`)
	s = strings.ReplaceAll(s, "_", `\_`)
	return s
}

type textWhereContext struct {
	titleField  string
	bodyField   string
	repoField   string
	numberField string
}

type repoScopeStatus int

const (
	repoScopeUnavailable repoScopeStatus = iota
	repoScopeNotFound
	repoScopeResolved
)

type repoSearchScope struct {
	ID       uint
	FullName string
}

func textWhereContextForTable(tableName string) textWhereContext {
	return textWhereContext{
		titleField:  tableName + ".title",
		bodyField:   tableName + ".body",
		repoField:   tableName + ".repository_id",
		numberField: tableName + ".number",
	}
}

func resolveTextSearchTargets(inValues []string) (searchTitle bool, searchBody bool, searchComments bool) {
	if len(inValues) == 0 {
		return true, true, false
	}
	for _, v := range inValues {
		switch strings.ToLower(v) {
		case "title":
			searchTitle = true
		case "body":
			searchBody = true
		case "comments":
			searchComments = true
		}
	}
	if !searchTitle && !searchBody && !searchComments {
		return true, true, false
	}
	return searchTitle, searchBody, searchComments
}

// buildTextWhere builds the WHERE clause for text search based on the in: qualifier.
// inValues contains values like "title", "body", "comments".
// Default (empty) searches both title and body.
// Note: issue_comments table links via (repository_id, issue_number), not issue_id.
func buildTextWhere(ctx textWhereContext, inValues []string, text string) (string, []any) {
	titleField := ctx.titleField
	bodyField := ctx.bodyField
	repoField := ctx.repoField
	numberField := ctx.numberField
	likeExpr := func(field string) string {
		return "LOWER(" + field + ") LIKE LOWER(?)"
	}

	searchTitle, searchBody, searchComments := resolveTextSearchTargets(inValues)

	// Helper for comments subquery: matches comments by (repository_id, issue_number)
	commentSubquery := "EXISTS (SELECT 1 FROM issue_comments WHERE repository_id = " + repoField + " AND issue_number = " + numberField + " AND " + likeExpr("issue_comments.body") + ")"

	// If only comments, search issue_comments.body via subquery
	if searchComments && !searchTitle && !searchBody {
		return commentSubquery, []any{text}
	}

	// If comments + title (but not body), search title OR comments
	if searchComments && searchTitle && !searchBody {
		return "(" + likeExpr(titleField) + " OR " + commentSubquery + ")", []any{text, text}
	}

	// If comments + body (but not title), search body OR comments
	if searchComments && searchBody && !searchTitle {
		return "(" + likeExpr(bodyField) + " OR " + commentSubquery + ")", []any{text, text}
	}

	// If all three (title, body, comments) or title+body, search all
	if searchTitle && searchBody && searchComments {
		return "(" + likeExpr(titleField) + " OR " + likeExpr(bodyField) + " OR " + commentSubquery + ")", []any{text, text, text}
	}

	// Fallback to title/body only (no comments)
	if searchTitle && searchBody {
		return "(" + likeExpr(titleField) + " OR " + likeExpr(bodyField) + ")", []any{text, text}
	}
	if searchTitle {
		return likeExpr(titleField), []any{text}
	}
	if searchBody {
		return likeExpr(bodyField), []any{text}
	}

	// Fallback to default if no valid in: values
	return "(" + likeExpr(titleField) + " OR " + likeExpr(bodyField) + ")", []any{text, text}
}

func applyTextTokens(q *gorm.DB, freeText []string, buildWhere func(string) (string, []any)) *gorm.DB {
	for _, token := range freeText {
		text := "%" + escapeLike(token) + "%"
		whereClause, whereArgs := buildWhere(text)
		q = q.Where(whereClause, whereArgs...)
	}
	return q
}

func resolveRepoSearchScope(baseDB *gorm.DB, repoName string) (*repoSearchScope, repoScopeStatus) {
	repoName = strings.TrimSpace(repoName)
	if baseDB == nil || repoName == "" {
		return nil, repoScopeUnavailable
	}

	var repo repoSearchScope
	err := baseDB.Session(&gorm.Session{NewDB: true}).
		Model(&db.Repository{}).
		Select("id", "full_name").
		Where("full_name = ?", repoName).
		Take(&repo).Error
	if err == nil {
		return &repo, repoScopeResolved
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, repoScopeNotFound
	}
	return nil, repoScopeUnavailable
}

func applyRepoFilterWithScope(
	q *gorm.DB,
	baseDB *gorm.DB,
	tableName string,
	repoName string,
	scope *repoSearchScope,
	status repoScopeStatus,
) *gorm.DB {
	if repoName == "" {
		return q
	}
	switch status {
	case repoScopeResolved:
		return q.Where(tableName+".repository_id = ?", scope.ID)
	case repoScopeNotFound:
		return q.Where("1 = 0")
	default:
		return applyRepoFilter(q, baseDB, tableName, repoName)
	}
}

func applyRepoScopedLabelGroups(
	q *gorm.DB,
	baseDB *gorm.DB,
	groups [][]string,
	junctionTable string,
	junctionCol string,
	entityCol string,
	negate bool,
	scope *repoSearchScope,
	status repoScopeStatus,
) *gorm.DB {
	if len(groups) == 0 {
		return q
	}
	if status == repoScopeNotFound {
		return q.Where("1 = 0")
	}
	if status != repoScopeResolved || scope == nil {
		return applyLabelGroups(q, baseDB, groups, junctionTable, junctionCol, entityCol, negate)
	}

	labelIDsByName, err := resolveRepoLabelIDs(baseDB, scope.ID, flattenLabelGroupNames(groups))
	if err != nil {
		return applyLabelGroups(q, baseDB, groups, junctionTable, junctionCol, entityCol, negate)
	}

	return applyResolvedLabelGroupIDs(q, baseDB, groups, labelIDsByName, junctionTable, junctionCol, entityCol, negate)
}

func applyResolvedLabelGroupIDs(
	q *gorm.DB,
	baseDB *gorm.DB,
	groups [][]string,
	labelIDsByName map[string][]uint,
	junctionTable string,
	junctionCol string,
	entityCol string,
	negate bool,
) *gorm.DB {
	op := "IN"
	if negate {
		op = "NOT IN"
	}

	for _, group := range groups {
		ids := collectRepoLabelGroupIDs(labelIDsByName, group)
		if len(ids) == 0 {
			if negate {
				continue
			}
			return q.Where("1 = 0")
		}
		subQ := baseDB.Session(&gorm.Session{NewDB: true}).
			Table(junctionTable).
			Select(junctionCol).
			Where(junctionTable+".label_id IN ?", ids)
		q = q.Where(entityCol+" "+op+" (?)", subQ)
	}

	return q
}

func flattenLabelGroupNames(groups [][]string) []string {
	seen := make(map[string]struct{})
	names := make([]string, 0)
	for _, group := range groups {
		for _, name := range group {
			key := strings.ToLower(strings.TrimSpace(name))
			if key == "" {
				continue
			}
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			names = append(names, key)
		}
	}
	return names
}

func collectRepoLabelGroupIDs(labelIDsByName map[string][]uint, group []string) []uint {
	seen := make(map[uint]struct{})
	ids := make([]uint, 0)
	for _, name := range group {
		key := strings.ToLower(strings.TrimSpace(name))
		for _, id := range labelIDsByName[key] {
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	return ids
}

// applyLabelGroups is a shared helper for applying label group filters.
// negate=true uses NOT IN instead of IN.
func applyLabelGroups(q *gorm.DB, baseDB *gorm.DB, groups [][]string, junctionTable, junctionCol, entityCol string, negate bool) *gorm.DB {
	op := "IN"
	if negate {
		op = "NOT IN"
	}
	for _, group := range groups {
		if len(group) == 1 {
			q = q.Where(entityCol+" "+op+" (?)",
				baseDB.Session(&gorm.Session{NewDB: true}).Table(junctionTable).Select(junctionCol).
					Joins("JOIN labels ON labels.id = "+junctionTable+".label_id").
					Where("LOWER(labels.name) = LOWER(?)", group[0]),
			)
		} else {
			lowerNames := make([]string, len(group))
			for i, n := range group {
				lowerNames[i] = strings.ToLower(n)
			}
			q = q.Where(entityCol+" "+op+" (?)",
				baseDB.Session(&gorm.Session{NewDB: true}).Table(junctionTable).Select(junctionCol).
					Joins("JOIN labels ON labels.id = "+junctionTable+".label_id").
					Where("LOWER(labels.name) IN ?", lowerNames),
			)
		}
	}
	return q
}

// applyRepoFilter applies repo qualifier to an entity table query.
func applyRepoFilter(q *gorm.DB, baseDB *gorm.DB, tableName, repoName string) *gorm.DB {
	if repoName == "" {
		return q
	}
	_ = baseDB
	return q.Joins("JOIN repositories ON repositories.id = "+tableName+".repository_id").
		Where("repositories.full_name = ?", repoName)
}

// applyAuthorFilter applies author qualifier to an entity table query.
func applyAuthorFilter(q *gorm.DB, baseDB *gorm.DB, tableName, author string) *gorm.DB {
	if author == "" {
		return q
	}
	return q.Where(tableName+".author_id IN (?)",
		baseDB.Session(&gorm.Session{NewDB: true}).Table("users").Select("id").Where("login = ?", author))
}

// applyAssigneeFilter applies assignee qualifier to an entity table query.
func applyAssigneeFilter(q *gorm.DB, baseDB *gorm.DB, tableName, assignee string) *gorm.DB {
	if assignee == "" {
		return q
	}
	_ = baseDB
	cond, args := assigneeMatchCondition(assignee)
	return q.Where(tableName+"."+cond, args...)
}

// applyNegatedAuthorFilter applies negated author qualifier to an entity table query.
func applyNegatedAuthorFilter(q *gorm.DB, baseDB *gorm.DB, tableName, author string) *gorm.DB {
	if author == "" {
		return q
	}
	return q.Where(tableName+".author_id NOT IN (?)",
		baseDB.Session(&gorm.Session{NewDB: true}).Table("users").Select("id").Where("login = ?", author))
}

// applyNegatedAssigneeFilter applies negated assignee qualifier to an entity table query.
func applyNegatedAssigneeFilter(q *gorm.DB, baseDB *gorm.DB, tableName, assignee string) *gorm.DB {
	if assignee == "" {
		return q
	}
	_ = baseDB
	cond, args := assigneeNotMatchCondition(assignee)
	prefixedCond := strings.ReplaceAll(cond, "assignee_logins", tableName+".assignee_logins")
	return q.Where(prefixedCond, args...)
}

// applyMetadataFilters applies no:/has: qualifiers to an entity table query.
func applyMetadataFilters(q *gorm.DB, baseDB *gorm.DB, tableName string, sq SearchQualifiers) *gorm.DB {
	labelTable := ""
	labelFK := ""
	switch tableName {
	case "issues":
		labelTable = "issue_labels"
		labelFK = "issue_labels.issue_id"
	case "pull_requests":
		labelTable = "pr_labels"
		labelFK = "pr_labels.pull_request_id"
	}
	if sq.NoLabel && labelTable != "" {
		q = q.Where(tableName+".id NOT IN (?)",
			baseDB.Session(&gorm.Session{NewDB: true}).Table(labelTable).Select(labelFK))
	}
	if sq.HasLabel && labelTable != "" {
		q = q.Where(tableName+".id IN (?)",
			baseDB.Session(&gorm.Session{NewDB: true}).Table(labelTable).Select(labelFK))
	}
	if sq.NoAssignee {
		q = q.Where(tableName + ".assignee_logins = '' OR " + tableName + ".assignee_logins IS NULL")
	}
	if sq.HasAssignee {
		q = q.Where(tableName + ".assignee_logins != '' AND " + tableName + ".assignee_logins IS NOT NULL")
	}
	if sq.NoMilestone {
		q = q.Where(tableName + ".milestone_id IS NULL")
	}
	return q
}

func applyNumericRangeQualifier(q *gorm.DB, expr, raw string) *gorm.DB {
	if r, ok := parseNumericRange(raw); ok {
		q = applyNumericRange(q, expr, r)
	}
	return q
}

func applyDateRangeQualifier(q *gorm.DB, expr, raw string) *gorm.DB {
	if r, ok := parseDateRange(raw); ok {
		q = applyDateRange(q, expr, r)
	}
	return q
}

func issueCommentCountExpr(tableName string) string {
	return "(SELECT COUNT(*) FROM issue_comments WHERE issue_comments.repository_id = " + tableName + ".repository_id AND issue_comments.issue_number = " + tableName + ".number)"
}

func issueReactionCountExpr(tableName string) string {
	return "(SELECT COUNT(*) FROM reactions r LEFT JOIN issue_comments ic ON r.comment_id = ic.id " +
		"WHERE r.issue_id = " + tableName + ".id OR (ic.repository_id = " + tableName + ".repository_id AND ic.issue_number = " + tableName + ".number))"
}

func issueInteractionCountExpr(tableName string) string {
	return "(" + issueCommentCountExpr(tableName) + " + " + issueReactionCountExpr(tableName) + ")"
}

func contentIDExpr(baseDB *gorm.DB, prefix, idExpr string) string {
	return fmt.Sprintf("CONCAT('%s', %s)", prefix, idExpr)
}

func applyMilestoneQualifier(q *gorm.DB, tableName, milestone string) *gorm.DB {
	milestone = strings.TrimSpace(milestone)
	if milestone == "" {
		return q
	}
	switch strings.ToLower(milestone) {
	case "*":
		return q.Where(tableName + ".milestone_id IS NOT NULL")
	case "none":
		return q.Where(tableName + ".milestone_id IS NULL")
	}
	return q.Joins("JOIN milestones ON milestones.id = "+tableName+".milestone_id").
		Where("LOWER(milestones.title) = LOWER(?)", milestone)
}

func applyProjectQualifier(q *gorm.DB, baseDB *gorm.DB, tableName, prefix, itemType, project string) *gorm.DB {
	project = strings.TrimSpace(project)
	if project == "" {
		return q
	}
	projQ := baseDB.Session(&gorm.Session{NewDB: true}).Table("projects").Select("id")
	if owner, num, ok := splitProjectSpec(project); ok {
		projQ = projQ.Where("LOWER(owner_login) = LOWER(?) AND number = ?", owner, num)
	} else {
		projQ = projQ.Where("LOWER(title) = LOWER(?)", project)
	}
	contentExpr := contentIDExpr(baseDB, prefix, tableName+".id")
	return q.Where("EXISTS ("+
		"SELECT 1 FROM project_items "+
		"WHERE project_items.project_id IN (?) AND project_items.type = ? AND project_items.content_id = "+contentExpr+")",
		projQ, itemType)
}

func splitProjectSpec(project string) (string, int, bool) {
	parts := strings.SplitN(project, "/", 2)
	if len(parts) != 2 {
		return "", 0, false
	}
	owner := strings.TrimSpace(parts[0])
	numStr := strings.TrimSpace(parts[1])
	if owner == "" || numStr == "" {
		return "", 0, false
	}
	num, err := parseNumber(numStr)
	if err != nil {
		return "", 0, false
	}
	return owner, num, true
}

func applyLanguageQualifier(q *gorm.DB, baseDB *gorm.DB, tableName, language string) *gorm.DB {
	language = strings.TrimSpace(language)
	if language == "" {
		return q
	}
	return q.Where(tableName+".repository_id IN (?)",
		baseDB.Session(&gorm.Session{NewDB: true}).Table("repositories").Select("id").
			Where("LOWER(language) = LOWER(?)", language))
}

func applyVisibilityQualifier(q *gorm.DB, baseDB *gorm.DB, tableName, visibility string) *gorm.DB {
	visibility = strings.ToLower(strings.TrimSpace(visibility))
	if visibility == "" {
		return q
	}
	switch visibility {
	case "public", "private", "internal":
		return q.Where(tableName+".repository_id IN (?)",
			baseDB.Session(&gorm.Session{NewDB: true}).Table("repositories").Select("id").
				Where("LOWER(visibility) = ?", visibility))
	default:
		return q
	}
}

func applyForkQualifier(q *gorm.DB, baseDB *gorm.DB, tableName, fork string) *gorm.DB {
	fork = strings.ToLower(strings.TrimSpace(fork))
	if fork == "" {
		return q
	}
	switch fork {
	case "true":
		// Include both forked and non-forked repos (no-op filter).
		return q
	case "false":
		return q.Where(tableName+".repository_id IN (?)",
			baseDB.Session(&gorm.Session{NewDB: true}).Table("repositories").Select("id").
				Where("fork = ?", false))
	case "only":
		return q.Where(tableName+".repository_id IN (?)",
			baseDB.Session(&gorm.Session{NewDB: true}).Table("repositories").Select("id").
				Where("fork = ?", true))
	default:
		return q
	}
}

func applyMentionsQualifier(q *gorm.DB, tableName, mention string) *gorm.DB {
	pattern := mentionCandidatePattern(mention)
	if pattern == "" {
		return q
	}
	commentExists := "EXISTS (" +
		"SELECT 1 FROM issue_comments icc " +
		"WHERE icc.repository_id = " + tableName + ".repository_id " +
		"AND icc.issue_number = " + tableName + ".number " +
		"AND LOWER(icc.body) LIKE ?)"
	return q.Where("(LOWER("+tableName+".title) LIKE ? OR LOWER("+tableName+".body) LIKE ? OR "+commentExists+")",
		pattern, pattern, pattern)
}

func normalizeTeamSlug(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.Contains(raw, "/") {
		parts := strings.Split(raw, "/")
		raw = parts[len(parts)-1]
	}
	return strings.TrimSpace(raw)
}

func assigneeListMatchExpr(baseDB *gorm.DB, tableName, loginExpr string) string {
	return "CONCAT(',', COALESCE(" + tableName + ".assignee_logins, ''), ',') LIKE CONCAT('%,', " + loginExpr + ", ',%')"
}

func applyTeamQualifier(q *gorm.DB, baseDB *gorm.DB, tableName, team string) *gorm.DB {
	teamSlug := normalizeTeamSlug(team)
	if teamSlug == "" {
		return q
	}
	authorSub := baseDB.Session(&gorm.Session{NewDB: true}).Table("team_members").Select("team_members.user_id").
		Joins("JOIN teams ON teams.id = team_members.team_id").
		Where("teams.slug = ?", teamSlug)

	assigneeExpr := assigneeListMatchExpr(baseDB, tableName, "u.login")
	assigneeExists := "EXISTS (" +
		"SELECT 1 FROM team_members tm " +
		"JOIN teams t ON t.id = tm.team_id " +
		"JOIN users u ON u.id = tm.user_id " +
		"WHERE t.slug = ? AND " + assigneeExpr + ")"

	commenterExists := "EXISTS (" +
		"SELECT 1 FROM issue_comments icc " +
		"JOIN team_members tm ON tm.user_id = icc.author_id " +
		"JOIN teams t ON t.id = tm.team_id " +
		"WHERE t.slug = ? " +
		"AND icc.repository_id = " + tableName + ".repository_id " +
		"AND icc.issue_number = " + tableName + ".number)"

	return q.Where(tableName+".author_id IN (?) OR "+assigneeExists+" OR "+commenterExists, authorSub, teamSlug, teamSlug)
}

func statusStates(raw string) []string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "success":
		return []string{"success"}
	case "pending":
		return []string{"pending"}
	case "failure":
		// Treat "error" as a failure-equivalent status.
		return []string{"failure", "error"}
	default:
		return nil
	}
}

func applyPRStatusQualifier(q *gorm.DB, status string) *gorm.DB {
	states := statusStates(status)
	if len(states) == 0 {
		return q
	}
	return q.Where("EXISTS ("+
		"SELECT 1 FROM commit_statuses cs "+
		"WHERE cs.repository_id = pull_requests.repository_id "+
		"AND cs.commit_sha = pull_requests.head_sha "+
		"AND cs.state IN ?)", states)
}

func applyIssueStatusQualifier(q *gorm.DB, status string) *gorm.DB {
	states := statusStates(status)
	if len(states) == 0 {
		return q
	}
	return q.Where("EXISTS ("+
		"SELECT 1 FROM linked_branches lb "+
		"JOIN pull_requests pr ON pr.repository_id = lb.repository_id AND pr.head_ref = lb.branch_name "+
		"JOIN commit_statuses cs ON cs.repository_id = pr.repository_id AND cs.commit_sha = pr.head_sha "+
		"WHERE lb.issue_id = issues.id "+
		"AND cs.state IN ?)", states)
}

// SortOrder returns the SQL ORDER BY clause for the given sort qualifier.
// prefix is the table prefix, e.g. "issues" or "pull_requests".
func SortOrder(sort, prefix string) string {
	switch sort {
	case "created-asc":
		return prefix + ".created_at ASC"
	case "created-desc":
		return prefix + ".created_at DESC"
	case "updated-asc":
		return prefix + ".updated_at ASC"
	case "updated-desc":
		return prefix + ".updated_at DESC"
	case "comments-desc":
		// Sort by actual comment count using a subquery.
		return "(SELECT COUNT(*) FROM issue_comments WHERE issue_comments.repository_id = " + prefix + ".repository_id AND issue_comments.issue_number = " + prefix + ".number) DESC"
	case "comments-asc":
		// Sort by actual comment count using a subquery.
		return "(SELECT COUNT(*) FROM issue_comments WHERE issue_comments.repository_id = " + prefix + ".repository_id AND issue_comments.issue_number = " + prefix + ".number) ASC"
	case "reactions-desc":
		// Sort by actual reaction count using a subquery.
		return "(SELECT COUNT(*) FROM reactions WHERE reactions.issue_id = " + prefix + ".id) DESC"
	default:
		return prefix + ".id DESC"
	}
}

// DeduplicateIssues merges primary and secondary slices, skipping any
// secondary items whose ID already appears in primary. Limit caps results.
func DeduplicateIssues(primary, secondary []db.Issue, limit int) []db.Issue {
	seen := make(map[uint]struct{}, len(primary))
	for _, iss := range primary {
		seen[iss.ID] = struct{}{}
	}
	result := make([]db.Issue, len(primary), len(primary)+len(secondary))
	copy(result, primary)
	for _, iss := range secondary {
		if _, ok := seen[iss.ID]; !ok {
			if limit > 0 && len(result) >= limit {
				break
			}
			seen[iss.ID] = struct{}{}
			result = append(result, iss)
		}
	}
	return result
}

// DeduplicatePRs merges primary and secondary slices, skipping any
// secondary items whose ID already appears in primary. Limit caps results.
func DeduplicatePRs(primary, secondary []db.PullRequest, limit int) []db.PullRequest {
	seen := make(map[uint]struct{}, len(primary))
	for _, pr := range primary {
		seen[pr.ID] = struct{}{}
	}
	result := make([]db.PullRequest, len(primary), len(primary)+len(secondary))
	copy(result, primary)
	for _, pr := range secondary {
		if _, ok := seen[pr.ID]; !ok {
			if limit > 0 && len(result) >= limit {
				break
			}
			seen[pr.ID] = struct{}{}
			result = append(result, pr)
		}
	}
	return result
}

// assigneeMatchCondition returns SQL conditions to match an exact assignee login
// in a comma-separated assignee_logins column.
// Matches: exact value, starts with, ends with, or in middle.
func assigneeMatchCondition(login string) (string, []any) {
	return "assignee_logins = ? OR assignee_logins LIKE ? OR assignee_logins LIKE ? OR assignee_logins LIKE ?",
		[]any{login, login + ",%", "%," + login, "%," + login + ",%"}
}

// assigneeNotMatchCondition returns SQL conditions to NOT match an assignee login
// in a comma-separated assignee_logins column.
func assigneeNotMatchCondition(login string) (string, []any) {
	return "(assignee_logins NOT LIKE ? AND assignee_logins NOT LIKE ? AND assignee_logins NOT LIKE ? AND assignee_logins NOT LIKE ?) OR assignee_logins IS NULL OR assignee_logins = ''",
		[]any{login, login + ",%", "%," + login, "%," + login + ",%"}
}

type numericRange struct {
	min          *int
	max          *int
	minInclusive bool
	maxInclusive bool
}

func parseNumericRange(raw string) (numericRange, bool) {
	spec := strings.TrimSpace(raw)
	if spec == "" {
		return numericRange{}, false
	}

	if strings.Contains(spec, "..") {
		parts := strings.SplitN(spec, "..", 2)
		left := strings.TrimSpace(parts[0])
		right := strings.TrimSpace(parts[1])
		var r numericRange
		if left != "" {
			v, err := strconv.Atoi(left)
			if err != nil {
				return numericRange{}, false
			}
			r.min = &v
			r.minInclusive = true
		}
		if right != "" {
			v, err := strconv.Atoi(right)
			if err != nil {
				return numericRange{}, false
			}
			r.max = &v
			r.maxInclusive = true
		}
		if r.min == nil && r.max == nil {
			return numericRange{}, false
		}
		return r, true
	}

	ops := []string{">=", "<=", ">", "<"}
	for _, op := range ops {
		if strings.HasPrefix(spec, op) {
			valStr := strings.TrimSpace(spec[len(op):])
			n, err := strconv.Atoi(valStr)
			if err != nil {
				return numericRange{}, false
			}
			r := numericRange{}
			switch op {
			case ">=":
				r.min = &n
				r.minInclusive = true
			case ">":
				r.min = &n
				r.minInclusive = false
			case "<=":
				r.max = &n
				r.maxInclusive = true
			case "<":
				r.max = &n
				r.maxInclusive = false
			}
			return r, true
		}
	}

	if strings.HasPrefix(spec, "=") {
		spec = strings.TrimSpace(spec[1:])
	}
	n, err := strconv.Atoi(spec)
	if err != nil {
		return numericRange{}, false
	}
	return numericRange{
		min:          &n,
		max:          &n,
		minInclusive: true,
		maxInclusive: true,
	}, true
}

func applyNumericRange(q *gorm.DB, expr string, r numericRange) *gorm.DB {
	if r.min != nil {
		op := ">"
		if r.minInclusive {
			op = ">="
		}
		q = q.Where(expr+" "+op+" ?", *r.min)
	}
	if r.max != nil {
		op := "<"
		if r.maxInclusive {
			op = "<="
		}
		q = q.Where(expr+" "+op+" ?", *r.max)
	}
	return q
}

type dateValue struct {
	t       time.Time
	dayOnly bool
}

type dateRange struct {
	min          *time.Time
	max          *time.Time
	minInclusive bool
	maxInclusive bool
}

func parseDateValue(raw string) (dateValue, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return dateValue{}, false
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return dateValue{t: t}, true
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return dateValue{t: t}, true
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return dateValue{t: t, dayOnly: true}, true
	}
	return dateValue{}, false
}

func dateBounds(v dateValue) (time.Time, time.Time) {
	if !v.dayOnly {
		return v.t, v.t
	}
	start := time.Date(v.t.Year(), v.t.Month(), v.t.Day(), 0, 0, 0, 0, time.UTC)
	end := start.Add(24*time.Hour - time.Nanosecond)
	return start, end
}

func parseDateRange(raw string) (dateRange, bool) {
	spec := strings.TrimSpace(raw)
	if spec == "" {
		return dateRange{}, false
	}

	if strings.Contains(spec, "..") {
		parts := strings.SplitN(spec, "..", 2)
		left := strings.TrimSpace(parts[0])
		right := strings.TrimSpace(parts[1])
		var r dateRange
		if left != "" {
			v, ok := parseDateValue(left)
			if !ok {
				return dateRange{}, false
			}
			start, _ := dateBounds(v)
			r.min = &start
			r.minInclusive = true
		}
		if right != "" {
			v, ok := parseDateValue(right)
			if !ok {
				return dateRange{}, false
			}
			_, end := dateBounds(v)
			r.max = &end
			r.maxInclusive = true
		}
		if r.min == nil && r.max == nil {
			return dateRange{}, false
		}
		return r, true
	}

	ops := []string{">=", "<=", ">", "<"}
	for _, op := range ops {
		if strings.HasPrefix(spec, op) {
			valStr := strings.TrimSpace(spec[len(op):])
			v, ok := parseDateValue(valStr)
			if !ok {
				return dateRange{}, false
			}
			start, end := dateBounds(v)
			r := dateRange{}
			switch op {
			case ">=":
				r.min = &start
				r.minInclusive = true
			case ">":
				r.min = &end
				r.minInclusive = false
			case "<=":
				r.max = &end
				r.maxInclusive = true
			case "<":
				r.max = &start
				r.maxInclusive = false
			}
			return r, true
		}
	}

	if strings.HasPrefix(spec, "=") {
		spec = strings.TrimSpace(spec[1:])
	}
	v, ok := parseDateValue(spec)
	if !ok {
		return dateRange{}, false
	}
	start, end := dateBounds(v)
	return dateRange{
		min:          &start,
		max:          &end,
		minInclusive: true,
		maxInclusive: true,
	}, true
}

func applyDateRange(q *gorm.DB, expr string, r dateRange) *gorm.DB {
	if r.min != nil {
		op := ">"
		if r.minInclusive {
			op = ">="
		}
		q = q.Where(expr+" "+op+" ?", *r.min)
	}
	if r.max != nil {
		op := "<"
		if r.maxInclusive {
			op = "<="
		}
		q = q.Where(expr+" "+op+" ?", *r.max)
	}
	return q
}
