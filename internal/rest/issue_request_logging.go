package rest

import (
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strings"
	"unicode/utf8"
)

const maxLoggedIssueBodyBytes = 4 << 10

func isIssueBodyTooLongError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "data too long for column 'body'") ||
		strings.Contains(msg, `data too long for column "body"`) ||
		strings.Contains(msg, "data too long for column `body`") ||
		strings.Contains(msg, "body exceeds mediumtext limit")
}

func logIssueBodyTooLong(op string, r *http.Request, repoFullName string, issueNumber *int, title string, body string, state *string, stateReason *string, labels []string, assignees []string) {
	bodyExcerpt, bodyTruncated := truncateForLog(body, maxLoggedIssueBodyBytes)
	attrs := []any{
		"path", r.URL.Path,
		"method", r.Method,
		"repo", repoFullName,
		"title", title,
		"title_bytes", len(title),
		"title_chars", utf8.RuneCountInString(title),
		"body_bytes", len(body),
		"body_chars", utf8.RuneCountInString(body),
		"body_sha256", sha256Hex(body),
		"body_truncated", bodyTruncated,
		"body_excerpt", bodyExcerpt,
	}
	if issueNumber != nil {
		attrs = append(attrs, "issue_number", *issueNumber)
	}
	if state != nil {
		attrs = append(attrs, "state", *state)
	}
	if stateReason != nil {
		attrs = append(attrs, "state_reason", *stateReason)
	}
	if len(labels) > 0 {
		attrs = append(attrs, "labels", labels)
	}
	if len(assignees) > 0 {
		attrs = append(attrs, "assignees", assignees)
	}
	slog.ErrorContext(r.Context(), op, attrs...)
}

func logIssueCommentBodyTooLong(op string, r *http.Request, repoFullName string, issueNumber *int, commentID *int, body string) {
	bodyExcerpt, bodyTruncated := truncateForLog(body, maxLoggedIssueBodyBytes)
	attrs := []any{
		"path", r.URL.Path,
		"method", r.Method,
		"repo", repoFullName,
		"body_bytes", len(body),
		"body_chars", utf8.RuneCountInString(body),
		"body_sha256", sha256Hex(body),
		"body_truncated", bodyTruncated,
		"body_excerpt", bodyExcerpt,
	}
	if issueNumber != nil {
		attrs = append(attrs, "issue_number", *issueNumber)
	}
	if commentID != nil {
		attrs = append(attrs, "comment_id", *commentID)
	}
	slog.ErrorContext(r.Context(), op, attrs...)
}

func truncateForLog(s string, maxBytes int) (string, bool) {
	if len(s) <= maxBytes {
		return s, false
	}
	truncated := s[:maxBytes]
	for len(truncated) > 0 && !utf8.ValidString(truncated) {
		truncated = truncated[:len(truncated)-1]
	}
	return truncated, true
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func ptrValue(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func ptrSliceValue[T any](s *[]T) []T {
	if s == nil {
		return nil
	}
	return *s
}
