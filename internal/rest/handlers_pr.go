package rest

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/gitstore"
	"github.com/ngaut/agent-git-service/internal/rest/respond"
	"github.com/ngaut/agent-git-service/internal/rest/transform"
	"github.com/ngaut/agent-git-service/internal/service"
)

// --- Pull Requests ---

type prResponseMode int

const (
	prResponseModeFull prResponseMode = iota
	prResponseModeCreate
)

type prMergeability struct {
	mergeable      any
	rebaseable     any
	mergeableState string
}

func (d *Deps) requestedReviewersAndTeams(ctx context.Context, resolver transform.UserResolver, reqs []db.ReviewRequest) ([]any, []any) {
	reviewers := make([]any, 0, len(reqs))
	teams := make([]any, 0, len(reqs))
	for _, rq := range reqs {
		if rq.TeamSlug != "" {
			teams = append(teams, map[string]any{"slug": rq.TeamSlug})
			continue
		}
		if resolver != nil {
			if user, err := resolver(rq.Login); err == nil {
				reviewers = append(reviewers, transform.User(user))
				continue
			}
		}
		reviewers = append(reviewers, map[string]any{"login": rq.Login, "type": "User"})
	}
	return reviewers, teams
}

func (d *Deps) restPRMergeability(ctx context.Context, pr db.PullRequest) prMergeability {
	if d == nil || d.Svc == nil || pr.Merged || pr.State != db.StateOpen || pr.Repository.FullName == "" {
		return prMergeability{mergeableState: "unknown"}
	}
	if pr.HeadRepositoryID != 0 && pr.HeadRepositoryID != pr.RepositoryID {
		return prMergeability{mergeableState: "unknown"}
	}
	switch d.Svc.CanMergePR(ctx, pr.Repository.FullName, pr.BaseRef, pr.HeadRef) {
	case "MERGEABLE":
		return prMergeability{mergeable: true, rebaseable: true, mergeableState: "clean"}
	case "CONFLICTING":
		return prMergeability{mergeable: false, rebaseable: false, mergeableState: "dirty"}
	default:
		return prMergeability{mergeableState: "unknown"}
	}
}

