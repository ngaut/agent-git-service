package rest_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"gh-server/internal/testharness"
)

func TestCreateLabel_InvalidColor_Returns422(t *testing.T) {
	h := testharness.New(t)
	repoName := "label-invalid-color-create"
	compatSeedRepo(t, h, repoName)

	cases := []struct {
		name  string
		color string
	}{
		{name: "non-hex", color: "zzzzzz"},
		{name: "len-3", color: "abc"},
		{name: "len-5", color: "abcde"},
		{name: "len-7", color: "abcdefg"},
	}

	for i, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			w := h.DoRESTJSON(t, "POST", fmt.Sprintf("/api/v3/repos/%s/%s/labels", h.User.Login, repoName), map[string]any{
				"name":        fmt.Sprintf("bad-color-%d", i),
				"color":       tc.color,
				"description": "",
			})
			if w.Code != 422 {
				t.Fatalf("expected 422, got %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestEditLabel_InvalidColor_Returns422(t *testing.T) {
	h := testharness.New(t)
	repoName := "label-invalid-color-edit"
	compatSeedRepo(t, h, repoName)

	createPath := fmt.Sprintf("/api/v3/repos/%s/%s/labels", h.User.Login, repoName)
	w := h.DoRESTJSON(t, "POST", createPath, map[string]any{
		"name":        "editable-label",
		"color":       "aabbcc",
		"description": "",
	})
	assertStatusCode(t, w, 201)

	cases := []struct {
		name  string
		color string
	}{
		{name: "non-hex", color: "zzzzzz"},
		{name: "len-3", color: "abc"},
		{name: "len-5", color: "abcde"},
		{name: "len-7", color: "abcdefg"},
	}

	patchPath := fmt.Sprintf("/api/v3/repos/%s/%s/labels/%s", h.User.Login, repoName, "editable-label")
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			w := h.DoRESTJSON(t, "PATCH", patchPath, map[string]any{
				"color": tc.color,
			})
			if w.Code != 422 {
				t.Fatalf("expected 422, got %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestRemoveIssueLabel_URLEncodedName(t *testing.T) {
	h := testharness.New(t)
	repoName := "label-url-encode-test"
	compatSeedRepo(t, h, repoName)

	// Create a label with colon in name
	labelPath := fmt.Sprintf("/api/v3/repos/%s/%s/labels", h.User.Login, repoName)
	w := h.DoRESTJSON(t, "POST", labelPath, map[string]any{
		"name":        "topic:test",
		"color":       "aabbcc",
		"description": "Label with colon",
	})
	assertStatusCode(t, w, 201)

	// Create an issue
	issuePath := fmt.Sprintf("/api/v3/repos/%s/%s/issues", h.User.Login, repoName)
	w = h.DoRESTJSON(t, "POST", issuePath, map[string]any{
		"title": "Test issue",
		"body":  "Test body",
	})
	assertStatusCode(t, w, 201)
	var issueResp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &issueResp); err != nil {
		t.Fatalf("failed to unmarshal issue response: %v", err)
	}
	issueNum := int(issueResp["number"].(float64))

	// Add the label to the issue
	addLabelPath := fmt.Sprintf("/api/v3/repos/%s/%s/issues/%d/labels", h.User.Login, repoName, issueNum)
	w = h.DoRESTJSON(t, "POST", addLabelPath, map[string]any{
		"labels": []string{"topic:test"},
	})
	assertStatusCode(t, w, 200)

	// Remove the label using URL-encoded name (%3A = colon)
	removeLabelPath := fmt.Sprintf("/api/v3/repos/%s/%s/issues/%d/labels/topic%%3Atest", h.User.Login, repoName, issueNum)
	w = h.DoREST(t, "DELETE", removeLabelPath, nil)
	assertStatusCode(t, w, 200)

	// Verify the label was removed
	w = h.DoREST(t, "GET", fmt.Sprintf("/api/v3/repos/%s/%s/issues/%d/labels", h.User.Login, repoName, issueNum), nil)
	assertStatusCode(t, w, 200)
	var labels []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &labels); err != nil {
		t.Fatalf("failed to unmarshal labels response: %v", err)
	}
	for _, label := range labels {
		if label["name"].(string) == "topic:test" {
			t.Fatal("label should have been removed but still exists")
		}
	}
}

func TestRemoveIssueLabel_URLEncodedName_Issue1281(t *testing.T) {
	h := testharness.New(t)
	repoName := "label-url-encode-issue-1281"
	compatSeedRepo(t, h, repoName)

	const labelName = "octoswarm:in-progress"
	const controlLabelName = "octoswarm:queued"
	const encodedLabelName = "octoswarm%3Ain-progress"

	labelPath := fmt.Sprintf("/api/v3/repos/%s/%s/labels", h.User.Login, repoName)
	for _, name := range []string{labelName, controlLabelName} {
		w := h.DoRESTJSON(t, "POST", labelPath, map[string]any{
			"name":        name,
			"color":       "aabbcc",
			"description": "Issue #1281 regression label",
		})
		assertStatusCode(t, w, 201)
	}

	issuePath := fmt.Sprintf("/api/v3/repos/%s/%s/issues", h.User.Login, repoName)
	w := h.DoRESTJSON(t, "POST", issuePath, map[string]any{
		"title": "Issue #1281 regression",
		"body":  "Reproduce delete label with URL-encoded colon",
	})
	assertStatusCode(t, w, 201)

	var issueResp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &issueResp); err != nil {
		t.Fatalf("failed to unmarshal issue response: %v", err)
	}
	issueNum := int(issueResp["number"].(float64))

	addLabelPath := fmt.Sprintf("/api/v3/repos/%s/%s/issues/%d/labels", h.User.Login, repoName, issueNum)
	w = h.DoRESTJSON(t, "POST", addLabelPath, map[string]any{
		"labels": []string{labelName, controlLabelName},
	})
	assertStatusCode(t, w, 200)

	w = h.DoREST(t, "GET", fmt.Sprintf("/api/v3/repos/%s/%s/issues/%d/labels", h.User.Login, repoName, issueNum), nil)
	assertStatusCode(t, w, 200)
	labels := decodeLabels(t, w.Body.Bytes())
	assertHasExactLabels(t, labels, labelName, controlLabelName)

	removeLabelPath := fmt.Sprintf("/api/v3/repos/%s/%s/issues/%d/labels/%s", h.User.Login, repoName, issueNum, encodedLabelName)
	w = h.DoREST(t, "DELETE", removeLabelPath, nil)
	if w.Code == 404 {
		t.Fatalf("DELETE returned 404 for URL-encoded label %q: %s", labelName, w.Body.String())
	}
	assertStatusCode(t, w, 200)

	w = h.DoREST(t, "GET", fmt.Sprintf("/api/v3/repos/%s/%s/issues/%d/labels", h.User.Login, repoName, issueNum), nil)
	assertStatusCode(t, w, 200)
	labels = decodeLabels(t, w.Body.Bytes())
	assertHasExactLabels(t, labels, controlLabelName)
}

func decodeLabels(t *testing.T, body []byte) []map[string]any {
	t.Helper()

	var labels []map[string]any
	if err := json.Unmarshal(body, &labels); err != nil {
		t.Fatalf("failed to unmarshal labels response: %v", err)
	}
	return labels
}

func assertHasExactLabels(t *testing.T, labels []map[string]any, expected ...string) {
	t.Helper()

	if len(labels) != len(expected) {
		t.Fatalf("expected %d labels, got %d: %#v", len(expected), len(labels), labels)
	}

	got := make(map[string]bool, len(labels))
	for _, label := range labels {
		name, ok := label["name"].(string)
		if !ok {
			t.Fatalf("label missing string name: %#v", label)
		}
		got[name] = true
	}
	for _, name := range expected {
		if !got[name] {
			t.Fatalf("expected label %q to be present, labels=%#v", name, labels)
		}
	}
}

func TestRemoveIssueLabel_ImmediateDeletePlainName(t *testing.T) {
	h := testharness.New(t)
	repoName := "label-remove-immediate-plain"
	compatSeedRepo(t, h, repoName)

	labelPath := fmt.Sprintf("/api/v3/repos/%s/%s/labels", h.User.Login, repoName)
	w := h.DoRESTJSON(t, "POST", labelPath, map[string]any{
		"name":        "octoswarm-in-progress",
		"color":       "aabbcc",
		"description": "Dispatcher state label",
	})
	assertStatusCode(t, w, 201)

	issuePath := fmt.Sprintf("/api/v3/repos/%s/%s/issues", h.User.Login, repoName)
	w = h.DoRESTJSON(t, "POST", issuePath, map[string]any{
		"title": "Immediate delete issue",
		"body":  "Test body",
	})
	assertStatusCode(t, w, 201)

	var issueResp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &issueResp); err != nil {
		t.Fatalf("failed to unmarshal issue response: %v", err)
	}
	issueNum := int(issueResp["number"].(float64))

	addLabelPath := fmt.Sprintf("/api/v3/repos/%s/%s/issues/%d/labels", h.User.Login, repoName, issueNum)
	w = h.DoRESTJSON(t, "POST", addLabelPath, map[string]any{
		"labels": []string{"octoswarm-in-progress"},
	})
	assertStatusCode(t, w, 200)

	removeLabelPath := fmt.Sprintf("/api/v3/repos/%s/%s/issues/%d/labels/octoswarm-in-progress", h.User.Login, repoName, issueNum)
	w = h.DoREST(t, "DELETE", removeLabelPath, nil)
	assertStatusCode(t, w, 200)

	var remaining []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &remaining); err != nil {
		t.Fatalf("failed to unmarshal remaining labels response: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("expected no remaining labels, got %d", len(remaining))
	}

	w = h.DoREST(t, "GET", fmt.Sprintf("/api/v3/repos/%s/%s/issues/%d/labels", h.User.Login, repoName, issueNum), nil)
	assertStatusCode(t, w, 200)
	if err := json.Unmarshal(w.Body.Bytes(), &remaining); err != nil {
		t.Fatalf("failed to unmarshal labels response: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("expected GET labels to be empty after delete, got %d", len(remaining))
	}
}

func TestRemoveIssueLabel_HostRewriteGhAPIStylePath(t *testing.T) {
	h := testharness.New(t)
	repoName := "label-host-rewrite-delete"
	compatSeedRepo(t, h, repoName)

	labelPath := fmt.Sprintf("/api/v3/repos/%s/%s/labels", h.User.Login, repoName)
	w := h.DoRESTJSON(t, "POST", labelPath, map[string]any{
		"name":        "topic:test",
		"color":       "aabbcc",
		"description": "Label with colon",
	})
	assertStatusCode(t, w, 201)

	issuePath := fmt.Sprintf("/api/v3/repos/%s/%s/issues", h.User.Login, repoName)
	w = h.DoRESTJSON(t, "POST", issuePath, map[string]any{
		"title": "Host rewrite issue",
		"body":  "Test body",
	})
	assertStatusCode(t, w, 201)

	var issueResp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &issueResp); err != nil {
		t.Fatalf("failed to unmarshal issue response: %v", err)
	}
	issueNum := int(issueResp["number"].(float64))

	host := "api.github.localhost"
	addLabelPath := fmt.Sprintf("/repos/%s/%s/issues/%d/labels", h.User.Login, repoName, issueNum)
	w = h.DoRESTWithHost(t, "POST", host, addLabelPath, map[string]any{
		"labels": []string{"topic:test"},
	})
	assertStatusCode(t, w, 200)

	removeLabelPath := fmt.Sprintf("/repos/%s/%s/issues/%d/labels/topic:test", h.User.Login, repoName, issueNum)
	w = h.DoRESTWithHost(t, "DELETE", host, removeLabelPath, nil)
	assertStatusCode(t, w, 200)

	var remaining []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &remaining); err != nil {
		t.Fatalf("failed to unmarshal remaining labels response: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("expected no remaining labels, got %d", len(remaining))
	}

	w = h.DoRESTWithHost(t, "GET", host, fmt.Sprintf("/repos/%s/%s/issues/%d/labels", h.User.Login, repoName, issueNum), nil)
	assertStatusCode(t, w, 200)
	if err := json.Unmarshal(w.Body.Bytes(), &remaining); err != nil {
		t.Fatalf("failed to unmarshal labels response: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("expected GET labels to be empty after delete, got %d", len(remaining))
	}
}

func TestGetLabel_URLEncodedName(t *testing.T) {
	h := testharness.New(t)
	repoName := "label-url-encode-get"
	compatSeedRepo(t, h, repoName)

	// Create a label with colon in name
	labelPath := fmt.Sprintf("/api/v3/repos/%s/%s/labels", h.User.Login, repoName)
	w := h.DoRESTJSON(t, "POST", labelPath, map[string]any{
		"name":        "topic:test",
		"color":       "aabbcc",
		"description": "Label with colon",
	})
	assertStatusCode(t, w, 201)

	// Get the label using URL-encoded name
	getLabelPath := fmt.Sprintf("/api/v3/repos/%s/%s/labels/topic%%3Atest", h.User.Login, repoName)
	w = h.DoREST(t, "GET", getLabelPath, nil)
	assertStatusCode(t, w, 200)

	var label map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &label); err != nil {
		t.Fatalf("failed to unmarshal label response: %v", err)
	}
	if label["name"].(string) != "topic:test" {
		t.Fatalf("expected label name 'topic:test', got '%s'", label["name"].(string))
	}
}

