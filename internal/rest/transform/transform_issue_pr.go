package transform

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"
)

// AuthorAssociationChecks provides optional callbacks for association resolution.
type AuthorAssociationChecks struct {
	IsCollaborator func(userID uint) bool
	IsOrgMember    func(userID uint) bool
}

// AuthorAssociation returns the association of a user with the repository owner.
// Priority: COLLABORATOR, MEMBER, OWNER, NONE.
func AuthorAssociation(authorID, repoOwnerID uint, checks AuthorAssociationChecks) string {
	if authorID == 0 {
		return "NONE"
	}
	if checks.IsCollaborator != nil && checks.IsCollaborator(authorID) {
		return "COLLABORATOR"
	}
	if checks.IsOrgMember != nil && checks.IsOrgMember(authorID) {
		return "MEMBER"
	}
	if authorID == repoOwnerID {
		return "OWNER"
	}
	return "NONE"
}

// authorAssociationByLogin returns the association based on login comparison.
// Used when only login strings are available (e.g., PRReview, PRReviewComment).
func authorAssociationByLogin(authorLogin, ownerLogin string) string {
	if authorLogin != "" && authorLogin == ownerLogin {
		return "OWNER"
	}
	return "NONE"
}

// PRStats holds computed statistics for a pull request.
type PRStats struct {
	Comments       int64
	ReviewComments int64
	Commits        int
	Additions      int
	Deletions      int
	ChangedFiles   int
}

// IssueCounts holds optional issue-related counts for REST responses.
type IssueCounts struct {
	Comments  int64
	Reactions map[string]int64
}

// UserResolver looks up a user by login for assignee expansion.
type UserResolver func(login string) (db.User, error)

// Issue converts a db.Issue to a GitHub REST API issue object.
// GraphQL counterpart: graphql/gql_shapes.go issueGQL().
func Issue(i db.Issue, resolver UserResolver, assoc AuthorAssociationChecks, counts ...IssueCounts) map[string]any {
	var comments int64
	var reactionCounts map[string]int64
	if len(counts) > 0 {
		comments = counts[0].Comments
		reactionCounts = counts[0].Reactions
	}
	assignees := issueAssignees(i.AssigneeLogins, resolver)
	var assignee any
	if len(assignees) > 0 {
		assignee = assignees[0]
	}
	var closedBy any
	if i.State == db.StateClosed {
		closedBy = issueClosedBy(i.ClosedByLogin, resolver)
	}
	var closedAt any
	if i.ClosedAt != nil {
		closedAt = i.ClosedAt.Format(time.RFC3339)
	}
	num := strconv.Itoa(i.Number)
	var stateReason any
	if i.StateReason != "" {
		stateReason = i.StateReason
	}
	issueURL := fmt.Sprintf("%s/repos/%s/issues/%s", apiBase(), i.Repository.FullName, num)
	return map[string]any{
		"id":                       i.ID,
		"node_id":                  nodeID("Issue", i.ID),
		"number":                   i.Number,
		"title":                    i.Title,
		"body":                     i.Body,
		"body_html":                nil,
		"body_text":                nil,
		"state":                    i.State,
		"state_reason":             stateReason,
		"locked":                   i.Locked,
		"active_lock_reason":       stringOrNil(i.ActiveLockReason),
		"user":                     User(i.Author),
		"labels":                   issueLabels(i.Labels, i.Repository),
		"assignee":                 assignee,
		"assignees":                assignees,
		"milestone":                Milestone(i.Milestone, i.Repository.FullName),
		"url":                      issueURL,
		"html_url":                 fmt.Sprintf("%s/%s/issues/%s", htmlBase(), i.Repository.FullName, num),
		"repository_url":           repoAPIURL(i.Repository.FullName),
		"comments_url":             fmt.Sprintf("%s/repos/%s/issues/%s/comments", apiBase(), i.Repository.FullName, num),
		"events_url":               fmt.Sprintf("%s/repos/%s/issues/%s/events", apiBase(), i.Repository.FullName, num),
		"timeline_url":             fmt.Sprintf("%s/repos/%s/issues/%d/timeline", apiBase(), i.Repository.FullName, i.Number),
		"labels_url":               fmt.Sprintf("%s/repos/%s/issues/%s/labels{/name}", apiBase(), i.Repository.FullName, num),
		"comments":                 comments,
		"sub_issues_summary":       nil,
		"reactions":                Reactions(issueURL, reactionCounts),
		"author_association":       AuthorAssociation(i.Author.ID, i.Repository.Owner.ID, assoc),
		"performed_via_github_app": nil,
		"pull_request":             nil, // Indicates this is an issue, not a PR (used for search disambiguation)
		"created_at":               i.CreatedAt.Format(time.RFC3339),
		"updated_at":               i.UpdatedAt.Format(time.RFC3339),
		"closed_at":                closedAt,
		"closed_by":                closedBy,
	}
}