// ListPRs handles GET /api/v3/repos/{owner}/{repo}/pulls
func (d *Deps) ListPRs(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	state := r.URL.Query().Get("state")
	if state == "" {
		state = "open"
	}
	prs, err := d.Svc.ListPRsFiltered(r.Context(), service.PRListFilter{
		RepoFullName: full,
		State:        state,
		Head:         r.URL.Query().Get("head"),
		Base:         r.URL.Query().Get("base"),
		Sort:         r.URL.Query().Get("sort"),
		Direction:    r.URL.Query().Get("direction"),
	})
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	page, perPage := parsePagination(r)
	paged := paginate(w, r, d.Svc.BaseURL, prs, page, perPage)
	if len(paged) == 0 {
		respond.JSON(w, 200, []any{})
		return
	}

	ctx := r.Context()

	// Collect IDs for batch queries.
	prIDs := make([]uint, len(paged))
	prNumbers := make([]int, len(paged))
	repoID := paged[0].RepositoryID
	for i, p := range paged {
		prIDs[i] = p.ID
		prNumbers[i] = p.Number
	}

	// Batch DB queries: 4 queries instead of 4×N.
	allReviewReqs, _ := d.Svc.ListReviewRequestsBatch(ctx, prIDs)
	allComments := d.Svc.CountPRCommentsBatch(ctx, repoID, prNumbers)
	allReviewComments := d.Svc.CountPRReviewCommentsBatch(ctx, prIDs)
	allReactions, _ := d.Svc.CountReactionsBatch(ctx, prIDs)
	reviewLogins := make([]string, 0, len(prIDs))
	for _, reqs := range allReviewReqs {
		for _, rq := range reqs {
			if rq.TeamSlug == "" {
				reviewLogins = append(reviewLogins, rq.Login)
			}
		}
	}
	reviewerResolver := d.batchUserResolver(ctx, reviewLogins)

	out := make([]any, len(paged))
	// Per-PR git ops (PRDiffStats + ListPRCommitsFromLoaded) are the dominant
	// cost on a list response — each is an exec.Command against git. Run
	// them concurrently across PRs with a bounded worker count so wall time
	// is max(per-PR) instead of sum(per-PR). Each goroutine writes only to
	// its own out[i], so no further synchronization is needed.
	var g errgroup.Group
	g.SetLimit(prListGitConcurrency)
	for i, pr := range paged {
		i, pr := i, pr
		g.Go(func() error {
			// Within one PR, diff-stats and commit-count are independent git
			// operations — run them concurrently and collect via inner group.
			var additions, deletions, changedFiles, commits int
			var inner errgroup.Group
			if pr.BaseSHA != "" && pr.HeadSHA != "" {
				inner.Go(func() error {
					additions, deletions, changedFiles = d.Svc.PRDiffStats(ctx, pr.Repository.FullName, pr.BaseSHA, pr.HeadSHA)
					return nil
				})
			}
			inner.Go(func() error {
				if prCommits, err := d.Svc.ListPRCommitsFromLoaded(ctx, pr); err == nil {
					commits = len(prCommits)
				}
				return nil
			})
			_ = inner.Wait()

			stats := transform.PRStats{
				Comments:       allComments[pr.Number],
				ReviewComments: allReviewComments[pr.ID],
				Commits:        commits,
				Additions:      additions,
				Deletions:      deletions,
				ChangedFiles:   changedFiles,
			}
			resolver := d.userResolver(ctx)
			if reviewerResolver != nil {
				resolver = reviewerResolver
			}
			assoc := d.authorAssociationChecks(ctx, pr.Repository)
			result := transform.PR(pr, resolver, assoc, allReactions[pr.ID], stats)
			reviewers, teams := d.requestedReviewersAndTeams(ctx, resolver, allReviewReqs[pr.ID])
			result["requested_reviewers"] = reviewers
			result["requested_teams"] = teams
			mergeability := d.restPRMergeability(ctx, pr)
			result["mergeable"] = mergeability.mergeable
			result["rebaseable"] = mergeability.rebaseable
			result["mergeable_state"] = mergeability.mergeableState

			var autoMerge any
			if pr.AutoMerge {
				autoMerge = map[string]any{
					"enabled_at":   pr.UpdatedAt.Format(time.RFC3339),
					"merge_method": strings.ToLower(pr.AutoMergeMethod),
				}
			}
			result["auto_merge"] = autoMerge

			out[i] = result
			return nil
		})
	}
	_ = g.Wait()
	respond.JSON(w, 200, out)
}

// prListGitConcurrency bounds how many PR list items run their per-PR git
// ops concurrently. Kept modest so a single request can't saturate the
// process's file-descriptor or subprocess budget.
const prListGitConcurrency = 8

// GetPR handles GET /api/v3/repos/{owner}/{repo}/pulls/{number}
// Supports Accept: application/vnd.github.v3.diff for raw diff output.
func (d *Deps) GetPR(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	num, ok := mustIntParam(w, r, "number")
	if !ok {
		return
	}

	// Handle diff format request
	accept := r.Header.Get("Accept")
	if strings.Contains(accept, "diff") || strings.HasSuffix(r.URL.Path, ".diff") {
		pr, err := d.Svc.GetPR(r.Context(), full, num)
		if err != nil {
			respond.ServiceErrorRequest(r, w, err)
			return
		}
		if pr.BaseSHA != "" && pr.HeadSHA != "" && d.Svc.Git != nil {
			diff, diffErr := d.Svc.Git.DiffRaw(r.Context(), full, pr.BaseSHA, pr.HeadSHA)
			if diffErr == nil {
				w.Header().Set("Content-Type", "text/plain; charset=utf-8")
				w.WriteHeader(200)
				w.Write([]byte(diff))
				return
			}
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(200)
		w.Write([]byte(""))
		return
	}

	pr, err := d.Svc.GetPR(r.Context(), full, num)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, 200, d.prResponse(r, pr, prResponseModeFull))
}

