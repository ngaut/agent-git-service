package service

import (
	"context"
	"sync"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// PresenceStatus represents the presence status of a user
type PresenceStatus string

const (
	PresenceActive  PresenceStatus = "active"  // heartbeat < 60s
	PresenceIdle    PresenceStatus = "idle"    // heartbeat 60s-5min
	PresenceOffline PresenceStatus = "offline" // no heartbeat or > 5min
)

// PresenceEntry represents a user's presence in an issue
type PresenceEntry struct {
	UserID        uint
	IssueID       uint
	LastHeartbeat time.Time
	Status        PresenceStatus
}

// PresenceHub manages ephemeral presence state with TTL-based expiry.
// It maintains in-memory presence data that expires after 5 minutes of no heartbeat.
type PresenceHub struct {
	mu       sync.RWMutex
	presence map[uint]map[uint]*PresenceEntry // issue_id -> user_id -> entry
	db       *gorm.DB
}

// NewPresenceHub creates a new PresenceHub instance
func NewPresenceHub(db *gorm.DB) *PresenceHub {
	hub := &PresenceHub{
		presence: make(map[uint]map[uint]*PresenceEntry),
		db:       db,
	}
	// Start background cleanup goroutine
	go hub.cleanupLoop()
	return hub
}

// cleanupLoop periodically removes expired presence entries
func (h *PresenceHub) cleanupLoop() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		h.cleanup()
	}
}

// cleanup removes presence entries older than 5 minutes
func (h *PresenceHub) cleanup() {
	h.mu.Lock()
	defer h.mu.Unlock()

	cutoff := time.Now().Add(-5 * time.Minute)
	for issueID, users := range h.presence {
		for userID, entry := range users {
			if entry.LastHeartbeat.Before(cutoff) {
				delete(users, userID)
			}
		}
		if len(users) == 0 {
			delete(h.presence, issueID)
		}
	}
}

// UpdateHeartbeat updates the presence state for a user in an issue.
// Called every 30s by clients. Updates presence within 1s.
func (h *PresenceHub) UpdateHeartbeat(ctx context.Context, userID, issueID uint) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	now := time.Now()

	// Initialize issue map if needed
	if h.presence[issueID] == nil {
		h.presence[issueID] = make(map[uint]*PresenceEntry)
	}

	// Update or create presence entry
	entry := h.presence[issueID][userID]
	if entry == nil {
		entry = &PresenceEntry{
			UserID:        userID,
			IssueID:       issueID,
			LastHeartbeat: now,
			Status:        PresenceActive,
		}
		h.presence[issueID][userID] = entry
	} else {
		entry.LastHeartbeat = now
		entry.Status = PresenceActive
	}

	if ctx == nil {
		ctx = context.Background()
	}

	// Persist last-seen synchronously so newer heartbeats cannot be overwritten by
	// older background writes, while still preserving request values without its
	// cancellation semantics.
	return h.persistLastSeen(context.WithoutCancel(ctx), userID, issueID)
}

// persistLastSeen persists the user's last-seen state to the database
func (h *PresenceHub) persistLastSeen(ctx context.Context, userID, issueID uint) error {
	now := time.Now().UTC()
	lastSeen := db.UserLastSeen{
		UserID:          userID,
		LastSeenAt:      now,
		LastSeenIssueID: &issueID,
		UpdatedAt:       now,
	}

	return h.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "user_id"}},
			DoUpdates: clause.Assignments(map[string]any{
				"last_seen_at":       now,
				"last_seen_issue_id": &issueID,
				"updated_at":         now,
			}),
		}).
		Create(&lastSeen).Error
}