// mergedByUser returns a user map for the person who merged the PR, or nil if not merged.
func mergedByUser(p db.PullRequest) any {
	if p.Merged && p.MergedByLogin != "" {
		return map[string]any{
			"login": p.MergedByLogin,
			"type":  db.TypeUser,
		}
	}
	return nil
}

// IssueFromPR converts a db.PullRequest to a GitHub REST API issue object containing the pull_request property.
// Used by the `/search/issues` endpoint when the search returns PRs.
func IssueFromPR(p db.PullRequest, resolver UserResolver, assoc AuthorAssociationChecks, commentCount ...int64) map[string]any {
	var comments int64
	if len(commentCount) > 0 {
		comments = commentCount[0]
	}
	assignees := issueAssignees(p.AssigneeLogins, resolver)
	var assignee any
	if len(assignees) > 0 {
		assignee = assignees[0]
	}
	var closedAt any
	if p.ClosedAt != nil {
		closedAt = p.ClosedAt.Format(time.RFC3339)
	}
	num := strconv.Itoa(p.Number)
	issueURL := fmt.Sprintf("%s/repos/%s/issues/%s", apiBase(), p.Repository.FullName, num)

	state := p.State
	if p.Merged {
		state = "closed"
	}

	return map[string]any{
		"id":                 p.ID,
		"node_id":            nodeID("Issue", p.ID),
		"number":             p.Number,
		"title":              p.Title,
		"body":               p.Body,
		"state":              state,
		"state_reason":       nil, // PRs don't have issue state reasons (completed/not_planned) in the same way
		"locked":             false,
		"active_lock_reason": nil,
		"user":               User(p.Author),
		"labels":             issueLabels(p.Labels, p.Repository),
		"assignee":           assignee,
		"assignees":          assignees,
		"milestone":          Milestone(p.Milestone, p.Repository.FullName),
		"url":                issueURL,
		"html_url":           fmt.Sprintf("%s/%s/pull/%s", htmlBase(), p.Repository.FullName, num),
		"repository_url":     repoAPIURL(p.Repository.FullName),
		"comments_url":       fmt.Sprintf("%s/repos/%s/issues/%s/comments", apiBase(), p.Repository.FullName, num),
		"events_url":         fmt.Sprintf("%s/repos/%s/issues/%s/events", apiBase(), p.Repository.FullName, num),
		"labels_url":         fmt.Sprintf("%s/repos/%s/issues/%s/labels{/name}", apiBase(), p.Repository.FullName, num),
		"comments":           comments,
		"reactions":          Reactions(issueURL, nil),
		"author_association": AuthorAssociation(p.Author.ID, p.Repository.Owner.ID, assoc),
		"pull_request": map[string]any{
			"url":       fmt.Sprintf("%s/repos/%s/pulls/%s", apiBase(), p.Repository.FullName, num),
			"html_url":  fmt.Sprintf("%s/%s/pull/%s", htmlBase(), p.Repository.FullName, num),
			"diff_url":  fmt.Sprintf("%s/%s/pull/%s.diff", base(), p.Repository.FullName, num),
			"patch_url": fmt.Sprintf("%s/%s/pull/%s.patch", base(), p.Repository.FullName, num),
			"merged_at": func() any {
				if p.Merged && p.MergedAt != nil {
					return p.MergedAt.Format(time.RFC3339)
				}
				return nil
			}(), // In issue objects returned by search, merged_at is populated if merged.
		},
		"created_at": p.CreatedAt.Format(time.RFC3339),
		"updated_at": p.UpdatedAt.Format(time.RFC3339),
		"closed_at":  closedAt,
	}
}

