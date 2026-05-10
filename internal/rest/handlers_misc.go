package rest

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"gh-server/internal/db"
	"gh-server/internal/gitstore"
	"gh-server/internal/rest/respond"
	"gh-server/internal/rest/transform"
	"gh-server/internal/service"

	"github.com/go-chi/chi/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

// --- Merge Upstream (repo sync) ---

// MergeUpstream handles POST /api/v3/repos/{owner}/{repo}/merge-upstream
// Used by `gh repo sync` to sync a fork with its upstream.
func (d *Deps) MergeUpstream(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	var body struct {
		Branch string `json:"branch"`
	}
	if err := decodeBodyStrictOptional(r, &body); err != nil {
		respond.ValidationFailed(w, "invalid body")
		return
	}

	repo, err := d.Svc.GetRepo(r.Context(), full)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	branch := body.Branch
	if branch == "" {
		branch = repo.DefaultBranch
	}

	// If the repo is a fork with a parent, sync from parent
	if repo.Parent != nil && d.Svc.Git != nil {
		parentFullName := repo.Parent.FullName
		// Get the parent's branch HEAD
		parentSHA, err := d.Svc.Git.HeadSHA(r.Context(), parentFullName, branch)
		if err != nil {
			respond.Error(w, 409, fmt.Sprintf("cannot sync: %v", err))
			return
		}
		// Update the fork's branch to point to the parent's SHA
		refName := fmt.Sprintf("refs/heads/%s", branch)
		if err := d.Svc.Git.UpdateRef(r.Context(), full, refName, parentSHA); err != nil {
			respond.Error(w, 409, fmt.Sprintf("cannot update ref: %v", err))
			return
		}
		respond.JSON(w, 200, map[string]any{
			"message":     "Successfully fetched and fast-forwarded from upstream " + parentFullName,
			"merge_type":  "fast-forward",
			"base_branch": fmt.Sprintf("%s:%s", repo.Owner.Login, branch),
		})
		return
	}

	// Not a fork — just return success with no-op
	respond.JSON(w, 200, map[string]any{
		"message":     "This branch is not behind the upstream " + branch,
		"merge_type":  "none",
		"base_branch": fmt.Sprintf("%s:%s", repo.Owner.Login, branch),
	})
}

// --- Autolinks ---
// Autolinks are stored in memory since they are rarely used and the CLI
// mostly just lists/creates them for configuration.

func autolinkJSON(a db.Autolink) map[string]any {
	return map[string]any{
		"id":              a.ID,
		"key_prefix":      a.KeyPrefix,
		"url_template":    a.URLTemplate,
		"is_alphanumeric": a.IsAlphanumeric,
	}
}

// ListAutolinks handles GET /api/v3/repos/{owner}/{repo}/autolinks
func (d *Deps) ListAutolinks(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	autolinks, err := d.Svc.ListAutolinks(r.Context(), full)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	nodes := make([]any, len(autolinks))
	for i, a := range autolinks {
		nodes[i] = autolinkJSON(a)
	}
	respond.JSON(w, 200, nodes)
}

// CreateAutolink handles POST /api/v3/repos/{owner}/{repo}/autolinks
func (d *Deps) CreateAutolink(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	var body struct {
		KeyPrefix      string `json:"key_prefix"`
		URLTemplate    string `json:"url_template"`
		IsAlphanumeric bool   `json:"is_alphanumeric"`
	}
	if err := decodeBodyStrict(r, &body); err != nil {
		respond.ValidationFailed(w, "invalid body")
		return
	}
	a := db.Autolink{
		RepositoryFullName: full,
		KeyPrefix:          body.KeyPrefix,
		URLTemplate:        body.URLTemplate,
		IsAlphanumeric:     body.IsAlphanumeric,
	}
	if err := d.Svc.CreateAutolink(r.Context(), &a); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, 201, autolinkJSON(a))
}

// GetAutolink handles GET /api/v3/repos/{owner}/{repo}/autolinks/{autolink_id}
func (d *Deps) GetAutolink(w http.ResponseWriter, r *http.Request) {
	id, ok := mustIntParam(w, r, "autolink_id")
	if !ok {
		return
	}
	a, err := d.Svc.GetAutolink(r.Context(), uint(id))
	if err != nil {
		respond.NotFound(w)
		return
	}
	respond.JSON(w, 200, autolinkJSON(a))
}

