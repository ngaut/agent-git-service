package logging

import (
	"context"
	"log/slog"
)

type contextHandler struct {
	next slog.Handler
}

func (h *contextHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h *contextHandler) Handle(ctx context.Context, record slog.Record) error {
	attrs := SnapshotAttrs(ctx)
	if len(attrs) == 0 {
		return h.next.Handle(ctx, record)
	}

	enriched := slog.NewRecord(record.Time, record.Level, record.Message, record.PC)
	enriched.AddAttrs(attrs...)
	record.Attrs(func(attr slog.Attr) bool {
		enriched.AddAttrs(attr)
		return true
	})
	return h.next.Handle(ctx, enriched)
}

func (h *contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &contextHandler{next: h.next.WithAttrs(attrs)}
}

func (h *contextHandler) WithGroup(name string) slog.Handler {
	return &contextHandler{next: h.next.WithGroup(name)}
}
