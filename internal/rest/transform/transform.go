// Package transform converts GORM DB models to GitHub-compatible JSON shapes.
// All URLs are generated from the baseURL set via Init() — never hardcoded.
package transform

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"gh-server/internal/db"
)

var baseURL string

// Init sets the base URL used by all transform functions.
// Must be called once at startup before any handler serves requests.
func Init(base string) { baseURL = base }

func base() string { return baseURL }

// Base returns the base URL for constructing API URLs.
// Exported for use by handler files that build URLs outside the transform package.
func Base() string { return baseURL }

// host extracts the hostname from baseURL for ssh/git URL generation.
func host() string {
	if u, err := url.Parse(baseURL); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	return "localhost"
}

// htmlBase returns the base URL with https:// scheme, used for html_url fields.
// GitHub always uses https:// for user-facing URLs; the CLI tests assert this.
func htmlBase() string {
	return strings.Replace(baseURL, "http://", "https://", 1)
}

// HTMLBase returns the HTTPS base URL for constructing html_url fields.
// Exported for use by handler files that build URLs outside the transform package.
func HTMLBase() string {
	return strings.Replace(baseURL, "http://", "https://", 1)
}

func repoAPIURL(fullName string) string  { return base() + "/api/v3/repos/" + fullName }
func repoHTMLURL(fullName string) string { return htmlBase() + "/" + fullName }
func userAPIURL(login string) string     { return base() + "/api/v3/users/" + login }
func userHTMLURL(login string) string    { return htmlBase() + "/" + login }

func canonicalRepositoryPermission(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "admin":
		return "admin"
	case "maintain", "write", "push":
		return "write"
	case "triage", "read", "pull":
		return "read"
	default:
		return "none"
	}
}

// NodeID generates a globally unique GraphQL-style Relay ID for a given type.
func NodeID(typ string, id any) string {
	raw := fmt.Sprintf("%s_%v", typ, id)
	return base64.StdEncoding.EncodeToString([]byte(raw))
}

// nodeID provides internal backwards compatibility.
func nodeID(typ string, id any) string {
	return NodeID(typ, id)
}

func actionRunURL(fullName string, runID uint) string {
	return fmt.Sprintf("%s/api/v3/repos/%s/actions/runs/%d", base(), fullName, runID)
}

// User converts a db.User to a GitHub REST API user object.
// GraphQL counterpart: graphql/gql_shapes.go repoGQL()/issueGQL() author fields.
func User(u db.User) map[string]any {
	nodeType := db.TypeUser
	if u.Type == db.TypeOrganization {
		nodeType = db.TypeOrganization
	}
	apiURL := userAPIURL(u.Login)
	out := map[string]any{
		"id":      u.ID,
		"node_id": nodeID(nodeType, u.ID),
		"login":   u.Login,
		"name":    u.Name,
		// Email is private; only the authenticated user sees it (see UserPrivate).
		"email":               "",
		"bio":                 u.Bio,
		"type":                u.Type,
		"site_admin":          u.SiteAdmin,
		"user_kind":           u.UserKind,
		"avatar_url":          fmt.Sprintf("%s/avatars/%s", base(), u.Login),
		"url":                 apiURL,
		"html_url":            userHTMLURL(u.Login),
		"repos_url":           apiURL + "/repos",
		"followers_url":       apiURL + "/followers",
		"following_url":       apiURL + "/following{/other_user}",
		"gists_url":           apiURL + "/gists{/gist_id}",
		"starred_url":         apiURL + "/starred{/owner}{/repo}",
		"organizations_url":   apiURL + "/orgs",
		"events_url":          apiURL + "/events{/privacy}",
		"received_events_url": apiURL + "/received_events",
		"created_at":          u.CreatedAt.Format(time.RFC3339),
		"updated_at":          u.UpdatedAt.Format(time.RFC3339),
	}
	if u.Type == db.TypeOrganization {
		out["default_repository_permission"] = canonicalRepositoryPermission(u.DefaultRepositoryPermission)
	}
	return out
}

