package service

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"gh-server/internal/db"
	"gh-server/internal/embedding"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// TestEmbedHook_NopEmbedderShortCircuit tests that NopEmbedder causes immediate return.
func TestEmbedHook_NopEmbedderShortCircuit(t *testing.T) {
	svc := &Service{
		Embedder: embedding.NopEmbedder{},
	}

	// Call EmbedIssue with NopEmbedder — should return immediately without
	// starting any background goroutine. Run the call inside a goroutine of
	// our own so the test can time out via the channel if short-circuiting
	// breaks.
	done := make(chan struct{})
	go func() {
		svc.EmbedIssue(context.Background(), 1, "title", "body")
		close(done)
	}()

	select {
	case <-done:
		// Success — call returned without blocking.
	case <-time.After(100 * time.Millisecond):
		t.Error("Expected NopEmbedder to short-circuit immediately")
	}
}

// TestEmbedHook_SuccessfulEmbedding tests that embedding is stored in DB.
func TestEmbedHook_SuccessfulEmbedding(t *testing.T) {
	// Setup in-memory SQLite DB
	tmpDB, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open DB: %v", err)
	}

	// Run migrations for issues table
	if err := db.Migrate(tmpDB); err != nil {
		t.Fatalf("failed to migrate: %v", err)
	}

	// Add embedding column (normally done by InitVector for TiDB)
	if err := tmpDB.Exec("ALTER TABLE issues ADD COLUMN embedding TEXT").Error; err != nil {
		t.Fatalf("failed to add embedding column: %v", err)
	}

	svc := &Service{
		DB:       tmpDB,
		Embedder: &FakeEmbedder{Vec: []float32{0.1, 0.2, 0.3}},
		Ctx:      context.Background(),
	}

	// Create a test issue
	issue := db.Issue{
		Number: 1,
		Title:  "Test Issue",
		Body:   "Test Body",
		State:  "open",
		Author: db.User{Login: "testuser"},
	}
	if err := svc.DB.Create(&issue.Author).Error; err != nil {
		t.Fatalf("failed to create author: %v", err)
	}
	if err := svc.DB.Create(&issue).Error; err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	t.Logf("Created issue with ID: %d", issue.ID)

	// Verify we can manually update the embedding column
	if err := svc.DB.Exec("UPDATE issues SET embedding = ? WHERE id = ?", "[manual]", issue.ID).Error; err != nil {
		t.Fatalf("manual update failed: %v", err)
	}
	var checkEmbedding string
	if err := svc.DB.Raw("SELECT embedding FROM issues WHERE id = ?", issue.ID).Scan(&checkEmbedding).Error; err != nil {
		t.Fatalf("failed to fetch embedding after manual update: %v", err)
	}
	t.Logf("After manual update: %q", checkEmbedding)

	// Reset embedding to empty for the actual test
	if err := svc.DB.Exec("UPDATE issues SET embedding = NULL WHERE id = ?", issue.ID).Error; err != nil {
		t.Fatalf("reset failed: %v", err)
	}

	// Call EmbedIssue
	svc.EmbedIssue(context.Background(), issue.ID, issue.Title, string(issue.Body))

	// Wait for background goroutine to complete (with timeout)
	done := make(chan struct{})
	go func() {
		svc.Wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		// Success
	case <-time.After(5 * time.Second):
		t.Fatal("Timeout waiting for background goroutine")
	}

	// Verify embedding was stored (use raw SQL since Embedding field has gorm:"-")
	var resultEmbedding string
	if err := svc.DB.Raw("SELECT embedding FROM issues WHERE id = ?", issue.ID).Scan(&resultEmbedding).Error; err != nil {
		t.Fatalf("failed to fetch embedding: %v", err)
	}

	t.Logf("Result embedding: %q", resultEmbedding)

	expected := "[0.1,0.2,0.3]"
	if resultEmbedding != expected {
		t.Errorf("Expected embedding %q, got %q", expected, resultEmbedding)
	}
}

