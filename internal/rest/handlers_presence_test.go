package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/gitstore"
	"github.com/ngaut/agent-git-service/internal/service"
)

func setupTestPresenceHandlers(t *testing.T) (*PresenceHandlers, *gorm.DB, func()) {
	t.Helper()

	tmpDir, err := os.MkdirTemp("", "gh-server-presence-test-")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	dbPath := filepath.Join(tmpDir, "test.sqlite")

	gdb, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to connect database: %v", err)
	}
	if err := gdb.Exec("PRAGMA busy_timeout = 5000").Error; err != nil {
		t.Fatalf("failed to set sqlite busy_timeout: %v", err)
	}
	if err := gdb.Exec("PRAGMA journal_mode = WAL").Error; err != nil {
		t.Fatalf("failed to set sqlite journal_mode: %v", err)
	}
	if err := gdb.AutoMigrate(&db.User{}, &db.Repository{}, &db.Issue{}, &db.UserLastSeen{}); err != nil {
		t.Fatalf("failed to migrate database: %v", err)
	}

	gitStore, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatalf("failed to init gitstore: %v", err)
	}

	hub := service.NewPresenceHub(gdb)
	svc := &service.Service{
		DB:          gdb,
		Git:         gitStore,
		BaseURL:     "http://localhost:8080",
		PresenceHub: hub,
	}

	cleanup := func() {
		_ = os.RemoveAll(tmpDir)
	}

	return &PresenceHandlers{Svc: svc, Hub: hub}, gdb, cleanup
}

func seedPresenceUser(t *testing.T, database *gorm.DB, login string) db.User {
	t.Helper()

	user := db.User{Login: login, Email: login + "@example.com"}
	if err := database.Create(&user).Error; err != nil {
		t.Fatalf("failed to create user %q: %v", login, err)
	}
	return user
}

func seedPresenceIssue(t *testing.T, database *gorm.DB, owner db.User, name string, private bool) db.Issue {
	t.Helper()

	repo := db.Repository{
		Name:     name,
		FullName: owner.Login + "/" + name,
		OwnerID:  owner.ID,
		Private:  private,
	}
	if err := database.Create(&repo).Error; err != nil {
		t.Fatalf("failed to create repo %q: %v", name, err)
	}

	issue := db.Issue{Number: 1, RepositoryID: repo.ID, Title: "Presence test issue"}
	if err := database.Create(&issue).Error; err != nil {
		t.Fatalf("failed to create issue for %q: %v", name, err)
	}
	return issue
}

func withURLParam(req *http.Request, key, value string) *http.Request {
	rc := chi.NewRouteContext()
	rc.URLParams.Add(key, value)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rc))
}

func TestPostPresenceHeartbeat(t *testing.T) {
	handlers, database, cleanup := setupTestPresenceHandlers(t)
	defer cleanup()

	user := seedPresenceUser(t, database, "testuser")
	issue := seedPresenceIssue(t, database, user, "presence-heartbeat", false)

	body := map[string]uint{"issue_id": issue.ID}
	jsonBody, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v3/presence/heartbeat", bytes.NewReader(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(service.ContextWithUser(req.Context(), user))

	rr := httptest.NewRecorder()
	handlers.PostPresenceHeartbeat(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	if resp["status"] != "ok" {
		t.Fatalf("expected status ok, got %q", resp["status"])
	}
}

func TestPostPresenceHeartbeat_Unauthorized(t *testing.T) {
	handlers, _, cleanup := setupTestPresenceHandlers(t)
	defer cleanup()

	body := map[string]uint{"issue_id": 1}
	jsonBody, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v3/presence/heartbeat", bytes.NewReader(jsonBody))
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()
	handlers.PostPresenceHeartbeat(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d", rr.Code)
	}
}

func TestPostPresenceHeartbeat_InvalidBody(t *testing.T) {
	handlers, database, cleanup := setupTestPresenceHandlers(t)
	defer cleanup()

	user := seedPresenceUser(t, database, "testuser")
	req := httptest.NewRequest(http.MethodPost, "/api/v3/presence/heartbeat", bytes.NewReader([]byte("invalid")))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(service.ContextWithUser(req.Context(), user))

	rr := httptest.NewRecorder()
	handlers.PostPresenceHeartbeat(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rr.Code)
	}
}