// GetIssuePresenceState returns presence state for all users in an issue.
// If viewerID is provided (non-zero), users with HidePresence=true are filtered out
// (except the viewer themselves).
func (h *PresenceHub) GetIssuePresenceState(ctx context.Context, issueID uint, viewerID uint) ([]PresenceEntry, error) {
	h.mu.RLock()
	users, ok := h.presence[issueID]
	if !ok {
		h.mu.RUnlock()
		return []PresenceEntry{}, nil
	}

	snapshot := make([]PresenceEntry, 0, len(users))
	// Build list of user IDs to check for hide_presence
	userIDs := make([]uint, 0, len(users))
	for _, entry := range users {
		userIDs = append(userIDs, entry.UserID)
		snapshot = append(snapshot, PresenceEntry{
			UserID:        entry.UserID,
			IssueID:       entry.IssueID,
			LastHeartbeat: entry.LastHeartbeat,
			Status:        entry.Status,
		})
	}
	h.mu.RUnlock()

	// Fetch hide_presence preferences for all users in this issue
	hidePresenceMap := make(map[uint]bool)
	if len(userIDs) > 0 {
		var usersWithHide []db.User
		if err := h.db.WithContext(ctx).Where("id IN ? AND hide_presence = ?", userIDs, true).Find(&usersWithHide).Error; err != nil {
			return nil, err
		}
		for _, u := range usersWithHide {
			hidePresenceMap[u.ID] = true
		}
	}

	// Recalculate status based on current time and filter hidden users
	now := time.Now()
	entries := make([]PresenceEntry, 0, len(snapshot))
	for _, entry := range snapshot {
		// Filter out users who have hide_presence=true (except the viewer themselves)
		if viewerID != 0 && entry.UserID != viewerID && hidePresenceMap[entry.UserID] {
			continue
		}
		status := h.calculateStatus(now, entry.LastHeartbeat)
		entries = append(entries, PresenceEntry{
			UserID:        entry.UserID,
			IssueID:       entry.IssueID,
			LastHeartbeat: entry.LastHeartbeat,
			Status:        status,
		})
	}
	return entries, nil
}

// GetUserLastSeen returns the last-seen timestamp for a user
func (h *PresenceHub) GetUserLastSeen(ctx context.Context, userID uint) (*db.UserLastSeen, error) {
	var lastSeen db.UserLastSeen
	err := h.db.WithContext(ctx).Where("user_id = ?", userID).First(&lastSeen).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &lastSeen, nil
}

// SetHidePresence updates the user's hide_presence preference
func (h *PresenceHub) SetHidePresence(ctx context.Context, userID uint, hide bool) error {
	return h.db.WithContext(ctx).Model(&db.User{}).Where("id = ?", userID).Update("hide_presence", hide).Error
}

// IsPresenceHidden returns whether a user has hidden their presence
func (h *PresenceHub) IsPresenceHidden(ctx context.Context, userID uint) (bool, error) {
	var user db.User
	err := h.db.WithContext(ctx).Select("hide_presence").Where("id = ?", userID).First(&user).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return false, nil
		}
		return false, err
	}
	return user.HidePresence, nil
}

// calculateStatus determines presence status based on time since last heartbeat
func (h *PresenceHub) calculateStatus(now, lastHeartbeat time.Time) PresenceStatus {
	elapsed := now.Sub(lastHeartbeat)
	if elapsed < 60*time.Second {
		return PresenceActive
	} else if elapsed < 5*time.Minute {
		return PresenceIdle
	}
	return PresenceOffline
}

// PresenceService interface for dependency injection
type PresenceService interface {
	UpdateHeartbeat(ctx context.Context, userID, issueID uint) error
	GetIssuePresenceState(ctx context.Context, issueID uint, viewerID uint) ([]PresenceEntry, error)
	GetUserLastSeen(ctx context.Context, userID uint) (*db.UserLastSeen, error)
	SetHidePresence(ctx context.Context, userID uint, hide bool) error
	IsPresenceHidden(ctx context.Context, userID uint) (bool, error)
}

// Ensure PresenceHub implements PresenceService
var _ PresenceService = (*PresenceHub)(nil)
