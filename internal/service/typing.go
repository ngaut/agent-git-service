package service

import (
	"sort"
	"sync"
	"time"
)

const DefaultTypingTTL = 5 * time.Second

type TypingUser struct {
	ID    uint   `json:"id"`
	Login string `json:"login"`
	Name  string `json:"name,omitempty"`
}

type TypingEnvelope struct {
	IssueID   uint         `json:"issue_id"`
	Users     []TypingUser `json:"users,omitempty"`
	User      *TypingUser  `json:"user,omitempty"`
	Typing    bool         `json:"typing"`
	ExpiresAt *time.Time   `json:"expires_at,omitempty"`
}

type typingKey struct {
	issueID uint
	userID  uint
}

type typingEntry struct {
	user       TypingUser
	expiresAt  time.Time
	generation uint64
	timer      *time.Timer
}

type TypingHub struct {
	ttl time.Duration

	mu               sync.Mutex
	nextSubscriberID uint64
	entries          map[typingKey]*typingEntry
	subscribers      map[uint]map[uint64]chan TypingEnvelope
}

func NewTypingHub(ttl time.Duration) *TypingHub {
	if ttl <= 0 {
		ttl = DefaultTypingTTL
	}
	return &TypingHub{
		ttl:         ttl,
		entries:     make(map[typingKey]*typingEntry),
		subscribers: make(map[uint]map[uint64]chan TypingEnvelope),
	}
}

func (s *Service) TypingHub() *TypingHub {
	s.typingHubOnce.Do(func() {
		s.typingHub = NewTypingHub(DefaultTypingTTL)
	})
	return s.typingHub
}

func (h *TypingHub) TTL() time.Duration {
	if h == nil {
		return DefaultTypingTTL
	}
	return h.ttl
}

func (h *TypingHub) Subscribe(issueID uint) ([]TypingUser, <-chan TypingEnvelope, func()) {
	ch := make(chan TypingEnvelope, 16)

	h.mu.Lock()
	h.nextSubscriberID++
	subID := h.nextSubscriberID
	if _, ok := h.subscribers[issueID]; !ok {
		h.subscribers[issueID] = make(map[uint64]chan TypingEnvelope)
	}
	h.subscribers[issueID][subID] = ch
	snapshot := h.snapshotLocked(issueID)
	h.mu.Unlock()

	var once sync.Once
	unsubscribe := func() {
		once.Do(func() {
			h.mu.Lock()
			if subs, ok := h.subscribers[issueID]; ok {
				delete(subs, subID)
				if len(subs) == 0 {
					delete(h.subscribers, issueID)
				}
			}
			h.mu.Unlock()
		})
	}

	return snapshot, ch, unsubscribe
}

func (h *TypingHub) Snapshot(issueID uint) []TypingUser {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.snapshotLocked(issueID)
}

func (h *TypingHub) Signal(issueID uint, user TypingUser, typing bool) bool {
	if h == nil || issueID == 0 || user.ID == 0 {
		return false
	}

	var (
		env   TypingEnvelope
		chans []chan TypingEnvelope
		ok    bool
	)

	h.mu.Lock()
	key := typingKey{issueID: issueID, userID: user.ID}
	entry, exists := h.entries[key]

	if !typing {
		if !exists {
			h.mu.Unlock()
			return false
		}
		if entry.timer != nil {
			entry.timer.Stop()
		}
		delete(h.entries, key)
		env = TypingEnvelope{
			IssueID: issueID,
			User:    cloneTypingUser(user),
			Typing:  false,
		}
		chans = h.subscriberChannelsLocked(issueID)
		ok = true
		h.mu.Unlock()
		broadcastTyping(chans, env)
		return ok
	}

	generation := uint64(1)
	if exists {
		generation = entry.generation + 1
		if entry.timer != nil {
			entry.timer.Stop()
		}
	}

	expiresAt := time.Now().UTC().Add(h.ttl)
	next := &typingEntry{
		user:       user,
		expiresAt:  expiresAt,
		generation: generation,
	}
	next.timer = time.AfterFunc(h.ttl, func() {
		h.expire(issueID, user.ID, generation)
	})
	h.entries[key] = next

	env = TypingEnvelope{
		IssueID:   issueID,
		User:      cloneTypingUser(user),
		Typing:    true,
		ExpiresAt: cloneTypingTime(expiresAt),
	}
	chans = h.subscriberChannelsLocked(issueID)
	ok = true
	h.mu.Unlock()

	broadcastTyping(chans, env)
	return ok
}

func (h *TypingHub) Close() {
	if h == nil {
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	for key, entry := range h.entries {
		if entry.timer != nil {
			entry.timer.Stop()
		}
		delete(h.entries, key)
	}
	for issueID := range h.subscribers {
		delete(h.subscribers, issueID)
	}
}

func (h *TypingHub) expire(issueID uint, userID uint, generation uint64) {
	var (
		env   TypingEnvelope
		chans []chan TypingEnvelope
	)

	h.mu.Lock()
	key := typingKey{issueID: issueID, userID: userID}
	entry, ok := h.entries[key]
	if !ok || entry.generation != generation {
		h.mu.Unlock()
		return
	}
	user := entry.user
	delete(h.entries, key)
	env = TypingEnvelope{
		IssueID: issueID,
		User:    cloneTypingUser(user),
		Typing:  false,
	}
	chans = h.subscriberChannelsLocked(issueID)
	h.mu.Unlock()

	broadcastTyping(chans, env)
}

func (h *TypingHub) snapshotLocked(issueID uint) []TypingUser {
	now := time.Now().UTC()
	users := make([]TypingUser, 0)
	for key, entry := range h.entries {
		if key.issueID != issueID {
			continue
		}
		if !entry.expiresAt.After(now) {
			continue
		}
		users = append(users, entry.user)
	}
	sort.Slice(users, func(i, j int) bool {
		if users[i].Login == users[j].Login {
			return users[i].ID < users[j].ID
		}
		return users[i].Login < users[j].Login
	})
	return users
}

func (h *TypingHub) subscriberChannelsLocked(issueID uint) []chan TypingEnvelope {
	subs := h.subscribers[issueID]
	if len(subs) == 0 {
		return nil
	}
	chans := make([]chan TypingEnvelope, 0, len(subs))
	for _, ch := range subs {
		chans = append(chans, ch)
	}
	return chans
}

func cloneTypingUser(user TypingUser) *TypingUser {
	out := user
	return &out
}

func cloneTypingTime(ts time.Time) *time.Time {
	out := ts
	return &out
}

func broadcastTyping(chans []chan TypingEnvelope, env TypingEnvelope) {
	for _, ch := range chans {
		select {
		case ch <- env:
		default:
		}
	}
}
