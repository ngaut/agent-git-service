package rest

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"gh-server/internal/rest/respond"
	"gh-server/internal/rest/transform"
	"gh-server/internal/service"
)

// ListMilestones handles GET /api/v3/repos/{owner}/{repo}/milestones
func (d *Deps) ListMilestones(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	if !d.requireRepoPermission(w, r, repo.ID, service.RepoPermissionRead) {
		return
	}

	full := repo.FullName
	page, perPage := parsePagination(r)
	if raw := strings.TrimSpace(r.URL.Query().Get("include_issue_count")); raw != "" {
		if _, err := strconv.ParseBool(raw); err != nil {
			respond.ValidationFailed(w, "include_issue_count must be a boolean")
			return
		}
	}
	state := strings.TrimSpace(r.URL.Query().Get("state"))
	if state != "" {
		state = strings.ToLower(state)
		switch state {
		case "open", "closed", "all":
		default:
			respond.ValidationFailed(w, "state must be one of: open, closed, all")
			return
		}
	}
	sort := strings.TrimSpace(r.URL.Query().Get("sort"))
	if sort != "" {
		sort = strings.ToLower(sort)
		switch sort {
		case "created", "updated", "due_on", "number":
		default:
			respond.ValidationFailed(w, "sort must be one of: created, updated, due_on, number")
			return
		}
	}
	direction := strings.TrimSpace(r.URL.Query().Get("direction"))
	if direction != "" {
		direction = strings.ToLower(direction)
		switch direction {
		case "asc", "desc":
		default:
			respond.ValidationFailed(w, "direction must be one of: asc, desc")
			return
		}
	}

	milestones, total, err := d.Svc.ListMilestones(r.Context(), full, state, sort, direction, page, perPage)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	setLinkHeader(w, r, d.Svc.BaseURL, int(total), page, perPage)
	milestoneIDs := make([]uint, len(milestones))
	for i, m := range milestones {
		milestoneIDs[i] = m.ID
	}
	countsByMilestone, err := d.Svc.CountMilestoneIssuesBatch(r.Context(), milestoneIDs)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	out := make([]any, len(milestones))
	for i, m := range milestones {
		counts := countsByMilestone[m.ID]
		out[i] = transform.Milestone(&m, full, transform.MilestoneCounts{
			OpenIssues:   counts.OpenIssues,
			ClosedIssues: counts.ClosedIssues,
		})
	}
	respond.JSON(w, 200, out)
}

// ListMilestoneIssues handles GET /api/v3/repos/{owner}/{repo}/milestones/{number}/issues
func (d *Deps) ListMilestoneIssues(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	num, ok := mustIntParam(w, r, "milestone_number")
	if !ok {
		return
	}
	if _, err := d.Svc.GetMilestoneByNumber(r.Context(), full, num); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	state := strings.TrimSpace(r.URL.Query().Get("state"))
	if state == "" {
		state = "all"
	}
	state = strings.ToLower(state)
	switch state {
	case "open", "closed", "all":
	default:
		respond.ValidationFailed(w, "state must be one of: open, closed, all")
		return
	}

	sort := strings.TrimSpace(r.URL.Query().Get("sort"))
	if sort != "" {
		sort = strings.ToLower(sort)
		switch sort {
		case "created", "updated", "comments":
		default:
			respond.ValidationFailed(w, "sort must be one of: created, updated, comments")
			return
		}
	}

	direction := strings.TrimSpace(r.URL.Query().Get("direction"))
	if direction != "" {
		direction = strings.ToLower(direction)
		switch direction {
		case "asc", "desc":
		default:
			respond.ValidationFailed(w, "direction must be one of: asc, desc")
			return
		}
	}

	since := strings.TrimSpace(r.URL.Query().Get("since"))
	if since != "" {
		if _, err := time.Parse(time.RFC3339Nano, since); err != nil {
			respond.ValidationFailed(w, "since must be ISO 8601")
			return
		}
	}

	issues, err := d.Svc.ListIssuesForREST(r.Context(), full, state, "", sort, direction, strconv.Itoa(num), since)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	items := make([]issueListItem, 0, len(issues))
	for i := range issues {
		iss := &issues[i]
		items = append(items, issueListItem{
			issue:     iss,
			createdAt: iss.CreatedAt,
			updatedAt: iss.UpdatedAt,
			number:    iss.Number,
		})
	}

	if err := d.countCommentsForItems(r.Context(), items); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	sortKey := strings.ToLower(sort)
	if sortKey == "" {
		sortKey = "created"
	}
	sortDir := strings.ToLower(direction)
	if sortDir != "asc" && sortDir != "desc" {
		sortDir = "desc"
	}
	sortIssueItems(items, sortKey, sortDir)

	page, perPage := parsePagination(r)
	items = paginate(w, r, d.Svc.BaseURL, items, page, perPage)
	resolver := d.userResolver(r.Context())
	var assoc transform.AuthorAssociationChecks
	if len(issues) > 0 {
		assoc = d.authorAssociationChecks(r.Context(), issues[0].Repository)
	}
	out, err := d.buildIssueListResponse(r.Context(), items, resolver, assoc)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, 200, out)
}

// GetMilestone handles GET /api/v3/repos/{owner}/{repo}/milestones/{number}
func (d *Deps) GetMilestone(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	if !d.requireRepoPermission(w, r, repo.ID, service.RepoPermissionRead) {
		return
	}

	full := repo.FullName
	num, ok := mustIntParam(w, r, "milestone_number")
	if !ok {
		return
	}
	m, err := d.Svc.GetMilestoneByNumber(r.Context(), full, num)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	if m.RepositoryID != repo.ID {
		respond.NotFound(w)
		return
	}
	openIssues, closedIssues, err := d.Svc.CountMilestoneIssues(r.Context(), m.ID)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, 200, transform.Milestone(&m, full, transform.MilestoneCounts{
		OpenIssues:   openIssues,
		ClosedIssues: closedIssues,
	}))
}