func TestEditLabel_URLEncodedName(t *testing.T) {
	h := testharness.New(t)
	repoName := "label-url-encode-edit"
	compatSeedRepo(t, h, repoName)

	// Create a label with colon in name
	labelPath := fmt.Sprintf("/api/v3/repos/%s/%s/labels", h.User.Login, repoName)
	w := h.DoRESTJSON(t, "POST", labelPath, map[string]any{
		"name":        "topic:test",
		"color":       "aabbcc",
		"description": "Label with colon",
	})
	assertStatusCode(t, w, 201)

	// Edit the label using URL-encoded name
	editLabelPath := fmt.Sprintf("/api/v3/repos/%s/%s/labels/topic%%3Atest", h.User.Login, repoName)
	w = h.DoRESTJSON(t, "PATCH", editLabelPath, map[string]any{
		"color": "bbccdd",
	})
	assertStatusCode(t, w, 200)

	var label map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &label); err != nil {
		t.Fatalf("failed to unmarshal label response: %v", err)
	}
	if label["name"].(string) != "topic:test" {
		t.Fatalf("expected label name 'topic:test', got '%s'", label["name"].(string))
	}
	if label["color"].(string) != "bbccdd" {
		t.Fatalf("expected color 'bbccdd', got '%s'", label["color"].(string))
	}
}

func TestDeleteLabel_URLEncodedName(t *testing.T) {
	h := testharness.New(t)
	repoName := "label-url-encode-delete"
	compatSeedRepo(t, h, repoName)

	// Create a label with colon in name
	labelPath := fmt.Sprintf("/api/v3/repos/%s/%s/labels", h.User.Login, repoName)
	w := h.DoRESTJSON(t, "POST", labelPath, map[string]any{
		"name":        "topic:test",
		"color":       "aabbcc",
		"description": "Label with colon",
	})
	assertStatusCode(t, w, 201)

	// Delete the label using URL-encoded name
	deleteLabelPath := fmt.Sprintf("/api/v3/repos/%s/%s/labels/topic%%3Atest", h.User.Login, repoName)
	w = h.DoREST(t, "DELETE", deleteLabelPath, nil)
	assertStatusCode(t, w, 204)

	// Verify the label was deleted
	getLabelPath := fmt.Sprintf("/api/v3/repos/%s/%s/labels/topic%%3Atest", h.User.Login, repoName)
	w = h.DoREST(t, "GET", getLabelPath, nil)
	assertStatusCode(t, w, 404)
}
