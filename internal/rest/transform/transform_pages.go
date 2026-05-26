package transform

import (
	"fmt"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"
)

// PagesConfig shapes a db.PagesConfig as a GitHub-compatible Pages JSON
// object. v1 doesn't run a real publishing pipeline, so keep status at
// "queued" instead of overstating that hosted content is already built.
func PagesConfig(repoFullName string, c db.PagesConfig) map[string]any {
	return map[string]any{
		"url":            fmt.Sprintf("%s/repos/%s/pages", apiBase(), repoFullName),
		"status":         "queued",
		"cname":          stringOrNil(c.CNAME),
		"custom_404":     false,
		"html_url":       pagesHTMLURL(repoFullName, c),
		"build_type":     defaultBuildType(c.BuildType),
		"source":         map[string]any{"branch": c.SourceBranch, "path": c.SourcePath},
		"public":         true,
		"https_enforced": c.HTTPSEnforced,
		"created_at":     c.CreatedAt.Format(time.RFC3339),
		"updated_at":     c.UpdatedAt.Format(time.RFC3339),
	}
}

// PagesBuild shapes a db.PagesBuild as a GitHub-compatible build entry.
// The "url" field GitHub returns is intentionally omitted: this server
// doesn't expose a single-build GET endpoint yet, so surfacing a URL
// would be a 404 trap. Add it back when the endpoint lands.
func PagesBuild(repoFullName string, b db.PagesBuild) map[string]any {
	_ = repoFullName
	return map[string]any{
		"status":     b.Status,
		"error":      map[string]any{"message": stringOrNil(b.ErrorMessage)},
		"pusher":     stringOrNil(b.PusherLogin),
		"commit":     stringOrNil(b.CommitSHA),
		"duration":   b.Duration,
		"created_at": b.CreatedAt.Format(time.RFC3339),
		"updated_at": b.UpdatedAt.Format(time.RFC3339),
	}
}

func defaultBuildType(s string) string {
	if s == "" {
		return "legacy"
	}
	return s
}

func pagesHTMLURL(repoFullName string, c db.PagesConfig) string {
	if c.CNAME != "" {
		return "https://" + c.CNAME
	}
	return fmt.Sprintf("%s/pages/%s", htmlBase(), repoFullName)
}