// DeleteAutolink handles DELETE /api/v3/repos/{owner}/{repo}/autolinks/{autolink_id}
func (d *Deps) DeleteAutolink(w http.ResponseWriter, r *http.Request) {
	id, ok := mustIntParam(w, r, "autolink_id")
	if !ok {
		return
	}
	if err := d.Svc.DeleteAutolink(r.Context(), uint(id)); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Check Runs (backed by workflow run jobs) ---

// GetCheckRun handles GET /api/v3/repos/{owner}/{repo}/check-runs/{check_run_id}
// Check run IDs map to workflow run job IDs.
func (d *Deps) GetCheckRun(w http.ResponseWriter, r *http.Request) {
	id, ok := mustIntParam(w, r, "check_run_id")
	if !ok {
		return
	}
	job, err := d.Svc.GetWorkflowRunJob(r.Context(), uint(id))
	if err != nil {
		respond.NotFound(w)
		return
	}
	run, err := d.Svc.GetWorkflowRunByID(r.Context(), job.RunID)
	if err != nil {
		respond.NotFound(w)
		return
	}
	full := repoFullName(r)
	respond.JSON(w, 200, workflowJobCheckRun(full, run, job))
}

// ListCheckRunAnnotations handles GET /api/v3/repos/{owner}/{repo}/check-runs/{check_run_id}/annotations
// Workflow run jobs don't have annotations — return empty.
func (d *Deps) ListCheckRunAnnotations(w http.ResponseWriter, r *http.Request) {
	respond.JSON(w, 200, []any{})
}

func nullableRFC3339(ts time.Time) any {
	if ts.IsZero() {
		return nil
	}
	return ts.UTC().Format(time.RFC3339)
}

func nullableString(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}

func workflowCheckApp(repoFullName string) map[string]any {
	actionsURL := fmt.Sprintf("%s/%s/actions", transform.HTMLBase(), repoFullName)
	return map[string]any{
		"id":           0,
		"slug":         "gh-server-actions",
		"name":         "gh-server Actions",
		"description":  "Workflow-backed GitHub Checks compatibility shim",
		"external_url": actionsURL,
		"html_url":     actionsURL,
	}
}

func workflowJobCheckRun(repoFullName string, run db.WorkflowRun, job db.WorkflowRunJob) map[string]any {
	apiURL := fmt.Sprintf("%s/api/v3/repos/%s/check-runs/%d", transform.Base(), repoFullName, job.ID)
	detailsURL := fmt.Sprintf("%s/%s/actions/runs/%d/job/%d", transform.HTMLBase(), repoFullName, run.ID, job.ID)
	return map[string]any{
		"id":           job.ID,
		"node_id":      transform.NodeID("CheckRun", job.ID),
		"external_id":  fmt.Sprintf("workflow-run/%d/job/%d", run.ID, job.ID),
		"head_sha":     run.HeadSHA,
		"name":         job.Name,
		"status":       job.Status,
		"conclusion":   nullableString(job.Conclusion),
		"started_at":   nullableRFC3339(job.StartedAt),
		"completed_at": nullableRFC3339(job.CompletedAt),
		"url":          apiURL,
		"details_url":  detailsURL,
		"html_url":     detailsURL,
		"check_suite": map[string]any{
			"id": run.ID,
		},
		"app": workflowCheckApp(repoFullName),
		"output": map[string]any{
			"title":             job.Name,
			"summary":           "Workflow-backed check run compatibility result",
			"text":              fmt.Sprintf("workflow run %d, job %d", run.ID, job.ID),
			"annotations_count": 0,
			"annotations_url":   fmt.Sprintf("%s/api/v3/repos/%s/check-runs/%d/annotations", transform.Base(), repoFullName, job.ID),
		},
	}
}

// resolveRunsByRef returns workflow runs for a ref while distinguishing
// supported empty results from unsupported or invalid lookups.
func (d *Deps) resolveRunsByRef(r *http.Request, repoID uint, fullName, ref string) ([]db.WorkflowRun, int, string) {
	runs, _ := d.Svc.ListWorkflowRunsBySHA(r.Context(), repoID, ref)
	if len(runs) > 0 {
		return runs, http.StatusOK, ""
	}

	if d.Svc.Git == nil {
		return nil, http.StatusNotImplemented, "Checks API compatibility for unresolved refs requires the Git backend"
	}

	if plumbing.IsHash(ref) {
		if _, err := d.Svc.Git.GetCommit(r.Context(), fullName, ref); err != nil {
			if errors.Is(err, gitstore.ErrCommitNotFound) {
				return nil, http.StatusNotFound, fmt.Sprintf("Commit %q not found", ref)
			}
			return nil, http.StatusNotImplemented, "Checks API compatibility requires a readable Git ref store"
		}
		return []db.WorkflowRun{}, http.StatusOK, ""
	}

	if !gitstore.IsValidRefName(ref) {
		return nil, http.StatusNotFound, fmt.Sprintf("Ref %q not found", ref)
	}

	sha, err := d.Svc.Git.HeadSHA(r.Context(), fullName, ref)
	if err != nil {
		if err == plumbing.ErrReferenceNotFound || err == gitstore.ErrRefNotFound {
			return nil, http.StatusNotFound, fmt.Sprintf("Ref %q not found", ref)
		}
		return nil, http.StatusNotImplemented, "Checks API compatibility requires a readable Git ref store"
	}

	runs, _ = d.Svc.ListWorkflowRunsBySHA(r.Context(), repoID, sha)
	if runs == nil {
		runs = []db.WorkflowRun{}
	}
	return runs, http.StatusOK, ""
}

// ListCheckRunsForRef handles GET /api/v3/repos/{owner}/{repo}/commits/{ref}/check-runs
// Returns workflow run jobs for runs matching the given commit ref.
func (d *Deps) ListCheckRunsForRef(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	ref := chi.URLParam(r, "ref")

	repo, err := d.Svc.GetRepo(r.Context(), full)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	runs, status, message := d.resolveRunsByRef(r, repo.ID, full, ref)
	if status != http.StatusOK {
		respond.Error(w, status, message)
		return
	}

	var checkRuns []any
	for _, run := range runs {
		jobs, _ := d.Svc.ListWorkflowRunJobsByRun(r.Context(), run.ID)
		for _, job := range jobs {
			checkRuns = append(checkRuns, workflowJobCheckRun(full, run, job))
		}
	}
	if checkRuns == nil {
		checkRuns = []any{}
	}
	respond.JSON(w, 200, map[string]any{"total_count": len(checkRuns), "check_runs": checkRuns})
}

// ListCheckSuitesForRef handles GET /api/v3/repos/{owner}/{repo}/commits/{ref}/check-suites
// Returns workflow runs as check suites for the given commit ref.
func (d *Deps) ListCheckSuitesForRef(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	ref := chi.URLParam(r, "ref")

	repo, err := d.Svc.GetRepo(r.Context(), full)
	if err != nil {
		respond.JSON(w, 200, map[string]any{"total_count": 0, "check_suites": []any{}})
		return
	}

	runs, _, _ := d.resolveRunsByRef(r, repo.ID, full, ref)

	suites := make([]any, len(runs))
	for i, run := range runs {
		suites[i] = map[string]any{
			"id":          run.ID,
			"status":      run.Status,
			"conclusion":  run.Conclusion,
			"head_branch": run.HeadBranch,
			"head_sha":    run.HeadSHA,
		}
	}
	respond.JSON(w, 200, map[string]any{"total_count": len(suites), "check_suites": suites})
}

// CombinedStatus handles GET /api/v3/repos/{owner}/{repo}/commits/{ref}/status
// Returns combined status from workflow runs for the given commit.
func (d *Deps) CombinedStatus(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	ref := chi.URLParam(r, "ref")

	repo, err := d.Svc.GetRepo(r.Context(), full)
	if err != nil {
		respond.JSON(w, 200, map[string]any{"state": "success", "statuses": []any{}, "total_count": 0})
		return
	}

	sha := ref
	if len(sha) != 40 && d.Svc.Git != nil {
		if resolved, err := d.Svc.Git.HeadSHA(r.Context(), full, ref); err == nil {
			sha = resolved
		}
	}

	runs, _, _ := d.resolveRunsByRef(r, repo.ID, full, ref)
	commitStatuses, _ := d.Svc.ListCommitStatuses(r.Context(), repo.ID, sha)

	var statuses []any
	overallState := "success"
	for _, run := range runs {
		state := "success"
		if run.Status != "completed" {
			state = "pending"
			if overallState == "success" {
				overallState = "pending"
			}
		} else if run.Conclusion != "success" && run.Conclusion != "" {
			state = "failure"
			overallState = "failure"
		}
		statuses = append(statuses, map[string]any{
			"state":       state,
			"context":     run.Name,
			"description": strings.ToTitle(run.Status),
		})
	}
	for _, cs := range commitStatuses {
		state := cs.State
		if overallState == "success" && (state == "pending" || state == "error" || state == "failure") {
			overallState = state
		} else if overallState == "pending" && (state == "error" || state == "failure") {
			overallState = state
		}
		statuses = append(statuses, map[string]any{
			"state":       state,
			"context":     cs.Context,
			"description": cs.Description,
			"target_url":  cs.TargetURL,
			"created_at":  cs.CreatedAt.Format(time.RFC3339),
			"updated_at":  cs.UpdatedAt.Format(time.RFC3339),
		})
	}
	if statuses == nil {
		statuses = []any{}
	}
	respond.JSON(w, 200, map[string]any{
		"state":       overallState,
		"statuses":    statuses,
		"total_count": len(statuses),
	})
}

// CreateCommitStatus handles POST /api/v3/repos/{owner}/{repo}/statuses/{sha}
func (d *Deps) CreateCommitStatus(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	sha := chi.URLParam(r, "sha")
	var body struct {
		State       string `json:"state"` // error, failure, pending, success
		TargetURL   string `json:"target_url"`
		Description string `json:"description"`
		Context     string `json:"context"`
	}
	if err := decodeBodyStrict(r, &body); err != nil {
		respond.ValidationFailed(w, "invalid JSON")
		return
	}
	if body.Context == "" {
		body.Context = "default"
	}
	if body.State != "error" && body.State != "failure" && body.State != "pending" && body.State != "success" {
		respond.ValidationFailed(w, "state must be one of: error, failure, pending, success")
		return
	}

	repo, err := d.Svc.GetRepo(r.Context(), full)
	if err != nil {
		respond.NotFound(w)
		return
	}
	user, err := d.Svc.GetCurrentUser(r.Context())
	if err != nil {
		respond.Unauthorized(w, "Bad credentials")
		return
	}
	cs := db.CommitStatus{
		RepositoryID: repo.ID,
		CommitSHA:    sha,
		State:        body.State,
		TargetURL:    body.TargetURL,
		Description:  body.Description,
		Context:      body.Context,
		CreatorID:    user.ID,
	}
	if err := d.Svc.CreateCommitStatus(r.Context(), &cs); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	cs.Creator = user
	respond.JSON(w, 201, map[string]any{
		"id":          cs.ID,
		"state":       cs.State,
		"description": cs.Description,
		"target_url":  cs.TargetURL,
		"context":     cs.Context,
		"created_at":  cs.CreatedAt.Format(time.RFC3339),
		"updated_at":  cs.UpdatedAt.Format(time.RFC3339),
		"creator":     transform.User(cs.Creator),
	})
}

// ListCommitStatuses handles GET /api/v3/repos/{owner}/{repo}/commits/{ref}/statuses
func (d *Deps) ListCommitStatuses(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	ref := chi.URLParam(r, "ref")

	repo, err := d.Svc.GetRepo(r.Context(), full)
	if err != nil {
		respond.JSON(w, 200, []any{})
		return
	}

	sha := ref
	if len(sha) != 40 && d.Svc.Git != nil {
		if resolved, err := d.Svc.Git.HeadSHA(r.Context(), full, ref); err == nil {
			sha = resolved
		}
	}

	commitStatuses, err := d.Svc.ListCommitStatuses(r.Context(), repo.ID, sha)
	if err != nil {
		respond.JSON(w, 200, []any{})
		return
	}

	var out []any
	for _, cs := range commitStatuses {
		out = append(out, map[string]any{
			"id":          cs.ID,
			"state":       cs.State,
			"description": cs.Description,
			"target_url":  cs.TargetURL,
			"context":     cs.Context,
			"created_at":  cs.CreatedAt.Format(time.RFC3339),
			"updated_at":  cs.UpdatedAt.Format(time.RFC3339),
			"creator":     transform.User(cs.Creator),
		})
	}
	if out == nil {
		out = []any{}
	}
	respond.JSON(w, 200, out)
}

// --- Notifications ---

// ListNotifications handles GET /api/v3/notifications
func (d *Deps) ListNotifications(w http.ResponseWriter, r *http.Request) {
	user, err := d.Svc.GetCurrentUser(r.Context())
	if err != nil {
		respond.Error(w, http.StatusUnauthorized, "Bad credentials")
		return
	}
	page, perPage := parsePagination(r)
	unreadOnly := true
	if allParam := strings.TrimSpace(r.URL.Query().Get("all")); allParam != "" {
		all, parseErr := strconv.ParseBool(allParam)
		if parseErr != nil {
			respond.ValidationFailed(w, "all must be a boolean")
			return
		}
		unreadOnly = !all
	}
	notifications, err := d.Svc.ListNotifications(r.Context(), user.ID, unreadOnly, 1000)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	out := make([]any, 0, len(notifications))
	for _, notification := range paginate(w, r, d.Svc.BaseURL, notifications, page, perPage) {
		item, buildErr := d.notificationJSON(r.Context(), notification)
		if buildErr == nil {
			out = append(out, item)
		}
	}
	respond.JSON(w, 200, out)
}

// MarkNotificationsRead handles PUT /api/v3/notifications
func (d *Deps) MarkNotificationsRead(w http.ResponseWriter, r *http.Request) {
	user, err := d.Svc.GetCurrentUser(r.Context())
	if err != nil {
		respond.Error(w, http.StatusUnauthorized, "Bad credentials")
		return
	}
	if err := d.Svc.MarkAllNotificationsRead(r.Context(), user.ID); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	w.WriteHeader(http.StatusResetContent)
}

func (d *Deps) notificationJSON(ctx context.Context, notification db.Notification) (map[string]any, error) {
	subject, err := d.notificationSubject(ctx, notification)
	if err != nil {
		return nil, err
	}
	var lastReadAt any
	if notification.LastReadAt != nil {
		lastReadAt = notification.LastReadAt.UTC().Format(time.RFC3339)
	}
	return map[string]any{
		"id":           notification.ID,
		"unread":       !notification.Read,
		"reason":       notificationReason(notification.Type),
		"updated_at":   notification.UpdatedAt.UTC().Format(time.RFC3339),
		"last_read_at": lastReadAt,
		"subject":      subject,
		"repository":   transform.Repo(notification.Repository),
	}, nil
}

func (d *Deps) notificationSubject(ctx context.Context, notification db.Notification) (map[string]any, error) {
	switch notification.SubjectType {
	case service.NotificationSubjectIssue:
		issue, err := d.Svc.GetIssueByID(ctx, notification.SubjectID)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"title":              issue.Title,
			"url":                fmt.Sprintf("%s/api/v3/repos/%s/issues/%d", d.Svc.BaseURL, issue.Repository.FullName, issue.Number),
			"latest_comment_url": notificationLatestCommentURL(notification.LatestCommentURL),
			"type":               "Issue",
		}, nil
	case service.NotificationSubjectPullRequest:
		pr, err := d.Svc.GetPRByID(ctx, notification.SubjectID)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"title":              pr.Title,
			"url":                fmt.Sprintf("%s/api/v3/repos/%s/pulls/%d", d.Svc.BaseURL, pr.Repository.FullName, pr.Number),
			"latest_comment_url": notificationLatestCommentURL(notification.LatestCommentURL),
			"type":               "PullRequest",
		}, nil
	case service.NotificationSubjectWorkflowRun:
		run, err := d.Svc.GetWorkflowRunByID(ctx, notification.SubjectID)
		if err != nil {
			return nil, err
		}
		title := run.Name
		if workflow, workflowErr := d.Svc.GetWorkflowByID(ctx, run.WorkflowID); workflowErr == nil && workflow.Name != "" {
			title = workflow.Name
		}
		return map[string]any{
			"title":              title,
			"url":                fmt.Sprintf("%s/api/v3/repos/%s/actions/runs/%d", d.Svc.BaseURL, notification.Repository.FullName, run.ID),
			"latest_comment_url": nil,
			"type":               "WorkflowRun",
		}, nil
	default:
		return nil, service.ErrNotFound
	}
}

