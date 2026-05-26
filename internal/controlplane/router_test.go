package controlplane

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/ngaut/agent-git-service/internal/db"
)

var testCounter atomic.Int64

// newTestCPDB creates an in-memory SQLite control plane DB with migrated schemas.
func newTestCPDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:cp_test_%d?mode=memory&cache=shared", testCounter.Add(1))
	cpDB, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open cp db: %v", err)
	}
	if err := cpDB.AutoMigrate(&CPUser{}, &CPToken{}); err != nil {
		t.Fatalf("migrate cp db: %v", err)
	}
	return cpDB
}

// testOpenDB returns an OpenDBFunc that creates file-based SQLite DBs with
// WAL mode (required for concurrent access) and tracks call count.
func testOpenDB(t *testing.T, callCount *atomic.Int64) OpenDBFunc {
	return func(dsn string) (*gorm.DB, error) {
		callCount.Add(1)
		dir := t.TempDir()
		dbPath := fmt.Sprintf("%s/tenant_%d.db", dir, testCounter.Add(1))
		tdb, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
		if err != nil {
			return nil, err
		}
		tdb.Exec("PRAGMA journal_mode = WAL")
		tdb.Exec("PRAGMA busy_timeout = 5000")
		return tdb, nil
	}
}

func seedAgent(t *testing.T, cpDB *gorm.DB, login, token, dsn string) CPUser {
	t.Helper()
	return seedAgentWithState(t, cpDB, login, token, dsn, AgentStateActive)
}

