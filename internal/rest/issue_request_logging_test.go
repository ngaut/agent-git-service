package rest

import (
	"bytes"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIsIssueBodyTooLongError(t *testing.T) {
	t.Parallel()

	if !isIssueBodyTooLongError(assertErr("service: create issue: Error 1406 (22001): Data too long for column 'body' at row 1")) {
		t.Fatal("expected MySQL body-too-long error to match")
	}
	if !isIssueBodyTooLongError(assertErr("body exceeds MEDIUMTEXT limit (16777216 bytes > 16777215)")) {
		t.Fatal("expected validation body-too-long error to match")
	}
	if isIssueBodyTooLongError(assertErr("service: create issue: Error 1406 (22001): Data too long for column 'title' at row 1")) {
		t.Fatal("did not expect non-body error to match")
	}
}

func TestLogIssueBodyTooLongIncludesBodyDiagnostics(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)
	t.Cleanup(func() { slog.SetDefault(prev) })

	req := httptest.NewRequest("POST", "/api/v3/repos/acme/demo/issues", nil)
	state := "open"
	stateReason := "completed"
	number := 42
	body := "first line\nsecond line"
	logIssueBodyTooLong("CreateIssue: body too long", req, "acme/demo", &number, "title", body, &state, &stateReason, []string{"bug"}, []string{"alice"})

	out := buf.String()
	for _, want := range []string{
		"CreateIssue: body too long",
		`repo=acme/demo`,
		`issue_number=42`,
		`body_bytes=22`,
		`body_chars=22`,
		`body_truncated=false`,
		`body_excerpt="first line\nsecond line"`,
		`labels=[bug]`,
		`assignees=[alice]`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("log output missing %q:\n%s", want, out)
		}
	}
}

func TestTruncateForLogKeepsUTF8Boundary(t *testing.T) {
	t.Parallel()

	body := strings.Repeat("中", maxLoggedIssueBodyBytes/3+10)
	got, truncated := truncateForLog(body, maxLoggedIssueBodyBytes)
	if !truncated {
		t.Fatal("expected body to be truncated")
	}
	if !utf8Valid(got) {
		t.Fatal("expected truncated excerpt to remain valid UTF-8")
	}
	if len(got) > maxLoggedIssueBodyBytes {
		t.Fatalf("excerpt too long: got %d bytes, max %d", len(got), maxLoggedIssueBodyBytes)
	}
}

func TestLogIssueCommentBodyTooLongIncludesBodyDiagnostics(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)
	t.Cleanup(func() { slog.SetDefault(prev) })

	req := httptest.NewRequest("POST", "/api/v3/repos/acme/demo/issues/42/comments", nil)
	issueNumber := 42
	commentID := 99
	logIssueCommentBodyTooLong("CreateIssueComment: body too long", req, "acme/demo", &issueNumber, &commentID, "comment body")

	out := buf.String()
	for _, want := range []string{
		"CreateIssueComment: body too long",
		`repo=acme/demo`,
		`issue_number=42`,
		`comment_id=99`,
		`body_bytes=12`,
		`body_chars=12`,
		`body_truncated=false`,
		`body_excerpt="comment body"`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("log output missing %q:\n%s", want, out)
		}
	}
}

type assertErr string

func (e assertErr) Error() string { return string(e) }

func utf8Valid(s string) bool {
	return len(s) == 0 || strings.ToValidUTF8(s, "") == s
}
