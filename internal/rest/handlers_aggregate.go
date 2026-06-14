package rest

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/rest/respond"
	"github.com/ngaut/agent-git-service/internal/rest/transform"
	"github.com/ngaut/agent-git-service/internal/service"
)

const (
	defaultWikiBatchBodyLimit = 20_000
	maxWikiBatchBodyLimit     = 100_000
	maxWikiBatchSlugs         = 50
)

// GetViewerSummary handles GET /api/v3/viewer/summary.
func (d *Deps) GetViewerSummary(w http.ResponseWriter, r *http.Request) {
	viewer, err := d.Svc.GetCurrentUser(r.Context())
	if err != nil {
		respond.Error(w, http.StatusUnauthorized, "Bad credentials")
		return
	}
	include := parseIncludeSet(r, []string{"user", "orgs", "repositories", "invitations", "agent_bindings"})
	out := map[string]any{}

	if include["user"] {
		out["user"] = transform.UserPrivate(viewer)
	}
	if include["orgs"] {
		orgs, err := d.Svc.ListOrgs(r.Context())
		if err != nil {
			respond.ServiceErrorRequest(r, w, err)
			return
		}
		items := make([]any, 0, len(orgs))
		for _, org := range orgs {
			row := transform.User(org)
			if membership, err := d.Svc.GetOrgMembership(r.Context(), org.ID, viewer.Login); err == nil {
				row["role"] = membership.Role
				row["state"] = membership.State
				row["permissions"] = map[string]any{
					"manage_members": membership.Role == "admin",
					"manage_repos":   membership.Role == "admin",
				}
			}
			items = append(items, row)
		}
		out["organizations"] = map[string]any{
			"total_count": len(orgs),
			"items":       items,
		}
	}
	if include["repositories"] {
		page, perPage := parsePagination(r)
		repos, err := d.Svc.ListViewerRepos(r.Context())
		if err != nil {
			respond.ServiceErrorRequest(r, w, err)
			return
		}
		repoAffiliation, err := parseRepoAffiliationSet(r.URL.Query().Get("repo_affiliation"))
		if err != nil {
			respond.ValidationFailed(w, err.Error())
			return
		}
		repos = filterViewerReposByAffiliation(repos, viewer, repoAffiliation)
		paged := paginate(w, r, d.Svc.BaseURL, repos, page, perPage)
		items := make([]any, 0, len(paged))
		for _, row := range paged {
			stats := d.repoStats(r, row.Repository)
			stats.HasPermissions = true
			stats.Permissions = repoPermissionsFor(row.Permission)
			item := transform.Repo(row.Repository, stats)
			item["permission"] = row.Permission.String()
			items = append(items, item)
		}
		out["repositories"] = map[string]any{
			"total_count": len(repos),
			"items":       items,
		}
	}
	if include["invitations"] {
		repoInvs, err := d.Svc.ListUserInvitations(r.Context(), viewer.ID)
		if err != nil {
			respond.ServiceErrorRequest(r, w, err)
			return
		}
		orgInvs, err := d.Svc.ListUserOrganizationInvitations(r.Context(), viewer.ID)
		if err != nil {
			respond.ServiceErrorRequest(r, w, err)
			return
		}
		repoItems := make([]any, 0, len(repoInvs))
		for _, inv := range repoInvs {
			repoItems = append(repoItems, repositoryInvitationJSON(inv))
		}
		orgItems := make([]any, 0, len(orgInvs))
		for _, inv := range orgInvs {
			orgItems = append(orgItems, organizationInvitationJSON(inv))
		}
		out["invitations"] = map[string]any{
			"total_count": len(repoInvs) + len(orgInvs),
			"repositories": map[string]any{
				"total_count": len(repoInvs),
				"items":       repoItems,
			},
			"organizations": map[string]any{
				"total_count": len(orgInvs),
				"items":       orgItems,
			},
		}
	}
	if include["agent_bindings"] {
		agents, err := d.Svc.ListBoundAgents(r.Context(), viewer.ID)
		if err != nil {
			respond.ServiceErrorRequest(r, w, err)
			return
		}
		items := make([]any, 0, len(agents))
		for _, agent := range agents {
			items = append(items, boundAgentJSON(agent))
		}
		out["agent_bindings"] = map[string]any{
			"total_count": len(agents),
			"items":       items,
		}
	}

	respond.JSON(w, http.StatusOK, out)
}

