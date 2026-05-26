package transform

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"
)

// MilestoneCounts holds open/closed counts for milestone issues and PRs.
type MilestoneCounts struct {
	OpenIssues   int64
	ClosedIssues int64
}

// Milestone converts a db.Milestone pointer to a REST API milestone object.
// Returns nil if the milestone is nil (no milestone set).
func Milestone(m *db.Milestone, repoFullName string, counts ...MilestoneCounts) any {
	if m == nil {
		return nil
	}
	if repoFullName == "" && m.Repository.FullName != "" {
		repoFullName = m.Repository.FullName
	}
	var dueOn any
	if m.DueOn != nil {
		dueOn = m.DueOn.Format(time.RFC3339)
	}
	var closedAt any
	if m.ClosedAt != nil {
		closedAt = m.ClosedAt.Format(time.RFC3339)
	}
	var creator any
	if m.Creator.ID != 0 {
		creator = User(m.Creator)
	}
	var openIssues, closedIssues int64
	if len(counts) > 0 {
		openIssues = counts[0].OpenIssues
		closedIssues = counts[0].ClosedIssues
	}
	return map[string]any{
		"url":           fmt.Sprintf("%s/repos/%s/milestones/%d", apiBase(), repoFullName, m.Number),
		"html_url":      fmt.Sprintf("%s/%s/milestone/%d", htmlBase(), repoFullName, m.Number),
		"labels_url":    fmt.Sprintf("%s/repos/%s/milestones/%d/labels", apiBase(), repoFullName, m.Number),
		"id":            m.ID,
		"node_id":       nodeID("Milestone", m.ID),
		"number":        m.Number,
		"title":         m.Title,
		"description":   m.Description,
		"creator":       creator,
		"open_issues":   openIssues,
		"closed_issues": closedIssues,
		"state":         m.State,
		"due_on":        dueOn,
		"created_at":    m.CreatedAt.Format(time.RFC3339),
		"updated_at":    m.UpdatedAt.Format(time.RFC3339),
		"closed_at":     closedAt,
	}
}

// Label converts a db.Label to JSON.
func Label(l db.Label) map[string]any {
	return map[string]any{
		"id":          l.ID,
		"node_id":     nodeID("Label", l.ID),
		"name":        l.Name,
		"color":       l.Color,
		"description": l.Description,
		"default":     l.Default,
		"url":         fmt.Sprintf("%s/repos/%s/labels/%s", apiBase(), l.Repository.FullName, l.Name),
	}
}

// Reaction converts a db.Reaction to a GitHub REST API reaction object.
func Reaction(r db.Reaction) map[string]any {
	return map[string]any{
		"id":         r.ID,
		"node_id":    nodeID("Reaction", r.ID),
		"user":       User(r.User),
		"content":    r.Content,
		"created_at": r.CreatedAt.Format(time.RFC3339),
	}
}

// Release converts a db.Release to JSON.
func Release(r db.Release) map[string]any {
	pub := ""
	if r.PublishedAt != nil {
		pub = r.PublishedAt.Format(time.RFC3339)
	}
	// Build assets list using ReleaseAsset() for a single source of truth.
	assets := make([]any, 0, len(r.Assets))
	for _, a := range r.Assets {
		assets = append(assets, ReleaseAsset(a, r.Repository.FullName))
	}
	return map[string]any{
		"id":               r.ID,
		"node_id":          nodeID("Release", r.ID),
		"tag_name":         r.TagName,
		"target_commitish": r.Repository.DefaultBranch,
		"name":             r.Name,
		"body":             r.Body,
		"draft":            r.Draft,
		"prerelease":       r.PreRelease,
		"make_latest":      "true",
		"author":           User(r.Author),
		"url":              fmt.Sprintf("%s/repos/%s/releases/%d", apiBase(), r.Repository.FullName, r.ID),
		"html_url":         fmt.Sprintf("%s/%s/releases/tag/%s", htmlBase(), r.Repository.FullName, r.TagName),
		"assets":           assets,
		"assets_url":       fmt.Sprintf("%s/repos/%s/releases/%d/assets", apiBase(), r.Repository.FullName, r.ID),
		"upload_url":       fmt.Sprintf("%s/repos/%s/releases/%d/assets{?name,label}", apiBase(), r.Repository.FullName, r.ID),
		"tarball_url":      fmt.Sprintf("%s/repos/%s/archive/refs/tags/%s.tar.gz", apiBase(), r.Repository.FullName, r.TagName),
		"zipball_url":      fmt.Sprintf("%s/repos/%s/archive/refs/tags/%s.zip", apiBase(), r.Repository.FullName, r.TagName),
		"created_at":       r.CreatedAt.Format(time.RFC3339),
		"published_at":     pub,
	}
}

