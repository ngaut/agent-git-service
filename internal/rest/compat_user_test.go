package rest_test

import (
	"testing"

	"gh-server/internal/testharness"
)

// ─── User GET Response Fields ───────────────────────────────────────────────

func TestCompat_UserGet_ResponseFields(t *testing.T) {
	h := testharness.New(t)

	w := h.DoREST(t, "GET", "/api/v3/user", nil)
	assertStatusCode(t, w, 200)
	body := testharness.DecodeJSON(t, w)

	// GitHub REST API User response fields (authenticated user).
	assertFieldsPresent(t, body, map[string]string{
		"id":                  "number",
		"node_id":             "string",
		"login":               "string",
		"name":                "string",
		"email":               "",
		"bio":                 "",
		"type":                "string",
		"site_admin":          "bool",
		"avatar_url":          "string",
		"url":                 "string",
		"html_url":            "string",
		"repos_url":           "string",
		"followers_url":       "string",
		"following_url":       "string",
		"gists_url":           "string",
		"starred_url":         "string",
		"organizations_url":   "string",
		"events_url":          "string",
		"received_events_url": "string",
		"created_at":          "string",
		"updated_at":          "string",
	})
}

// ─── User by Login Response Fields ──────────────────────────────────────────

func TestCompat_UserByLogin_ResponseFields(t *testing.T) {
	h := testharness.New(t)

	w := h.DoREST(t, "GET", "/api/v3/users/testuser", nil)
	assertStatusCode(t, w, 200)
	body := testharness.DecodeJSON(t, w)

	assertFieldsPresent(t, body, map[string]string{
		"id":         "number",
		"login":      "string",
		"type":       "string",
		"url":        "string",
		"html_url":   "string",
		"avatar_url": "string",
	})
}
