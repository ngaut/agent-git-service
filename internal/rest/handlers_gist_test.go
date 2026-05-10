package rest_test

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"gh-server/internal/testharness"
)

func TestCreateGist_IDIsHex(t *testing.T) {
	h := testharness.New(t)

	body := `{"description":"test gist","public":true,"files":{"hello.txt":{"content":"hi"}}}`
	w := h.DoREST(t, "POST", "/api/v3/gists", strings.NewReader(body))
	if w.Code != http.StatusCreated {
		t.Fatalf("POST /api/v3/gists: expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var gist map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &gist); err != nil {
		t.Fatalf("failed to decode gist response: %v", err)
	}

	id, _ := gist["id"].(string)
	if len(id) != 20 {
		t.Errorf("gist id: expected length 20, got %d (%q)", len(id), id)
	}
	if !regexp.MustCompile(`^[0-9a-f]+$`).MatchString(id) {
		t.Errorf("gist id: expected only hex chars [0-9a-f], got %q", id)
	}
}
