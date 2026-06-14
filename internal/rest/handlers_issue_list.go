package rest

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/rest/respond"
	"github.com/ngaut/agent-git-service/internal/rest/transform"
	"github.com/ngaut/agent-git-service/internal/service"
)

// --- Issues: Listing & Filtering ---

// ListAssignees handles GET /api/v3/repos/{owner}/{repo}/assignees
// Returns the list of users that can be assigned to issues in this repository.
func (d *Deps) ListAssignees(w http.ResponseWriter, r *http.Request) {
	d.ListCollaborators(w, r) // assignees are the repo collaborators
}

type issueListParams struct {
	repoFullName string
	state        string
	labels       string
	assignee     string
	creator      string
	mentioned    string
	kind         string
	titlePrefix  string
	includeBody  bool
	fields       []string
	sort         string
	direction    string
	milestone    string
	since        string
	sinceTime    time.Time
	hasSince     bool
}

type issueListItem struct {
	issue     *db.Issue
	pr        *db.PullRequest
	comments  int64
	createdAt time.Time
	updatedAt time.Time
	number    int
}

type validationError string

func (e validationError) Error() string {
	return string(e)
}

// ListIssues handles GET /api/v3/repos/{owner}/{repo}/issues
func (d *Deps) ListIssues(w http.ResponseWriter, r *http.Request) {
	params, err := parseIssueListParams(r)
	if err != nil {
		respond.ValidationFailed(w, err.Error())
		return
	}
	page, perPage := parsePagination(r)
	if params.requiresLegacyIssueList() {
		d.listIssuesLegacy(w, r, params, page, perPage)
		return
	}
	result, err := d.Svc.ListIssuesForRESTPage(r.Context(), service.IssueListPageFilter{
		RepoFullName:  params.repoFullName,
		State:         params.state,
		Labels:        params.labels,
		Kind:          params.kind,
		TitlePrefix:   params.titlePrefix,
		Sort:          params.sort,
		Direction:     params.direction,
		Milestone:     params.milestone,
		Since:         params.since,
		Page:          page,
		PerPage:       perPage,
		OmitIssueBody: !params.includeBody,
	})
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	setLinkHeader(w, r, d.Svc.BaseURL, int(result.Total), page, perPage)
	items := issueListItemsFromPage(result.Items)
	resolver := d.batchUserResolver(r.Context(), collectIssueListUserLogins(items))
	assoc := d.issueListAuthorAssociationChecks(r.Context(), items)

	out, err := d.buildIssueListResponse(r.Context(), items, resolver, assoc)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	out = filterIssueListResponseFields(out, params.fields)
	respond.JSON(w, 200, out)
}

func (d *Deps) listIssuesLegacy(w http.ResponseWriter, r *http.Request, params *issueListParams, page, perPage int) {
	issues, prs, err := d.fetchIssuesAndPRs(r.Context(), params)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	items := mergeIssuesAndPRs(issues, prs)
	items = filterIssueListItems(items, params)
	if err := d.countCommentsForItems(r.Context(), items); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	sortKey := strings.ToLower(params.sort)
	if sortKey == "" {
		sortKey = "created"
	}
	sortDir := strings.ToLower(params.direction)
	if sortDir != "asc" && sortDir != "desc" {
		sortDir = "desc"
	}
	sortIssueItems(items, sortKey, sortDir)
	paged := paginate(w, r, d.Svc.BaseURL, items, page, perPage)
	resolver := d.batchUserResolver(r.Context(), collectIssueListUserLogins(paged))
	assoc := d.issueListAuthorAssociationChecks(r.Context(), paged)

	out, err := d.buildIssueListResponse(r.Context(), paged, resolver, assoc)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	out = filterIssueListResponseFields(out, params.fields)
	respond.JSON(w, 200, out)
}

func (params *issueListParams) requiresLegacyIssueList() bool {
	return params.assignee != "" || params.creator != "" || params.mentioned != ""
}