// UpdatePRBranch handles PUT /api/v3/repos/{owner}/{repo}/pulls/{number}/update-branch.
func (d *Deps) UpdatePRBranch(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	num, ok := mustIntParam(w, r, "number")
	if !ok {
		return
	}

	var body struct {
		ExpectedHeadSHA string `json:"expected_head_sha"`
	}
	if err := decodeBodyStrictOptional(r, &body); err != nil {
		respond.ValidationFailed(w, "invalid body")
		return
	}

	pr, err := d.Svc.GetPR(r.Context(), full, num)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	if pr.State != db.StateOpen || pr.Merged {
		respond.ValidationFailed(w, "pull request branch cannot be updated")
		return
	}
	if expected := strings.TrimSpace(body.ExpectedHeadSHA); expected != "" && !strings.EqualFold(expected, pr.HeadSHA) {
		respond.ValidationFailed(w, "expected_head_sha does not match current head sha")
		return
	}

	viewer, ok := service.UserFromContext(r.Context())
	if !ok {
		respond.NotFound(w)
		return
	}

	headRepo := pr.HeadRepository
	if headRepo.ID == 0 {
		headRepo = pr.Repository
	}
	if !d.requireRepoPermission(w, r, headRepo.ID, service.RepoPermissionWrite) {
		return
	}

	sha, err := d.Svc.UpdatePRBranch(r.Context(), gitstore.UpdatePRBranchOptions{
		FullName:     headRepo.FullName,
		BaseBranch:   pr.BaseRef,
		HeadBranch:   pr.HeadRef,
		Committer:    viewer.Login,
		Email:        viewer.Email,
		UpdateMethod: "MERGE",
	})
	if err != nil {
		respond.ValidationFailed(w, err.Error())
		return
	}
	if err := d.Svc.UpdatePRFields(r.Context(), pr.ID, map[string]any{"head_sha": sha}); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	updatedPR, err := d.Svc.GetPR(r.Context(), full, num)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	logErr(r.Context(), "UpdatePRBranch: webhook", d.Svc.DispatchWebhookEvent(
		r.Context(),
		updatedPR.RepositoryID,
		"pull_request",
		"synchronize",
		d.webhookPRPayload(r.Context(), updatedPR, "synchronize", d.prWithExtras(r, updatedPR)),
	))

	respond.JSON(w, http.StatusAccepted, map[string]any{
		"message": "Updating pull request branch.",
		"url":     transform.Base() + "/api/v3/repos/" + full + "/pulls/" + strconv.Itoa(num),
	})
}

// prWithExtras enriches a PR response with requested_reviewers and auto_merge.
func (d *Deps) prWithExtras(r *http.Request, pr db.PullRequest) map[string]any {
	return d.prResponse(r, pr, prResponseModeFull)
}

func (d *Deps) prWithCreateExtras(r *http.Request, pr db.PullRequest) map[string]any {
	return d.prResponse(r, pr, prResponseModeCreate)
}

func (d *Deps) prResponse(r *http.Request, pr db.PullRequest, mode prResponseMode) map[string]any {
	ctx := r.Context()

	reviewers := []any{}
	teams := []any{}
	if mode == prResponseModeFull {
		reqs, _ := d.Svc.ListReviewRequestsByPRID(ctx, pr.ID)
		reviewers, teams = d.requestedReviewersAndTeams(ctx, d.userResolver(ctx), reqs)
	}

	var autoMerge any
	if pr.AutoMerge {
		autoMerge = map[string]any{
			"enabled_at":   pr.UpdatedAt.Format(time.RFC3339),
			"merge_method": strings.ToLower(pr.AutoMergeMethod),
		}
	}

	var stats transform.PRStats
	var reactionCounts map[string]int64
	if mode == prResponseModeFull {
		stats.Comments = d.Svc.CountPRComments(ctx, pr.RepositoryID, pr.Number)
		stats.ReviewComments = d.Svc.CountPRReviewComments(ctx, pr.ID)
		if pr.BaseSHA != "" && pr.HeadSHA != "" {
			stats.Additions, stats.Deletions, stats.ChangedFiles = d.Svc.PRDiffStats(ctx, pr.Repository.FullName, pr.BaseSHA, pr.HeadSHA)
		}
		if prCommits, err := d.Svc.ListPRCommitsFromLoaded(ctx, pr); err == nil {
			stats.Commits = len(prCommits)
		}

		var err error
		reactionCounts, err = d.Svc.CountReactions(ctx, pr.ID, 0)
		if err != nil {
			logErr(r.Context(), "prResponse: reaction counts", err)
			reactionCounts = nil
		}
	}

	resolver := d.userResolver(ctx)
	assoc := d.authorAssociationChecks(ctx, pr.Repository)
	result := transform.PR(pr, resolver, assoc, reactionCounts, stats)
	result["requested_reviewers"] = reviewers
	result["requested_teams"] = teams
	mergeability := d.restPRMergeability(ctx, pr)
	result["mergeable"] = mergeability.mergeable
	result["rebaseable"] = mergeability.rebaseable
	result["mergeable_state"] = mergeability.mergeableState
	result["auto_merge"] = autoMerge
	return result
}

