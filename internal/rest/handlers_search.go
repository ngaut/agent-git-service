package rest

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/gitstore"
	"github.com/ngaut/agent-git-service/internal/rest/respond"
	"github.com/ngaut/agent-git-service/internal/rest/transform"
	"github.com/ngaut/agent-git-service/internal/service"
	"golang.org/x/sync/errgroup"
)

// --- Search ---

// GitHub Search API caps accessible results at 1000 items.
const searchMaxItems = 1000

// Bound concurrent per-repo git searches so broad search queries do not fan
// out unbounded RPC/process work.
const searchGitConcurrency = 8

// SearchRepos handles GET /api/v3/search/repositories
func (d *Deps) SearchRepos(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if strings.TrimSpace(q) == "" {
		respond.ValidationFailed(w, "Validation Failed")
		return
	}
	q = searchQueryWithRESTSortOrder(q, r)

	sq := service.ParseSearchQuery(q)
	textQuery := strings.Join(sq.FreeText, " ")

	hasFilters := service.HasRepoSearchFilters(sq)
	var repos []db.Repository
	if textQuery != "" || hasFilters {
		var err error
		repos, err = d.Svc.SearchRepos(r.Context(), q)
		if err != nil {
			if isContextCanceled(err) {
				respond.ServiceErrorRequest(r, w, err)
				return
			}
			logErr(r.Context(), "search repositories failed", err, "query", q)
			respond.JSON(w, http.StatusInternalServerError, map[string]any{"error": "internal server error"})
			return
		}
	} else {
		repos = []db.Repository{}
	}
	sizePred, sizeOK := parseNumericQualifier(sq.Size)
	topicsPred, topicsOK := parseNumericQualifier(sq.Topics)

	// Collect all candidate repo IDs so we can fetch stars/forks/issue counts
	// in three batch queries instead of 3*N single-row queries inside the loop.
	repoIDs := make([]uint, len(repos))
	for i, rep := range repos {
		repoIDs[i] = rep.ID
	}
	starCounts := d.Svc.StarCountBatch(r.Context(), repoIDs)
	forkCounts := d.Svc.ForkCountBatch(r.Context(), repoIDs)
	openIssueCounts := d.Svc.CountIssuesByRepoIDBatch(r.Context(), repoIDs)

	items := make([]any, 0, len(repos))
	for _, rep := range repos {
		sizeKB := d.Svc.RepoDiskUsageKB(r.Context(), rep)
		if sizeOK && !sizePred(sizeKB) {
			continue
		}
		if topicsOK {
			if !topicsPred(len(parseTopicsStr(rep.Topics))) {
				continue
			}
		}
		stats := transform.RepoStats{
			ForksCount:      forkCounts[rep.ID],
			OpenIssuesCount: openIssueCounts[rep.ID],
			StargazersCount: starCounts[rep.ID],
			Size:            sizeKB,
		}
		item := transform.Repo(rep, stats)
		item["score"] = 1.0
		items = append(items, item)
	}
	page, perPage := parsePagination(r)
	totalCount := len(items)
	if totalCount > searchMaxItems {
		items = items[:searchMaxItems]
		totalCount = searchMaxItems
	}
	paged := paginate(w, r, d.Svc.BaseURL, items, page, perPage)
	respond.JSON(w, 200, map[string]any{
		"total_count":        totalCount,
		"incomplete_results": false,
		"items":              paged,
	})
}

func searchQueryWithRESTSortOrder(q string, r *http.Request) string {
	qualifiers := make([]string, 0, 2)
	addQualifier := func(key, value string) {
		value = strings.TrimSpace(value)
		if value == "" || strings.ContainsAny(value, " \t\r\n") {
			return
		}
		qualifiers = append(qualifiers, key+":"+value)
	}
	addQualifier("sort", r.URL.Query().Get("sort"))
	addQualifier("order", r.URL.Query().Get("order"))
	if len(qualifiers) == 0 {
		return q
	}
	return strings.TrimSpace(q + " " + strings.Join(qualifiers, " "))
}