// UserPrivate returns the same shape as User but includes private fields.
func UserPrivate(u db.User) map[string]any {
	m := User(u)
	m["email"] = u.Email
	return m
}

// RepoStats holds computed values that require service/git access.
type RepoStats struct {
	ForksCount      int
	OpenIssuesCount int
	StargazersCount int
	Size            int // kilobytes
	Permissions     RepoPermissions
	HasPermissions  bool
	HasPages        bool
}

// RepoPermissions holds effective capabilities for the current viewer.
type RepoPermissions struct {
	Pull     bool
	Triage   bool
	Push     bool
	Maintain bool
	Admin    bool
}

// Repo converts a db.Repository to a GitHub REST API repository object.
// GraphQL counterpart: graphql/gql_queries.go doRepository().
func Repo(r db.Repository, stats ...RepoStats) map[string]any {
	var st RepoStats
	if len(stats) > 0 {
		st = stats[0]
	}
	permissions := map[string]any{
		"admin":    false,
		"maintain": false,
		"push":     true,
		"triage":   true,
		"pull":     true,
	}
	if st.HasPermissions {
		permissions = map[string]any{
			"admin":    st.Permissions.Admin,
			"maintain": st.Permissions.Maintain,
			"push":     st.Permissions.Push,
			"triage":   st.Permissions.Triage,
			"pull":     st.Permissions.Pull,
		}
	}
	pushedAt := r.CreatedAt.Format(time.RFC3339)
	if r.PushedAt != nil {
		pushedAt = r.PushedAt.Format(time.RFC3339)
	}
	var parent any
	if r.Fork && r.Parent != nil {
		p := Repo(*r.Parent)
		parent = p
	}
	visibility := strings.ToLower(strings.TrimSpace(r.Visibility))
	switch visibility {
	case "public", "private", "internal":
	default:
		visibility = "public"
		if r.Private {
			visibility = "private"
		}
	}
	if visibility != "internal" && r.Private {
		visibility = "private"
	}
	private := visibility != "public"
	return map[string]any{
		"id":                             r.ID,
		"node_id":                        nodeID("Repository", r.ID),
		"name":                           r.Name,
		"full_name":                      r.FullName,
		"description":                    r.Description,
		"private":                        private,
		"visibility":                     visibility,
		"fork":                           r.Fork,
		"archived":                       r.Archived,
		"disabled":                       r.Disabled,
		"is_template":                    r.IsTemplate,
		"has_issues":                     r.HasIssues,
		"has_wiki":                       r.HasWiki,
		"has_projects":                   r.HasProjects,
		"has_discussions":                r.HasDiscussions,
		"default_branch":                 r.DefaultBranch,
		"language":                       r.Language,
		"owner":                          User(r.Owner),
		"parent":                         parent,
		"template_repository":            nil,
		"url":                            repoAPIURL(r.FullName),
		"html_url":                       repoHTMLURL(r.FullName),
		"clone_url":                      fmt.Sprintf("%s/%s.git", base(), r.FullName),
		"ssh_url":                        fmt.Sprintf("git@%s:%s.git", host(), r.FullName),
		"git_url":                        fmt.Sprintf("git://%s/%s.git", host(), r.FullName),
		"issues_url":                     fmt.Sprintf("%s/api/v3/repos/%s/issues{/number}", base(), r.FullName),
		"pulls_url":                      fmt.Sprintf("%s/api/v3/repos/%s/pulls{/number}", base(), r.FullName),
		"branches_url":                   fmt.Sprintf("%s/api/v3/repos/%s/branches{/branch}", base(), r.FullName),
		"pushed_at":                      pushedAt,
		"created_at":                     r.CreatedAt.Format(time.RFC3339),
		"updated_at":                     r.UpdatedAt.Format(time.RFC3339),
		"forks_count":                    st.ForksCount,
		"stargazers_count":               st.StargazersCount,
		"watchers_count":                 st.StargazersCount,
		"open_issues_count":              st.OpenIssuesCount,
		"size":                           st.Size,
		"has_pages":                      st.HasPages,
		"has_downloads":                  r.HasDownloads,
		"homepage":                       stringOrNil(r.Homepage),
		"license":                        RepoLicense(r.License),
		"allow_forking":                  true,
		"allow_merge_commit":             r.AllowMergeCommit,
		"allow_squash_merge":             r.AllowSquashMerge,
		"allow_rebase_merge":             r.AllowRebaseMerge,
		"allow_auto_merge":               r.AllowAutoMerge,
		"allow_update_branch":            r.AllowUpdateBranch,
		"delete_branch_on_merge":         r.DeleteBranchOnMerge,
		"use_squash_pr_title_as_default": false,
		"squash_merge_commit_title":      "COMMIT_OR_PR_TITLE",
		"squash_merge_commit_message":    "COMMIT_MESSAGES",
		"merge_commit_title":             "MERGE_MESSAGE",
		"merge_commit_message":           "PR_TITLE",
		"web_commit_signoff_required":    false,
		"security_and_analysis":          repoSecurityAndAnalysis(st),
		"permissions":                    permissions,
		"topics":                         RepoTopics(r.Topics),
		"forks":                          st.ForksCount,
		"open_issues":                    st.OpenIssuesCount,
		"watchers":                       st.StargazersCount,
	}
}

