package rest_test

import (
	"bytes"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"gh-server/internal/testharness"
)

func createRelease(t *testing.T, h *testharness.Harness, repo, tag string) map[string]any {
	t.Helper()
	w := h.DoRESTJSON(t, "POST", fmt.Sprintf("/api/v3/repos/%s/%s/releases", h.User.Login, repo), map[string]any{
		"tag_name": tag,
		"name":     "Release " + tag,
		"body":     "Notes for " + tag,
	})
	assertStatusCode(t, w, 201)
	return testharness.DecodeJSON(t, w)
}

func requireID(t *testing.T, m map[string]any, field string) int {
	t.Helper()
	raw, ok := m[field]
	if !ok {
		t.Fatalf("missing %s field", field)
	}
	id, ok := raw.(float64)
	if !ok {
		t.Fatalf("expected %s to be number, got %T", field, raw)
	}
	return int(id)
}

func TestReleaseHandlers_ListAndGet(t *testing.T) {
	h := testharness.New(t)
	repo := "release-list"
	compatSeedRepo(t, h, repo)

	rel1 := createRelease(t, h, repo, "v1.0.0")
	_ = createRelease(t, h, repo, "v1.1.0")

	w := h.DoREST(t, "GET", fmt.Sprintf("/api/v3/repos/%s/%s/releases", h.User.Login, repo), nil)
	assertStatusCode(t, w, 200)
	list := testharness.DecodeJSONArray(t, w)
	if len(list) != 2 {
		t.Fatalf("expected 2 releases, got %d", len(list))
	}
	tags := map[string]bool{}
	for _, item := range list {
		tag, _ := item["tag_name"].(string)
		tags[tag] = true
	}
	if !tags["v1.0.0"] || !tags["v1.1.0"] {
		t.Fatalf("expected tags v1.0.0 and v1.1.0 in list, got %v", tags)
	}

	releaseID := requireID(t, rel1, "id")
	w = h.DoREST(t, "GET", fmt.Sprintf("/api/v3/repos/%s/%s/releases/%d", h.User.Login, repo, releaseID), nil)
	assertStatusCode(t, w, 200)
	got := testharness.DecodeJSON(t, w)
	if got["tag_name"] != "v1.0.0" {
		t.Fatalf("expected tag_name v1.0.0, got %v", got["tag_name"])
	}

	w = h.DoREST(t, "GET", fmt.Sprintf("/api/v3/repos/%s/%s/releases/%d", h.User.Login, repo, releaseID+9999), nil)
	assertStatusCode(t, w, 404)
}

func TestReleaseHandlers_DownloadArchiveByTag(t *testing.T) {
	h := testharness.New(t)
	repo := "release-archive-tag"
	compatSeedRepo(t, h, repo)
	_ = createRelease(t, h, repo, "v1.0.0")

	basePath := fmt.Sprintf("/api/v3/repos/%s/%s/archive/refs/tags/", h.User.Login, repo)

	t.Run("zip", func(t *testing.T) {
		w := h.DoREST(t, "GET", basePath+"v1.0.0.zip", nil)
		assertStatusCode(t, w, 200)
		if ct := w.Header().Get("Content-Type"); ct != "application/zip" {
			t.Fatalf("expected Content-Type application/zip, got %q", ct)
		}
		wantCD := "attachment; filename=" + repo + "-1.0.0.zip"
		if cd := w.Header().Get("Content-Disposition"); cd != wantCD {
			t.Fatalf("expected Content-Disposition %q, got %q", wantCD, cd)
		}
		if w.Body.Len() == 0 {
			t.Fatal("expected non-empty zip archive body")
		}
	})

	t.Run("tar.gz", func(t *testing.T) {
		w := h.DoREST(t, "GET", basePath+"v1.0.0.tar.gz", nil)
		assertStatusCode(t, w, 200)
		if ct := w.Header().Get("Content-Type"); ct != "application/x-gzip" {
			t.Fatalf("expected Content-Type application/x-gzip, got %q", ct)
		}
		wantCD := "attachment; filename=" + repo + "-1.0.0.tar.gz"
		if cd := w.Header().Get("Content-Disposition"); cd != wantCD {
			t.Fatalf("expected Content-Disposition %q, got %q", wantCD, cd)
		}
		if w.Body.Len() == 0 {
			t.Fatal("expected non-empty tar.gz archive body")
		}
	})

	t.Run("missing tag", func(t *testing.T) {
		w := h.DoREST(t, "GET", basePath+"v9.9.9.zip", nil)
		assertStatusCode(t, w, 404)
	})
}