func parseTopicsStr(t string) []string {
	if t == "" {
		return nil
	}
	parts := strings.Split(t, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

func repoPushedAt(rep db.Repository) time.Time {
	if rep.PushedAt != nil {
		return *rep.PushedAt
	}
	return rep.CreatedAt
}

func parseNumericQualifier(raw string) (func(int) bool, bool) {
	spec := strings.TrimSpace(raw)
	if spec == "" {
		return nil, false
	}

	if strings.Contains(spec, "..") {
		parts := strings.SplitN(spec, "..", 2)
		left := strings.TrimSpace(parts[0])
		right := strings.TrimSpace(parts[1])
		hasLeft := left != ""
		hasRight := right != ""
		var leftVal, rightVal int
		if hasLeft {
			v, err := strconv.Atoi(left)
			if err != nil {
				return nil, false
			}
			leftVal = v
		}
		if hasRight {
			v, err := strconv.Atoi(right)
			if err != nil {
				return nil, false
			}
			rightVal = v
		}
		if !hasLeft && !hasRight {
			return nil, false
		}
		return func(v int) bool {
			if hasLeft && v < leftVal {
				return false
			}
			if hasRight && v > rightVal {
				return false
			}
			return true
		}, true
	}

	ops := []string{">=", "<=", ">", "<"}
	for _, op := range ops {
		if strings.HasPrefix(spec, op) {
			valStr := strings.TrimSpace(spec[len(op):])
			n, err := strconv.Atoi(valStr)
			if err != nil {
				return nil, false
			}
			switch op {
			case ">=":
				return func(v int) bool { return v >= n }, true
			case "<=":
				return func(v int) bool { return v <= n }, true
			case ">":
				return func(v int) bool { return v > n }, true
			case "<":
				return func(v int) bool { return v < n }, true
			}
		}
	}

	if strings.HasPrefix(spec, "=") {
		spec = strings.TrimSpace(spec[1:])
	}
	n, err := strconv.Atoi(spec)
	if err != nil {
		return nil, false
	}
	return func(v int) bool { return v == n }, true
}

type dateValue struct {
	t       time.Time
	dayOnly bool
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

func parseDateQualifier(raw string) (func(time.Time) bool, bool) {
	spec := strings.TrimSpace(raw)
	if spec == "" {
		return nil, false
	}

	if strings.Contains(spec, "..") {
		parts := strings.SplitN(spec, "..", 2)
		left := strings.TrimSpace(parts[0])
		right := strings.TrimSpace(parts[1])
		var (
			hasLeft  bool
			hasRight bool
			leftVal  time.Time
			rightVal time.Time
		)
		if left != "" {
			v, ok := parseDateValue(left)
			if !ok {
				return nil, false
			}
			leftVal, _ = dateBounds(v)
			hasLeft = true
		}
		if right != "" {
			v, ok := parseDateValue(right)
			if !ok {
				return nil, false
			}
			_, rightVal = dateBounds(v)
			hasRight = true
		}
		if !hasLeft && !hasRight {
			return nil, false
		}
		return func(t time.Time) bool {
			if hasLeft && t.Before(leftVal) {
				return false
			}
			if hasRight && t.After(rightVal) {
				return false
			}
			return true
		}, true
	}

	ops := []string{">=", "<=", ">", "<"}
	for _, op := range ops {
		if strings.HasPrefix(spec, op) {
			valStr := strings.TrimSpace(spec[len(op):])
			v, ok := parseDateValue(valStr)
			if !ok {
				return nil, false
			}
			start, end := dateBounds(v)
			switch op {
			case ">=":
				return func(t time.Time) bool { return !t.Before(start) }, true
			case "<=":
				return func(t time.Time) bool { return !t.After(end) }, true
			case ">":
				return func(t time.Time) bool { return t.After(end) }, true
			case "<":
				return func(t time.Time) bool { return t.Before(start) }, true
			}
		}
	}

	if strings.HasPrefix(spec, "=") {
		spec = strings.TrimSpace(spec[1:])
	}
	v, ok := parseDateValue(spec)
	if !ok {
		return nil, false
	}
	start, end := dateBounds(v)
	return func(t time.Time) bool {
		return !t.Before(start) && !t.After(end)
	}, true
}

// SearchIssues handles GET /api/v3/search/issues
// Supports ?q=<text>+repo:<owner>/<name>+type:<type> used by `gh search issues`.
func (d *Deps) SearchIssues(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if strings.TrimSpace(q) == "" {
		respond.ValidationFailed(w, "Validation Failed")
		return
	}
	resolver := d.userResolver(r.Context())
	assocCache := map[uint]transform.AuthorAssociationChecks{}
	getAssoc := func(repo db.Repository) transform.AuthorAssociationChecks {
		if repo.ID == 0 {
			return transform.AuthorAssociationChecks{}
		}
		if assoc, ok := assocCache[repo.ID]; ok {
			return assoc
		}
		assoc := d.authorAssociationChecks(r.Context(), repo)
		assocCache[repo.ID] = assoc
		return assoc
	}

	// Strip parentheses used by gh CLI for grouping.
	q = strings.ReplaceAll(q, "(", "")
	q = strings.ReplaceAll(q, ")", "")

	// Pass the full query to the service layer.
	// We parse the query first to determine whether we should route to issues or PRs.
	sq := service.ParseSearchQuery(q)

	var items []any
	if sq.IsPR {
		prs, err := d.Svc.SearchPRs(r.Context(), q)
		if err != nil {
			logErr(r.Context(), "search pull requests failed", err, "query", q)
		}
		for _, pr := range prs {
			assoc := getAssoc(pr.Repository)
			item := transform.IssueFromPR(pr, resolver, assoc)
			item["score"] = 1.0
			items = append(items, item)
		}
	} else {
		issues, err := d.Svc.SearchIssues(r.Context(), q)
		if err != nil {
			logErr(r.Context(), "search issues failed", err, "query", q)
		}
		// Batch-fetch reaction counts for all issues in one query
		// instead of N individual queries.
		issueIDs := make([]uint, len(issues))
		for i, iss := range issues {
			issueIDs[i] = iss.ID
		}
		allReactions, err := d.Svc.CountReactionsBatch(r.Context(), issueIDs)
		if err != nil {
			logErr(r.Context(), "search issues batch reaction count failed", err)
			allReactions = nil
		}
		for _, iss := range issues {
			assoc := getAssoc(iss.Repository)
			item := transform.Issue(iss, resolver, assoc, transform.IssueCounts{
				Reactions: allReactions[iss.ID],
			})
			item["score"] = 1.0
			items = append(items, item)
		}
	}

	if items == nil {
		items = make([]any, 0)
	}

	page, perPage := parsePagination(r)
	totalCount := len(items)
	if totalCount > searchMaxItems {
		items = items[:searchMaxItems]
		totalCount = searchMaxItems
	}
	paged := paginate(w, r, d.Svc.BaseURL, items, page, perPage)
	respond.JSON(w, 200, map[string]any{
		"total_count":        totalCount,
		"incomplete_results": false,
		"items":              paged,
	})
}

// SearchLabels handles GET /api/v3/search/labels
func (d *Deps) SearchLabels(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	repositoryID := strings.TrimSpace(r.URL.Query().Get("repository_id"))
	if strings.TrimSpace(q) == "" || repositoryID == "" {
		respond.ValidationFailed(w, "Validation Failed")
		return
	}
	if _, err := strconv.ParseUint(repositoryID, 10, 64); err != nil {
		respond.ValidationFailed(w, "Validation Failed")
		return
	}

	labels, err := d.Svc.SearchLabels(r.Context(), repositoryID, q, r.URL.Query().Get("sort"), r.URL.Query().Get("order"))
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	items := make([]any, 0, len(labels))
	for _, label := range labels {
		item := transform.Label(label)
		item["score"] = 1.0
		items = append(items, item)
	}

	page, perPage := parsePagination(r)
	totalCount := len(items)
	if totalCount > searchMaxItems {
		items = items[:searchMaxItems]
		totalCount = searchMaxItems
	}
	paged := paginate(w, r, d.Svc.BaseURL, items, page, perPage)
	respond.JSON(w, 200, map[string]any{
		"total_count":        totalCount,
		"incomplete_results": false,
		"items":              paged,
	})
}

// SearchUsers handles GET /api/v3/search/users
func (d *Deps) SearchUsers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if strings.TrimSpace(q) == "" {
		respond.ValidationFailed(w, "Validation Failed")
		return
	}

	users, err := d.Svc.SearchUsers(r.Context(), q, r.URL.Query().Get("sort"), r.URL.Query().Get("order"))
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	items := make([]any, 0, len(users))
	for _, user := range users {
		item := transform.User(user)
		item["score"] = 1.0
		items = append(items, item)
	}

	page, perPage := parsePagination(r)
	totalCount := len(items)
	if totalCount > searchMaxItems {
		items = items[:searchMaxItems]
		totalCount = searchMaxItems
	}
	paged := paginate(w, r, d.Svc.BaseURL, items, page, perPage)
	respond.JSON(w, 200, map[string]any{
		"total_count":        totalCount,
		"incomplete_results": false,
		"items":              paged,
	})
}

// SearchTopics handles GET /api/v3/search/topics
func (d *Deps) SearchTopics(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if strings.TrimSpace(q) == "" {
		respond.ValidationFailed(w, "Validation Failed")
		return
	}

	topics, err := d.Svc.SearchTopics(r.Context(), q)
	if err != nil {
		respond.ServiceErrorRequest(r, w, err)
		return
	}

	items := make([]any, 0, len(topics))
	for _, topic := range topics {
		items = append(items, topicSearchItem(topic))
	}

	page, perPage := parsePagination(r)
	totalCount := len(items)
	if totalCount > searchMaxItems {
		items = items[:searchMaxItems]
		totalCount = searchMaxItems
	}
	paged := paginate(w, r, d.Svc.BaseURL, items, page, perPage)
	respond.JSON(w, 200, map[string]any{
		"total_count":        totalCount,
		"incomplete_results": false,
		"items":              paged,
	})
}

func topicSearchItem(topic service.TopicSearchResult) map[string]any {
	return map[string]any{
		"name":              topic.Name,
		"display_name":      topicDisplayName(topic.Name),
		"short_description": "",
		"description":       "",
		"created_by":        "",
		"released":          "",
		"created_at":        topic.CreatedAt.Format(time.RFC3339),
		"updated_at":        topic.UpdatedAt.Format(time.RFC3339),
		"featured":          false,
		"curated":           false,
		"score":             1.0,
		"repository_count":  topic.RepositoryCount,
	}
}

func topicDisplayName(name string) string {
	parts := strings.FieldsFunc(name, func(r rune) bool { return r == '-' || r == '_' })
	for i, part := range parts {
		if part == "" {
			continue
		}
		parts[i] = strings.ToUpper(part[:1]) + part[1:]
	}
	return strings.Join(parts, " ")
}

// SearchCommits handles GET /api/v3/search/commits
// Supports ?q=<text>+repo:<owner>/<name>+author:<user>+committer:<user>+hash:<sha>+is:merge+author-date:>2020-01-01 etc.
func (d *Deps) SearchCommits(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if strings.TrimSpace(q) == "" {
		respond.ValidationFailed(w, "Validation Failed")
		return
	}

	sq := service.ParseCommitSearchQuery(q)
	textQuery := strings.Join(sq.FreeText, " ")
	repoFilter := repoFilterSet(sq.Repos, sq.Repo)

	var items []any
	if textQuery != "" || sq.HasQualifiers {
		// Build commit filters
		var filters *gitstore.CommitSearchFilters
		if sq.HasCommitFilters() {
			filters = &gitstore.CommitSearchFilters{
				Author:         sq.Author,
				Committer:      sq.Committer,
				AuthorName:     sq.AuthorName,
				AuthorEmail:    sq.AuthorEmail,
				CommitterName:  sq.CommitterName,
				CommitterEmail: sq.CommitterEmail,
				Hash:           sq.Hash,
				Parent:         sq.Parent,
				Tree:           sq.Tree,
				AuthorDate:     sq.AuthorDate,
				CommitterDate:  sq.CommitterDate,
				Merge:          sq.Merge,
			}
		}

		// Search across repos matching the filter (or all repos if no filter)
		repos, _ := d.Svc.ListAllRepos(r.Context())
		perRepoItems := make([][]any, len(repos))
		g, ctx := errgroup.WithContext(r.Context())
		sem := make(chan struct{}, searchGitConcurrency)
		for i, rep := range repos {
			if repoFilter != nil {
				if _, ok := repoFilter[rep.FullName]; !ok {
					continue
				}
			}

			// Apply user/org/visibility filters at repo level
			if sq.User != "" && !strings.EqualFold(rep.Owner.Login, sq.User) {
				continue
			}
			if sq.Org != "" && !strings.EqualFold(rep.Owner.Login, sq.Org) {
				continue
			}
			if sq.Visibility != "" {
				// Use Visibility field for three-state visibility (public/private/internal)
				// Fall back to Private bool for backward compatibility if Visibility is empty
				repoVisibility := rep.Visibility
				if repoVisibility == "" {
					if rep.Private {
						repoVisibility = "private"
					} else {
						repoVisibility = "public"
					}
				}
				if !strings.EqualFold(repoVisibility, sq.Visibility) {
					continue
				}
			}

			i, rep := i, rep
			g.Go(func() error {
				select {
				case sem <- struct{}{}:
					defer func() { <-sem }()
				case <-ctx.Done():
					return nil
				}

				commits, err := d.Svc.Git.SearchCommits(ctx, rep.FullName, textQuery, filters)
				if err != nil {
					return nil
				}
				repoItems := make([]any, 0, len(commits))
				for _, c := range commits {
					commitObj := transform.Commit(rep.FullName, c.SHA, transform.CommitMeta{
						Message:    c.Message,
						AuthorName: c.Author,
						Email:      c.Email,
						Date:       c.Date,
						ParentSHAs: c.ParentSHAs,
					})
					commitObj["author"] = map[string]any{"login": c.Author}
					commitObj["committer"] = map[string]any{"login": c.Committer}
					commitObj["repository"] = transform.Repo(rep)
					commitObj["score"] = 1.0
					repoItems = append(repoItems, commitObj)
				}
				perRepoItems[i] = repoItems
				return nil
			})
		}
		if err := g.Wait(); err != nil {
			if isContextCanceled(err) {
				respond.ServiceErrorRequest(r, w, err)
				return
			}
			logErr(r.Context(), "search commits failed", err, "query", q)
			respond.JSON(w, http.StatusInternalServerError, map[string]any{"error": "internal server error"})
			return
		}
		if r.Context().Err() != nil {
			return
		}
		for _, repoItems := range perRepoItems {
			items = append(items, repoItems...)
		}
	}
	if items == nil {
		items = []any{}
	}
	page, perPage := parsePagination(r)
	totalCount := len(items)
	if totalCount > searchMaxItems {
		items = items[:searchMaxItems]
		totalCount = searchMaxItems
	}
	paged := paginate(w, r, d.Svc.BaseURL, items, page, perPage)
	respond.JSON(w, 200, map[string]any{
		"total_count":        totalCount,
		"incomplete_results": false,
		"items":              paged,
	})
}

// SearchCode handles GET /api/v3/search/code
// Supports ?q=<text>+repo:<owner>/<name>+filename:<name>+extension:<ext>+path:<path>+language:<lang>
func (d *Deps) SearchCode(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if strings.TrimSpace(q) == "" {
		respond.ValidationFailed(w, "Validation Failed")
		return
	}

	cq := service.ParseCodeSearchQuery(q)
	repoFilter := repoFilterSet(cq.Repos, cq.Repo)

	var warnings []string
	if len(cq.NegatedQualifiers) > 0 {
		warnings = append(warnings, fmt.Sprintf("Negated qualifiers are not supported for code search and were ignored: %s", strings.Join(cq.NegatedQualifiers, ", ")))
	}

	// Check if text_matches are requested via Accept header or query param
	requestTextMatches := strings.Contains(r.Header.Get("Accept"), "text-match") ||
		r.URL.Query().Get("text_matches") == "true"
	page, perPage := parsePagination(r)

	respondSearchCode := func(items []any) {
		if items == nil {
			items = []any{}
		}
		totalCount := len(items)
		if totalCount > searchMaxItems {
			items = items[:searchMaxItems]
			totalCount = searchMaxItems
		}
		paged := paginate(w, r, d.Svc.BaseURL, items, page, perPage)
		response := map[string]any{
			"total_count":        totalCount,
			"incomplete_results": false,
			"items":              paged,
		}
		if len(warnings) > 0 {
			response["warnings"] = warnings
		}
		respond.JSON(w, 200, response)
	}

	if len(cq.FreeText) == 0 && !cq.HasQualifiers {
		respondSearchCode(nil)
		return
	}

	// Build filters from qualifiers
	var filters *gitstore.CodeSearchFilters
	if cq.Filename != "" || len(cq.Extensions) > 0 || cq.Path != "" || cq.Language != "" {
		filters = &gitstore.CodeSearchFilters{
			Filename:   cq.Filename,
			Extensions: cq.Extensions,
			Path:       cq.Path,
			Language:   cq.Language,
		}
		// If language is specified, intersect with explicit extensions
		if cq.Language != "" {
			langExts := service.GetExtensionsForLanguage(cq.Language)
			if len(langExts) == 0 {
				// Unknown language - return no results
				respondSearchCode(nil)
				return
			}
			if len(filters.Extensions) > 0 {
				// Intersect: keep only extensions that match both language and explicit extension filter
				requestedExtensions := append([]string(nil), filters.Extensions...)
				intersected := make([]string, 0, len(langExts))
				for _, le := range langExts {
					for _, e := range filters.Extensions {
						if strings.EqualFold(le, e) {
							intersected = append(intersected, le)
							break
						}
					}
				}
				if len(intersected) == 0 {
					// No intersection - return no results
					warnings = append(warnings, fmt.Sprintf("No matching files: language:%s conflicts with extension:%s", cq.Language, strings.Join(requestedExtensions, ",")))
					respondSearchCode(nil)
					return
				}
				filters.Extensions = intersected
			} else {
				filters.Extensions = langExts
			}
		}
	}

	viewerRepos, err := d.Svc.ListViewerRepos(r.Context())
	if err != nil {
		if isContextCanceled(err) {
			respond.ServiceErrorRequest(r, w, err)
			return
		}
		logErr(r.Context(), "search code list viewer repos failed", err, "query", q)
		respond.JSON(w, http.StatusInternalServerError, map[string]any{"error": "internal server error"})
		return
	}
	repos := make([]db.Repository, 0, len(viewerRepos))
	for _, viewerRepo := range viewerRepos {
		repos = append(repos, viewerRepo.Repository)
	}
	var items []any

	perRepoItems := make([][]any, len(repos))
	g, ctx := errgroup.WithContext(r.Context())
	sem := make(chan struct{}, searchGitConcurrency)
	for i, rep := range repos {
		if repoFilter != nil {
			if _, ok := repoFilter[rep.FullName]; !ok {
				continue
			}
		}

		i, rep := i, rep
		g.Go(func() error {
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return nil
			}

			results, err := d.Svc.Git.SearchCode(ctx, rep.FullName, cq.FreeText, filters, requestTextMatches)
			if err != nil {
				return nil
			}

			repoItems := make([]any, 0, len(results))
			for _, f := range results {
				item := map[string]any{
					"name":       f.Path[strings.LastIndex(f.Path, "/")+1:],
					"path":       f.Path,
					"sha":        "HEAD",
					"score":      1.0,
					"html_url":   fmt.Sprintf("%s/%s/blob/HEAD/%s", transform.HTMLBase(), rep.FullName, f.Path),
					"url":        fmt.Sprintf("%s/api/v3/repos/%s/contents/%s", transform.Base(), rep.FullName, f.Path),
					"repository": transform.Repo(rep),
				}

				// Add text_matches only if requested and content is available
				if requestTextMatches && f.Content != "" && len(cq.FreeText) > 0 {
					// Use the first query term for text_matches display
					matchQuery := strings.NewReplacer("\n", " ", "\r", " ").Replace(cq.FreeText[0])
					// Find the match in the content (case-insensitive)
					contentLower := strings.ToLower(f.Content)
					queryLower := strings.ToLower(matchQuery)
					matchStart := strings.Index(contentLower, queryLower)
					if matchStart < 0 {
						matchStart = 0
					}
					matchEnd := matchStart + len(matchQuery)
					if matchEnd > len(f.Content) {
						matchEnd = len(f.Content)
					}

					item["text_matches"] = []any{
						map[string]any{
							"fragment": f.Content,
							"matches": []any{
								map[string]any{
									"indices": []int{matchStart, matchEnd},
									"text":    matchQuery,
								},
							},
							"object_type": "content",
							"property":    "content",
						},
					}
				}

				repoItems = append(repoItems, item)
			}
			perRepoItems[i] = repoItems
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		if isContextCanceled(err) {
			respond.ServiceErrorRequest(r, w, err)
			return
		}
		logErr(r.Context(), "search code failed", err, "query", q)
		respond.JSON(w, http.StatusInternalServerError, map[string]any{"error": "internal server error"})
		return
	}
	if r.Context().Err() != nil {
		return
	}
	for _, repoItems := range perRepoItems {
		items = append(items, repoItems...)
	}
	if items == nil {
		items = []any{}
	}
	respondSearchCode(items)
}

func repoFilterSet(repos []string, fallback string) map[string]struct{} {
	if len(repos) == 0 && fallback != "" {
		repos = []string{fallback}
	}
	if len(repos) == 0 {
		return nil
	}
	filter := make(map[string]struct{}, len(repos))
	for _, repo := range repos {
		if repo == "" {
			continue
		}
		filter[repo] = struct{}{}
	}
	if len(filter) == 0 {
		return nil
	}
	return filter
}
