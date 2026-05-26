package rest_test

import (
	"testing"

	"github.com/ngaut/agent-git-service/internal/testharness"
)

// ─── Release GET Response Fields ────────────────────────────────────────────

func TestCompat_ReleaseGet_ResponseFields(t *testing.T) {
	h := testharness.New(t)
	compatSeedRepo(t, h, "compat-release")

	// Create a release via API.
	w := h.DoRESTJSON(t, "POST", "/api/v3/repos/testuser/compat-release/releases", map[string]any{
		"tag_name": "v1.0.0",
		"name":     "Release 1.0",
		"body":     "First release",
	})
	assertStatusCode(t, w, 201)
	created := testharness.DecodeJSON(t, w)

	assertFieldsPresent(t, created, map[string]string{
		"id":               "number",
		"node_id":          "string",
		"tag_name":         "string",
		"target_commitish": "string",
		"name":             "string",
		"body":             "string",
		"draft":            "bool",
		"prerelease":       "bool",
		"make_latest":      "string",
		"author":           "object",
		"url":              "string",
		"html_url":         "string",
		"assets":           "array",
		"assets_url":       "string",
		"upload_url":       "string",
		"tarball_url":      "string",
		"zipball_url":      "string",
		"created_at":       "string",
		"published_at":     "string",
	})
}