func seedAgentWithState(t *testing.T, cpDB *gorm.DB, login, token, dsn string, state AgentState) CPUser {
	t.Helper()
	user := CPUser{Login: login, DSN: dsn, State: state}
	if err := cpDB.Create(&user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := cpDB.Create(&CPToken{Value: token, CPUserID: user.ID}).Error; err != nil {
		t.Fatalf("seed token: %v", err)
	}
	return user
}

func TestResolveToken_KnownToken(t *testing.T) {
	cpDB := newTestCPDB(t)
	var calls atomic.Int64
	router := NewDBRouter(cpDB, testOpenDB(t, &calls), true, RouterConfig{MaxAgents: 10})
	defer router.Close()

	seedAgent(t, cpDB, "agent1", "tok-1", "agent1-dsn")

	user, tenantDB, err := router.ResolveToken(context.Background(), "tok-1")
	if err != nil {
		t.Fatalf("ResolveToken: %v", err)
	}
	if user.Login != "agent1" {
		t.Errorf("expected login=agent1, got %s", user.Login)
	}
	if tenantDB == nil {
		t.Fatal("tenantDB is nil")
	}
	// Verify tenant user was created in tenant DB
	var tenantUser db.User
	if err := tenantDB.First(&tenantUser, "login = ?", "agent1").Error; err != nil {
		t.Fatalf("tenant user not found: %v", err)
	}
	if tenantUser.SiteAdmin != true {
		t.Error("tenant user should be SiteAdmin")
	}
	if tenantUser.UserKind != db.UserKindAgent {
		t.Errorf("tenant user kind = %q, want %q", tenantUser.UserKind, db.UserKindAgent)
	}
}

func TestTenantDBs_ActiveUsersOnly(t *testing.T) {
	cpDB := newTestCPDB(t)
	var calls atomic.Int64
	router := NewDBRouter(cpDB, testOpenDB(t, &calls), true, RouterConfig{MaxAgents: 10})
	defer router.Close()

	seedAgentWithState(t, cpDB, "active-1", "tok-1", "dsn-1", AgentStateActive)
	seedAgentWithState(t, cpDB, "pending-1", "tok-2", "dsn-2", AgentStatePending)
	seedAgentWithState(t, cpDB, "active-2", "tok-3", "dsn-3", AgentStateActive)

	dbs, err := router.TenantDBs(context.Background())
	if err != nil {
		t.Fatalf("TenantDBs: %v", err)
	}
	if len(dbs) != 2 {
		t.Fatalf("len(TenantDBs) = %d, want 2", len(dbs))
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("openDB calls = %d, want 2", got)
	}
}

func TestResolveToken_NilRouterReturnsError(t *testing.T) {
	var router *DBRouter

	_, _, err := router.ResolveToken(context.Background(), "token")
	if err == nil {
		t.Fatal("expected error for nil router")
	}
	if !strings.Contains(err.Error(), "not initialized") {
		t.Fatalf("expected initialization error, got %v", err)
	}
}

func TestResolveToken_UnknownToken(t *testing.T) {
	cpDB := newTestCPDB(t)
	var calls atomic.Int64
	router := NewDBRouter(cpDB, testOpenDB(t, &calls), true, RouterConfig{MaxAgents: 10})
	defer router.Close()

	_, _, err := router.ResolveToken(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error for unknown token")
	}
}

func TestResolveToken_CachesConnection(t *testing.T) {
	cpDB := newTestCPDB(t)
	var calls atomic.Int64
	router := NewDBRouter(cpDB, testOpenDB(t, &calls), true, RouterConfig{MaxAgents: 10})
	defer router.Close()

	seedAgent(t, cpDB, "agent2", "tok-2", "agent2-dsn")

	_, db1, err := router.ResolveToken(context.Background(), "tok-2")
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	_, db2, err := router.ResolveToken(context.Background(), "tok-2")
	if err != nil {
		t.Fatalf("second call: %v", err)
	}

	// Same *gorm.DB pointer
	if db1 != db2 {
		t.Error("expected cached connection to return same *gorm.DB")
	}
	// openDB should have been called exactly once
	if calls.Load() != 1 {
		t.Errorf("expected openDB called 1 time, got %d", calls.Load())
	}
}

func TestResolveToken_ReconcilesLegacyTenantUserKind(t *testing.T) {
	cpDB := newTestCPDB(t)
	var calls atomic.Int64
	var tenantDB *gorm.DB
	openDB := func(dsn string) (*gorm.DB, error) {
		calls.Add(1)
		if tenantDB != nil {
			return tenantDB, nil
		}
		dir := t.TempDir()
		dbPath := fmt.Sprintf("%s/tenant_%d.db", dir, testCounter.Add(1))
		tdb, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
		if err != nil {
			return nil, err
		}
		tdb.Exec("PRAGMA journal_mode = WAL")
		tdb.Exec("PRAGMA busy_timeout = 5000")
		if err := db.Migrate(tdb); err != nil {
			return nil, err
		}
		legacyUser := db.User{
			Login:    "legacy-agent",
			Name:     "legacy-agent",
			Type:     db.TypeUser,
			UserKind: db.UserKindHuman,
		}
		if err := tdb.Create(&legacyUser).Error; err != nil {
			return nil, err
		}
		tenantDB = tdb
		return tenantDB, nil
	}
	router := NewDBRouter(cpDB, openDB, true, RouterConfig{MaxAgents: 10})
	defer router.Close()

	seedAgent(t, cpDB, "legacy-agent", "tok-legacy", "legacy-dsn")

	user, resolvedDB, err := router.ResolveToken(context.Background(), "tok-legacy")
	if err != nil {
		t.Fatalf("ResolveToken: %v", err)
	}
	if user.UserKind != db.UserKindAgent {
		t.Fatalf("resolved user kind = %q, want %q", user.UserKind, db.UserKindAgent)
	}

	var tenantUser db.User
	if err := resolvedDB.First(&tenantUser, "login = ?", "legacy-agent").Error; err != nil {
		t.Fatalf("tenant user not found: %v", err)
	}
	if tenantUser.UserKind != db.UserKindAgent {
		t.Fatalf("tenant user kind = %q, want %q", tenantUser.UserKind, db.UserKindAgent)
	}
	if !tenantUser.SiteAdmin {
		t.Fatal("tenant user should be SiteAdmin")
	}
}

func TestResolveToken_ConcurrentSameAgent(t *testing.T) {
	cpDB := newTestCPDB(t)
	var calls atomic.Int64
	router := NewDBRouter(cpDB, testOpenDB(t, &calls), true, RouterConfig{MaxAgents: 10})
	defer router.Close()

	seedAgent(t, cpDB, "agent3", "tok-3", "agent3-dsn")

	const goroutines = 10
	var wg sync.WaitGroup
	wg.Add(goroutines)
	errs := make([]error, goroutines)
	dbs := make([]*gorm.DB, goroutines)

	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			_, tdb, err := router.ResolveToken(context.Background(), "tok-3")
			errs[idx] = err
			dbs[idx] = tdb
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}
	// All should return the same DB
	for i := 1; i < goroutines; i++ {
		if dbs[i] != dbs[0] {
			t.Errorf("goroutine %d returned different DB pointer", i)
		}
	}
	// openDB should have been called exactly once (singleflight)
	if calls.Load() != 1 {
		t.Errorf("expected openDB called 1 time, got %d", calls.Load())
	}
}

