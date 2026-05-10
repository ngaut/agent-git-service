package httputil

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestErrorBody(t *testing.T) {
	tests := []struct {
		name  string
		input string
		limit int64
		want  string
	}{
		{"trims_whitespace", "  \n hello \t", 1024, "hello"},
		{"caps_at_limit", strings.Repeat("x", 10_000), 64, strings.Repeat("x", 64)},
		{"zero_limit_uses_default", strings.Repeat("y", 10_000), 0, strings.Repeat("y", int(DefaultErrorBody))},
		{"negative_limit_uses_default", "boom", -1, "boom"},
		{"empty_input", "", 1024, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ErrorBody(strings.NewReader(tt.input), tt.limit)
			if got != tt.want {
				t.Errorf("got %q (len=%d), want %q (len=%d)", got, len(got), tt.want, len(tt.want))
			}
		})
	}
}

func TestErrorBody_InvalidUTF8(t *testing.T) {
	got := ErrorBody(bytes.NewReader([]byte{'b', 'a', 'd', ':', ' ', 0xff}), 1024)
	want := "bad: \ufffd"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestStatusError(t *testing.T) {
	tests := []struct {
		name        string
		resp        *http.Response
		limit       int64
		wantMessage string
	}{
		{
			name: "decodes_declared_charset",
			resp: &http.Response{
				StatusCode: http.StatusBadRequest,
				Header: http.Header{
					"Content-Type": []string{"text/plain; charset=iso-8859-1"},
				},
				Body: io.NopCloser(bytes.NewReader([]byte("ol\xe1"))),
			},
			limit:       1024,
			wantMessage: "upstream returned 400: ol\u00e1",
		},
		{
			name: "reports_oversized_body",
			resp: &http.Response{
				StatusCode: http.StatusBadGateway,
				Body:       io.NopCloser(strings.NewReader(strings.Repeat("x", 16))),
			},
			limit:       8,
			wantMessage: "upstream returned 502: response body exceeds 8 bytes",
		},
		{
			name: "reports_empty_body",
			resp: &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Body:       io.NopCloser(strings.NewReader(" \n\t ")),
			},
			limit:       64,
			wantMessage: "upstream returned 503: empty response body",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := StatusError("upstream returned", tt.resp, tt.limit)
			if err == nil {
				t.Fatal("StatusError returned nil")
			}
			if err.Error() != tt.wantMessage {
				t.Fatalf("got %q, want %q", err.Error(), tt.wantMessage)
			}
		})
	}
}