// GetOrgManagementSummary handles GET /api/v3/orgs/{org}/management-summary.
func (d *Deps) GetOrgManagementSummary(w http.ResponseWriter, r *http.Request) {
	org := d.mustGetOrg(w, r)
	if org == nil {
		return
	}
	if !d.requireOrgAdmin(w, r, org) {
		return
	}
	include := parseIncludeSet(r, []string{"org", "viewer", "repos", "members", "invitations", "teams", "outside_collaborators"})
	out := map[string]any{}

	if include["org"] {
		out["organization"] = transform.User(*org)
	}
	if include["viewer"] {
		viewer, err := d.Svc.GetCurrentUser(r.Context())
		if err != nil {
			respond.Error(w, http.StatusUnauthorized, "Bad credentials")
			return
		}
		membership, err := d.Svc.GetOrgMembership(r.Context(), org.ID, viewer.Login)
		if err != nil {
			respond.ServiceErrorRequest(r, w, err)
			return
		}
		out["viewer"] = map[string]any{
			"user":  transform.User(viewer),
			"role":  membership.Role,
			"state": membership.State,
			"permissions": map[string]any{
				"manage_members": true,
				"manage_repos":   true,
				"manage_teams":   true,
			},
		}
	}
	counts := map[string]int{}
	if include["repos"] {
		repos, err := d.Svc.ListUserRepos(r.Context(), org.Login)
		if err != nil {
			respond.ServiceErrorRequest(r, w, err)
			return
		}
		items := make([]any, 0, len(repos))
		for _, repo := range repos {
			items = append(items, transform.Repo(repo, d.repoStats(r, repo)))
		}
		counts["repos"] = len(repos)
		out["repositories"] = items
	}
	if include["members"] {
		members, err := d.Svc.ListOrgMembers(r.Context(), org.ID, "")
		if err != nil {
			respond.ServiceErrorRequest(r, w, err)
			return
		}
		items := make([]any, 0, len(members))
		for _, member := range members {
			items = append(items, orgMemberSummaryJSON(member))
		}
		counts["members"] = len(members)
		out["members"] = items
	}
	if include["invitations"] {
		invs, err := d.Svc.ListOrganizationInvitations(r.Context(), org.ID)
		if err != nil {
			respond.ServiceErrorRequest(r, w, err)
			return
		}
		items := make([]any, 0, len(invs))
		for _, inv := range invs {
			items = append(items, organizationInvitationJSON(inv))
		}
		counts["invitations"] = len(invs)
		out["invitations"] = items
	}
	if include["teams"] {
		teams, err := d.Svc.ListOrgTeams(r.Context(), org.ID)
		if err != nil {
			respond.ServiceErrorRequest(r, w, err)
			return
		}
		items := make([]any, 0, len(teams))
		for _, team := range teams {
			team.Organization = *org
			items = append(items, transform.Team(team))
		}
		counts["teams"] = len(teams)
		out["teams"] = items
	}
	if include["outside_collaborators"] {
		rows, err := d.Svc.ListOutsideCollaborators(r.Context(), org.ID)
		if err != nil {
			respond.ServiceErrorRequest(r, w, err)
			return
		}
		items := make([]any, 0, len(rows))
		for _, row := range rows {
			user := transform.User(row.User)
			user["organization_member"] = false
			user["outside_collaborator"] = true
			user["created_at"] = row.CreatedAt.UTC().Format(time.RFC3339)
			items = append(items, user)
		}
		counts["outside_collaborators"] = len(rows)
		out["outside_collaborators"] = items
	}
	out["counts"] = counts
	respond.JSON(w, http.StatusOK, out)
}