// CreatePR handles POST /api/v3/repos/{owner}/{repo}/pulls
func (d *Deps) CreatePR(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	var body struct {
		Title               string `json:"title"`
		Body                string `json:"body"`
		Head                string `json:"head"`
		Base                string `json:"base"`
		Draft               bool   `json:"draft"`
		MaintainerCanModify bool   `json:"maintainer_can_modify"`
	}
	if err := decodeBodyStrict(r, &body); err != nil {
		respond.ValidationFailed(w, "invalid body")
		return
	}
	u, err := d.Svc.GetCurrentUser(r.Context())
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	// Parse cross-repo head format: "user:branch" or "org:branch"
	headRef := body.Head
	headRepoFullName := full // default: same repo
	if parts := strings.SplitN(body.Head, ":", 2); len(parts) == 2 {
		headOwner := parts[0]
		headRef = parts[1]
		// Assume the GitHub-compatible owner:branch form targets owner/repoName.
		// The service resolves the repo once and falls back safely if it does not exist.
		baseParts := strings.SplitN(full, "/", 2)
		if len(baseParts) == 2 {
			headRepoFullName = headOwner + "/" + baseParts[1]
		}
	}

	pr, err := d.Svc.CreatePR(r.Context(), service.CreatePRInput{
		RepoFullName:        full,
		HeadRepoFullName:    headRepoFullName,
		Title:               body.Title,
		Body:                body.Body,
		HeadRef:             headRef,
		BaseRef:             body.Base,
		Draft:               body.Draft,
		MaintainerCanModify: body.MaintainerCanModify,
		AuthorLogin:         u.Login,
	})
	// GitHub API returns 422 for creation failures — intentional compatibility.
	if err != nil {
		respond.ValidationFailed(w, err.Error())
		return
	}
	logErr(r.Context(), "CreatePR: webhook", d.Svc.DispatchWebhookEvent(r.Context(), pr.RepositoryID, "pull_request", "opened", d.webhookPRPayload(r.Context(), pr, "opened", d.prWithCreateExtras(r, pr))))
	respond.JSON(w, 201, d.prWithCreateExtras(r, pr))
}

// UpdatePR handles PATCH /api/v3/repos/{owner}/{repo}/pulls/{number}
func (d *Deps) UpdatePR(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	num, ok := mustIntParam(w, r, "number")
	if !ok {
		return
	}
	var body struct {
		Title *string `json:"title"`
		Body  *string `json:"body"`
		State *string `json:"state"`
		Base  *string `json:"base"`
		Draft *bool   `json:"draft"`
	}
	decodeBody(r, &body)
	pr, err := d.Svc.UpdatePR(r.Context(), full, num, service.UpdatePRInput{
		Title: body.Title, Body: body.Body, State: body.State, BaseRef: body.Base, Draft: body.Draft,
	})
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	action := ""
	if body.State != nil {
		switch *body.State {
		case db.StateClosed:
			action = "closed"
		case db.StateOpen:
			action = "reopened"
		}
	}
	if action == "" && (body.Title != nil || body.Body != nil || body.Base != nil || body.Draft != nil) {
		action = "edited"
	}
	if action != "" {
		logErr(r.Context(), "UpdatePR: webhook", d.Svc.DispatchWebhookEvent(r.Context(), pr.RepositoryID, "pull_request", action, d.webhookPRPayload(r.Context(), pr, action, d.prWithExtras(r, pr))))
	}
	respond.JSON(w, 200, d.prWithExtras(r, pr))
}