func issueListItemsFromPage(entries []service.IssueListPageItem) []issueListItem {
	items := make([]issueListItem, 0, len(entries))
	for i := range entries {
		entry := entries[i]
		if entry.Issue != nil {
			items = append(items, issueListItem{
				issue:     entry.Issue,
				comments:  entry.Comments,
				createdAt: entry.Issue.CreatedAt,
				updatedAt: entry.Issue.UpdatedAt,
				number:    entry.Issue.Number,
			})
			continue
		}
		if entry.PullRequest != nil {
			items = append(items, issueListItem{
				pr:        entry.PullRequest,
				comments:  entry.Comments,
				createdAt: entry.PullRequest.CreatedAt,
				updatedAt: entry.PullRequest.UpdatedAt,
				number:    entry.PullRequest.Number,
			})
		}
	}
	return items
}

func (d *Deps) issueListAuthorAssociationChecks(ctx context.Context, items []issueListItem) transform.AuthorAssociationChecks {
	repo, ok := issueListRepository(items)
	if !ok || d == nil || d.Svc == nil {
		return transform.AuthorAssociationChecks{}
	}
	authorIDs := collectIssueListAuthorIDs(items)
	collabIDs := make(map[uint]struct{})
	if ids, err := d.Svc.ListCollaboratorUserIDs(ctx, repo.ID); err != nil {
		logErr(ctx, "issueListAuthorAssociation: list collaborators", err)
	} else {
		for _, id := range ids {
			collabIDs[id] = struct{}{}
		}
	}
	memberIDs := make(map[uint]struct{})
	if repo.Owner.Type == db.TypeOrganization {
		memberCheckIDs := issueListAuthorIDsNeedingOrgMemberCheck(authorIDs, collabIDs, repo.OwnerID)
		var err error
		memberIDs, err = d.Svc.ListOrgMemberUserIDs(ctx, repo.OwnerID, memberCheckIDs)
		if err != nil {
			logErr(ctx, "issueListAuthorAssociation: list org members", err)
			memberIDs = make(map[uint]struct{})
		}
	}
	return transform.AuthorAssociationChecks{
		IsCollaborator: func(userID uint) bool {
			_, ok := collabIDs[userID]
			return ok
		},
		IsOrgMember: func(userID uint) bool {
			_, ok := memberIDs[userID]
			return ok
		},
	}
}

func issueListRepository(items []issueListItem) (db.Repository, bool) {
	for _, item := range items {
		if item.issue != nil {
			return item.issue.Repository, true
		}
		if item.pr != nil {
			return item.pr.Repository, true
		}
	}
	return db.Repository{}, false
}

func collectIssueListAuthorIDs(items []issueListItem) []uint {
	ids := make([]uint, 0, len(items))
	seen := make(map[uint]struct{})
	add := func(id uint) {
		if id == 0 {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	for _, item := range items {
		if item.issue != nil {
			add(item.issue.AuthorID)
		}
		if item.pr != nil {
			add(item.pr.AuthorID)
		}
	}
	return ids
}

func issueListAuthorIDsNeedingOrgMemberCheck(authorIDs []uint, collabIDs map[uint]struct{}, ownerID uint) []uint {
	ids := make([]uint, 0, len(authorIDs))
	for _, id := range authorIDs {
		if id == 0 || id == ownerID {
			continue
		}
		if _, ok := collabIDs[id]; ok {
			continue
		}
		ids = append(ids, id)
	}
	return ids
}

func parseIssueListParams(r *http.Request) (*issueListParams, error) {
	full := repoFullName(r)
	state := r.URL.Query().Get("state")
	if state == "" {
		state = "open"
	}
	labels := r.URL.Query().Get("labels") // comma-separated label names
	assignee := r.URL.Query().Get("assignee")
	creator := r.URL.Query().Get("creator")
	mentioned := r.URL.Query().Get("mentioned")
	kind := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("kind")))
	if kind == "" {
		kind = "all"
	}
	if kind == "pr" {
		kind = "pull"
	}
	switch kind {
	case "issue", "pull", "all":
	default:
		return nil, validationError("kind must be one of: issue, pull, all")
	}
	titlePrefix := strings.TrimSpace(r.URL.Query().Get("title_prefix"))
	includeBody := queryListContains(r.URL.Query().Get("include"), "body")
	fields := parseIssueListFields(r.URL.Query().Get("fields"))
	sortParam := strings.TrimSpace(r.URL.Query().Get("sort"))
	if sortParam != "" {
		sortParam = strings.ToLower(sortParam)
		switch sortParam {
		case "created", "updated", "comments":
		default:
			return nil, validationError("sort must be one of: created, updated, comments")
		}
	}
	direction := strings.TrimSpace(r.URL.Query().Get("direction"))
	if direction != "" {
		direction = strings.ToLower(direction)
		switch direction {
		case "asc", "desc":
		default:
			return nil, validationError("direction must be one of: asc, desc")
		}
	}
	milestone := strings.TrimSpace(r.URL.Query().Get("milestone"))
	since := strings.TrimSpace(r.URL.Query().Get("since"))
	var (
		sinceTime time.Time
		hasSince  bool
	)
	if since != "" {
		parsed, err := time.Parse(time.RFC3339Nano, since)
		if err != nil {
			return nil, validationError("since must be ISO 8601")
		}
		sinceTime = parsed
		hasSince = true
	}

	return &issueListParams{
		repoFullName: full,
		state:        state,
		labels:       labels,
		assignee:     assignee,
		creator:      creator,
		mentioned:    mentioned,
		kind:         kind,
		titlePrefix:  titlePrefix,
		includeBody:  includeBody,
		fields:       fields,
		sort:         sortParam,
		direction:    direction,
		milestone:    milestone,
		since:        since,
		sinceTime:    sinceTime,
		hasSince:     hasSince,
	}, nil
}

