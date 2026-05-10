package rest_test

import (
	"context"
	"testing"

	"gh-server/internal/testharness"
)

// ─── Label GET Response Fields ──────────────────────────────────────────────

func TestCompat_LabelGet_ResponseFields(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	compatSeedRepo(t, h, "compat-label")

	// Use a unique label name to avoid conflict with default labels.
	_, err := h.Svc.CreateLabel(ctx, "testuser/compat-label", "compat-unique-label", "d73a4a", "Something is broken")
	if err != nil {
		t.Fatalf("seed label: %v", err)
	}

	w := h.DoREST(t, "GET", "/api/v3/repos/testuser/compat-label/labels/compat-unique-label", nil)
	assertStatusCode(t, w, 200)
	body := testharness.DecodeJSON(t, w)

	assertFieldsPresent(t, body, map[string]string{
		"id":          "number",
		"node_id":     "string",
		"name":        "string",
		"color":       "string",
		"description": "string",
		"default":     "bool",
		"url":         "string",
	})
}

// ─── Label List Response ────────────────────────────────────────────────────

func TestCompat_LabelList_ResponseShape(t *testing.T) {
	h := testharness.New(t)
	ctx := context.Background()
	compatSeedRepo(t, h, "compat-label-list")

	_, err := h.Svc.CreateLabel(ctx, "testuser/compat-label-list", "compat-list-label", "d73a4a", "List response seed")
	if err != nil {
		t.Fatalf("seed label: %v", err)
	}

	w := h.DoREST(t, "GET", "/api/v3/repos/testuser/compat-label-list/labels", nil)
	assertStatusCode(t, w, 200)
	items := testharness.DecodeJSONArray(t, w)

	if len(items) == 0 {
		t.Fatal("expected at least 1 label")
	}
	for _, lbl := range items {
		assertFieldPresent(t, lbl, "id", "number")
		assertFieldPresent(t, lbl, "name", "string")
		assertFieldPresent(t, lbl, "color", "string")
		assertFieldPresent(t, lbl, "url", "string")
	}
}