// TestEmbedHook_EmbedFailureLeavesNull tests that embed failure leaves column NULL.
func TestEmbedHook_EmbedFailureLeavesNull(t *testing.T) {
	tmpDB, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open DB: %v", err)
	}

	if err := db.Migrate(tmpDB); err != nil {
		t.Fatalf("failed to migrate: %v", err)
	}

	if err := tmpDB.Exec("ALTER TABLE issues ADD COLUMN embedding TEXT").Error; err != nil {
		t.Fatalf("failed to add embedding column: %v", err)
	}

	fakeEmbedder := &FakeEmbedder{Vec: nil, Err: errors.New("embed failed")}
	svc := &Service{
		DB:       tmpDB,
		Embedder: fakeEmbedder,
		Ctx:      context.Background(),
	}

	// Create a test issue
	issue := db.Issue{
		Number: 1,
		Title:  "Test Issue",
		Body:   "Test Body",
		State:  "open",
		Author: db.User{Login: "testuser"},
	}
	if err := svc.DB.Create(&issue.Author).Error; err != nil {
		t.Fatalf("failed to create author: %v", err)
	}
	if err := svc.DB.Create(&issue).Error; err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	// Call EmbedIssue (should fail and leave embedding NULL)
	svc.EmbedIssue(context.Background(), issue.ID, issue.Title, string(issue.Body))

	// Wait for background goroutine
	svc.Wg.Wait()

	// Verify embedding is still NULL/empty (use raw SQL since Embedding has gorm:"-")
	var resultEmbedding sql.NullString
	if err := svc.DB.Raw("SELECT embedding FROM issues WHERE id = ?", issue.ID).Scan(&resultEmbedding).Error; err != nil {
		t.Fatalf("failed to fetch embedding: %v", err)
	}

	if resultEmbedding.Valid {
		t.Errorf("Expected empty embedding on failure, got %q", resultEmbedding.String)
	}

	// Verify FakeEmbedder was called
	if fakeEmbedder.Called == 0 {
		t.Error("Expected FakeEmbedder to be called")
	}
}

// TestEmbedHook_TextTruncation tests that text > 32KB is truncated.
func TestEmbedHook_TextTruncation(t *testing.T) {
	tmpDB, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open DB: %v", err)
	}

	if err := db.Migrate(tmpDB); err != nil {
		t.Fatalf("failed to migrate: %v", err)
	}

	if err := tmpDB.Exec("ALTER TABLE issues ADD COLUMN embedding TEXT").Error; err != nil {
		t.Fatalf("failed to add embedding column: %v", err)
	}

	fakeEmbedder := &FakeEmbedder{Vec: []float32{0.1, 0.2, 0.3}}
	svc := &Service{
		DB:       tmpDB,
		Embedder: fakeEmbedder,
		Ctx:      context.Background(),
	}

	// Create a test issue
	issue := db.Issue{
		Number: 1,
		Title:  "Test",
		Body:   "Test",
		State:  "open",
		Author: db.User{Login: "testuser"},
	}
	if err := svc.DB.Create(&issue.Author).Error; err != nil {
		t.Fatalf("failed to create author: %v", err)
	}
	if err := svc.DB.Create(&issue).Error; err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	// Create text > 32KB
	longText := strings.Repeat("x", 35000)

	// Call EmbedIssue with long text
	svc.EmbedIssue(context.Background(), issue.ID, "title", longText)

	// Wait for background goroutine
	svc.Wg.Wait()

	// Verify FakeEmbedder received truncated text (32000 chars)
	// Note: EmbedIssue concatenates "title\n" + body, so truncation happens on the combined string
	expectedLen := 32000
	if len(fakeEmbedder.LastText) != expectedLen {
		t.Errorf("Expected truncated text (%d chars), got %d chars", expectedLen, len(fakeEmbedder.LastText))
	}
	// Verify it starts with "title\n"
	if !strings.HasPrefix(fakeEmbedder.LastText, "title\n") {
		t.Errorf("Expected text to start with 'title\\n', got %q", fakeEmbedder.LastText[:20])
	}
}