func notificationReason(notificationType string) string {
	switch notificationType {
	case service.NotificationTypeMention:
		return "mention"
	case service.NotificationTypeAssignment:
		return "assign"
	case service.NotificationTypeReply:
		return "subscribed"
	case service.NotificationTypeWorkflowEvent:
		return "ci_activity"
	default:
		return "manual"
	}
}

func notificationLatestCommentURL(url string) any {
	if url == "" {
		return nil
	}
	return url
}

// ListIssueReactions handles GET /api/v3/repos/{owner}/{repo}/issues/{number}/reactions
// and GET /api/v3/repos/{owner}/{repo}/issues/comments/{comment_id}/reactions.
func (d *Deps) ListIssueReactions(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	content := strings.TrimSpace(r.URL.Query().Get("content"))

	var (
		reactions []db.Reaction
		err       error
	)
	if commentIDStr := chi.URLParam(r, "comment_id"); commentIDStr != "" {
		commentID, ok := mustIntParam(w, r, "comment_id")
		if !ok {
			return
		}
		reactions, err = d.Svc.ListCommentReactions(r.Context(), int64(commentID))
	} else {
		number, ok := mustIntParam(w, r, "number")
		if !ok {
			return
		}
		issue, getErr := d.Svc.GetIssue(r.Context(), full, number)
		if getErr != nil {
			respond.ServiceErrorRequest(r, w, getErr)
			return
		}
		reactions, err = d.Svc.ListIssueReactions(r.Context(), int64(issue.ID))
	}
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	if content != "" {
		filtered := make([]db.Reaction, 0, len(reactions))
		for _, reaction := range reactions {
			if reaction.Content == content {
				filtered = append(filtered, reaction)
			}
		}
		reactions = filtered
	}
	out := make([]any, len(reactions))
	for i, reaction := range reactions {
		out[i] = transform.Reaction(reaction)
	}
	respond.JSON(w, 200, out)
}