func TestReleaseHandlers_Assets(t *testing.T) {
	h := testharness.New(t)
	repo := "release-assets"
	compatSeedRepo(t, h, repo)
	rel := createRelease(t, h, repo, "v2.0.0")
	releaseID := requireID(t, rel, "id")

	t.Run("upload requires name", func(t *testing.T) {
		req := httptest.NewRequest("POST",
			fmt.Sprintf("/api/v3/repos/%s/%s/releases/%d/assets", h.User.Login, repo, releaseID),
			bytes.NewReader([]byte("payload")),
		)
		req.Header.Set("Authorization", "token "+h.Token)
		req.Header.Set("Content-Type", "application/octet-stream")
		w := httptest.NewRecorder()
		h.Mux.ServeHTTP(w, req)
		assertStatusCode(t, w, 422)
		body := testharness.DecodeJSON(t, w)
		if body["message"] != "name is required" {
			t.Fatalf("expected validation message, got %v", body["message"])
		}
	})

	payload := []byte("asset-content")
	uploadReq := httptest.NewRequest("POST",
		fmt.Sprintf("/api/v3/repos/%s/%s/releases/%d/assets?name=artifact.txt", h.User.Login, repo, releaseID),
		bytes.NewReader(payload),
	)
	uploadReq.Header.Set("Authorization", "token "+h.Token)
	uploadReq.Header.Set("Content-Type", "text/plain")
	uploadResp := httptest.NewRecorder()
	h.Mux.ServeHTTP(uploadResp, uploadReq)
	assertStatusCode(t, uploadResp, 201)
	asset := testharness.DecodeJSON(t, uploadResp)
	assetID := requireID(t, asset, "id")

	t.Run("get asset JSON", func(t *testing.T) {
		w := h.DoREST(t, "GET", fmt.Sprintf("/api/v3/repos/%s/%s/releases/assets/%d", h.User.Login, repo, assetID), nil)
		assertStatusCode(t, w, 200)
		if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Fatalf("expected JSON Content-Type, got %q", ct)
		}
		body := testharness.DecodeJSON(t, w)
		if body["name"] != "artifact.txt" {
			t.Fatalf("expected asset name artifact.txt, got %v", body["name"])
		}
	})

	t.Run("get asset binary when Accept octet-stream", func(t *testing.T) {
		req := httptest.NewRequest("GET",
			fmt.Sprintf("/api/v3/repos/%s/%s/releases/assets/%d", h.User.Login, repo, assetID),
			nil,
		)
		req.Header.Set("Authorization", "token "+h.Token)
		req.Header.Set("Accept", "application/octet-stream")
		w := httptest.NewRecorder()
		h.Mux.ServeHTTP(w, req)
		assertStatusCode(t, w, 200)
		if ct := w.Header().Get("Content-Type"); ct != "text/plain" {
			t.Fatalf("expected Content-Type text/plain, got %q", ct)
		}
		wantCD := `attachment; filename="artifact.txt"`
		if cd := w.Header().Get("Content-Disposition"); cd != wantCD {
			t.Fatalf("expected Content-Disposition %q, got %q", wantCD, cd)
		}
		if !bytes.Equal(w.Body.Bytes(), payload) {
			t.Fatalf("unexpected asset content: %q", w.Body.String())
		}
	})

	t.Run("download asset content", func(t *testing.T) {
		w := h.DoREST(t, "GET", fmt.Sprintf("/api/v3/repos/%s/%s/releases/assets/%d/download", h.User.Login, repo, assetID), nil)
		assertStatusCode(t, w, 200)
		if ct := w.Header().Get("Content-Type"); ct != "text/plain" {
			t.Fatalf("expected Content-Type text/plain, got %q", ct)
		}
		wantCD := `attachment; filename="artifact.txt"`
		if cd := w.Header().Get("Content-Disposition"); cd != wantCD {
			t.Fatalf("expected Content-Disposition %q, got %q", wantCD, cd)
		}
		if !bytes.Equal(w.Body.Bytes(), payload) {
			t.Fatalf("unexpected asset content: %q", w.Body.String())
		}
	})
}

func TestReleaseHandlers_DownloadReleaseArchive(t *testing.T) {
	h := testharness.New(t)
	repo := "release-archive"
	compatSeedRepo(t, h, repo)
	rel := createRelease(t, h, repo, "v3.0.0")
	releaseID := requireID(t, rel, "id")

	t.Run("zipball", func(t *testing.T) {
		w := h.DoREST(t, "GET", fmt.Sprintf("/api/v3/repos/%s/%s/releases/%d/archive/zipball", h.User.Login, repo, releaseID), nil)
		assertStatusCode(t, w, 200)
		if ct := w.Header().Get("Content-Type"); ct != "application/zip" {
			t.Fatalf("expected Content-Type application/zip, got %q", ct)
		}
		wantCD := "attachment; filename=" + repo + "-3.0.0.zip"
		if cd := w.Header().Get("Content-Disposition"); cd != wantCD {
			t.Fatalf("expected Content-Disposition %q, got %q", wantCD, cd)
		}
		if w.Body.Len() == 0 {
			t.Fatal("expected non-empty zipball body")
		}
	})

	t.Run("tarball", func(t *testing.T) {
		w := h.DoREST(t, "GET", fmt.Sprintf("/api/v3/repos/%s/%s/releases/%d/archive/tarball", h.User.Login, repo, releaseID), nil)
		assertStatusCode(t, w, 200)
		if ct := w.Header().Get("Content-Type"); ct != "application/x-gzip" {
			t.Fatalf("expected Content-Type application/x-gzip, got %q", ct)
		}
		wantCD := "attachment; filename=" + repo + "-3.0.0.tar.gz"
		if cd := w.Header().Get("Content-Disposition"); cd != wantCD {
			t.Fatalf("expected Content-Disposition %q, got %q", wantCD, cd)
		}
		if w.Body.Len() == 0 {
			t.Fatal("expected non-empty tarball body")
		}
	})

	t.Run("missing release", func(t *testing.T) {
		w := h.DoREST(t, "GET", fmt.Sprintf("/api/v3/repos/%s/%s/releases/%d/archive/zipball", h.User.Login, repo, releaseID+9999), nil)
		assertStatusCode(t, w, 404)
	})
}