func (d *Deps) fetchIssuesAndPRs(ctx context.Context, params *issueListParams) ([]db.Issue, []db.PullRequest, error) {
	var issues []db.Issue
	var err error
	if params.kind != "pull" {
		if params.assignee != "" || params.creator != "" || params.mentioned != "" {
			issues, err = d.Svc.ListIssuesFiltered(ctx, service.IssueListFilter{
				RepoFullName: params.repoFullName,
				State:        params.state,
				Assignee:     params.assignee,
				Mentioned:    params.mentioned,
				CreatedBy:    params.creator,
				Labels:       params.labels,
				Sort:         params.sort,
				Direction:    params.direction,
				Milestone:    params.milestone,
				Since:        params.since,
			})
		} else {
			issues, err = d.Svc.ListIssuesForREST(ctx, params.repoFullName, params.state, params.labels, params.sort, params.direction, params.milestone, params.since)
		}
		if err != nil {
			return nil, nil, err
		}
	}

	var prs []db.PullRequest
	if params.kind != "issue" {
		prs, err = d.Svc.ListPRsFiltered(ctx, service.PRListFilter{
			RepoFullName: params.repoFullName,
			State:        params.state,
			Mentioned:    params.mentioned,
		})
		if err != nil {
			return nil, nil, err
		}
		labelFilters := parseFilterList(params.labels)
		if len(prs) > 0 {
			var invalidMilestone bool
			prs, invalidMilestone = filterPRs(prs, labelFilters, params.assignee, params.creator, params.milestone, params.sinceTime, params.hasSince)
			if invalidMilestone {
				prs = nil
			}
		}
	}

	return issues, prs, nil
}

func queryListContains(raw, wanted string) bool {
	wanted = strings.ToLower(strings.TrimSpace(wanted))
	for _, part := range strings.Split(raw, ",") {
		if strings.ToLower(strings.TrimSpace(part)) == wanted {
			return true
		}
	}
	return false
}

func parseIssueListFields(raw string) []string {
	parts := strings.Split(raw, ",")
	fields := make([]string, 0, len(parts))
	seen := make(map[string]struct{})
	for _, part := range parts {
		field := strings.TrimSpace(part)
		if field == "" {
			continue
		}
		if _, ok := seen[field]; ok {
			continue
		}
		seen[field] = struct{}{}
		fields = append(fields, field)
	}
	return fields
}

func filterIssueListItems(items []issueListItem, params *issueListParams) []issueListItem {
	if params.titlePrefix == "" {
		return items
	}
	out := make([]issueListItem, 0, len(items))
	for _, item := range items {
		title := ""
		if item.issue != nil {
			title = item.issue.Title
		}
		if item.pr != nil {
			title = item.pr.Title
		}
		if strings.HasPrefix(strings.ToLower(title), strings.ToLower(params.titlePrefix)) {
			out = append(out, item)
		}
	}
	return out
}