// PR converts a db.PullRequest to a GitHub REST API pull request object.
// GraphQL counterpart: graphql/gql_shapes.go prGQL().
func PR(p db.PullRequest, resolver UserResolver, assoc AuthorAssociationChecks, reactionCounts map[string]int64, stats ...PRStats) map[string]any {
	var st PRStats
	if len(stats) > 0 {
		st = stats[0]
	}
	var closedAt, mergedAt any
	if p.ClosedAt != nil {
		closedAt = p.ClosedAt.Format(time.RFC3339)
	}
	if p.MergedAt != nil {
		mergedAt = p.MergedAt.Format(time.RFC3339)
	}
	state := p.State
	if p.Merged {
		state = "closed"
	}
	baseSHA := p.BaseSHA
	headSHA := p.HeadSHA
	if baseSHA == "" {
		baseSHA = "0000000000000000000000000000000000000000"
	}
	if headSHA == "" {
		headSHA = "0000000000000000000000000000000000000000"
	}
	num := strconv.Itoa(p.Number)
	baseRepo := Repo(p.Repository)
	headRepo := baseRepo
	headOwner := p.Repository.Owner
	if p.HeadRepositoryID != 0 && p.HeadRepository.ID != 0 {
		headRepo = Repo(p.HeadRepository)
		headOwner = p.HeadRepository.Owner
	}
	prURL := fmt.Sprintf("%s/repos/%s/pulls/%s", apiBase(), p.Repository.FullName, num)
	assignees := issueAssignees(p.AssigneeLogins, resolver)
	var assignee any
	if len(assignees) > 0 {
		assignee = assignees[0]
	}
	return map[string]any{
		"id":                    p.ID,
		"node_id":               nodeID("PullRequest", p.ID),
		"number":                p.Number,
		"title":                 p.Title,
		"body":                  p.Body,
		"state":                 state,
		"draft":                 p.Draft,
		"merged":                p.Merged,
		"merge_commit_sha":      p.MergeCommitSHA,
		"merged_by":             mergedByUser(p),
		"maintainer_can_modify": p.MaintainerCanModify,
		"user":                  User(p.Author),
		"url":                   prURL,
		"html_url":              fmt.Sprintf("%s/%s/pull/%s", htmlBase(), p.Repository.FullName, num),
		"diff_url":              fmt.Sprintf("%s/%s/pull/%s.diff", base(), p.Repository.FullName, num),
		"patch_url":             fmt.Sprintf("%s/%s/pull/%s.patch", base(), p.Repository.FullName, num),
		"issue_url":             fmt.Sprintf("%s/repos/%s/issues/%s", apiBase(), p.Repository.FullName, num),
		"comments_url":          fmt.Sprintf("%s/repos/%s/issues/%s/comments", apiBase(), p.Repository.FullName, num),
		"commits_url":           fmt.Sprintf("%s/repos/%s/pulls/%s/commits", apiBase(), p.Repository.FullName, num),
		"review_comments_url":   fmt.Sprintf("%s/repos/%s/pulls/%s/comments", apiBase(), p.Repository.FullName, num),
		"review_comment_url":    fmt.Sprintf("%s/repos/%s/pulls/comments{/number}", apiBase(), p.Repository.FullName),
		"statuses_url":          fmt.Sprintf("%s/repos/%s/statuses/%s", apiBase(), p.Repository.FullName, headSHA),
		"head": map[string]any{
			"ref":   p.HeadRef,
			"sha":   headSHA,
			"repo":  headRepo,
			"user":  User(headOwner),
			"label": fmt.Sprintf("%s:%s", headOwner.Login, p.HeadRef),
		},
		"base": map[string]any{
			"ref":   p.BaseRef,
			"sha":   baseSHA,
			"repo":  baseRepo,
			"user":  User(p.Repository.Owner),
			"label": fmt.Sprintf("%s:%s", p.Repository.Owner.Login, p.BaseRef),
		},
		"labels":              issueLabels(p.Labels, p.Repository),
		"assignee":            assignee,
		"assignees":           assignees,
		"requested_reviewers": []any{},
		"requested_teams":     []any{},
		"milestone":           Milestone(p.Milestone, p.Repository.FullName),
		"comments":            st.Comments,
		"review_comments":     st.ReviewComments,
		"commits":             st.Commits,
		"additions":           st.Additions,
		"deletions":           st.Deletions,
		"changed_files":       st.ChangedFiles,
		"reactions":           Reactions(prURL, reactionCounts),
		"author_association":  AuthorAssociation(p.Author.ID, p.Repository.Owner.ID, assoc),
		"mergeable":           nil,
		"rebaseable":          nil,
		"mergeable_state":     "unknown",
		"created_at":          p.CreatedAt.Format(time.RFC3339),
		"updated_at":          p.UpdatedAt.Format(time.RFC3339),
		"closed_at":           closedAt,
		"merged_at":           mergedAt,
	}
}