// MergePR handles PUT /api/v3/repos/{owner}/{repo}/pulls/{number}/merge
func (d *Deps) MergePR(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	num, ok := mustIntParam(w, r, "number")
	if !ok {
		return
	}
	var body struct {
		CommitTitle   string `json:"commit_title"`
		CommitMessage string `json:"commit_message"`
		MergeMethod   string `json:"merge_method"` // merge, squash, rebase
	}
	if err := decodeBodyStrictOptional(r, &body); err != nil {
		respond.ValidationFailed(w, "invalid body")
		return
	}
	if body.MergeMethod == "" {
		body.MergeMethod = "merge"
	}
	pr, err := d.Svc.MergePR(r.Context(), full, num, body.MergeMethod, body.CommitTitle)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	logErr(r.Context(), "MergePR: webhook", d.Svc.DispatchWebhookEvent(r.Context(), pr.RepositoryID, "pull_request", "closed", d.webhookPRPayload(r.Context(), pr, "closed", d.prWithExtras(r, pr))))
	respond.JSON(w, 200, map[string]any{
		"sha":     pr.MergeCommitSHA,
		"merged":  true,
		"message": "Pull Request successfully merged",
	})
}

// ListPRCommits handles GET /api/v3/repos/{owner}/{repo}/pulls/{number}/commits
func (d *Deps) ListPRCommits(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	num, ok := mustIntParam(w, r, "number")
	if !ok {
		return
	}
	commits, err := d.Svc.ListPRCommits(r.Context(), full, num)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, 200, commits)
}

// ListPRFiles handles GET /api/v3/repos/{owner}/{repo}/pulls/{number}/files
func (d *Deps) ListPRFiles(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	num, ok := mustIntParam(w, r, "number")
	if !ok {
		return
	}
	files, err := d.Svc.ListPRFiles(r.Context(), full, num)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, 200, files)
}

// AddRequestedReviewers handles POST /api/v3/repos/{owner}/{repo}/pulls/{number}/requested_reviewers
func (d *Deps) AddRequestedReviewers(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	num, ok := mustIntParam(w, r, "number")
	if !ok {
		return
	}
	pr, err := d.Svc.GetPR(r.Context(), full, num)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	var body struct {
		Reviewers     []string `json:"reviewers"`
		TeamReviewers []string `json:"team_reviewers"`
	}
	if err := decodeBodyStrict(r, &body); err != nil {
		respond.ValidationFailed(w, "invalid body")
		return
	}
	for _, login := range body.Reviewers {
		logErr(r.Context(), "requestReview", d.Svc.RequestReview(r.Context(), pr.ID, login))
	}
	for _, slug := range body.TeamReviewers {
		logErr(r.Context(), "requestTeamReview", d.Svc.RequestTeamReview(r.Context(), pr.ID, slug))
	}
	respond.JSON(w, 200, d.prWithExtras(r, pr))
}

// RemoveRequestedReviewers handles DELETE /api/v3/repos/{owner}/{repo}/pulls/{number}/requested_reviewers
func (d *Deps) RemoveRequestedReviewers(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	num, ok := mustIntParam(w, r, "number")
	if !ok {
		return
	}
	pr, err := d.Svc.GetPR(r.Context(), full, num)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	var body struct {
		Reviewers     []string `json:"reviewers"`
		TeamReviewers []string `json:"team_reviewers"`
	}
	decodeBody(r, &body)
	for _, login := range body.Reviewers {
		logErr(r.Context(), "removeReviewRequest", d.Svc.RemoveReviewRequest(r.Context(), pr.ID, login))
	}
	for _, slug := range body.TeamReviewers {
		logErr(r.Context(), "removeTeamReview", d.Svc.RemoveTeamReviewRequest(r.Context(), pr.ID, slug))
	}
	respond.JSON(w, 200, d.prWithExtras(r, pr))
}