// CreateMilestone handles POST /api/v3/repos/{owner}/{repo}/milestones
func (d *Deps) CreateMilestone(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	if !d.requireRepoPermission(w, r, repo.ID, service.RepoPermissionWrite) {
		return
	}

	full := repo.FullName
	var body struct {
		Title       string  `json:"title"`
		Description string  `json:"description"`
		State       string  `json:"state"`
		DueOn       *string `json:"due_on"`
	}
	if err := decodeBodyStrict(r, &body); err != nil {
		respond.ValidationFailed(w, "invalid body")
		return
	}
	if body.Title == "" {
		respond.ValidationFailed(w, "title is required")
		return
	}
	state := strings.TrimSpace(body.State)
	if state != "" {
		state = strings.ToLower(state)
		switch state {
		case "open", "closed":
		default:
			respond.ValidationFailed(w, "state must be one of: open, closed")
			return
		}
	}
	var dueOn *time.Time
	if body.DueOn != nil {
		parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(*body.DueOn))
		if err != nil {
			respond.ValidationFailed(w, "due_on must be ISO 8601")
			return
		}
		dueOn = &parsed
	}

	m, err := d.Svc.CreateMilestone(r.Context(), full, body.Title, body.Description, state)
	if err != nil {
		if errors.Is(err, service.ErrValidation) || errors.Is(err, service.ErrInvalidState) {
			respond.ValidationFailed(w, err.Error())
			return
		}
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	if dueOn != nil {
		m, err = d.Svc.UpdateMilestone(r.Context(), full, m.Number, service.UpdateMilestoneInput{
			DueOn: dueOn,
		})
		if err != nil {
			if errors.Is(err, service.ErrValidation) || errors.Is(err, service.ErrInvalidState) {
				respond.ValidationFailed(w, err.Error())
				return
			}
			respond.ServiceErrorRequest(r, w, err)
			return
		}
	}
	openIssues, closedIssues, err := d.Svc.CountMilestoneIssues(r.Context(), m.ID)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, 201, transform.Milestone(&m, full, transform.MilestoneCounts{
		OpenIssues:   openIssues,
		ClosedIssues: closedIssues,
	}))
}

// UpdateMilestone handles PATCH /api/v3/repos/{owner}/{repo}/milestones/{number}
func (d *Deps) UpdateMilestone(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	if !d.requireRepoPermission(w, r, repo.ID, service.RepoPermissionWrite) {
		return
	}

	full := repo.FullName
	num, ok := mustIntParam(w, r, "milestone_number")
	if !ok {
		return
	}
	var body struct {
		Title       *string `json:"title"`
		Description *string `json:"description"`
		State       *string `json:"state"`
		DueOn       *string `json:"due_on"`
	}
	decodeBody(r, &body)
	if body.State != nil {
		state := strings.TrimSpace(*body.State)
		if state == "" {
			respond.ValidationFailed(w, "state must be one of: open, closed")
			return
		}
		state = strings.ToLower(state)
		switch state {
		case "open", "closed":
			*body.State = state
		default:
			respond.ValidationFailed(w, "state must be one of: open, closed")
			return
		}
	}
	var dueOn *time.Time
	if body.DueOn != nil {
		parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(*body.DueOn))
		if err != nil {
			respond.ValidationFailed(w, "due_on must be ISO 8601")
			return
		}
		dueOn = &parsed
	}

	m, err := d.Svc.UpdateMilestone(r.Context(), full, num, service.UpdateMilestoneInput{
		Title:       body.Title,
		Description: body.Description,
		State:       body.State,
		DueOn:       dueOn,
	})
	if err != nil {
		if errors.Is(err, service.ErrValidation) || errors.Is(err, service.ErrInvalidState) {
			respond.ValidationFailed(w, err.Error())
			return
		}
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	openIssues, closedIssues, err := d.Svc.CountMilestoneIssues(r.Context(), m.ID)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, 200, transform.Milestone(&m, full, transform.MilestoneCounts{
		OpenIssues:   openIssues,
		ClosedIssues: closedIssues,
	}))
}

// DeleteMilestone handles DELETE /api/v3/repos/{owner}/{repo}/milestones/{number}
func (d *Deps) DeleteMilestone(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	if !d.requireRepoPermission(w, r, repo.ID, service.RepoPermissionWrite) {
		return
	}

	full := repo.FullName
	num, ok := mustIntParam(w, r, "milestone_number")
	if !ok {
		return
	}
	if err := d.Svc.DeleteMilestone(r.Context(), full, num); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.NoContent(w)
}

// ListMilestoneLabels handles GET /api/v3/repos/{owner}/{repo}/milestones/{number}/labels
func (d *Deps) ListMilestoneLabels(w http.ResponseWriter, r *http.Request) {
	repo := d.mustGetRepo(w, r)
	if repo == nil {
		return
	}
	if !d.requireRepoPermission(w, r, repo.ID, service.RepoPermissionRead) {
		return
	}

	full := repo.FullName
	num, ok := mustIntParam(w, r, "milestone_number")
	if !ok {
		return
	}
	labels, err := d.Svc.ListMilestoneLabels(r.Context(), full, num)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	out := make([]any, len(labels))
	for i, l := range labels {
		out[i] = transform.Label(l)
	}
	respond.JSON(w, 200, out)
}