// GetRepoSummary handles GET /api/v3/repos/{owner}/{repo}/summary.
func (d *Deps) GetRepoSummary(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	repo, err := d.Svc.GetRepo(r.Context(), full)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	include := parseIncludeSet(r, []string{"repo", "viewer", "counts", "labels", "wiki", "agents"})
	out := map[string]any{}

	if include["repo"] {
		out["repository"] = transform.Repo(repo, d.repoStats(r, repo))
	}
	if include["viewer"] {
		out["viewer"] = d.repoViewerSummary(r, repo)
	}
	if include["counts"] {
		aggregates := d.Svc.LoadRepoAggregates(r.Context(), repo.ID)
		out["counts"] = map[string]any{
			"open_issues": aggregates.OpenIssuesCount,
			"forks":       aggregates.ForksCount,
			"stargazers":  aggregates.StargazersCount,
		}
	}
	if include["labels"] {
		labels, err := d.Svc.ListLabels(r.Context(), full)
		if err != nil {
			respond.ServiceErrorRequest(r, w, err)
			return
		}
		items := make([]any, 0, len(labels))
		for _, label := range labels {
			items = append(items, transform.Label(label))
		}
		out["labels"] = map[string]any{
			"total_count": len(labels),
			"items":       items,
		}
	}
	if include["wiki"] {
		pages, err := d.Svc.ListWikiPages(r.Context(), full, service.ListWikiPagesOptions{Recursive: true})
		if err != nil && !errors.Is(err, service.ErrNotFound) {
			respond.ServiceErrorRequest(r, w, err)
			return
		}
		items := make([]any, 0, len(pages))
		for _, page := range pages {
			items = append(items, transform.WikiPageSummary(full, page))
		}
		out["wiki"] = map[string]any{
			"total_count": len(pages),
			"pages":       items,
		}
	}
	if include["agents"] {
		out["agents"] = d.repoVisibleAgentBindings(r)
	}

	respond.JSON(w, http.StatusOK, out)
}

// GetIssueThread handles GET /api/v3/repos/{owner}/{repo}/issues/{number}/thread.
func (d *Deps) GetIssueThread(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	num, ok := mustIntParam(w, r, "number")
	if !ok {
		return
	}
	include := parseIncludeSet(r, []string{"issue", "comments", "viewer"})
	out := map[string]any{}
	issue, err := d.Svc.GetIssue(r.Context(), full, num)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	if include["issue"] {
		cc := d.Svc.CountIssueComments(r.Context(), issue.RepositoryID, issue.Number)
		reactionCounts, err := d.Svc.CountReactions(r.Context(), issue.ID, 0)
		if err != nil {
			respond.ServiceErrorRequest(r, w, err)
			return
		}
		assoc := d.authorAssociationChecks(r.Context(), issue.Repository)
		out["issue"] = transform.Issue(issue, d.userResolver(r.Context()), assoc, transform.IssueCounts{
			Comments:  cc,
			Reactions: reactionCounts,
		})
	}
	if include["comments"] {
		page := intQuery(r, "comments_page", 1)
		perPage := intQuery(r, "comments_per_page", 30)
		if perPage < 1 {
			perPage = 30
		}
		if perPage > 100 {
			perPage = 100
		}
		sortParam := normalizedQueryChoiceAny(r, []string{"comment_sort", "comments_sort"}, "created", []string{"created", "updated"})
		direction := normalizedQueryChoiceAny(r, []string{"comment_direction", "comments_direction"}, "asc", []string{"asc", "desc"})
		comments, total, err := d.Svc.ListIssueCommentsPaginated(r.Context(), full, num, "", sortParam, direction, page, perPage)
		if err != nil {
			respond.ServiceErrorRequest(r, w, err)
			return
		}
		commentIDs := make([]uint, len(comments))
		for i, comment := range comments {
			commentIDs[i] = comment.ID
		}
		allReactions, err := d.Svc.CountReactionsBatchForComments(r.Context(), commentIDs)
		if err != nil {
			respond.ServiceErrorRequest(r, w, err)
			return
		}
		assoc := d.authorAssociationChecks(r.Context(), issue.Repository)
		items := make([]any, 0, len(comments))
		for _, comment := range comments {
			items = append(items, transform.IssueComment(comment, assoc, allReactions[comment.ID]))
		}
		out["comments"] = map[string]any{
			"total_count": total,
			"page":        page,
			"per_page":    perPage,
			"items":       items,
		}
	}
	if include["viewer"] {
		out["viewer"] = d.repoViewerSummary(r, issue.Repository)
	}
	respond.JSON(w, http.StatusOK, out)
}