// ListReviewRequests handles GET /api/v3/repos/{owner}/{repo}/pulls/{number}/requested_reviewers
func (d *Deps) ListReviewRequests(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	num, ok := mustIntParam(w, r, "number")
	if !ok {
		return
	}
	pr, err := d.Svc.GetPR(r.Context(), full, num)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	reqs, err := d.Svc.ListReviewRequests(r.Context(), pr.ID)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	users, teams := d.requestedReviewersAndTeams(r.Context(), d.userResolver(r.Context()), reqs)
	respond.JSON(w, 200, map[string]any{"users": users, "teams": teams})
}

// CreatePRReview handles POST /api/v3/repos/{owner}/{repo}/pulls/{number}/reviews
func (d *Deps) CreatePRReview(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	num, ok := mustIntParam(w, r, "number")
	if !ok {
		return
	}
	pr, err := d.Svc.GetPR(r.Context(), full, num)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	var body struct {
		Body     string `json:"body"`
		Event    string `json:"event"`
		CommitID string `json:"commit_id"`
	}
	if err := decodeBodyStrictOptional(r, &body); err != nil {
		respond.ValidationFailed(w, "invalid body")
		return
	}
	if body.Event == "" {
		body.Event = "COMMENTED"
	}
	u, err := d.Svc.GetCurrentUser(r.Context())
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	review, err := d.Svc.AddPRReview(r.Context(), pr.ID, u.Login, body.Event, body.Body, body.CommitID)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, 200, transform.PRReview(review, full, num))
}

// UpdatePRReview handles PUT /api/v3/repos/{owner}/{repo}/pulls/{number}/reviews/{review_id}
func (d *Deps) UpdatePRReview(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	num, ok := mustIntParam(w, r, "number")
	if !ok {
		return
	}
	reviewID, ok := mustIntParam(w, r, "review_id")
	if !ok {
		return
	}
	var body struct {
		Body string `json:"body"`
	}
	decodeBody(r, &body)
	if body.Body == "" {
		respond.ValidationFailed(w, "body is required")
		return
	}

	review, err := d.Svc.UpdatePRReview(r.Context(), uint(reviewID), body.Body)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, 200, transform.PRReview(review, full, num))
}

// ListPRReviews handles GET /api/v3/repos/{owner}/{repo}/pulls/{number}/reviews
func (d *Deps) ListPRReviews(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	num, ok := mustIntParam(w, r, "number")
	if !ok {
		return
	}
	page, perPage := parsePagination(r)
	pr, err := d.Svc.GetPR(r.Context(), full, num)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	reviews, err := d.Svc.ListPRReviews(r.Context(), pr.ID)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	out := make([]any, len(reviews))
	for i, rv := range reviews {
		out[i] = transform.PRReview(rv, full, num)
	}
	respond.JSON(w, 200, paginate(w, r, d.Svc.BaseURL, out, page, perPage))
}