func filterIssueListResponseFields(rows []any, fields []string) []any {
	if len(fields) == 0 {
		return rows
	}
	out := make([]any, len(rows))
	for i, row := range rows {
		src, ok := row.(map[string]any)
		if !ok {
			out[i] = row
			continue
		}
		dst := make(map[string]any, len(fields))
		for _, field := range fields {
			if value, ok := src[field]; ok {
				dst[field] = value
			}
		}
		out[i] = dst
	}
	return out
}

func mergeIssuesAndPRs(issues []db.Issue, prs []db.PullRequest) []issueListItem {
	items := make([]issueListItem, 0, len(issues)+len(prs))
	for i := range issues {
		iss := &issues[i]
		items = append(items, issueListItem{
			issue:     iss,
			createdAt: iss.CreatedAt,
			updatedAt: iss.UpdatedAt,
			number:    iss.Number,
		})
	}
	for i := range prs {
		pr := &prs[i]
		items = append(items, issueListItem{
			pr:        pr,
			createdAt: pr.CreatedAt,
			updatedAt: pr.UpdatedAt,
			number:    pr.Number,
		})
	}
	return items
}

func collectIssueListUserLogins(items []issueListItem) []string {
	logins := make([]string, 0)
	seen := make(map[string]struct{})
	addLogin := func(login string) {
		login = strings.TrimSpace(login)
		if login == "" {
			return
		}
		if _, ok := seen[login]; ok {
			return
		}
		seen[login] = struct{}{}
		logins = append(logins, login)
	}
	addAssignees := func(raw string) {
		for _, login := range strings.Split(raw, ",") {
			addLogin(login)
		}
	}
	for _, item := range items {
		if item.issue != nil {
			addAssignees(item.issue.AssigneeLogins)
			addLogin(item.issue.ClosedByLogin)
		}
		if item.pr != nil {
			addAssignees(item.pr.AssigneeLogins)
		}
	}
	return logins
}

func (d *Deps) countCommentsForItems(ctx context.Context, items []issueListItem) error {
	if len(items) == 0 {
		return nil
	}

	var issueNumbers []int
	var issueRepoID uint
	hasIssueRepo := false
	for i := range items {
		if items[i].issue == nil {
			continue
		}
		issueNumbers = append(issueNumbers, items[i].issue.Number)
		if !hasIssueRepo {
			issueRepoID = items[i].issue.RepositoryID
			hasIssueRepo = true
		}
	}
	if len(issueNumbers) > 0 {
		commentCounts, err := d.Svc.CountIssueCommentsBatch(ctx, issueRepoID, issueNumbers)
		if err != nil {
			return err
		}
		for i := range items {
			if items[i].issue != nil {
				items[i].comments = commentCounts[items[i].issue.Number]
			}
		}
	}
	var prNumbers []int
	var prRepoID uint
	hasPRRepo := false
	for i := range items {
		if items[i].pr == nil {
			continue
		}
		prNumbers = append(prNumbers, items[i].pr.Number)
		if !hasPRRepo {
			prRepoID = items[i].pr.RepositoryID
			hasPRRepo = true
		}
	}
	if len(prNumbers) > 0 {
		prCommentCounts := d.Svc.CountPRCommentsBatch(ctx, prRepoID, prNumbers)
		for i := range items {
			if items[i].pr != nil {
				items[i].comments = prCommentCounts[items[i].pr.Number]
			}
		}
	}

	return nil
}

func sortIssueItems(items []issueListItem, sortKey, sortDir string) {
	compareTime := func(a, b time.Time) int {
		if a.Before(b) {
			return -1
		}
		if a.After(b) {
			return 1
		}
		return 0
	}
	compareInt64 := func(a, b int64) int {
		if a < b {
			return -1
		}
		if a > b {
			return 1
		}
		return 0
	}
	sort.Slice(items, func(i, j int) bool {
		a := items[i]
		b := items[j]
		var cmp int
		switch sortKey {
		case "updated":
			cmp = compareTime(a.updatedAt, b.updatedAt)
		case "comments":
			cmp = compareInt64(a.comments, b.comments)
		default:
			cmp = compareTime(a.createdAt, b.createdAt)
		}
		if cmp == 0 {
			cmp = compareInt64(int64(a.number), int64(b.number))
		}
		if sortDir == "asc" {
			return cmp < 0
		}
		return cmp > 0
	})
}