// CreateIssueReaction handles POST /api/v3/repos/{owner}/{repo}/issues/{number}/reactions
// and POST /api/v3/repos/{owner}/{repo}/issues/comments/{comment_id}/reactions
// Used by `gh issue comment --reaction` and direct `gh api` calls.
func (d *Deps) CreateIssueReaction(w http.ResponseWriter, r *http.Request) {
	user, err := d.Svc.GetCurrentUser(r.Context())
	if err != nil {
		respond.Unauthorized(w, "Bad credentials")
		return
	}
	var body struct {
		Content string `json:"content"`
	}
	if err := decodeBodyStrictOptional(r, &body); err != nil {
		respond.ValidationFailed(w, "invalid body")
		return
	}
	if body.Content == "" {
		body.Content = "+1"
	}

	// Determine whether this is an issue reaction or comment reaction
	var issueID *uint
	var commentID *uint

	full := repoFullName(r)
	if numStr := chi.URLParam(r, "comment_id"); numStr != "" {
		cid, ok := mustIntParam(w, r, "comment_id")
		if !ok {
			return
		}
		uid := uint(cid)
		commentID = &uid
	} else {
		num, ok := mustIntParam(w, r, "number")
		if !ok {
			return
		}
		issue, err := d.Svc.GetIssue(r.Context(), full, num)
		if err != nil {
			respond.ServiceErrorRequest(r, w, err)
			return
		}
		issueID = &issue.ID
	}

	reaction, err := d.Svc.CreateReaction(r.Context(), issueID, commentID, user.ID, body.Content)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, 201, map[string]any{
		"id":         reaction.ID,
		"content":    reaction.Content,
		"user":       transform.User(reaction.User),
		"created_at": reaction.CreatedAt.Format(time.RFC3339),
	})
}