func TestResolveToken_MaxAgentsCap(t *testing.T) {
	cpDB := newTestCPDB(t)
	var calls atomic.Int64
	router := NewDBRouter(cpDB, testOpenDB(t, &calls), true, RouterConfig{MaxAgents: 1})
	defer router.Close()

	seedAgent(t, cpDB, "agentA", "tok-A", "dsnA")
	seedAgent(t, cpDB, "agentB", "tok-B", "dsnB")

	// First agent fills the cache
	_, _, err := router.ResolveToken(context.Background(), "tok-A")
	if err != nil {
		t.Fatalf("first agent: %v", err)
	}

	// Second agent should fail (cache at capacity)
	_, _, err = router.ResolveToken(context.Background(), "tok-B")
	if err == nil {
		t.Fatal("expected error when cache is at capacity")
	}
}

func TestResolveToken_ConcurrentDifferentAgents_MaxCap(t *testing.T) {
	cpDB := newTestCPDB(t)
	var calls atomic.Int64
	router := NewDBRouter(cpDB, testOpenDB(t, &calls), true, RouterConfig{MaxAgents: 1})
	defer router.Close()

	seedAgent(t, cpDB, "agentX", "tok-X", "dsnX")
	seedAgent(t, cpDB, "agentY", "tok-Y", "dsnY")

	const goroutines = 2
	var wg sync.WaitGroup
	wg.Add(goroutines)
	errs := make([]error, goroutines)

	// Fire two concurrent requests for different agents with MaxAgents=1.
	// Exactly one must succeed and one must fail.
	tokens := []string{"tok-X", "tok-Y"}
	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			_, _, errs[idx] = router.ResolveToken(context.Background(), tokens[idx])
		}(i)
	}
	wg.Wait()

	successes := 0
	failures := 0
	for _, err := range errs {
		if err == nil {
			successes++
		} else {
			failures++
		}
	}
	if successes != 1 || failures != 1 {
		t.Errorf("expected 1 success + 1 failure, got %d successes + %d failures", successes, failures)
	}

	router.mu.RLock()
	cacheLen := len(router.cache)
	router.mu.RUnlock()
	if cacheLen != 1 {
		t.Errorf("expected cache size 1, got %d", cacheLen)
	}
}

func TestClose_DrainsConnections(t *testing.T) {
	cpDB := newTestCPDB(t)
	var calls atomic.Int64
	router := NewDBRouter(cpDB, testOpenDB(t, &calls), true, RouterConfig{MaxAgents: 10})

	seedAgent(t, cpDB, "agentC", "tok-C", "dsnC")
	seedAgent(t, cpDB, "agentD", "tok-D", "dsnD")

	_, _, _ = router.ResolveToken(context.Background(), "tok-C")
	_, _, _ = router.ResolveToken(context.Background(), "tok-D")

	if err := router.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	router.mu.RLock()
	defer router.mu.RUnlock()
	if len(router.cache) != 0 {
		t.Errorf("expected empty cache after Close, got %d entries", len(router.cache))
	}
}

func TestResolveToken_RejectsPendingAgent(t *testing.T) {
	cpDB := newTestCPDB(t)
	var calls atomic.Int64
	router := NewDBRouter(cpDB, testOpenDB(t, &calls), true, RouterConfig{MaxAgents: 10})
	defer router.Close()

	// Seed agent with pending state
	seedAgentWithState(t, cpDB, "pending-agent", "tok-pending", "pending-dsn", AgentStatePending)

	_, _, err := router.ResolveToken(context.Background(), "tok-pending")
	if err == nil {
		t.Fatal("expected error for pending agent")
	}
	if !strings.Contains(err.Error(), "not active") || !strings.Contains(err.Error(), "pending") {
		t.Errorf("expected 'not active' error with state, got: %v", err)
	}
}

func TestResolveToken_RejectsFailedAgent(t *testing.T) {
	cpDB := newTestCPDB(t)
	var calls atomic.Int64
	router := NewDBRouter(cpDB, testOpenDB(t, &calls), true, RouterConfig{MaxAgents: 10})
	defer router.Close()

	// Seed agent with failed state
	seedAgentWithState(t, cpDB, "failed-agent", "tok-failed", "failed-dsn", AgentStateFailed)

	_, _, err := router.ResolveToken(context.Background(), "tok-failed")
	if err == nil {
		t.Fatal("expected error for failed agent")
	}
	if !strings.Contains(err.Error(), "not active") || !strings.Contains(err.Error(), "failed") {
		t.Errorf("expected 'not active' error with state, got: %v", err)
	}
}

