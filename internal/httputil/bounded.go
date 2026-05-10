// Package httputil holds small helpers shared across the server's outbound
// HTTP clients. The focus is narrow: patterns (bounded body reads, status
// triage) that are easy to get wrong and invite DoS when an upstream
// misbehaves.
package httputil

import (
	"bytes"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html/charset"
)

// ErrorBody reads up to limit bytes from r, trims surrounding whitespace, and
// returns the result as a diagnostic string. Invalid UTF-8 is replaced so the
// returned string is always safe to format into logs and wrapped errors. It is
// intended for the error branches of HTTP clients where the caller wants to
// embed the upstream's error payload in a wrapped Go error without risking
// unbounded memory use if the upstream streams a hostile response. Read errors
// are intentionally swallowed.
//
// limit must be positive; a non-positive limit falls back to DefaultErrorBody.
func ErrorBody(r io.Reader, limit int64) string {
	raw, _, err := readErrorBody(r, limit)
	if err != nil {
		return ""
	}
	return normalizeErrorSnippet(raw)
}

// StatusError reads, bounds, decodes, and formats an upstream HTTP error as
// "<prefix> <status>: <detail>". When the body is empty, unreadable, or larger
// than the configured limit, it returns a stable fallback detail instead of
// leaking transport-specific errors or partially decoded garbage.
func StatusError(prefix string, resp *http.Response, limit int64) error {
	if resp == nil {
		return fmt.Errorf("%s: missing HTTP response", prefix)
	}

	raw, oversized, err := readErrorBody(resp.Body, limit)
	if err != nil {
		return fmt.Errorf("%s %d: unable to read response body", prefix, resp.StatusCode)
	}
	if oversized {
		return fmt.Errorf("%s %d: response body exceeds %d bytes", prefix, resp.StatusCode, normalizeLimit(limit))
	}
	return fmt.Errorf("%s %d: %s", prefix, resp.StatusCode, normalizeErrorBody(raw, resp.Header.Get("Content-Type")))
}

// DefaultErrorBody is the fallback cap when a caller passes limit<=0.
const DefaultErrorBody int64 = 4 * 1024

func readErrorBody(r io.Reader, limit int64) ([]byte, bool, error) {
	limit = normalizeLimit(limit)
	if r == nil {
		return nil, false, nil
	}

	raw, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(raw)) > limit {
		return raw[:limit], true, nil
	}
	return raw, false, nil
}

func normalizeErrorBody(raw []byte, contentType string) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return "empty response body"
	}

	decoded := raw
	if charsetName := contentTypeCharset(contentType); charsetName != "" {
		reader, err := charset.NewReaderLabel(charsetName, bytes.NewReader(raw))
		if err == nil {
			if body, readErr := io.ReadAll(reader); readErr == nil {
				decoded = body
			}
		}
	}
	if !utf8.Valid(decoded) {
		decoded = bytes.ToValidUTF8(decoded, []byte("\ufffd"))
	}

	text := strings.TrimSpace(string(decoded))
	if text == "" {
		return "empty response body"
	}
	return text
}

func normalizeErrorSnippet(raw []byte) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return ""
	}
	if !utf8.Valid(raw) {
		raw = bytes.ToValidUTF8(raw, []byte("\ufffd"))
	}
	return string(raw)
}

func contentTypeCharset(contentType string) string {
	if contentType == "" {
		return ""
	}

	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(params["charset"])
}

func normalizeLimit(limit int64) int64 {
	if limit <= 0 {
		return DefaultErrorBody
	}
	return limit
}