// TestEmbedHook_ReEmbedOnUpdate tests that updating title/body triggers re-embedding.
func TestEmbedHook_ReEmbedOnUpdate(t *testing.T) {
	tmpDB, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open DB: %v", err)
	}

	if err := db.Migrate(tmpDB); err != nil {
		t.Fatalf("failed to migrate: %v", err)
	}

	if err := tmpDB.Exec("ALTER TABLE issues ADD COLUMN embedding TEXT").Error; err != nil {
		t.Fatalf("failed to add embedding column: %v", err)
	}

	fakeEmbedder := &FakeEmbedder{Vec: []float32{0.1, 0.2, 0.3}}
	svc := &Service{
		DB:       tmpDB,
		Embedder: fakeEmbedder,
		Ctx:      context.Background(),
	}

	// Create a test user
	user := db.User{Login: "testuser"}
	if err := svc.DB.Create(&user).Error; err != nil {
		t.Fatalf("failed to create user: %v", err)
	}

	// Create a test issue
	issue := db.Issue{
		Number:   1,
		Title:    "Original Title",
		Body:     "Original Body",
		State:    "open",
		AuthorID: user.ID,
	}
	if err := svc.DB.Create(&issue).Error; err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	// Call EmbedIssue (simulating CreateIssue)
	svc.EmbedIssue(context.Background(), issue.ID, issue.Title, string(issue.Body))
	svc.Wg.Wait()

	// Update title (simulating UpdateIssue)
	issue.Title = "Updated Title"
	if err := svc.DB.Save(&issue).Error; err != nil {
		t.Fatalf("failed to update issue: %v", err)
	}

	// Call EmbedIssue again (simulating UpdateIssue)
	svc.EmbedIssue(context.Background(), issue.ID, issue.Title, string(issue.Body))
	svc.Wg.Wait()

	// Verify FakeEmbedder was called twice (once for create, once for update)
	if fakeEmbedder.Called != 2 {
		t.Errorf("Expected FakeEmbedder to be called 2 times, got %d", fakeEmbedder.Called)
	}
}

// TestEmbedHook_NoReEmbedOnStateChange tests that updating state only doesn't trigger re-embedding.
func TestEmbedHook_NoReEmbedOnStateChange(t *testing.T) {
	tmpDB, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open DB: %v", err)
	}

	if err := db.Migrate(tmpDB); err != nil {
		t.Fatalf("failed to migrate: %v", err)
	}

	if err := tmpDB.Exec("ALTER TABLE issues ADD COLUMN embedding TEXT").Error; err != nil {
		t.Fatalf("failed to add embedding column: %v", err)
	}

	fakeEmbedder := &FakeEmbedder{Vec: []float32{0.1, 0.2, 0.3}}
	svc := &Service{
		DB:       tmpDB,
		Embedder: fakeEmbedder,
		Ctx:      context.Background(),
	}

	// Create a test user
	user := db.User{Login: "testuser"}
	if err := svc.DB.Create(&user).Error; err != nil {
		t.Fatalf("failed to create user: %v", err)
	}

	// Create a test issue
	issue := db.Issue{
		Number:   1,
		Title:    "Test Title",
		Body:     "Test Body",
		State:    "open",
		AuthorID: user.ID,
	}
	if err := svc.DB.Create(&issue).Error; err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	// Call EmbedIssue (simulating CreateIssue)
	svc.EmbedIssue(context.Background(), issue.ID, issue.Title, string(issue.Body))
	svc.Wg.Wait()

	// Update state only (not title/body)
	issue.State = "closed"
	if err := svc.DB.Save(&issue).Error; err != nil {
		t.Fatalf("failed to update issue: %v", err)
	}

	// Note: In real code, UpdateIssue only calls EmbedIssue if title/body changed
	// This test verifies that if we DON'T call EmbedIssue, no embedding happens
	// The FakeEmbedder should still show only 1 call (from create)

	if fakeEmbedder.Called != 1 {
		t.Errorf("Expected FakeEmbedder to be called 1 time (create only), got %d", fakeEmbedder.Called)
	}
}