func repoSecurityAndAnalysis(st RepoStats) any {
	if !st.HasPermissions || !st.Permissions.Admin {
		return nil
	}
	disabled := func() map[string]any {
		return map[string]any{"status": "disabled"}
	}
	return map[string]any{
		"advanced_security":                         disabled(),
		"code_security":                             disabled(),
		"dependabot_security_updates":               disabled(),
		"secret_scanning":                           disabled(),
		"secret_scanning_ai_detection":              disabled(),
		"secret_scanning_delegated_alert_dismissal": disabled(),
		"secret_scanning_non_provider_patterns":     disabled(),
		"secret_scanning_push_protection":           disabled(),
		"secret_scanning_validity_checks":           disabled(),
	}
}

// RepoLicense converts the stored SPDX-ish license key into GitHub's license object.
func RepoLicense(raw string) any {
	key := strings.ToLower(strings.TrimSpace(raw))
	if key == "" {
		return nil
	}
	names := map[string]string{
		"mit":        "MIT License",
		"apache-2.0": "Apache License 2.0",
		"gpl-3.0":    "GNU General Public License v3.0",
	}
	spdx := map[string]string{
		"mit":        "MIT",
		"apache-2.0": "Apache-2.0",
		"gpl-3.0":    "GPL-3.0",
	}
	name := names[key]
	if name == "" {
		name = raw
	}
	spdxID := spdx[key]
	if spdxID == "" {
		spdxID = raw
	}
	return map[string]any{
		"key":     key,
		"name":    name,
		"spdx_id": spdxID,
		"url":     fmt.Sprintf("%s/api/v3/licenses/%s", base(), key),
		"node_id": NodeID("License", key),
	}
}

// RepoTopics splits a comma-separated topic string into a slice.
// Returns an empty non-nil slice when the input is empty.
func RepoTopics(t string) []string {
	if t == "" {
		return []string{}
	}
	return strings.Split(t, ",")
}

// Branch converts branch name + sha + repo full name to a GitHub branch object.
func Branch(repoFullName, name, sha string) map[string]any {
	return map[string]any{
		"name":      name,
		"commit":    BranchCommit(repoFullName, sha),
		"protected": false,
	}
}

