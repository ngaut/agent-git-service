package rest_test

import (
	"testing"
	"time"

	"gh-server/internal/testharness"
)

func TestTokenAPI_CRUD(t *testing.T) {
	h := testharness.New(t)

	expiresAt := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	w := h.DoRESTJSON(t, "POST", "/api/v3/user/tokens", map[string]any{
		"name":       "ci-token",
		"expires_at": expiresAt,
	})
	assertStatusCode(t, w, 201)
	created := testharness.DecodeJSON(t, w)
	assertFieldsPresent(t, created, map[string]string{
		"id":           "number",
		"name":         "string",
		"token":        "string",
		"created_at":   "string",
		"last_used_at": "string",
		"expires_at":   "string",
	})

	idFloat, ok := created["id"].(float64)
	if !ok || idFloat == 0 {
		t.Fatalf("expected numeric id in response, got %v", created["id"])
	}
	createdID := int(idFloat)

	w = h.DoREST(t, "GET", "/api/v3/user/tokens", nil)
	assertStatusCode(t, w, 200)
	tokens := testharness.DecodeJSONArray(t, w)
	if _, found := findTokenByID(tokens, createdID); !found {
		t.Fatalf("expected token id %d in list", createdID)
	}

	w = h.DoRESTJSON(t, "DELETE", "/api/v3/user/tokens", map[string]any{
		"id": createdID,
	})
	assertStatusCode(t, w, 204)

	w = h.DoREST(t, "GET", "/api/v3/user/tokens", nil)
	assertStatusCode(t, w, 200)
	tokens = testharness.DecodeJSONArray(t, w)
	if _, found := findTokenByID(tokens, createdID); found {
		t.Fatalf("expected token id %d to be deleted", createdID)
	}
}

func findTokenByID(tokens []map[string]any, id int) (map[string]any, bool) {
	for _, tok := range tokens {
		if raw, ok := tok["id"].(float64); ok && int(raw) == id {
			return tok, true
		}
	}
	return nil, false
}
