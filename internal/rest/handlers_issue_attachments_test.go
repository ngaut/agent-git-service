package rest_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gorm.io/gorm"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/testharness"
)

func TestIssueAttachmentRESTFlow(t *testing.T) {
	h := testharness.New(t)
	issue := seedTypingIssue(t, h, "attachment-flow")
	png := mustDecodeBase64(t, "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO5O3ioAAAAASUVORK5CYII=")

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "pixel.png")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := part.Write(png); err != nil {
		t.Fatalf("multipart write: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("multipart close: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v3/issues/%d/attachments", issue.ID), &body)
	req.Header.Set("Authorization", "token "+h.Token)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp := httptest.NewRecorder()
	h.Mux.ServeHTTP(resp, req)
	if resp.Code != http.StatusCreated {
		t.Fatalf("POST attachment: expected 201, got %d: %s", resp.Code, resp.Body.String())
	}

	var created map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode upload response: %v", err)
	}
	uuid, _ := created["uuid"].(string)
	if uuid == "" {
		t.Fatalf("upload response missing uuid: %#v", created)
	}
	if markdown, _ := created["markdown"].(string); !strings.HasPrefix(markdown, "![pixel.png](") {
		t.Fatalf("upload response markdown = %q, want image markdown", markdown)
	}

	var stored db.Attachment
	if err := h.DB.First(&stored, "uuid = ?", uuid).Error; err != nil {
		t.Fatalf("load stored attachment: %v", err)
	}
	storedPath := filepath.Join(h.GitRoot, stored.StoredPath)
	if _, err := os.Stat(storedPath); err != nil {
		t.Fatalf("stat stored attachment: %v", err)
	}

	req = httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v3/issues/%d/attachments", issue.ID), nil)
	resp = httptest.NewRecorder()
	h.Mux.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("GET attachment list: expected 200, got %d: %s", resp.Code, resp.Body.String())
	}
	var listed []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&listed); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(listed) != 1 || listed[0]["uuid"] != uuid {
		t.Fatalf("list response = %#v, want uuid %q", listed, uuid)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v3/attachments/"+uuid, nil)
	resp = httptest.NewRecorder()
	h.Mux.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("GET attachment download: expected 200, got %d: %s", resp.Code, resp.Body.String())
	}
	if got := resp.Header().Get("Content-Type"); !strings.Contains(got, "image/png") {
		t.Fatalf("download Content-Type = %q, want image/png", got)
	}
	if got := resp.Header().Get("Content-Disposition"); !strings.Contains(got, "inline") {
		t.Fatalf("download Content-Disposition = %q, want inline", got)
	}
	if !bytes.Equal(resp.Body.Bytes(), png) {
		t.Fatalf("download body mismatch")
	}

	req = httptest.NewRequest(http.MethodDelete, "/api/v3/attachments/"+uuid, nil)
	req.Header.Set("Authorization", "token "+h.Token)
	resp = httptest.NewRecorder()
	h.Mux.ServeHTTP(resp, req)
	if resp.Code != http.StatusNoContent {
		t.Fatalf("DELETE attachment: expected 204, got %d: %s", resp.Code, resp.Body.String())
	}
	if err := h.DB.First(&db.Attachment{}, "uuid = ?", uuid).Error; err == nil {
		t.Fatalf("expected attachment row to be deleted")
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("load attachment after delete: %v", err)
	}
	if _, err := os.Stat(storedPath); !os.IsNotExist(err) {
		t.Fatalf("expected attachment file to be removed, got %v", err)
	}
}

func TestRepoAttachmentRESTFlow(t *testing.T) {
	h := testharness.New(t)
	issue := seedTypingIssue(t, h, "repo-attachment-flow")
	png := mustDecodeBase64(t, "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO5O3ioAAAAASUVORK5CYII=")

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "pixel.png")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := part.Write(png); err != nil {
		t.Fatalf("multipart write: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("multipart close: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v3/repos/%s/%s/attachments", issue.Repository.Owner.Login, issue.Repository.Name), &body)
	req.Header.Set("Authorization", "token "+h.Token)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp := httptest.NewRecorder()
	h.Mux.ServeHTTP(resp, req)
	if resp.Code != http.StatusCreated {
		t.Fatalf("POST repo attachment: expected 201, got %d: %s", resp.Code, resp.Body.String())
	}

	var created map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode upload response: %v", err)
	}
	uuid, _ := created["uuid"].(string)
	if uuid == "" {
		t.Fatalf("upload response missing uuid: %#v", created)
	}

	var stored db.Attachment
	if err := h.DB.First(&stored, "uuid = ?", uuid).Error; err != nil {
		t.Fatalf("load stored attachment: %v", err)
	}
	if stored.IssueID != nil {
		t.Fatalf("stored issue_id = %v, want nil", stored.IssueID)
	}

	body.Reset()
	writer = multipart.NewWriter(&body)
	part, err = writer.CreateFormFile("file", "pixel.png")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := part.Write(png); err != nil {
		t.Fatalf("multipart write: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("multipart close: %v", err)
	}

	req = httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v3/repositories/%d/attachments", issue.RepositoryID), &body)
	req.Header.Set("Authorization", "token "+h.Token)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp = httptest.NewRecorder()
	h.Mux.ServeHTTP(resp, req)
	if resp.Code != http.StatusCreated {
		t.Fatalf("POST repo attachment by id: expected 201, got %d: %s", resp.Code, resp.Body.String())
	}
}

func TestIssueAttachmentRejectsUnsupportedType(t *testing.T) {
	h := testharness.New(t)
	issue := seedTypingIssue(t, h, "attachment-invalid")

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "payload.exe")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := part.Write([]byte("MZ")); err != nil {
		t.Fatalf("multipart write: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("multipart close: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v3/issues/%d/attachments", issue.ID), &body)
	req.Header.Set("Authorization", "token "+h.Token)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp := httptest.NewRecorder()
	h.Mux.ServeHTTP(resp, req)
	if resp.Code != http.StatusUnprocessableEntity {
		t.Fatalf("POST invalid attachment: expected 422, got %d: %s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "unsupported attachment type") {
		t.Fatalf("unexpected validation response: %s", resp.Body.String())
	}
}

func mustDecodeBase64(t *testing.T, raw string) []byte {
	t.Helper()

	data, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		t.Fatalf("DecodeString: %v", err)
	}
	return data
}