// ReleaseAsset converts a db.ReleaseAsset to a GitHub REST API asset object.
// repoFullName is needed to build the asset URL (e.g. "owner/repo").
func ReleaseAsset(a db.ReleaseAsset, repoFullName string) map[string]any {
	assetURL := fmt.Sprintf("%s/repos/%s/releases/assets/%d", apiBase(), repoFullName, a.ID)
	return map[string]any{
		"id":                   a.ID,
		"node_id":              nodeID("ReleaseAsset", a.ID),
		"name":                 a.Name,
		"label":                a.Label,
		"content_type":         a.ContentType,
		"size":                 a.Size,
		"state":                "uploaded",
		"url":                  assetURL, // used by gh CLI (ReleaseAsset.APIURL json:"url")
		"browser_download_url": assetURL,
		"created_at":           a.CreatedAt.Format(time.RFC3339),
		"updated_at":           a.UpdatedAt.Format(time.RFC3339),
	}
}

// DeployKey converts a db.DeployKey to a GitHub REST API deploy key object.
func DeployKey(k db.DeployKey, repoFullName string) map[string]any {
	return map[string]any{
		"id":         k.ID,
		"title":      k.Title,
		"key":        k.Key,
		"read_only":  k.ReadOnly,
		"created_at": k.CreatedAt.Format(time.RFC3339),
		"url":        fmt.Sprintf("%s/repos/%s/keys/%d", apiBase(), repoFullName, k.ID),
	}
}

// SSHKey converts a db.SSHKey to a GitHub REST API SSH key object.
func SSHKey(k db.SSHKey) map[string]any {
	return map[string]any{
		"id":         k.ID,
		"title":      k.Title,
		"key":        k.Key,
		"created_at": k.CreatedAt.Format(time.RFC3339),
		"url":        fmt.Sprintf("%s/user/keys/%d", apiBase(), k.ID),
	}
}

// SSHSigningKey converts a db.SSHSigningKey to a GitHub REST API SSH signing key object.
func SSHSigningKey(k db.SSHSigningKey) map[string]any {
	return map[string]any{
		"id":         k.ID,
		"title":      k.Title,
		"key":        k.Key,
		"created_at": k.CreatedAt.Format(time.RFC3339),
	}
}

// GPGKey converts a db.GPGKey to a GitHub REST API GPG key object.
func GPGKey(k db.GPGKey) map[string]any {
	return map[string]any{
		"id":         k.ID,
		"key_id":     k.KeyID,
		"public_key": k.PublicKey,
		"created_at": k.CreatedAt.Format(time.RFC3339),
	}
}

// Token converts a db.Token to JSON.
func Token(t db.Token) map[string]any {
	var lastUsed any
	if t.LastUsedAt != nil {
		lastUsed = t.LastUsedAt.UTC().Format(time.RFC3339)
	}
	var expiresAt any
	if t.ExpiresAt != nil {
		expiresAt = t.ExpiresAt.UTC().Format(time.RFC3339)
	}
	return map[string]any{
		"id":           t.ID,
		"name":         t.Name,
		"token":        t.Value,
		"created_at":   t.CreatedAt.UTC().Format(time.RFC3339),
		"last_used_at": lastUsed,
		"expires_at":   expiresAt,
	}
}