// BatchGetWikiPages handles POST /api/v3/repos/{owner}/{repo}/wiki/pages/batch.
func (d *Deps) BatchGetWikiPages(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	var req struct {
		Slugs     []string `json:"slugs"`
		Include   []string `json:"include"`
		BodyLimit int      `json:"body_limit"`
		Ref       string   `json:"ref"`
	}
	if err := decodeBodyStrict(r, &req); err != nil {
		respond.ValidationFailed(w, "invalid JSON")
		return
	}
	include := includeSliceSet(req.Include)
	if len(include) == 0 {
		include["body"] = true
		include["labels"] = true
	}
	if len(req.Slugs) == 0 {
		respond.ValidationFailed(w, "slugs are required")
		return
	}
	if len(req.Slugs) > maxWikiBatchSlugs {
		respond.ValidationFailed(w, fmt.Sprintf("slugs must contain at most %d entries", maxWikiBatchSlugs))
		return
	}
	bodyLimit := req.BodyLimit
	if bodyLimit == 0 {
		bodyLimit = defaultWikiBatchBodyLimit
	}
	if bodyLimit < 0 || bodyLimit > maxWikiBatchBodyLimit {
		respond.ValidationFailed(w, fmt.Sprintf("body_limit must be between 0 and %d", maxWikiBatchBodyLimit))
		return
	}

	items := make([]any, 0, len(req.Slugs))
	missing := make([]string, 0)
	seen := map[string]struct{}{}
	for _, rawSlug := range req.Slugs {
		slug := strings.TrimSpace(rawSlug)
		if slug == "" {
			continue
		}
		if _, ok := seen[slug]; ok {
			continue
		}
		seen[slug] = struct{}{}
		page, err := d.Svc.GetWikiPageAtRef(r.Context(), full, slug, strings.TrimSpace(req.Ref))
		if err != nil {
			if errors.Is(err, service.ErrNotFound) {
				missing = append(missing, slug)
				continue
			}
			respond.ServiceErrorRequest(r, w, err)
			return
		}
		item := transform.WikiPage(full, page)
		if !include["body"] {
			delete(item, "body")
		} else if body, ok := item["body"].(string); ok && bodyLimit > 0 {
			item["body"] = truncateRunes(body, bodyLimit)
			item["body_truncated"] = len([]rune(body)) > bodyLimit
		}
		if !include["labels"] {
			delete(item, "labels")
		}
		if include["backlinks"] || include["backlink_count"] {
			backlinks, err := d.Svc.ListWikiBacklinks(r.Context(), full, page.Slug)
			if err != nil {
				respond.ServiceErrorRequest(r, w, err)
				return
			}
			item["backlink_count"] = len(backlinks)
			if include["backlinks"] {
				backlinkItems := make([]any, 0, len(backlinks))
				for _, backlink := range backlinks {
					backlinkItems = append(backlinkItems, transform.WikiBacklink(full, backlink))
				}
				item["backlinks"] = backlinkItems
			}
		}
		items = append(items, item)
	}
	respond.JSON(w, http.StatusOK, map[string]any{
		"items":   items,
		"missing": missing,
		"limits": map[string]any{
			"max_slugs":  maxWikiBatchSlugs,
			"body_limit": bodyLimit,
		},
	})
}

// GetWikiCatalog handles GET /api/v3/repos/{owner}/{repo}/wiki/catalog.
func (d *Deps) GetWikiCatalog(w http.ResponseWriter, r *http.Request) {
	full := repoFullName(r)
	include := parseIncludeSet(r, []string{"tree", "pages", "labels"})
	path := strings.Trim(strings.TrimSpace(r.URL.Query().Get("path")), "/")
	recursive := strings.TrimSpace(r.URL.Query().Get("recursive")) != "false"
	pages, err := d.Svc.ListWikiPages(r.Context(), full, service.ListWikiPagesOptions{
		Path:          path,
		Recursive:     recursive,
		Labels:        parseCSV(r.URL.Query().Get("labels")),
		ExcludeLabels: parseCSV(r.URL.Query().Get("exclude_labels")),
	})
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	out := map[string]any{}
	if include["pages"] {
		items := make([]any, 0, len(pages))
		for _, page := range pages {
			items = append(items, transform.WikiPageSummary(full, page))
		}
		out["pages"] = items
	}
	if include["tree"] {
		tree, err := d.Svc.ListWikiTreeAtRef(r.Context(), full, path, "")
		if err != nil {
			respond.ServiceErrorRequest(r, w, err)
			return
		}
		items := make([]any, 0, len(tree))
		for _, entry := range tree {
			items = append(items, wikiTreeEntryJSON(full, entry))
		}
		out["tree"] = items
	}
	if include["labels"] {
		labelNames := collectWikiCatalogLabels(pages)
		out["labels"] = labelNames
	}
	out["total_count"] = len(pages)
	respond.JSON(w, http.StatusOK, out)
}