func (d *Deps) buildIssueListResponse(ctx context.Context, items []issueListItem, resolver transform.UserResolver, assoc transform.AuthorAssociationChecks) ([]any, error) {
	// Batch-fetch reaction counts for all issues in one query instead of N+1.
	issueIDs := make([]uint, 0, len(items))
	for _, item := range items {
		if item.issue != nil {
			issueIDs = append(issueIDs, item.issue.ID)
		}
	}
	allReactions, err := d.Svc.CountReactionsBatch(ctx, issueIDs)
	if err != nil {
		return nil, err
	}

	out := make([]any, len(items))
	for i, item := range items {
		if item.issue != nil {
			out[i] = transform.Issue(*item.issue, resolver, assoc, transform.IssueCounts{
				Comments:  item.comments,
				Reactions: allReactions[item.issue.ID],
			})
			continue
		}
		if item.pr != nil {
			out[i] = transform.IssueFromPR(*item.pr, resolver, assoc, item.comments)
		}
	}
	return out, nil
}

func parseFilterList(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, strings.ToLower(p))
	}
	return out
}

func prHasAllLabels(pr db.PullRequest, labels []string) bool {
	if len(labels) == 0 {
		return true
	}
	if len(pr.Labels) == 0 {
		return false
	}
	prLabels := make(map[string]struct{}, len(pr.Labels))
	for _, label := range pr.Labels {
		prLabels[strings.ToLower(label.Name)] = struct{}{}
	}
	for _, label := range labels {
		if _, ok := prLabels[label]; !ok {
			return false
		}
	}
	return true
}

func prHasAssignee(pr db.PullRequest, login string) bool {
	login = strings.TrimSpace(login)
	if login == "" {
		return true
	}
	for _, assignee := range strings.Split(pr.AssigneeLogins, ",") {
		assignee = strings.TrimSpace(assignee)
		if assignee == "" {
			continue
		}
		if strings.EqualFold(assignee, login) {
			return true
		}
	}
	return false
}

func filterPRs(prs []db.PullRequest, labels []string, assignee, creator, milestone string, since time.Time, hasSince bool) ([]db.PullRequest, bool) {
	rawMilestone := strings.TrimSpace(milestone)
	milestone = strings.ToLower(rawMilestone)
	var (
		milestoneMode   string
		milestoneRaw    string
		milestoneNum    int
		hasMilestoneNum bool
	)
	switch milestone {
	case "":
		milestoneMode = ""
	case "*":
		milestoneMode = "any"
	case "none":
		milestoneMode = "none"
	default:
		milestoneMode = "match"
		milestoneRaw = rawMilestone
		n, err := strconv.Atoi(rawMilestone)
		if err == nil {
			hasMilestoneNum = true
			milestoneNum = n
		}
	}
	out := make([]db.PullRequest, 0, len(prs))
	for _, pr := range prs {
		if assignee != "" && !prHasAssignee(pr, assignee) {
			continue
		}
		if creator != "" && !strings.EqualFold(pr.Author.Login, creator) {
			continue
		}
		if !prHasAllLabels(pr, labels) {
			continue
		}
		if hasSince && pr.UpdatedAt.Before(since) {
			continue
		}
		switch milestoneMode {
		case "any":
			if pr.MilestoneID == nil {
				continue
			}
		case "none":
			if pr.MilestoneID != nil {
				continue
			}
		case "match":
			if pr.Milestone == nil {
				continue
			}
			if hasMilestoneNum && pr.Milestone.Number == milestoneNum {
				break
			}
			if !strings.EqualFold(pr.Milestone.Title, milestoneRaw) {
				continue
			}
		}
		out = append(out, pr)
	}
	return out, false
}