func TestPostPresenceHeartbeat_PrivateRepoRequiresReadAccess(t *testing.T) {
	handlers, database, cleanup := setupTestPresenceHandlers(t)
	defer cleanup()

	owner := seedPresenceUser(t, database, "owner")
	outsider := seedPresenceUser(t, database, "outsider")
	issue := seedPresenceIssue(t, database, owner, "presence-private-heartbeat", true)

	body := map[string]uint{"issue_id": issue.ID}
	jsonBody, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v3/presence/heartbeat", bytes.NewReader(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(service.ContextWithUser(req.Context(), outsider))

	rr := httptest.NewRecorder()
	handlers.PostPresenceHeartbeat(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestGetIssuePresence(t *testing.T) {
	handlers, database, cleanup := setupTestPresenceHandlers(t)
	defer cleanup()

	user1 := seedPresenceUser(t, database, "user1")
	user2 := seedPresenceUser(t, database, "user2")
	issue := seedPresenceIssue(t, database, user1, "presence-list", false)

	if err := handlers.Hub.UpdateHeartbeat(context.Background(), user1.ID, issue.ID); err != nil {
		t.Fatalf("UpdateHeartbeat user1 failed: %v", err)
	}
	if err := handlers.Hub.UpdateHeartbeat(context.Background(), user2.ID, issue.ID); err != nil {
		t.Fatalf("UpdateHeartbeat user2 failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v3/issues/1/presence", nil)
	req = withURLParam(req, "issue_id", strconv.FormatUint(uint64(issue.ID), 10))
	req = req.WithContext(service.ContextWithUser(req.Context(), user1))
	rr := httptest.NewRecorder()

	handlers.GetIssuePresence(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp map[string][]map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	if len(resp["presence"]) != 2 {
		t.Fatalf("expected 2 presence entries, got %d", len(resp["presence"]))
	}
}

func TestGetIssuePresence_Unauthorized(t *testing.T) {
	handlers, _, cleanup := setupTestPresenceHandlers(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/v3/issues/1/presence", nil)
	req = withURLParam(req, "issue_id", "1")
	rr := httptest.NewRecorder()

	handlers.GetIssuePresence(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d", rr.Code)
	}
}

func TestGetIssuePresence_PrivateRepoRequiresReadAccess(t *testing.T) {
	handlers, database, cleanup := setupTestPresenceHandlers(t)
	defer cleanup()

	owner := seedPresenceUser(t, database, "owner")
	outsider := seedPresenceUser(t, database, "outsider")
	issue := seedPresenceIssue(t, database, owner, "presence-private-list", true)
	if err := handlers.Hub.UpdateHeartbeat(context.Background(), owner.ID, issue.ID); err != nil {
		t.Fatalf("UpdateHeartbeat failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v3/issues/1/presence", nil)
	req = withURLParam(req, "issue_id", strconv.FormatUint(uint64(issue.ID), 10))
	req = req.WithContext(service.ContextWithUser(req.Context(), outsider))
	rr := httptest.NewRecorder()

	handlers.GetIssuePresence(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestPutPresencePrivacy(t *testing.T) {
	handlers, database, cleanup := setupTestPresenceHandlers(t)
	defer cleanup()

	user := seedPresenceUser(t, database, "testuser")
	body := map[string]bool{"hide": true}
	jsonBody, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPut, "/api/v3/user/presence/privacy", bytes.NewReader(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(service.ContextWithUser(req.Context(), user))

	rr := httptest.NewRecorder()
	handlers.PutPresencePrivacy(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]bool
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	if !resp["hide_presence"] {
		t.Fatal("expected hide_presence to be true")
	}

	var updatedUser db.User
	if err := database.First(&updatedUser, user.ID).Error; err != nil {
		t.Fatalf("failed to fetch user: %v", err)
	}
	if !updatedUser.HidePresence {
		t.Fatal("expected HidePresence to be true in DB")
	}
}

func TestGetPresencePrivacy(t *testing.T) {
	handlers, database, cleanup := setupTestPresenceHandlers(t)
	defer cleanup()

	user := db.User{Login: "testuser", Email: "test@example.com", HidePresence: true}
	if err := database.Create(&user).Error; err != nil {
		t.Fatalf("failed to create user: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v3/user/presence/privacy", nil)
	req = req.WithContext(service.ContextWithUser(req.Context(), user))

	rr := httptest.NewRecorder()
	handlers.GetPresencePrivacy(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]bool
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	if !resp["hide_presence"] {
		t.Fatal("expected hide_presence to be true")
	}
}

func TestGetUserLastSeen(t *testing.T) {
	handlers, database, cleanup := setupTestPresenceHandlers(t)
	defer cleanup()

	user := seedPresenceUser(t, database, "testuser")
	issue := seedPresenceIssue(t, database, user, "presence-last-seen", false)
	lastSeen := db.UserLastSeen{
		UserID:          user.ID,
		LastSeenAt:      time.Now().UTC(),
		LastSeenIssueID: &issue.ID,
	}
	if err := database.Create(&lastSeen).Error; err != nil {
		t.Fatalf("failed to create lastSeen: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v3/users/1/last-seen", nil)
	req = withURLParam(req, "user_id", strconv.FormatUint(uint64(user.ID), 10))
	req = req.WithContext(service.ContextWithUser(req.Context(), user))
	rr := httptest.NewRecorder()

	handlers.GetUserLastSeen(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	if resp["user_id"] != float64(user.ID) {
		t.Fatalf("expected user_id %d, got %v", user.ID, resp["user_id"])
	}
	if resp["last_seen_at"] == nil {
		t.Fatal("expected last_seen_at to be non-nil")
	}
}

func TestGetUserLastSeen_NotFound(t *testing.T) {
	handlers, database, cleanup := setupTestPresenceHandlers(t)
	defer cleanup()

	viewer := seedPresenceUser(t, database, "viewer")
	req := httptest.NewRequest(http.MethodGet, "/api/v3/users/999/last-seen", nil)
	req = withURLParam(req, "user_id", "999")
	req = req.WithContext(service.ContextWithUser(req.Context(), viewer))
	rr := httptest.NewRecorder()

	handlers.GetUserLastSeen(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}

	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	if resp["last_seen_at"] != nil {
		t.Fatal("expected last_seen_at to be nil for missing user")
	}
}

func TestGetUserLastSeen_HiddenFromOtherUsers(t *testing.T) {
	handlers, database, cleanup := setupTestPresenceHandlers(t)
	defer cleanup()

	target := db.User{Login: "target", Email: "target@example.com", HidePresence: true}
	if err := database.Create(&target).Error; err != nil {
		t.Fatalf("failed to create hidden user: %v", err)
	}
	viewer := seedPresenceUser(t, database, "viewer")
	issue := seedPresenceIssue(t, database, target, "presence-hidden", false)
	lastSeen := db.UserLastSeen{
		UserID:          target.ID,
		LastSeenAt:      time.Now().UTC(),
		LastSeenIssueID: &issue.ID,
	}
	if err := database.Create(&lastSeen).Error; err != nil {
		t.Fatalf("failed to create lastSeen: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v3/users/1/last-seen", nil)
	req = withURLParam(req, "user_id", strconv.FormatUint(uint64(target.ID), 10))
	req = req.WithContext(service.ContextWithUser(req.Context(), viewer))
	rr := httptest.NewRecorder()

	handlers.GetUserLastSeen(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	if resp["last_seen_at"] != nil {
		t.Fatalf("expected hidden last_seen_at to be nil, got %v", resp["last_seen_at"])
	}
}

func TestGetUserLastSeen_HidesInaccessibleIssue(t *testing.T) {
	handlers, database, cleanup := setupTestPresenceHandlers(t)
	defer cleanup()

	target := seedPresenceUser(t, database, "target")
	viewer := seedPresenceUser(t, database, "viewer")
	issue := seedPresenceIssue(t, database, target, "presence-private-last-seen", true)
	lastSeen := db.UserLastSeen{
		UserID:          target.ID,
		LastSeenAt:      time.Now().UTC(),
		LastSeenIssueID: &issue.ID,
	}
	if err := database.Create(&lastSeen).Error; err != nil {
		t.Fatalf("failed to create lastSeen: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v3/users/1/last-seen", nil)
	req = withURLParam(req, "user_id", strconv.FormatUint(uint64(target.ID), 10))
	req = req.WithContext(service.ContextWithUser(req.Context(), viewer))
	rr := httptest.NewRecorder()

	handlers.GetUserLastSeen(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	if resp["last_seen_at"] != nil {
		t.Fatalf("expected inaccessible last_seen_at to be nil, got %v", resp["last_seen_at"])
	}
}