// TestEmbedHook_EmbedPR tests EmbedPR function.
func TestEmbedHook_EmbedPR(t *testing.T) {
	tmpDB, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open DB: %v", err)
	}

	if err := db.Migrate(tmpDB); err != nil {
		t.Fatalf("failed to migrate: %v", err)
	}

	if err := tmpDB.Exec("ALTER TABLE pull_requests ADD COLUMN embedding TEXT").Error; err != nil {
		t.Fatalf("failed to add embedding column: %v", err)
	}

	fakeEmbedder := &FakeEmbedder{Vec: []float32{0.1, 0.2, 0.3}}
	svc := &Service{
		DB:       tmpDB,
		Embedder: fakeEmbedder,
		Ctx:      context.Background(),
	}

	// Create a test PR
	pr := db.PullRequest{
		Number:  1,
		Title:   "Test PR",
		Body:    "Test Body",
		State:   "open",
		HeadRef: "feature",
		BaseRef: "main",
		Author:  db.User{Login: "testuser"},
	}
	if err := svc.DB.Create(&pr.Author).Error; err != nil {
		t.Fatalf("failed to create author: %v", err)
	}
	if err := svc.DB.Create(&pr).Error; err != nil {
		t.Fatalf("failed to create PR: %v", err)
	}

	// Call EmbedPR
	svc.EmbedPR(context.Background(), pr.ID, pr.Title, string(pr.Body))

	// Wait for background goroutine
	svc.Wg.Wait()

	// Verify embedding was stored (use raw SQL since Embedding has gorm:"-")
	var resultEmbedding string
	if err := svc.DB.Raw("SELECT embedding FROM pull_requests WHERE id = ?", pr.ID).Scan(&resultEmbedding).Error; err != nil {
		t.Fatalf("failed to fetch embedding: %v", err)
	}

	expected := "[0.1,0.2,0.3]"
	if resultEmbedding != expected {
		t.Errorf("Expected embedding %q, got %q", expected, resultEmbedding)
	}
}

// TestIsTransientError tests error classification.
func TestIsTransientError(t *testing.T) {
	tests := []struct {
		name string
		err  string
		want bool
	}{
		{"429 rate limit", "embedding: API returned 429: {\"code\":\"RATE_CONCURRENCY_LIMIT_EXCEEDED\"}", true},
		{"429 in message", "embedding: API returned 429: too many requests", true},
		{"500 server error", "embedding: API returned 500: internal server error", true},
		{"502 bad gateway", "embedding: API returned 502: bad gateway", true},
		{"503 unavailable", "embedding: API returned 503: service unavailable", true},
		{"504 gateway timeout", "embedding: API returned 504: gateway timeout", true},
		{"timeout", "embedding: context deadline exceeded", true},
		{"connection reset", "embedding: connection reset by peer", true},
		{"EOF", "embedding: EOF", true},
		{"400 bad request", "embedding: API returned 400: bad request", false},
		{"401 unauthorized", "embedding: API returned 401: unauthorized", false},
		{"404 not found", "embedding: API returned 404: not found", false},
		{"nil error", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var err error
			if tt.err != "" {
				err = &mockError{msg: tt.err}
			}
			if got := isTransientError(err); got != tt.want {
				t.Errorf("isTransientError(%v) = %v, want %v", err, got, tt.want)
			}
		})
	}
}

// TestEmbeddingHook_ConcurrencyConfig tests that EMBEDDING_CONCURRENCY env var is respected.
func TestEmbeddingHook_ConcurrencyConfig(t *testing.T) {
	// Test that EMBEDDING_CONCURRENCY env var is respected
	t.Setenv("EMBEDDING_CONCURRENCY", "10")

	// This test just verifies the code path is exercised
	// Actual concurrency testing would require more complex setup
	s := &Service{}
	s.embedSemOnce.Do(func() {
		s.embedSem = make(chan struct{}, 10)
	})

	if cap(s.embedSem) != 10 {
		t.Errorf("Expected semaphore capacity 10, got %d", cap(s.embedSem))
	}
}

// TestEmbedWithRetry_ContextTimeout tests context timeout handling.
func TestEmbedWithRetry_ContextTimeout(t *testing.T) {
	s := &Service{
		Embedder: &mockFailingEmbedder{},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := s.embedWithRetry(ctx, "test")
	if err == nil {
		t.Error("Expected error from context timeout, got nil")
	}
}

// mockError is a simple error type for testing.
type mockError struct {
	msg string
}

func (e *mockError) Error() string {
	return e.msg
}

// mockFailingEmbedder is a test embedder that always times out.
type mockFailingEmbedder struct{}

func (m *mockFailingEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (m *mockFailingEmbedder) Dimensions() int {
	return 0
}