// GetPRMerged handles GET /api/v3/repos/{owner}/{repo}/pulls/{number}/merge (is merged check)
func (d *Deps) GetPRMerged(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	num, ok := mustIntParam(w, r, "number")
	if !ok {
		return
	}
	pr, err := d.Svc.GetPR(r.Context(), full, num)
	if err != nil || !pr.Merged {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// SubmitPRReview handles POST /api/v3/repos/{owner}/{repo}/pulls/{number}/reviews/{review_id}/events
func (d *Deps) SubmitPRReview(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	num, ok := mustIntParam(w, r, "number")
	if !ok {
		return
	}
	reviewID, ok := mustIntParam(w, r, "review_id")
	if !ok {
		return
	}
	var body struct {
		Event string `json:"event"`
		Body  string `json:"body"`
	}
	if err := decodeBodyStrict(r, &body); err != nil {
		respond.ValidationFailed(w, "invalid body")
		return
	}
	if body.Event == "" {
		respond.ValidationFailed(w, "event is required")
		return
	}
	review, err := d.Svc.SubmitPRReview(r.Context(), uint(reviewID), body.Event, body.Body)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, 200, transform.PRReview(review, full, num))
}

// ListPRReviewComments handles GET /api/v3/repos/{owner}/{repo}/pulls/{number}/comments
func (d *Deps) ListPRReviewComments(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	num, ok := mustIntParam(w, r, "number")
	if !ok {
		return
	}
	page, perPage := parsePagination(r)
	pr, err := d.Svc.GetPR(r.Context(), full, num)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	comments, err := d.Svc.ListPRReviewComments(r.Context(), pr.ID)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	out := make([]any, len(comments))
	for i, c := range comments {
		out[i] = transform.PRReviewComment(c, full, num)
	}
	respond.JSON(w, 200, paginate(w, r, d.Svc.BaseURL, out, page, perPage))
}

// CreatePRReviewComment handles POST /api/v3/repos/{owner}/{repo}/pulls/{number}/comments
func (d *Deps) CreatePRReviewComment(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	num, ok := mustIntParam(w, r, "number")
	if !ok {
		return
	}
	pr, err := d.Svc.GetPR(r.Context(), full, num)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	var body struct {
		Body     string `json:"body"`
		CommitID string `json:"commit_id"`
		Path     string `json:"path"`
		Line     int    `json:"line"`
		Side     string `json:"side"`
	}
	if err := decodeBodyStrict(r, &body); err != nil {
		respond.ValidationFailed(w, "invalid body")
		return
	}
	if body.Side == "" {
		body.Side = "RIGHT" // Default to right
	}
	u, err := d.Svc.GetCurrentUser(r.Context())
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	c := db.PRReviewComment{
		AuthorLogin: u.Login,
		Body:        db.LargeText(body.Body),
		CommitID:    body.CommitID,
		Path:        body.Path,
		Line:        body.Line,
		Side:        body.Side,
	}
	if err := d.Svc.CreatePRReviewComment(r.Context(), pr.ID, &c); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, 201, transform.PRReviewComment(c, full, num))
}

// MarkPRReadyForReview handles PUT /api/v3/repos/{owner}/{repo}/pulls/{number}/ready_for_review
func (d *Deps) MarkPRReadyForReview(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	num, ok := mustIntParam(w, r, "number")
	if !ok {
		return
	}
	pr, err := d.Svc.GetPR(r.Context(), full, num)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	if err := d.Svc.MarkPRReadyForReview(r.Context(), pr.ID); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	// Reload to get updated state
	pr, err = d.Svc.GetPR(r.Context(), full, num)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, 200, d.prWithExtras(r, pr))
}

// ReplyToPRReviewComment handles POST /api/v3/repos/{owner}/{repo}/pulls/{number}/comments/{comment_id}/replies
func (d *Deps) ReplyToPRReviewComment(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	num, ok := mustIntParam(w, r, "number")
	if !ok {
		return
	}
	commentID, ok := mustIntParam(w, r, "comment_id")
	if !ok {
		return
	}
	pr, err := d.Svc.GetPR(r.Context(), full, num)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	var body struct {
		Body string `json:"body"`
	}
	if err := decodeBodyStrict(r, &body); err != nil {
		respond.ValidationFailed(w, "invalid body")
		return
	}
	u, err := d.Svc.GetCurrentUser(r.Context())
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	reply, err := d.Svc.ReplyToPRReviewComment(r.Context(), pr.ID, uint(commentID), body.Body, u.Login)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, 201, transform.PRReviewComment(reply, full, num))
}

// UpdatePRReviewComment handles PATCH /api/v3/repos/{owner}/{repo}/pulls/comments/{comment_id}
func (d *Deps) UpdatePRReviewComment(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	commentID, ok := mustIntParam(w, r, "comment_id")
	if !ok {
		return
	}
	var body struct {
		Body string `json:"body"`
	}
	decodeBody(r, &body)
	c, err := d.Svc.UpdatePRReviewComment(r.Context(), uint(commentID), body.Body)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	// Get the PR number for the response URLs
	pr, _ := d.Svc.ReloadPR(r.Context(), c.PullRequestID)
	prNum := pr.Number
	respond.JSON(w, 200, transform.PRReviewComment(c, full, prNum))
}

// ResolvePRReviewComment handles PUT /api/v3/repos/{owner}/{repo}/pulls/{number}/comments/{comment_id}/resolve
func (d *Deps) ResolvePRReviewComment(w http.ResponseWriter, r *http.Request) {
	d.toggleReviewCommentResolution(w, r, true)
}

