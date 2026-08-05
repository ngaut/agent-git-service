package middleware

import (
	"compress/flate"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCompressJSONNegotiatesAcceptEncoding(t *testing.T) {
	const body = `{"pages":["one","two","three"]}`
	tests := []struct {
		name         string
		headers      []string
		wantStatus   int
		wantEncoding string
	}{
		{name: "no header", wantStatus: http.StatusOK},
		{name: "empty header", headers: []string{""}, wantStatus: http.StatusOK},
		{name: "gzip", headers: []string{"gzip"}, wantStatus: http.StatusOK, wantEncoding: "gzip"},
		{name: "gzip with identity rejected", headers: []string{"gzip, identity;q=0"}, wantStatus: http.StatusOK, wantEncoding: "gzip"},
		{name: "case insensitive", headers: []string{"GZip"}, wantStatus: http.StatusOK, wantEncoding: "gzip"},
		{name: "explicit gzip rejection", headers: []string{"gzip;q=0"}, wantStatus: http.StatusOK},
		{name: "next acceptable coding", headers: []string{"gzip;q=0, deflate;q=0.7"}, wantStatus: http.StatusOK, wantEncoding: "deflate"},
		{name: "highest quality", headers: []string{"gzip;q=0.4, deflate;q=0.8"}, wantStatus: http.StatusOK, wantEncoding: "deflate"},
		{name: "identity has higher quality", headers: []string{"gzip;q=0.4, identity;q=1"}, wantStatus: http.StatusOK},
		{name: "server precedence breaks tie", headers: []string{"deflate, gzip"}, wantStatus: http.StatusOK, wantEncoding: "gzip"},
		{name: "wildcard", headers: []string{"*;q=0.5"}, wantStatus: http.StatusOK, wantEncoding: "gzip"},
		{name: "explicit rejection overrides wildcard", headers: []string{"gzip;q=0, *;q=0.5"}, wantStatus: http.StatusOK, wantEncoding: "deflate"},
		{name: "multiple field lines", headers: []string{"gzip;q=0", "deflate;q=1"}, wantStatus: http.StatusOK, wantEncoding: "deflate"},
		{name: "unsupported coding falls back to identity", headers: []string{"br"}, wantStatus: http.StatusOK},
		{name: "substring is not a coding", headers: []string{"xgzip"}, wantStatus: http.StatusOK},
		{name: "identity also rejected", headers: []string{"br, identity;q=0"}, wantStatus: http.StatusNotAcceptable},
		{name: "wildcard rejects identity", headers: []string{"*;q=0"}, wantStatus: http.StatusNotAcceptable},
		{name: "explicit identity overrides wildcard", headers: []string{"*;q=0, identity;q=1"}, wantStatus: http.StatusOK},
		{name: "invalid quality rejects coding", headers: []string{"gzip;q=bogus"}, wantStatus: http.StatusOK},
		{name: "out of range quality rejects coding", headers: []string{"gzip;q=2"}, wantStatus: http.StatusOK},
		{name: "overprecise quality rejects coding", headers: []string{"gzip;q=0.1234"}, wantStatus: http.StatusOK},
		{name: "duplicate rejection wins", headers: []string{"gzip;q=1, gzip;q=0"}, wantStatus: http.StatusOK},
	}

	handler := CompressJSON(gzip.BestSpeed)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = io.WriteString(w, body)
	}))

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v3/example", nil)
			for _, value := range tt.headers {
				req.Header.Add("Accept-Encoding", value)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			if w.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", w.Code, tt.wantStatus)
			}
			if got := w.Header().Get("Content-Encoding"); got != tt.wantEncoding {
				t.Fatalf("Content-Encoding = %q, want %q", got, tt.wantEncoding)
			}
			if !varyContains(w.Header(), "Accept-Encoding") {
				t.Fatalf("Vary = %q, want Accept-Encoding", w.Header().Values("Vary"))
			}
			if tt.wantStatus != http.StatusOK {
				if w.Body.Len() != 0 {
					t.Fatalf("body = %q, want empty", w.Body.String())
				}
				return
			}

			decoded := decodeCompressedBody(t, tt.wantEncoding, w.Body.String())
			if decoded != body {
				t.Fatalf("decoded body = %q, want %q", decoded, body)
			}
		})
	}
}

func TestCompressJSONDoesNotMutateRequestAcceptEncoding(t *testing.T) {
	const original = "gzip;q=0.5, deflate;q=1"
	handler := CompressJSON(gzip.BestSpeed)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Accept-Encoding"); got != "deflate" {
			t.Fatalf("inner Accept-Encoding = %q, want canonical deflate", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{}`)
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", original)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if got := req.Header.Get("Accept-Encoding"); got != original {
		t.Fatalf("request Accept-Encoding = %q, want %q", got, original)
	}
}

func TestCompressJSONCombinesVaryValues(t *testing.T) {
	handler := CompressJSON(gzip.BestSpeed)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		appendVary(w.Header(), "Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{}`)
	}))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))

	if got := w.Header().Get("Vary"); got != "Accept-Encoding, Authorization" {
		t.Fatalf("Vary = %q, want combined compression and authorization fields", got)
	}
	if got := w.Header().Values("Vary"); len(got) != 1 {
		t.Fatalf("Vary field lines = %q, want one combined field line", got)
	}
}

func TestCompressJSONRejectsUnencodedResponseWhenIdentityForbidden(t *testing.T) {
	handler := CompressJSON(gzip.BestSpeed)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Content-Length", "14")
		w.Header().Set("ETag", `"plain"`)
		_, _ = io.WriteString(w, "plain response")
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip, identity;q=0")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNotAcceptable {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusNotAcceptable)
	}
	if w.Body.Len() != 0 {
		t.Fatalf("body = %q, want empty", w.Body.String())
	}
	for _, header := range []string{"Content-Encoding", "Content-Length", "Content-Type", "ETag"} {
		if got := w.Header().Get(header); got != "" {
			t.Fatalf("%s = %q, want empty", header, got)
		}
	}
}

func TestCompressJSONAllowsUnencodedResponseWhenIdentityAcceptable(t *testing.T) {
	handler := CompressJSON(gzip.BestSpeed)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "plain response")
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if got := w.Body.String(); got != "plain response" {
		t.Fatalf("body = %q, want plain response", got)
	}
}

func TestCompressJSONAllowsBodylessResponseWhenIdentityForbidden(t *testing.T) {
	handler := CompressJSON(gzip.BestSpeed)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip, identity;q=0")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusNoContent)
	}
}

func TestCompressJSONRejectsImplicitIdentityResponseWhenIdentityForbidden(t *testing.T) {
	handler := CompressJSON(gzip.BestSpeed)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip, identity;q=0")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNotAcceptable {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusNotAcceptable)
	}
}

func decodeCompressedBody(t *testing.T, encoding, body string) string {
	t.Helper()
	if encoding == "" {
		return body
	}
	var reader io.ReadCloser
	var err error
	switch encoding {
	case "gzip":
		reader, err = gzip.NewReader(strings.NewReader(body))
	case "deflate":
		reader = flate.NewReader(strings.NewReader(body))
	default:
		t.Fatalf("unsupported test encoding %q", encoding)
	}
	if err != nil {
		t.Fatalf("open %s body: %v", encoding, err)
	}
	defer reader.Close()
	decoded, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("decode %s body: %v", encoding, err)
	}
	return string(decoded)
}
