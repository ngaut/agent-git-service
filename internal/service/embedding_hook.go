package service

import (
	"context"
	"database/sql"
	"log/slog"
	"math/rand"
	"os"
	"strconv"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"
	"github.com/ngaut/agent-git-service/internal/embedding"
	applog "github.com/ngaut/agent-git-service/internal/logging"

	"gorm.io/gorm"
)

// embeddingUpdateSQL maps an allowed target table to its parameterized UPDATE
// statement. Confining the table name to compile-time entries forecloses any
// caller from injecting an arbitrary identifier via fmt.Sprintf.
var embeddingUpdateSQL = map[string]string{
	"issues":        "UPDATE `issues` SET `embedding` = ? WHERE `id` = ?",
	"pull_requests": "UPDATE `pull_requests` SET `embedding` = ? WHERE `id` = ?",
}

// embedAndStore computes an embedding for the given text and stores it
// in the specified table's embedding column via raw SQL UPDATE.
//
// This runs in a background goroutine so that write latency is not impacted.
// If the embedder is a NopEmbedder, or if the embedding API call fails after retries,
// the column is left NULL and search falls back to lexical-only matching.
func (s *Service) embedAndStore(ctx context.Context, table string, id uint, text string) {
	if s.Embedder == nil || embedding.IsNop(s.Embedder) {
		return
	}
	sqlStmt, ok := embeddingUpdateSQL[table]
	if !ok {
		slog.ErrorContext(ctx, "embedding table not in allowlist", "table", table, "id", id)
		return
	}

	s.Wg.Add(1)
	go func() {
		defer s.Wg.Done()

		// Lazy-init the semaphore on first use (thread-safe via sync.Once).
		s.embedSemOnce.Do(func() {
			// Make concurrency configurable via EMBEDDING_CONCURRENCY env var
			concurrency := 50
			if env := os.Getenv("EMBEDDING_CONCURRENCY"); env != "" {
				if n, err := strconv.Atoi(env); err == nil && n > 0 {
					concurrency = n
				}
			}
			s.embedSem = make(chan struct{}, concurrency)
		})
		// Acquire concurrency slot
		s.embedSem <- struct{}{}
		defer func() { <-s.embedSem }()

		// Propagate any scoped DB so background writes target the correct handle.
		bgCtx := s.ServerCtx()
		bgCtx = applog.CloneContext(bgCtx, ctx)
		if scopedDB, ok := DBFromContext(ctx); ok {
			bgCtx = ContextWithDB(bgCtx, scopedDB)
		}
		applog.AddAttrs(bgCtx,
			slog.String("embedding_table", table),
			slog.Uint64("embedding_id", uint64(id)),
		)

		ctx, cancel := context.WithTimeout(bgCtx, 60*time.Second)
		defer cancel()

		// Retry with exponential backoff for transient errors
		vec, err := s.embedWithRetry(ctx, text)
		if err != nil {
			slog.WarnContext(ctx, "embedding failed after retries", "table", table, "id", id, "error", err)
			return
		}
		if len(vec) == 0 {
			return
		}

		// If the context timed out exactly as Embed returned, don't attempt the DB write
		// using the dead context.
		if ctx.Err() != nil {
			slog.WarnContext(ctx, "embedding context expired after embed", "table", table, "id", id, "error", ctx.Err())
			return
		}

		// Ensure vector column exists on the target DB.
		targetDB := s.DB
		if scopedDB, ok := DBFromContext(bgCtx); ok {
			targetDB = scopedDB
		}
		s.ensureVectorInit(targetDB, len(vec))

		vecStr := embedding.FormatVector(vec)

		// Use a fresh timeout for the DB so it doesn't fail if the embed call used most of the deadline
		dbCtx, dbCancel := context.WithTimeout(bgCtx, 5*time.Second)
		defer dbCancel()

		if err := targetDB.WithContext(dbCtx).Exec(sqlStmt, vecStr, id).Error; err != nil {
			slog.WarnContext(dbCtx, "embedding store failed", "table", table, "id", id, "error", err)
		}
	}()
}

// embedWithRetry sends text to the embeddings API with exponential backoff for transient errors.
// Returns the embedding vector or an error if all retries are exhausted.
func (s *Service) embedWithRetry(ctx context.Context, text string) ([]float32, error) {
	const maxRetries = 3
	var lastErr error
	text = embedding.TruncateInput(text)

	for attempt := 0; attempt < maxRetries; attempt++ {
		vec, err := s.Embedder.Embed(ctx, text)
		if err == nil {
			return vec, nil
		}
		lastErr = err

		// Only retry transient errors (429, 5xx, network timeouts)
		if !embedding.IsRetryableError(err) {
			return nil, err
		}
		if attempt == maxRetries-1 {
			break
		}

		// Exponential backoff with jitter between attempts.
		backoff := time.Duration(1<<attempt) * time.Second
		jitter := time.Duration(rand.Int63n(int64(backoff)))
		backoff += jitter

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff):
			// Continue to next retry
		}
	}

	return nil, lastErr
}

// isTransientError reports whether an error is transient and should be retried.
// Transient errors include: HTTP 429 (rate limit), 5xx server errors, network timeouts.
func isTransientError(err error) bool {
	return embedding.IsRetryableError(err)
}

// ensureVectorInit ensures the embedding column exists on the target DB.
// Uses per-DB tracking to avoid redundant init calls.
func (s *Service) ensureVectorInit(targetDB *gorm.DB, dims int) {
	if targetDB == nil {
		return
	}
	sqlDB, err := s.sqlDBHandle(targetDB)
	if err != nil {
		db.InitVector(targetDB, dims)
		s.refreshWikiSearchEmbeddingColumn(targetDB)
		return
	}

	s.vectorInitMu.Lock()
	defer s.vectorInitMu.Unlock()

	if s.vectorInitDBs == nil {
		s.vectorInitDBs = make(map[*sql.DB]bool)
	}
	if s.vectorInitDBs[sqlDB] {
		return // Already initialized for this DB
	}

	// InitVector is idempotent - safe to call multiple times
	db.InitVector(targetDB, dims)
	s.vectorInitDBs[sqlDB] = true
	s.refreshWikiSearchEmbeddingColumn(targetDB)
}

// EmbedIssue computes and stores an embedding for an issue's title + body.
func (s *Service) EmbedIssue(ctx context.Context, id uint, title, body string) {
	s.embedAndStore(ctx, "issues", id, title+"\n"+body)
}

// EmbedPR computes and stores an embedding for a PR's title + body.
func (s *Service) EmbedPR(ctx context.Context, id uint, title, body string) {
	s.embedAndStore(ctx, "pull_requests", id, title+"\n"+body)
}