// BranchCommit returns the slim commit object embedded inside a branch response.
func BranchCommit(repoFullName, sha string) map[string]any {
	return map[string]any{
		"sha": sha,
		"url": fmt.Sprintf("%s/api/v3/repos/%s/commits/%s", base(), repoFullName, sha),
	}
}

// CommitMeta holds optional git commit metadata for enriching the Commit response.
type CommitMeta struct {
	Message    string
	AuthorName string
	Email      string
	Date       string
	ParentSHAs []string
}

// Commit converts a sha into a GitHub commit object.
// When meta is provided, the commit message and author are filled with real data.
func Commit(repoFullName, sha string, meta ...CommitMeta) map[string]any {
	commitURL := fmt.Sprintf("%s/api/v3/repos/%s/commits/%s", base(), repoFullName, sha)

	message := "commit"
	authorName := "gh-server"
	email := fmt.Sprintf("noreply@%s", host())
	date := time.Now().Format(time.RFC3339)

	var m CommitMeta
	hasMeta := len(meta) > 0
	if hasMeta {
		m = meta[0]
	}
	if hasMeta && m.Message != "" {
		message = m.Message
		if m.AuthorName != "" {
			authorName = m.AuthorName
		}
		if m.Email != "" {
			email = m.Email
		}
		if m.Date != "" {
			date = m.Date
		}
	}

	parents := []any{}
	if hasMeta && len(m.ParentSHAs) > 0 {
		parents = make([]any, 0, len(m.ParentSHAs))
		for _, parentSHA := range m.ParentSHAs {
			if parentSHA == "" {
				continue
			}
			parents = append(parents, map[string]any{
				"sha":      parentSHA,
				"url":      fmt.Sprintf("%s/api/v3/repos/%s/git/commits/%s", base(), repoFullName, parentSHA),
				"html_url": fmt.Sprintf("%s/%s/commit/%s", htmlBase(), repoFullName, parentSHA),
			})
		}
	}

	ghAuthor := map[string]any{"name": authorName, "email": email, "date": date}
	return map[string]any{
		"sha": sha,
		"commit": map[string]any{
			"message":   message,
			"author":    ghAuthor,
			"committer": ghAuthor,
			"tree":      map[string]any{"sha": sha},
			"url":       fmt.Sprintf("%s/api/v3/repos/%s/git/commits/%s", base(), repoFullName, sha),
		},
		"author": map[string]any{
			"login": authorName,
			"type":  db.TypeUser,
		},
		"committer": map[string]any{
			"login": authorName,
			"type":  db.TypeUser,
		},
		"parents":  parents,
		"url":      commitURL,
		"html_url": fmt.Sprintf("%s/%s/commit/%s", htmlBase(), repoFullName, sha),
	}
}

// stringOrNil returns nil for empty strings, otherwise the string value.
// GitHub REST API returns null for unset string fields rather than "".
func stringOrNil(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Gist converts a db.Gist to GitHub JSON.
func Gist(g db.Gist) map[string]any {
	var files map[string]any
	if g.Files != "" {
		if err := json.Unmarshal([]byte(g.Files), &files); err != nil {
			slog.Warn("failed to unmarshal gist Files", "error", err, "gist_id", g.ID)
		}
	}
	if files == nil {
		files = map[string]any{}
	}
	return map[string]any{
		"id":           g.ID,
		"node_id":      nodeID("Gist", g.Owner.ID), // gists use string IDs; use owner ID for uniqueness
		"description":  g.Description,
		"public":       g.Public,
		"owner":        User(g.Owner),
		"files":        files,
		"html_url":     fmt.Sprintf("%s/gist/%s", base(), g.ID),
		"git_pull_url": fmt.Sprintf("git://%s/gist/%s.git", host(), g.ID),
		"git_push_url": fmt.Sprintf("git@%s:gist/%s.git", host(), g.ID),
		"comments":     0,
		"created_at":   g.CreatedAt.Format(time.RFC3339),
		"updated_at":   g.UpdatedAt.Format(time.RFC3339),
	}
}