// UnresolvePRReviewComment handles PUT /api/v3/repos/{owner}/{repo}/pulls/{number}/comments/{comment_id}/unresolve
func (d *Deps) UnresolvePRReviewComment(w http.ResponseWriter, r *http.Request) {
	d.toggleReviewCommentResolution(w, r, false)
}

func (d *Deps) toggleReviewCommentResolution(w http.ResponseWriter, r *http.Request, resolve bool) {
	full := repoFullName(r)
	num, ok := mustIntParam(w, r, "number")
	if !ok {
		return
	}
	commentID, ok := mustIntParam(w, r, "comment_id")
	if !ok {
		return
	}
	c, err := d.Svc.GetPRReviewComment(r.Context(), uint(commentID))
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	pr, err := d.Svc.GetPR(r.Context(), full, num)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	// Resource scoping: refuse to mutate a comment that belongs to a
	// different PR or repo than the one named in the route. Returning
	// 404 (not 403) avoids leaking the existence of foreign comments.
	if c.PullRequestID != pr.ID {
		respond.NotFound(w)
		return
	}
	if resolve {
		err = d.Svc.ResolvePRReviewThread(r.Context(), uint(commentID))
	} else {
		err = d.Svc.UnresolvePRReviewThread(r.Context(), uint(commentID))
	}
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	c.IsResolved = resolve
	respond.JSON(w, 200, transform.PRReviewComment(c, full, num))
}

// DeletePRReviewComment handles DELETE /api/v3/repos/{owner}/{repo}/pulls/comments/{comment_id}
func (d *Deps) DeletePRReviewComment(w http.ResponseWriter, r *http.Request) {
	commentID, ok := mustIntParam(w, r, "comment_id")
	if !ok {
		return
	}
	if err := d.Svc.DeletePRReviewComment(r.Context(), uint(commentID)); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GetPRReview handles GET /api/v3/repos/{owner}/{repo}/pulls/{number}/reviews/{review_id}
func (d *Deps) GetPRReview(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	num, ok := mustIntParam(w, r, "number")
	if !ok {
		return
	}
	reviewID, ok := mustIntParam(w, r, "review_id")
	if !ok {
		return
	}
	review, err := d.Svc.GetPRReview(r.Context(), uint(reviewID))
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, 200, transform.PRReview(review, full, num))
}

// ListReviewCommentsForReview handles GET /api/v3/repos/{owner}/{repo}/pulls/{number}/reviews/{review_id}/comments
func (d *Deps) ListReviewCommentsForReview(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	num, ok := mustIntParam(w, r, "number")
	if !ok {
		return
	}
	reviewID, ok := mustIntParam(w, r, "review_id")
	if !ok {
		return
	}
	comments, err := d.Svc.ListReviewCommentsForReview(r.Context(), uint(reviewID))
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	out := make([]any, len(comments))
	for i, c := range comments {
		out[i] = transform.PRReviewComment(c, full, num)
	}
	respond.JSON(w, 200, out)
}

// DismissPRReview handles PUT /api/v3/repos/{owner}/{repo}/pulls/{number}/reviews/{review_id}/dismissals
func (d *Deps) DismissPRReview(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	num, ok := mustIntParam(w, r, "number")
	if !ok {
		return
	}
	reviewID, ok := mustIntParam(w, r, "review_id")
	if !ok {
		return
	}
	var body struct {
		Message string `json:"message"`
	}
	if err := decodeBodyStrictOptional(r, &body); err != nil {
		respond.ValidationFailed(w, "invalid body")
		return
	}
	review, err := d.Svc.DismissPRReview(r.Context(), uint(reviewID), body.Message)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, 200, transform.PRReview(review, full, num))
}

// DeletePRReview handles DELETE /api/v3/repos/{owner}/{repo}/pulls/{number}/reviews/{review_id}
func (d *Deps) DeletePRReview(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	num, ok := mustIntParam(w, r, "number")
	if !ok {
		return
	}
	reviewID, ok := mustIntParam(w, r, "review_id")
	if !ok {
		return
	}
	review, err := d.Svc.GetPRReview(r.Context(), uint(reviewID))
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	if err := d.Svc.DeletePRReview(r.Context(), uint(reviewID)); err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	respond.JSON(w, 200, transform.PRReview(review, full, num))
}
