package transform

import (
	"fmt"
	"strings"
	"time"

	"gh-server/internal/db"
)

// Attachment converts a db.Attachment to a JSON response object.
func Attachment(a db.Attachment) map[string]any {
	downloadURL := fmt.Sprintf("%s/api/v3/attachments/%s", base(), a.UUID)
	out := map[string]any{
		"id":            a.ID,
		"uuid":          a.UUID,
		"issue_id":      a.IssueID,
		"repository_id": a.RepositoryID,
		"name":          a.OriginalName,
		"content_type":  a.ContentType,
		"size":          a.Size,
		"is_image":      a.IsImage,
		"download_url":  downloadURL,
		"url":           downloadURL,
		"markdown":      attachmentMarkdown(a.OriginalName, downloadURL, a.IsImage),
		"created_at":    a.CreatedAt.Format(time.RFC3339),
		"updated_at":    a.UpdatedAt.Format(time.RFC3339),
	}
	if a.Uploader.ID != 0 || a.Uploader.Login != "" {
		out["uploader"] = User(a.Uploader)
	}
	return out
}

func attachmentMarkdown(name, url string, isImage bool) string {
	label := escapeMarkdownLabel(name)
	if isImage {
		return fmt.Sprintf("![%s](%s)", label, url)
	}
	return fmt.Sprintf("[%s](%s)", label, url)
}

func escapeMarkdownLabel(s string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `[`, `\[`, `]`, `\]`)
	return replacer.Replace(s)
}
