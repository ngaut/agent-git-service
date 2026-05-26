package service_test

import (
	"context"
	"testing"

	"github.com/ngaut/agent-git-service/internal/db"
)

func TestGetUsersByLogins(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	ctx := context.Background()

	alice := db.User{Login: "alice", Type: db.TypeUser}
	bob := db.User{Login: "bob", Type: db.TypeUser}
	if err := svc.DB.Create(&alice).Error; err != nil {
		t.Fatalf("create alice: %v", err)
	}
	if err := svc.DB.Create(&bob).Error; err != nil {
		t.Fatalf("create bob: %v", err)
	}

	t.Run("resolves_each_login", func(t *testing.T) {
		got := svc.GetUsersByLogins(ctx, []string{"alice", "bob"})
		if got["alice"].ID != alice.ID {
			t.Errorf("alice ID = %d, want %d", got["alice"].ID, alice.ID)
		}
		if got["bob"].ID != bob.ID {
			t.Errorf("bob ID = %d, want %d", got["bob"].ID, bob.ID)
		}
	})

	t.Run("missing_login_absent_from_map", func(t *testing.T) {
		got := svc.GetUsersByLogins(ctx, []string{"alice", "nobody"})
		if _, ok := got["nobody"]; ok {
			t.Errorf("nobody unexpectedly present; missing logins must be absent")
		}
		if got["alice"].ID != alice.ID {
			t.Errorf("alice ID = %d, want %d", got["alice"].ID, alice.ID)
		}
	})

	t.Run("dedupes_and_trims", func(t *testing.T) {
		// Repeated login + whitespace-only entries + empty strings must not
		// multiply the WHERE clause or generate empty-string placeholders.
		got := svc.GetUsersByLogins(ctx, []string{"alice", " alice ", "alice", "", "   "})
		if len(got) != 1 {
			t.Errorf("len=%d want 1 (deduped)", len(got))
		}
		if got["alice"].ID != alice.ID {
			t.Errorf("alice missing from dedup result: %v", got)
		}
	})

	t.Run("empty_and_nil_input", func(t *testing.T) {
		if got := svc.GetUsersByLogins(ctx, nil); got != nil {
			t.Errorf("nil input: got %v, want nil", got)
		}
		if got := svc.GetUsersByLogins(ctx, []string{}); got != nil {
			t.Errorf("empty slice: got %v, want nil", got)
		}
		if got := svc.GetUsersByLogins(ctx, []string{"", "   "}); got != nil {
			t.Errorf("all-whitespace input: got %v, want nil", got)
		}
	})
}