// GetNotificationsSummary handles GET /api/v3/notifications/summary.
func (d *Deps) GetNotificationsSummary(w http.ResponseWriter, r *http.Request) {
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
	reasonFilter := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("reason")))
	include := parseIncludeSet(r, []string{"subject", "repository"})
	includeComments := include["latest_comments"]
	commentLimit := intQuery(r, "latest_comments_limit", 3)
	if commentLimit < 1 {
		commentLimit = 1
	}
	if commentLimit > 20 {
		commentLimit = 20
	}

	notifications, err := d.Svc.ListNotifications(r.Context(), user.ID, unreadOnly, 1000)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}
	filtered := make([]db.Notification, 0, len(notifications))
	for _, notification := range notifications {
		if reasonFilter != "" && notificationReason(notification.Type) != reasonFilter {
			continue
		}
		filtered = append(filtered, notification)
	}
	paged := paginate(w, r, d.Svc.BaseURL, filtered, page, perPage)
	out := make([]any, 0, len(paged))
	for _, notification := range paged {
		item, buildErr := d.notificationJSON(r.Context(), notification)
		if buildErr != nil {
			continue
		}
		if !include["subject"] {
			delete(item, "subject")
		}
		if !include["repository"] {
			delete(item, "repository")
		}
		if includeComments && notification.SubjectType == service.NotificationSubjectIssue {
			issueNumber := notificationSubjectNumber(r.Context(), d, notification)
			if issueNumber > 0 {
				comments, _, err := d.Svc.ListIssueCommentsPaginated(r.Context(), notification.Repository.FullName, issueNumber, "", "updated", "desc", 1, commentLimit)
				if err == nil {
					item["latest_comments"] = latestCommentSummaries(comments)
				}
			}
		}
		out = append(out, item)
	}
	respond.JSON(w, http.StatusOK, out)
}

func (d *Deps) repoViewerSummary(r *http.Request, repo db.Repository) map[string]any {
	viewer, ok := service.UserFromContext(r.Context())
	if !ok || viewer.ID == 0 {
		if !repo.Private {
			return map[string]any{
				"authenticated": false,
				"permission":    service.RepoPermissionRead.String(),
				"permissions":   repoPermissionMapFor(service.RepoPermissionRead),
			}
		}
		return map[string]any{
			"authenticated": false,
			"permission":    service.RepoPermissionNone.String(),
			"permissions":   repoPermissionMapFor(service.RepoPermissionNone),
		}
	}
	perm, err := d.Svc.HasRepoAccess(r.Context(), repo.ID, viewer.ID)
	if err != nil {
		perm = service.RepoPermissionNone
	}
	return map[string]any{
		"authenticated": true,
		"user":          transform.User(viewer),
		"permission":    perm.String(),
		"permissions":   repoPermissionMapFor(perm),
	}
}

func (d *Deps) repoVisibleAgentBindings(r *http.Request) map[string]any {
	viewer, ok := service.UserFromContext(r.Context())
	if !ok || viewer.ID == 0 {
		return map[string]any{"total_count": 0, "items": []any{}}
	}
	agents, err := d.Svc.ListBoundAgents(r.Context(), viewer.ID)
	if err != nil {
		return map[string]any{"total_count": 0, "items": []any{}}
	}
	items := make([]any, 0, len(agents))
	for _, agent := range agents {
		items = append(items, boundAgentJSON(agent))
	}
	return map[string]any{"total_count": len(agents), "items": items}
}

func parseIncludeSet(r *http.Request, defaults []string) map[string]bool {
	raw := strings.TrimSpace(r.URL.Query().Get("include"))
	out := map[string]bool{}
	if raw == "" {
		for _, item := range defaults {
			out[item] = true
		}
		return out
	}
	for _, item := range parseCSV(raw) {
		out[item] = true
	}
	return out
}

func includeSliceSet(items []string) map[string]bool {
	out := map[string]bool{}
	for _, item := range items {
		key := strings.ToLower(strings.TrimSpace(item))
		if key != "" {
			out[key] = true
		}
	}
	return out
}

