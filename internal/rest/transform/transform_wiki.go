package transform

import (
	"fmt"
	"net/url"
	"time"

	"gh-server/internal/db"
	"gh-server/internal/service"
)

// WikiPage shapes a service.WikiPage as a JSON object suitable for
// gh-web's wiki UI. The shape is intentionally close to GitHub's
// (slug, title, body, html_url, sha) so future GitHub-compat work
// doesn't churn clients.
func WikiPage(repoFullName string, p service.WikiPage) map[string]any {
	apiSlug := url.PathEscape(p.Slug)
	out := map[string]any{
		"slug":     p.Slug,
		"title":    p.Title,
		"body":     p.Body,
		"html_url": fmt.Sprintf("%s/%s/wiki/%s", htmlBase(), repoFullName, p.Slug),
		"url":      fmt.Sprintf("%s/api/v3/repos/%s/wiki/pages/%s", base(), repoFullName, apiSlug),
		"sha":      p.SHA,
		"labels":   WikiLabels(p.Labels),
	}
	if !p.UpdatedAt.IsZero() {
		out["updated_at"] = p.UpdatedAt.Format(time.RFC3339)
	}
	if p.LastAuthor != nil {
		out["last_author"] = User(*p.LastAuthor)
	} else {
		out["last_author"] = nil
	}
	return out
}

// WikiPageSummary shapes a service.WikiPageSummary for list responses.
func WikiPageSummary(repoFullName string, p service.WikiPageSummary) map[string]any {
	apiSlug := url.PathEscape(p.Slug)
	out := map[string]any{
		"slug":     p.Slug,
		"title":    p.Title,
		"html_url": fmt.Sprintf("%s/%s/wiki/%s", htmlBase(), repoFullName, p.Slug),
		"url":      fmt.Sprintf("%s/api/v3/repos/%s/wiki/pages/%s", base(), repoFullName, apiSlug),
		"labels":   WikiLabels(p.Labels),
	}
	if p.SHA != "" {
		out["sha"] = p.SHA
	}
	if !p.UpdatedAt.IsZero() {
		out["updated_at"] = p.UpdatedAt.Format(time.RFC3339)
	}
	if p.LastAuthor != nil {
		out["last_author"] = User(*p.LastAuthor)
	} else {
		out["last_author"] = nil
	}
	return out
}

// WikiBacklink shapes a service.WikiBacklink for backlink responses.
func WikiBacklink(repoFullName string, p service.WikiBacklink) map[string]any {
	apiSlug := url.PathEscape(p.Slug)
	return map[string]any{
		"slug":     p.Slug,
		"title":    p.Title,
		"snippet":  p.Snippet,
		"html_url": fmt.Sprintf("%s/%s/wiki/%s", htmlBase(), repoFullName, p.Slug),
		"url":      fmt.Sprintf("%s/api/v3/repos/%s/wiki/pages/%s", base(), repoFullName, apiSlug),
	}
}

// WikiSearchResponse shapes repo-scoped wiki search results and metadata.
func WikiSearchResponse(repoFullName string, resp service.WikiSearchResponse) map[string]any {
	results := make([]any, 0, len(resp.Results))
	for _, row := range resp.Results {
		apiSlug := url.PathEscape(row.Slug)
		results = append(results, map[string]any{
			"slug":     row.Slug,
			"title":    row.Title,
			"score":    row.Score,
			"snippet":  row.Snippet,
			"html_url": fmt.Sprintf("%s/%s/wiki/%s", htmlBase(), repoFullName, row.Slug),
			"url":      fmt.Sprintf("%s/api/v3/repos/%s/wiki/pages/%s", base(), repoFullName, apiSlug),
			"labels":   WikiLabels(row.Labels),
		})
	}
	return map[string]any{
		"results":    results,
		"query":      resp.Query,
		"method":     resp.Method,
		"elapsed_ms": resp.ElapsedMS,
	}
}

func WikiLabels(labels []db.Label) []any {
	out := make([]any, 0, len(labels))
	for _, label := range labels {
		out = append(out, Label(label))
	}
	return out
}

// WikiMoveResult shapes a wiki move plus any inbound reference rewrites.
func WikiMoveResult(repoFullName string, result service.WikiMoveResult) map[string]any {
	rewrites := make([]any, 0, len(result.Rewrites))
	for _, rewrite := range result.Rewrites {
		rewrites = append(rewrites, WikiPageSummary(repoFullName, rewrite))
	}
	skipped := make([]any, 0, len(result.Skipped))
	for _, skip := range result.Skipped {
		skipped = append(skipped, map[string]any{
			"slug":   skip.Slug,
			"reason": skip.Reason,
		})
	}
	return map[string]any{
		"moved":    WikiPage(repoFullName, result.Moved),
		"rewrites": rewrites,
		"skipped":  skipped,
	}
}

// WikiPageHistoryEntry shapes one wiki revision row for REST responses.
func WikiPageHistoryEntry(p service.WikiPageHistoryEntry) map[string]any {
	out := map[string]any{
		"sha":       p.SHA,
		"message":   p.Message,
		"body_size": p.BodySize,
		"author":    nil,
		"committer": nil,
	}
	if p.Author != nil {
		out["author"] = User(*p.Author)
	}
	if p.Committer != nil {
		out["committer"] = User(*p.Committer)
	}
	if !p.Date.IsZero() {
		out["date"] = p.Date.Format(time.RFC3339)
	}
	return out
}

// WikiBulkMoveEntry shapes one bulk wiki move row for REST responses.
func WikiBulkMoveEntry(p service.WikiBulkMoveEntry) map[string]any {
	return map[string]any{
		"from": p.From,
		"to":   p.To,
		"sha":  p.SHA,
	}
}

// WikiBulkMoveResult shapes a bulk wiki move plus any inbound reference rewrites.
func WikiBulkMoveResult(repoFullName string, result service.WikiBulkMoveResult) map[string]any {
	moved := make([]any, 0, len(result.Moved))
	for _, item := range result.Moved {
		moved = append(moved, WikiBulkMoveEntry(item))
	}
	rewrites := make([]any, 0, len(result.Rewrites))
	for _, rewrite := range result.Rewrites {
		rewrites = append(rewrites, WikiPageSummary(repoFullName, rewrite))
	}
	skipped := make([]any, 0, len(result.Skipped))
	for _, skip := range result.Skipped {
		skipped = append(skipped, map[string]any{
			"slug":   skip.Slug,
			"reason": skip.Reason,
		})
	}
	return map[string]any{
		"moved":    moved,
		"rewrites": rewrites,
		"skipped":  skipped,
		"commit":   result.Commit,
	}
}
