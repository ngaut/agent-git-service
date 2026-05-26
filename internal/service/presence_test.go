package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/gitstore"
)

func setupTestPresenceHub(t *testing.T) (*PresenceHub, func()) {
	t.Helper()

	// Setup local tmp gitstore
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

	// Migrate required tables
	err = gdb.AutoMigrate(
		&db.User{},
		&db.Repository{},
		&db.Issue{},
		&db.UserLastSeen{},
	)
	if err != nil {
		t.Fatalf("failed to migrate database: %v", err)
	}

	gitStore, err := gitstore.New(tmpDir)
	if err != nil {
		t.Fatalf("failed to init gitstore: %v", err)
	}
	_ = gitStore // not used but required for service struct

	hub := NewPresenceHub(gdb)

	cleanup := func() {
		_ = os.RemoveAll(tmpDir)
	}

	return hub, cleanup
}

func seedPresenceUser(t *testing.T, hub *PresenceHub, login string) db.User {
	t.Helper()

	user := db.User{Login: login, Email: login + "@example.com"}
	if err := hub.db.Create(&user).Error; err != nil {
		t.Fatalf("failed to create user %q: %v", login, err)
	}
	return user
}

func seedPresenceIssue(t *testing.T, hub *PresenceHub, owner db.User, name string, private bool) db.Issue {
	t.Helper()

	repo := db.Repository{
		Name:     name,
		FullName: owner.Login + "/" + name,
		OwnerID:  owner.ID,
		Private:  private,
	}
	if err := hub.db.Create(&repo).Error; err != nil {
		t.Fatalf("failed to create repo %q: %v", name, err)
	}

	issue := db.Issue{Number: 1, RepositoryID: repo.ID, Title: "Presence test issue"}
	if err := hub.db.Create(&issue).Error; err != nil {
		t.Fatalf("failed to create issue for %q: %v", name, err)
	}
	return issue
}

func TestPresenceHub_UpdateHeartbeat(t *testing.T) {
	hub, cleanup := setupTestPresenceHub(t)
	defer cleanup()

	ctx := context.Background()
	user := seedPresenceUser(t, hub, "owner")
	issue := seedPresenceIssue(t, hub, user, "presence-update", false)

	// Update heartbeat
	err := hub.UpdateHeartbeat(ctx, user.ID, issue.ID)
	if err != nil {
		t.Fatalf("UpdateHeartbeat failed: %v", err)
	}

	// Verify presence was updated
	entries, err := hub.GetIssuePresenceState(ctx, issue.ID, 0)
	if err != nil {
		t.Fatalf("GetIssuePresenceState failed: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}

	entry := entries[0]
	if entry.UserID != user.ID {
		t.Errorf("expected userID %d, got %d", user.ID, entry.UserID)
	}
	if entry.IssueID != issue.ID {
		t.Errorf("expected issueID %d, got %d", issue.ID, entry.IssueID)
	}
	if entry.Status != PresenceActive {
		t.Errorf("expected status active, got %s", entry.Status)
	}
}

func TestPresenceHub_CalculateStatus(t *testing.T) {
	hub, cleanup := setupTestPresenceHub(t)
	defer cleanup()

	now := time.Now()

	tests := []struct {
		name           string
		lastBeat       time.Time
		expectedStatus PresenceStatus
	}{
		{"active", now.Add(-30 * time.Second), PresenceActive},
		{"active_boundary", now.Add(-59 * time.Second), PresenceActive},
		{"idle", now.Add(-90 * time.Second), PresenceIdle},
		{"idle_boundary", now.Add(-4*time.Minute + 59*time.Second), PresenceIdle},
		{"offline", now.Add(-6 * time.Minute), PresenceOffline},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status := hub.calculateStatus(now, tt.lastBeat)
			if status != tt.expectedStatus {
				t.Errorf("expected %s, got %s", tt.expectedStatus, status)
			}
		})
	}
}

func TestPresenceHub_GetIssuePresenceState(t *testing.T) {
	hub, cleanup := setupTestPresenceHub(t)
	defer cleanup()

	ctx := context.Background()
	owner := seedPresenceUser(t, hub, "owner")
	issue1 := seedPresenceIssue(t, hub, owner, "presence-issue-1", false)
	issue2 := seedPresenceIssue(t, hub, owner, "presence-issue-2", false)

	users := []db.User{
		owner,
		seedPresenceUser(t, hub, "reader-1"),
		seedPresenceUser(t, hub, "reader-2"),
		seedPresenceUser(t, hub, "reader-3"),
	}

	// Add multiple users to the same issue
	for _, user := range users[:3] {
		err := hub.UpdateHeartbeat(ctx, user.ID, issue1.ID)
		if err != nil {
			t.Fatalf("UpdateHeartbeat failed: %v", err)
		}
	}

	// Add a user to a different issue
	err := hub.UpdateHeartbeat(ctx, users[3].ID, issue2.ID)
	if err != nil {
		t.Fatalf("UpdateHeartbeat failed: %v", err)
	}

	// Verify issue 1 has 3 users (no viewer filtering)
	entries, err := hub.GetIssuePresenceState(ctx, issue1.ID, 0)
	if err != nil {
		t.Fatalf("GetIssuePresenceState failed: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries for issue 1, got %d", len(entries))
	}

	// Verify issue 2 has 1 user
	entries, err = hub.GetIssuePresenceState(ctx, issue2.ID, 0)
	if err != nil {
		t.Fatalf("GetIssuePresenceState failed: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry for issue 2, got %d", len(entries))
	}

	// Verify non-existent issue has no users
	entries, err = hub.GetIssuePresenceState(ctx, 999, 0)
	if err != nil {
		t.Fatalf("GetIssuePresenceState failed: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries for issue 999, got %d", len(entries))
	}
}