func parseCSV(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	seen := map[string]struct{}{}
	for _, part := range parts {
		item := strings.ToLower(strings.TrimSpace(part))
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func intQuery(r *http.Request, key string, fallback int) int {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return value
}

func normalizedQueryChoice(r *http.Request, key, fallback string, allowed []string) string {
	value := strings.ToLower(strings.TrimSpace(r.URL.Query().Get(key)))
	if value == "" {
		return fallback
	}
	for _, candidate := range allowed {
		if value == candidate {
			return value
		}
	}
	return fallback
}

func normalizedQueryChoiceAny(r *http.Request, keys []string, fallback string, allowed []string) string {
	for _, key := range keys {
		if strings.TrimSpace(r.URL.Query().Get(key)) == "" {
			continue
		}
		return normalizedQueryChoice(r, key, fallback, allowed)
	}
	return fallback
}

func parseRepoAffiliationSet(raw string) (map[string]bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	out := map[string]bool{}
	for _, item := range parseCSV(raw) {
		switch item {
		case "owner", "collaborator", "organization_member":
			out[item] = true
		default:
			return nil, fmt.Errorf("repo_affiliation must contain only owner, collaborator, or organization_member")
		}
	}
	return out, nil
}

func filterViewerReposByAffiliation(repos []service.RepoWithPermission, viewer db.User, affiliation map[string]bool) []service.RepoWithPermission {
	if len(affiliation) == 0 {
		return repos
	}
	out := make([]service.RepoWithPermission, 0, len(repos))
	for _, row := range repos {
		owner := row.Repository.Owner
		isOwner := owner.ID == viewer.ID
		isOrgRepo := owner.Type == db.TypeOrganization
		isCollaborator := !isOwner && !isOrgRepo
		if affiliation["owner"] && isOwner {
			out = append(out, row)
			continue
		}
		if affiliation["organization_member"] && isOrgRepo {
			out = append(out, row)
			continue
		}
		if affiliation["collaborator"] && isCollaborator {
			out = append(out, row)
		}
	}
	return out
}

func truncateRunes(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit])
}

func orgMemberSummaryJSON(member service.OrganizationMembershipView) map[string]any {
	row := transform.User(member.User)
	row["role"] = member.Role
	row["state"] = member.State
	return row
}

func wikiTreeEntryJSON(repoFullName string, entry service.WikiTreeEntry) map[string]any {
	out := map[string]any{
		"path":  entry.Path,
		"name":  entry.Name,
		"type":  entry.Kind,
		"kind":  entry.Kind,
		"sha":   entry.SHA,
		"size":  entry.Size,
		"title": entry.Title,
	}
	if entry.Slug != "" {
		out["slug"] = entry.Slug
		encodedSlug := url.PathEscape(entry.Slug)
		out["html_url"] = fmt.Sprintf("%s/%s/wiki/%s", transform.HTMLBase(), repoFullName, encodedSlug)
		out["url"] = fmt.Sprintf("%s/repos/%s/wiki/pages/%s", transform.APIBase(), repoFullName, encodedSlug)
	}
	return out
}

func collectWikiCatalogLabels(pages []service.WikiPageSummary) []string {
	seen := map[string]struct{}{}
	for _, page := range pages {
		for _, label := range page.Labels {
			name := strings.TrimSpace(label.Name)
			if name != "" {
				seen[name] = struct{}{}
			}
		}
	}
	labels := make([]string, 0, len(seen))
	for name := range seen {
		labels = append(labels, name)
	}
	sort.Strings(labels)
	return labels
}

func notificationSubjectNumber(ctx context.Context, d *Deps, notification db.Notification) int {
	if notification.SubjectType != service.NotificationSubjectIssue {
		return 0
	}
	issue, err := d.Svc.GetIssueByID(ctx, notification.SubjectID)
	if err != nil {
		return 0
	}
	return issue.Number
}

func latestCommentSummaries(comments []db.IssueComment) []any {
	items := make([]any, 0, len(comments))
	for _, comment := range comments {
		items = append(items, map[string]any{
			"id":         comment.ID,
			"body":       comment.Body,
			"user":       transform.User(comment.Author),
			"created_at": comment.CreatedAt.UTC().Format(time.RFC3339),
			"updated_at": comment.UpdatedAt.UTC().Format(time.RFC3339),
		})
	}
	return items
}
