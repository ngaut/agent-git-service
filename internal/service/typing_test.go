package service

import (
	"testing"
	"time"
)

func TestTypingHubExpiresAfterTTL(t *testing.T) {
	hub := NewTypingHub(80 * time.Millisecond)
	t.Cleanup(hub.Close)

	snapshot, ch, unsubscribe := hub.Subscribe(42)
	defer unsubscribe()

	if len(snapshot) != 0 {
		t.Fatalf("expected empty snapshot, got %d users", len(snapshot))
	}

	user := TypingUser{ID: 7, Login: "alice"}
	if ok := hub.Signal(42, user, true); !ok {
		t.Fatal("expected typing signal to be accepted")
	}

	start := mustReadTypingEvent(t, ch, 250*time.Millisecond)
	if !start.Typing || start.User == nil || start.User.Login != "alice" {
		t.Fatalf("unexpected typing-start event: %+v", start)
	}

	if users := hub.Snapshot(42); len(users) != 1 || users[0].Login != "alice" {
		t.Fatalf("unexpected snapshot after start: %+v", users)
	}

	stop := mustReadTypingEvent(t, ch, 500*time.Millisecond)
	if stop.Typing || stop.User == nil || stop.User.Login != "alice" {
		t.Fatalf("unexpected typing-stop event: %+v", stop)
	}

	if users := hub.Snapshot(42); len(users) != 0 {
		t.Fatalf("expected snapshot to be empty after expiry, got %+v", users)
	}
}

func TestTypingHubExplicitStopClearsState(t *testing.T) {
	hub := NewTypingHub(200 * time.Millisecond)
	t.Cleanup(hub.Close)

	_, ch, unsubscribe := hub.Subscribe(77)
	defer unsubscribe()

	user := TypingUser{ID: 9, Login: "bob"}
	hub.Signal(77, user, true)
	_ = mustReadTypingEvent(t, ch, 250*time.Millisecond)

	if ok := hub.Signal(77, user, false); !ok {
		t.Fatal("expected explicit stop to clear active typing state")
	}

	stop := mustReadTypingEvent(t, ch, 250*time.Millisecond)
	if stop.Typing || stop.User == nil || stop.User.Login != "bob" {
		t.Fatalf("unexpected explicit stop event: %+v", stop)
	}

	select {
	case extra := <-ch:
		t.Fatalf("unexpected extra typing event after explicit stop: %+v", extra)
	case <-time.After(300 * time.Millisecond):
	}

	if users := hub.Snapshot(77); len(users) != 0 {
		t.Fatalf("expected explicit stop to clear snapshot, got %+v", users)
	}
}

func mustReadTypingEvent(t *testing.T, ch <-chan TypingEnvelope, timeout time.Duration) TypingEnvelope {
	t.Helper()

	select {
	case ev := <-ch:
		return ev
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for typing event after %s", timeout)
		return TypingEnvelope{}
	}
}
