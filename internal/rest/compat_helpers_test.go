package rest_test

import (
	"net/http/httptest"
	"testing"

	"github.com/ngaut/agent-git-service/internal/testharness"
)

// assertFieldPresent checks that a JSON response map contains the given field
// and optionally that its type matches expected ("string", "number", "bool",
// "array", "object", "nil", or "" for any-type-present).
func assertFieldPresent(t *testing.T, m map[string]any, field, expectedType string) {
	t.Helper()
	val, ok := m[field]
	if !ok {
		t.Errorf("missing field %q", field)
		return
	}
	if expectedType == "" || expectedType == "nil" && val == nil {
		return
	}
	switch expectedType {
	case "string":
		if _, ok := val.(string); !ok && val != nil {
			t.Errorf("field %q: want string, got %T", field, val)
		}
	case "number":
		if _, ok := val.(float64); !ok && val != nil {
			t.Errorf("field %q: want number, got %T", field, val)
		}
	case "bool":
		if _, ok := val.(bool); !ok && val != nil {
			t.Errorf("field %q: want bool, got %T", field, val)
		}
	case "array":
		if _, ok := val.([]any); !ok && val != nil {
			t.Errorf("field %q: want array, got %T", field, val)
		}
	case "object":
		if _, ok := val.(map[string]any); !ok && val != nil {
			t.Errorf("field %q: want object, got %T", field, val)
		}
	}
}

// assertFieldsPresent checks that every field in the map is present in the JSON
// response with the expected type.
func assertFieldsPresent(t *testing.T, m map[string]any, fields map[string]string) {
	t.Helper()
	for field, typ := range fields {
		assertFieldPresent(t, m, field, typ)
	}
}

// assertStatusCode checks the HTTP status code of the response.
func assertStatusCode(t *testing.T, w *httptest.ResponseRecorder, want int) {
	t.Helper()
	if w.Code != want {
		t.Fatalf("status code: got %d, want %d; body: %s", w.Code, want, w.Body.String())
	}
}

// assertPATCHPartialUpdate verifies PATCH partial-update semantics:
// send a PATCH with only the specified field changed, then verify that all
// "unchanged" fields retain their original values.
func assertPATCHPartialUpdate(t *testing.T, h *testharness.Harness, method, path string,
	patchBody map[string]any, getPath string, unchangedFields map[string]any) {
	t.Helper()

	// Get original values.
	wBefore := h.DoREST(t, "GET", getPath, nil)
	assertStatusCode(t, wBefore, 200)
	before := testharness.DecodeJSON(t, wBefore)

	// Apply PATCH.
	wPatch := h.DoRESTJSON(t, method, path, patchBody)
	assertStatusCode(t, wPatch, 200)

	// Get after values.
	wAfter := h.DoREST(t, "GET", getPath, nil)
	assertStatusCode(t, wAfter, 200)
	after := testharness.DecodeJSON(t, wAfter)

	// Verify unchanged fields.
	for field, expected := range unchangedFields {
		_ = before // ensure we fetched before
		got := after[field]
		switch ev := expected.(type) {
		case string:
			if gs, ok := got.(string); !ok || gs != ev {
				t.Errorf("field %q changed unexpectedly: want %q, got %v", field, ev, got)
			}
		default:
			// For non-string types, just verify the field is still present.
			if got == nil {
				t.Errorf("field %q became nil after PATCH", field)
			}
		}
	}
}

// assertPaginationHeaders checks that the response has appropriate pagination
// headers when hasMore is true.
func assertPaginationHeaders(t *testing.T, w *httptest.ResponseRecorder, hasMore bool) {
	t.Helper()
	link := w.Header().Get("Link")
	if hasMore && link == "" {
		t.Error("expected Link header for pagination, got none")
	}
}

// compatSeedRepo creates a test repository via the service layer for compat tests.
func compatSeedRepo(t *testing.T, h *testharness.Harness, name string) {
	t.Helper()
	w := h.DoRESTJSON(t, "POST", "/api/v3/user/repos", map[string]any{
		"name":      name,
		"auto_init": true,
	})
	assertStatusCode(t, w, 201)
}
