package logging

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
)

type requestStateKey struct{}

type requestState struct {
	mu    sync.RWMutex
	attrs map[string]slog.Attr
}

type envConfig struct {
	addSource bool
	format    string
	level     slog.Level
}

// Init configures the process-wide structured logger and bridges the standard
// library logger onto the same handler so all operational logs share one sink.
func Init() *slog.Logger {
	cfg := configFromEnv()
	handler := newHandler(cfg, os.Stdout)
	logger := slog.New(handler)
	slog.SetDefault(logger)

	stdlib := slog.NewLogLogger(handler, slog.LevelInfo)
	log.SetFlags(0)
	log.SetOutput(stdlib.Writer())
	return logger
}

func configFromEnv() envConfig {
	environment := strings.ToLower(strings.TrimSpace(os.Getenv("ENVIRONMENT")))
	format := strings.ToLower(strings.TrimSpace(os.Getenv("LOG_FORMAT")))
	if format == "" {
		if environment == "production" {
			format = "json"
		} else {
			format = "text"
		}
	}
	if format != "json" {
		format = "text"
	}

	level := slog.LevelInfo
	switch strings.ToLower(strings.TrimSpace(os.Getenv("LOG_LEVEL"))) {
	case "debug":
		level = slog.LevelDebug
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}

	addSource := environment != "production"
	if raw := strings.ToLower(strings.TrimSpace(os.Getenv("LOG_ADD_SOURCE"))); raw != "" {
		addSource = raw == "1" || raw == "true" || raw == "yes"
	}

	return envConfig{
		addSource: addSource,
		format:    format,
		level:     level,
	}
}

func newHandler(cfg envConfig, w io.Writer) slog.Handler {
	opts := &slog.HandlerOptions{
		AddSource: cfg.addSource,
		Level:     cfg.level,
	}
	var base slog.Handler
	if cfg.format == "json" {
		base = slog.NewJSONHandler(w, opts)
	} else {
		base = slog.NewTextHandler(w, opts)
	}
	return &contextHandler{next: base}
}

// WithRequestContext returns a derived context that carries mutable structured
// attributes for the lifetime of a request or background task.
func WithRequestContext(ctx context.Context, attrs ...slog.Attr) context.Context {
	state := &requestState{attrs: make(map[string]slog.Attr, len(attrs))}
	ctx = context.WithValue(ctx, requestStateKey{}, state)
	AddAttrs(ctx, attrs...)
	return ctx
}

// AddAttrs appends or replaces structured attributes on the request-scoped log state.
func AddAttrs(ctx context.Context, attrs ...slog.Attr) {
	state := requestStateFromContext(ctx)
	if state == nil {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	for _, attr := range attrs {
		if attr.Key == "" {
			continue
		}
		state.attrs[attr.Key] = attr
	}
}

// CloneContext copies structured request attributes from src onto base.
// Useful for background goroutines that should keep request correlation fields
// while switching onto a server-lifecycle context.
func CloneContext(base context.Context, src context.Context) context.Context {
	attrs := SnapshotAttrs(src)
	if len(attrs) == 0 {
		return base
	}
	return WithRequestContext(base, attrs...)
}

// SnapshotAttrs returns a stable snapshot of the current structured attributes.
func SnapshotAttrs(ctx context.Context) []slog.Attr {
	state := requestStateFromContext(ctx)
	if state == nil {
		return nil
	}

	state.mu.RLock()
	defer state.mu.RUnlock()

	keys := make([]string, 0, len(state.attrs))
	for key := range state.attrs {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	attrs := make([]slog.Attr, 0, len(keys))
	for _, key := range keys {
		attrs = append(attrs, state.attrs[key])
	}
	return attrs
}

// TokenFingerprint returns a short, irreversible fingerprint for secrets such
// as tokens so repeated failures can be correlated without leaking the secret.
func TokenFingerprint(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:6])
}

func requestStateFromContext(ctx context.Context) *requestState {
	state, _ := ctx.Value(requestStateKey{}).(*requestState)
	return state
}