// DeleteIssueReaction handles DELETE /api/v3/repos/{owner}/{repo}/issues/{number}/reactions/{reaction_id}
// and DELETE /api/v3/repos/{owner}/{repo}/issues/comments/{comment_id}/reactions/{reaction_id}
func (d *Deps) DeleteIssueReaction(w http.ResponseWriter, r *http.Request) {
	user, err := d.Svc.GetCurrentUser(r.Context())
	if err != nil {
		respond.Unauthorized(w, "Bad credentials")
		return
	}
	reactionID, ok := mustIntParam(w, r, "reaction_id")
	if !ok {
		return
	}

	if chi.URLParam(r, "comment_id") != "" {
		if _, ok := mustIntParam(w, r, "comment_id"); !ok {
			return
		}
	} else {
		if _, ok := mustIntParam(w, r, "number"); !ok {
			return
		}
	}

	if err := d.Svc.DeleteReaction(r.Context(), int64(reactionID), user.ID); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.NoContent(w)
}

// --- User Events ---

// ListUserReceivedEvents handles GET /api/v3/users/{username}/received_events
// Used by `gh status` to show repository activity.
// INTENTIONAL NO-OP: User events are not modeled in gh-server.
func (d *Deps) ListUserReceivedEvents(w http.ResponseWriter, r *http.Request) {
	respond.JSON(w, 200, []any{})
}

// ListUserEvents handles GET /api/v3/users/{username}/events
// INTENTIONAL NO-OP: User events are not modeled in gh-server.
func (d *Deps) ListUserEvents(w http.ResponseWriter, r *http.Request) {
	respond.JSON(w, 200, []any{})
}