// IssueComment converts a db.IssueComment to JSON.
func IssueComment(c db.IssueComment, assoc AuthorAssociationChecks, reactionCounts ...map[string]int64) map[string]any {
	var counts map[string]int64
	if len(reactionCounts) > 0 {
		counts = reactionCounts[0]
	}
	var pinnedAt any
	if c.PinnedAt != nil {
		pinnedAt = c.PinnedAt.Format(time.RFC3339)
	}
	var inReplyToID any
	if c.InReplyToID != nil {
		inReplyToID = *c.InReplyToID
	}
	var threadRootID any
	if c.ThreadRootID != nil {
		threadRootID = *c.ThreadRootID
	}
	commentURL := fmt.Sprintf("%s/repos/%s/issues/comments/%d", apiBase(), c.Repository.FullName, c.ID)
	return map[string]any{
		"id":                       c.ID,
		"node_id":                  nodeID("IssueComment", c.ID),
		"body":                     c.Body,
		"user":                     User(c.Author),
		"author_association":       AuthorAssociation(c.Author.ID, c.Repository.Owner.ID, assoc),
		"performed_via_github_app": nil,
		"url":                      commentURL,
		"issue_url":                fmt.Sprintf("%s/repos/%s/issues/%d", apiBase(), c.Repository.FullName, c.IssueNumber),
		"html_url":                 fmt.Sprintf("%s/%s/issues/%d#issuecomment-%d", htmlBase(), c.Repository.FullName, c.IssueNumber, c.ID),
		"reactions":                Reactions(commentURL, counts),
		"is_pinned":                c.IsPinned,
		"pinned_at":                pinnedAt,
		"isPinned":                 c.IsPinned,
		"pinnedAt":                 pinnedAt,
		"in_reply_to_id":           inReplyToID,
		"thread_root_id":           threadRootID,
		"created_at":               c.CreatedAt.Format(time.RFC3339),
		"updated_at":               c.UpdatedAt.Format(time.RFC3339),
	}
}

// PRReview converts a db.PullRequestReview to a GitHub REST API review object.
func PRReview(rv db.PullRequestReview, repoFullName string, prNumber int, ownerLogin ...string) map[string]any {
	var submittedAt any
	if rv.SubmittedAt != nil {
		submittedAt = rv.SubmittedAt.Format(time.RFC3339)
	} else {
		submittedAt = rv.CreatedAt.Format(time.RFC3339)
	}
	ownLogin := ""
	if len(ownerLogin) > 0 {
		ownLogin = ownerLogin[0]
	}
	htmlURL := fmt.Sprintf("%s/%s/pull/%d#pullrequestreview-%d", htmlBase(), repoFullName, prNumber, rv.ID)
	prURL := fmt.Sprintf("%s/repos/%s/pulls/%d", apiBase(), repoFullName, prNumber)
	return map[string]any{
		"id":                 rv.ID,
		"node_id":            nodeID("PRReview", rv.ID),
		"body":               rv.Body,
		"state":              rv.State,
		"submitted_at":       submittedAt,
		"commit_id":          rv.CommitSHA,
		"author_association": authorAssociationByLogin(rv.AuthorLogin, ownLogin),
		"user": map[string]any{
			"login": rv.AuthorLogin,
			"type":  db.TypeUser,
		},
		"html_url":         htmlURL,
		"pull_request_url": prURL,
		"_links": map[string]any{
			"html":         map[string]any{"href": htmlURL},
			"pull_request": map[string]any{"href": prURL},
		},
	}
}

