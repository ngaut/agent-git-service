package db

import (
	"context"
	"log/slog"
	"sync"
	"testing"
)

type logEntry struct {
	level   slog.Level
	message string
	attrs   map[string]any
}

type logSink struct {
	mu      sync.Mutex
	entries []logEntry
}

func (s *logSink) Entries() []logEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries := make([]logEntry, len(s.entries))
	copy(entries, s.entries)
	return entries
}

type recordingHandler struct {
	sink  *logSink
	attrs []slog.Attr
}

func (h *recordingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return true
}

func (h *recordingHandler) Handle(ctx context.Context, record slog.Record) error {
	entry := logEntry{
		level:   record.Level,
		message: record.Message,
		attrs:   make(map[string]any),
	}
	for _, attr := range h.attrs {
		entry.attrs[attr.Key] = attr.Value.Any()
	}
	record.Attrs(func(a slog.Attr) bool {
		entry.attrs[a.Key] = a.Value.Any()
		return true
	})
	h.sink.mu.Lock()
	h.sink.entries = append(h.sink.entries, entry)
	h.sink.mu.Unlock()
	return nil
}

func (h *recordingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := &recordingHandler{
		sink:  h.sink,
		attrs: append([]slog.Attr{}, h.attrs...),
	}
	next.attrs = append(next.attrs, attrs...)
	return next
}

func (h *recordingHandler) WithGroup(name string) slog.Handler {
	return h
}

func captureLogs(t *testing.T) *logSink {
	t.Helper()
	sink := &logSink{}
	handler := &recordingHandler{sink: sink}
	logger := slog.New(handler)
	prev := slog.Default()
	slog.SetDefault(logger)
	t.Cleanup(func() {
		slog.SetDefault(prev)
	})
	return sink
}