func TestPresenceHub_PrivacyToggle(t *testing.T) {
	hub, cleanup := setupTestPresenceHub(t)
	defer cleanup()

	ctx := context.Background()

	// Create users in DB
	users := []db.User{
		{Login: "user1", Email: "user1@example.com"},
		{Login: "user2", Email: "user2@example.com"},
		{Login: "user3", Email: "user3@example.com"},
	}
	for i := range users {
		if err := hub.db.Create(&users[i]).Error; err != nil {
			t.Fatalf("failed to create user: %v", err)
		}
	}
	issue := seedPresenceIssue(t, hub, users[0], "presence-privacy", false)

	// Add all users to the same issue
	for _, u := range users {
		err := hub.UpdateHeartbeat(ctx, u.ID, issue.ID)
		if err != nil {
			t.Fatalf("UpdateHeartbeat failed: %v", err)
		}
	}

	// User 3 (index 2) hides their presence
	err := hub.SetHidePresence(ctx, users[2].ID, true)
	if err != nil {
		t.Fatalf("SetHidePresence failed: %v", err)
	}

	// Verify: viewer=user1 (index 0) should see only user1 and user2 (not user3 who is hidden)
	entries, err := hub.GetIssuePresenceState(ctx, issue.ID, users[0].ID)
	if err != nil {
		t.Fatalf("GetIssuePresenceState failed: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries (user3 hidden), got %d", len(entries))
	}

	// Verify hidden user can still see themselves and others
	entries, err = hub.GetIssuePresenceState(ctx, issue.ID, users[2].ID)
	if err != nil {
		t.Fatalf("GetIssuePresenceState failed: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries (hidden user sees all), got %d", len(entries))
	}

	// Verify IsPresenceHidden
	hidden, err := hub.IsPresenceHidden(ctx, users[2].ID)
	if err != nil {
		t.Fatalf("IsPresenceHidden failed: %v", err)
	}
	if !hidden {
		t.Error("expected user3 to be hidden")
	}

	hidden, err = hub.IsPresenceHidden(ctx, users[0].ID)
	if err != nil {
		t.Fatalf("IsPresenceHidden failed: %v", err)
	}
	if hidden {
		t.Error("expected user1 to not be hidden")
	}
}

func TestPresenceHub_Cleanup(t *testing.T) {
	hub, cleanup := setupTestPresenceHub(t)
	defer cleanup()

	ctx := context.Background()
	user := seedPresenceUser(t, hub, "cleanup-owner")
	issue := seedPresenceIssue(t, hub, user, "presence-cleanup", false)

	// Add a user
	err := hub.UpdateHeartbeat(ctx, user.ID, issue.ID)
	if err != nil {
		t.Fatalf("UpdateHeartbeat failed: %v", err)
	}

	// Manually set the heartbeat to be old (simulate 6 minutes ago)
	hub.mu.Lock()
	if hub.presence[issue.ID] != nil && hub.presence[issue.ID][user.ID] != nil {
		hub.presence[issue.ID][user.ID].LastHeartbeat = time.Now().Add(-6 * time.Minute)
	}
	hub.mu.Unlock()

	// Run cleanup
	hub.cleanup()

	// Verify the entry was removed
	entries, err := hub.GetIssuePresenceState(ctx, issue.ID, 0)
	if err != nil {
		t.Fatalf("GetIssuePresenceState failed: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries after cleanup, got %d", len(entries))
	}
}

func TestPresenceHub_GetUserLastSeen(t *testing.T) {
	hub, cleanup := setupTestPresenceHub(t)
	defer cleanup()

	ctx := context.Background()
	user := seedPresenceUser(t, hub, "last-seen-owner")
	issue := seedPresenceIssue(t, hub, user, "presence-last-seen", false)

	// Update heartbeat (this should persist last-seen)
	err := hub.UpdateHeartbeat(ctx, user.ID, issue.ID)
	if err != nil {
		t.Fatalf("UpdateHeartbeat failed: %v", err)
	}

	// Get last-seen
	lastSeen, err := hub.GetUserLastSeen(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetUserLastSeen failed: %v", err)
	}

	if lastSeen == nil {
		t.Fatal("expected lastSeen to be non-nil")
	}
	if lastSeen.UserID != user.ID {
		t.Errorf("expected userID %d, got %d", user.ID, lastSeen.UserID)
	}
	if lastSeen.LastSeenIssueID == nil || *lastSeen.LastSeenIssueID != issue.ID {
		t.Errorf("expected issueID %d, got %v", issue.ID, lastSeen.LastSeenIssueID)
	}
}

func TestPresenceHub_UpdateHeartbeat_PersistsLastSeenWithCanceledContext(t *testing.T) {
	hub, cleanup := setupTestPresenceHub(t)
	defer cleanup()

	user := seedPresenceUser(t, hub, "canceled-owner")
	issue := seedPresenceIssue(t, hub, user, "presence-canceled", false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := hub.UpdateHeartbeat(ctx, user.ID, issue.ID); err != nil {
		t.Fatalf("UpdateHeartbeat failed: %v", err)
	}

	lastSeen, err := hub.GetUserLastSeen(context.Background(), user.ID)
	if err != nil {
		t.Fatalf("GetUserLastSeen failed: %v", err)
	}
	if lastSeen == nil {
		t.Fatal("expected lastSeen to persist even with canceled context")
	}
}