func TestCPUser_TransitionTo_ValidTransitions(t *testing.T) {
	tests := []struct {
		name    string
		from    AgentState
		to      AgentState
		reason  *string
		wantErr bool
	}{
		{"pending to active", AgentStatePending, AgentStateActive, nil, false},
		{"pending to failed", AgentStatePending, AgentStateFailed, strPtr("agent setup failed"), false},
		{"failed to pending", AgentStateFailed, AgentStatePending, nil, false},
		{"failed to active", AgentStateFailed, AgentStateActive, nil, false},
		{"active to failed", AgentStateActive, AgentStateFailed, strPtr("runtime error"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			user := &CPUser{Login: "test", DSN: "test-dsn", State: tt.from}
			err := user.TransitionTo(tt.to, tt.reason)
			if (err != nil) != tt.wantErr {
				t.Errorf("TransitionTo() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil {
				if user.State != tt.to {
					t.Errorf("state = %v, want %v", user.State, tt.to)
				}
				if tt.to == AgentStateFailed {
					if user.FailureReason == nil || *user.FailureReason == "" {
						t.Error("expected FailureReason to be set for failed state")
					}
				} else {
					if user.FailureReason != nil {
						t.Error("expected FailureReason to be nil for non-failed state")
					}
				}
			}
		})
	}
}

func TestCPUser_TransitionTo_InvalidTransitions(t *testing.T) {
	tests := []struct {
		name string
		from AgentState
		to   AgentState
	}{
		{"active to pending", AgentStateActive, AgentStatePending},
		{"pending to pending", AgentStatePending, AgentStatePending},
		{"active to active", AgentStateActive, AgentStateActive},
		{"failed to failed", AgentStateFailed, AgentStateFailed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			user := &CPUser{Login: "test", DSN: "test-dsn", State: tt.from}
			err := user.TransitionTo(tt.to, nil)
			if err == nil {
				t.Errorf("expected error for invalid transition %s -> %s", tt.from, tt.to)
			}
		})
	}
}

func TestCPUser_CreateUser(t *testing.T) {
	user := CreateUser("testuser", "test@example.com", "test-dsn")
	if user.Login != "testuser" {
		t.Errorf("Login = %v, want testuser", user.Login)
	}
	if user.Email != "test@example.com" {
		t.Errorf("Email = %v, want test@example.com", user.Email)
	}
	if user.DSN != "test-dsn" {
		t.Errorf("DSN = %v, want test-dsn", user.DSN)
	}
	if user.State != AgentStatePending {
		t.Errorf("State = %v, want pending", user.State)
	}
	if user.FailureReason != nil {
		t.Error("expected FailureReason to be nil")
	}
}

func TestCPUser_ActivateUser(t *testing.T) {
	reason := "test failure"
	user := &CPUser{Login: "test", DSN: "test-dsn", State: AgentStatePending, FailureReason: &reason}
	err := user.ActivateUser()
	if err != nil {
		t.Errorf("ActivateUser() error = %v", err)
	}
	if user.State != AgentStateActive {
		t.Errorf("State = %v, want active", user.State)
	}
	if user.FailureReason != nil {
		t.Error("expected FailureReason to be cleared")
	}
}

func TestCPUser_FailUser(t *testing.T) {
	user := &CPUser{Login: "test", DSN: "test-dsn", State: AgentStatePending}
	err := user.FailUser("test failure reason")
	if err != nil {
		t.Errorf("FailUser() error = %v", err)
	}
	if user.State != AgentStateFailed {
		t.Errorf("State = %v, want failed", user.State)
	}
	if user.FailureReason == nil || *user.FailureReason != "test failure reason" {
		t.Errorf("FailureReason = %v, want 'test failure reason'", user.FailureReason)
	}
}

func TestCPUser_RetryUser(t *testing.T) {
	reason := "previous failure"
	user := &CPUser{Login: "test", DSN: "test-dsn", State: AgentStateFailed, FailureReason: &reason}
	err := user.RetryUser()
	if err != nil {
		t.Errorf("RetryUser() error = %v", err)
	}
	if user.State != AgentStatePending {
		t.Errorf("State = %v, want pending", user.State)
	}
	if user.FailureReason != nil {
		t.Error("expected FailureReason to be cleared")
	}
}

func strPtr(s string) *string {
	return &s
}
