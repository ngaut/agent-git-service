package logging

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// NewGormLogger returns a structured GORM logger that routes SQL diagnostics
// through slog and inherits request context fields when available.
func NewGormLogger(cfg gormlogger.Config) gormlogger.Interface {
	if cfg.SlowThreshold == 0 {
		cfg.SlowThreshold = 200 * time.Millisecond
	}
	return &gormSlogLogger{cfg: cfg}
}

type gormSlogLogger struct {
	cfg gormlogger.Config
}

func (l *gormSlogLogger) Config() gormlogger.Config {
	return l.cfg
}

func (l *gormSlogLogger) LogMode(level gormlogger.LogLevel) gormlogger.Interface {
	clone := *l
	clone.cfg.LogLevel = level
	return &clone
}

func (l *gormSlogLogger) Info(ctx context.Context, msg string, data ...interface{}) {
	if l.cfg.LogLevel < gormlogger.Info {
		return
	}
	slog.InfoContext(ctx, "gorm info",
		"component", "gorm",
		"message", fmt.Sprintf(msg, data...),
	)
}

func (l *gormSlogLogger) Warn(ctx context.Context, msg string, data ...interface{}) {
	if l.cfg.LogLevel < gormlogger.Warn {
		return
	}
	slog.WarnContext(ctx, "gorm warning",
		"component", "gorm",
		"message", fmt.Sprintf(msg, data...),
	)
}

func (l *gormSlogLogger) Error(ctx context.Context, msg string, data ...interface{}) {
	if l.cfg.LogLevel < gormlogger.Error {
		return
	}
	slog.ErrorContext(ctx, "gorm error",
		"component", "gorm",
		"message", fmt.Sprintf(msg, data...),
	)
}

func (l *gormSlogLogger) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	if l.cfg.LogLevel == gormlogger.Silent {
		return
	}

	elapsed := time.Since(begin)
	switch {
	case err != nil && l.cfg.LogLevel >= gormlogger.Error && (!errors.Is(err, gorm.ErrRecordNotFound) || !l.cfg.IgnoreRecordNotFoundError):
		sql, rows := fc()
		slog.ErrorContext(ctx, "gorm query failed",
			"component", "gorm",
			"elapsed_ms", elapsed.Milliseconds(),
			"rows_affected", normalizeRows(rows),
			"sql", sanitizeSQL(sql),
			"error", err,
		)
	case l.cfg.SlowThreshold > 0 && elapsed > l.cfg.SlowThreshold && l.cfg.LogLevel >= gormlogger.Warn:
		sql, rows := fc()
		slog.WarnContext(ctx, "gorm slow query",
			"component", "gorm",
			"elapsed_ms", elapsed.Milliseconds(),
			"slow_threshold_ms", l.cfg.SlowThreshold.Milliseconds(),
			"rows_affected", normalizeRows(rows),
			"sql", sanitizeSQL(sql),
		)
	case l.cfg.LogLevel >= gormlogger.Info:
		sql, rows := fc()
		slog.InfoContext(ctx, "gorm query",
			"component", "gorm",
			"elapsed_ms", elapsed.Milliseconds(),
			"rows_affected", normalizeRows(rows),
			"sql", sanitizeSQL(sql),
		)
	}
}

func normalizeRows(rows int64) any {
	if rows < 0 {
		return nil
	}
	return rows
}

func sanitizeSQL(sql string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(sql)), " ")
}