// Ruleset converts a db.Ruleset to GitHub REST API JSON.
func Ruleset(rs db.Ruleset, repoFullName string) map[string]any {
	var conditions any
	if rs.ConditionsJSON != "" {
		if err := json.Unmarshal([]byte(rs.ConditionsJSON), &conditions); err != nil {
			slog.Warn("failed to unmarshal ConditionsJSON", "error", err, "ruleset_id", rs.ID)
		}
	}
	var rules any
	if rs.RulesJSON != "" {
		if err := json.Unmarshal([]byte(rs.RulesJSON), &rules); err != nil {
			slog.Warn("failed to unmarshal RulesJSON", "error", err, "ruleset_id", rs.ID)
		}
	}
	return map[string]any{
		"id":                      rs.ID,
		"name":                    rs.Name,
		"target":                  rs.Target,
		"enforcement":             rs.Enforcement,
		"source_type":             "Repository",
		"source":                  repoFullName,
		"conditions":              conditions,
		"rules":                   rules,
		"node_id":                 nodeID("Ruleset", rs.ID),
		"current_user_can_bypass": "always",
		"created_at":              rs.CreatedAt.UTC().Format(time.RFC3339),
		"updated_at":              rs.UpdatedAt.UTC().Format(time.RFC3339),
		"_links": map[string]any{
			"self": map[string]any{"href": fmt.Sprintf("%s/repos/%s/rulesets/%d", apiBase(), repoFullName, rs.ID)},
			"html": map[string]any{"href": fmt.Sprintf("%s/%s/rules/%d", htmlBase(), repoFullName, rs.ID)},
		},
	}
}

// Variable converts a db.Variable to GitHub REST API JSON.
func Variable(v db.Variable) map[string]any {
	return map[string]any{
		"name":       v.Name,
		"value":      v.Value,
		"created_at": v.CreatedAt.UTC().Format(time.RFC3339),
		"updated_at": v.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// Secret converts a db.Secret to GitHub REST API JSON.
func Secret(s db.Secret) map[string]any {
	return map[string]any{
		"name":       s.Name,
		"created_at": s.CreatedAt.UTC().Format(time.RFC3339),
		"updated_at": s.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// OrgSecret converts a db.Secret to GitHub REST API JSON for org-level secrets.
func OrgSecret(s db.Secret, orgLogin string) map[string]any {
	return map[string]any{
		"name":                      s.Name,
		"visibility":                s.Visibility,
		"selected_repositories_url": fmt.Sprintf("%s/orgs/%s/actions/secrets/%s/repositories", apiBase(), orgLogin, s.Name),
		"created_at":                s.CreatedAt.UTC().Format(time.RFC3339),
		"updated_at":                s.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// UserCodespacesSecret converts a db.Secret to GitHub REST API JSON for
// user-level Codespaces secrets.
func UserCodespacesSecret(s db.Secret) map[string]any {
	visibility := s.Visibility
	if visibility == "" {
		visibility = "selected"
	}
	return map[string]any{
		"name":                      s.Name,
		"visibility":                visibility,
		"selected_repositories_url": fmt.Sprintf("%s/user/codespaces/secrets/%s/repositories", apiBase(), s.Name),
		"created_at":                s.CreatedAt.UTC().Format(time.RFC3339),
		"updated_at":                s.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// GitRef returns the canonical REST envelope for a Git reference:
// {"ref": <ref>, "object": {"sha": <sha>, "type": "commit"}}.
// Used by GET/POST/PATCH /git/refs handlers so the shape stays consistent.
func GitRef(ref, sha string) map[string]any {
	return map[string]any{
		"ref": ref,
		"object": map[string]any{
			"sha":  sha,
			"type": "commit",
		},
	}
}
