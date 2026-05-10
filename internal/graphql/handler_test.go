package graphql

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAstChild_MissingOrNonMapChild(t *testing.T) {
	ast := map[string]any{
		"repo":   map[string]any{"id": true},
		"viewer": true,
	}

	child := astChild(ast, "repo")
	if child == nil {
		t.Fatal("expected repo child to be a map")
	}
	if _, ok := child["id"]; !ok {
		t.Error("expected repo child to contain id")
	}

	if got := astChild(ast, "viewer"); got != nil {
		t.Errorf("expected viewer child to be nil, got: %v", got)
	}
	if got := astChild(ast, "missing"); got != nil {
		t.Errorf("expected missing child to be nil, got: %v", got)
	}
}

func TestHandler_UnknownMutationReturnsEmptyData(t *testing.T) {
	srv := &Server{}
	handler := http.HandlerFunc(srv.Handler)

	reqBody := map[string]any{
		"query": "mutation { totallyUnknown { id } }",
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	req := httptest.NewRequest("POST", "/graphql", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	data, ok := resp["data"].(map[string]any)
	if !ok {
		t.Fatalf("expected data map, got: %T", resp["data"])
	}
	if len(data) != 0 {
		t.Errorf("expected empty data for unknown mutation, got: %v", data)
	}
}

func TestHandler_PanicRecovery(t *testing.T) {
	srv := &Server{}
	handler := http.HandlerFunc(srv.Handler)

	reqBody := map[string]any{
		"query": "mutation { addComment(input: {body: \"hi\", subjectId: \"Issue_1\"}) { commentEdge { node { id } } } }",
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	req := httptest.NewRequest("POST", "/graphql", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["data"] != nil {
		t.Errorf("expected data to be null, got: %v", resp["data"])
	}
	errorsVal, ok := resp["errors"].([]any)
	if !ok || len(errorsVal) == 0 {
		t.Fatalf("expected errors array, got: %v", resp["errors"])
	}
	firstErr, ok := errorsVal[0].(map[string]any)
	if !ok {
		t.Fatalf("expected error object, got: %T", errorsVal[0])
	}
	msg, _ := firstErr["message"].(string)
	if msg == "" {
		t.Error("expected non-empty panic message")
	}
}