// PRReviewComment converts a db.PRReviewComment to a GitHub REST API review comment object.
func PRReviewComment(c db.PRReviewComment, repoFullName string, prNumber int) map[string]any {
	var prrID any
	if c.PullRequestReviewID != nil {
		prrID = *c.PullRequestReviewID
	}
	var inReplyToID any
	if c.InReplyToID != nil {
		inReplyToID = *c.InReplyToID
	}
	var startLine any
	if c.StartLine != nil {
		startLine = *c.StartLine
	}
	subjectType := c.SubjectType
	if subjectType == "" {
		subjectType = "line"
	}
	selfURL := fmt.Sprintf("%s/repos/%s/pulls/comments/%d", apiBase(), repoFullName, c.ID)
	htmlURL := fmt.Sprintf("%s/%s/pull/%d#discussion_r%d", htmlBase(), repoFullName, prNumber, c.ID)
	prURL := fmt.Sprintf("%s/repos/%s/pulls/%d", apiBase(), repoFullName, prNumber)
	return map[string]any{
		"id":                     c.ID,
		"node_id":                nodeID("PRReviewComment", c.ID),
		"pull_request_review_id": prrID,
		"in_reply_to_id":         inReplyToID,
		"diff_hunk":              c.DiffHunk,
		"path":                   c.Path,
		"position":               c.Line,
		"original_position":      c.OriginalLine,
		"commit_id":              c.CommitID,
		"original_commit_id":     c.CommitID,
		"user": map[string]any{
			"login": c.AuthorLogin,
			"type":  db.TypeUser,
		},
		"body":                c.Body,
		"created_at":          c.CreatedAt.Format(time.RFC3339),
		"updated_at":          c.UpdatedAt.Format(time.RFC3339),
		"url":                 selfURL,
		"html_url":            htmlURL,
		"pull_request_url":    prURL,
		"author_association":  authorAssociationByLogin(c.AuthorLogin, repoFullName[:strings.IndexByte(repoFullName, '/')]),
		"line":                c.Line,
		"original_line":       c.OriginalLine,
		"start_line":          startLine,
		"original_start_line": startLine,
		"start_side":          nil,
		"side":                c.Side,
		"subject_type":        subjectType,
		"resolved":            c.IsResolved,
		"resolved_by":         nil,                     // schema doesn't yet track the resolving actor
		"reactions":           Reactions(selfURL, nil), // Default to 0 without db hit if not aggregated
		"_links": map[string]any{
			"self":         map[string]any{"href": selfURL},
			"html":         map[string]any{"href": htmlURL},
			"pull_request": map[string]any{"href": prURL},
		},
	}
}

// Reactions returns a reactions object populated from per-content counts.
func Reactions(url string, counts map[string]int64) map[string]any {
	total := int64(0)
	if counts != nil {
		for _, count := range counts {
			total += count
		}
	}
	return map[string]any{
		"url":         url + "/reactions",
		"total_count": total,
		"+1":          reactionCount(counts, "+1"),
		"-1":          reactionCount(counts, "-1"),
		"laugh":       reactionCount(counts, "laugh"),
		"hooray":      reactionCount(counts, "hooray"),
		"confused":    reactionCount(counts, "confused"),
		"heart":       reactionCount(counts, "heart"),
		"rocket":      reactionCount(counts, "rocket"),
		"eyes":        reactionCount(counts, "eyes"),
	}
}

func reactionCount(counts map[string]int64, key string) int64 {
	if counts == nil {
		return 0
	}
	return counts[key]
}

// issueLabels converts a slice of db.Label (preloaded via many2many) to the
// GitHub REST API label shape. Uses Label() so the URL is always consistent.
func issueLabels(labels []db.Label, repo db.Repository) []any {
	out := make([]any, len(labels))
	for i, l := range labels {
		if l.Repository.FullName == "" {
			l.Repository = repo
		}
		out[i] = Label(l)
	}
	return out
}

// issueAssignees converts a comma-separated login string to a GitHub REST API
// assignee list. Returns an empty non-nil slice when there are no assignees.
func issueAssignees(logins string, resolver UserResolver) []any {
	if logins == "" {
		return []any{}
	}
	parts := strings.Split(logins, ",")
	out := make([]any, 0, len(parts))
	resolved := make(map[string]any, len(parts))
	for _, login := range parts {
		login = strings.TrimSpace(login)
		if login == "" {
			continue
		}
		if assignee, ok := resolved[login]; ok {
			out = append(out, assignee)
			continue
		}
		if resolver != nil {
			if u, err := resolver(login); err == nil {
				assignee := User(u)
				resolved[login] = assignee
				out = append(out, assignee)
				continue
			}
		}
		assignee := map[string]any{
			"login":      login,
			"avatar_url": fmt.Sprintf("%s/avatars/%s", base(), login),
			"url":        userAPIURL(login),
			"html_url":   userHTMLURL(login),
			"type":       db.TypeUser,
			"site_admin": false,
		}
		resolved[login] = assignee
		out = append(out, assignee)
	}
	return out
}

// issueClosedBy returns a GitHub REST API user shape for the closer, or nil.
// It mirrors the assignee resolution pattern and falls back to a minimal user map.
func issueClosedBy(login string, resolver UserResolver) any {
	login = strings.TrimSpace(login)
	if login == "" {
		return nil
	}
	if resolver != nil {
		if u, err := resolver(login); err == nil {
			return User(u)
		}
	}
	return map[string]any{
		"login":      login,
		"avatar_url": fmt.Sprintf("%s/avatars/%s", base(), login),
		"url":        userAPIURL(login),
		"html_url":   userHTMLURL(login),
		"type":       db.TypeUser,
		"site_admin": false,
	}
}
