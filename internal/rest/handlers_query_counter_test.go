package rest_test

import (
	"context"
	"sync/atomic"
	"time"

	gormlogger "gorm.io/gorm/logger"
)

type queryCounterLogger struct {
	gormlogger.Interface
	count atomic.Int64
}

func newQueryCounterLogger() *queryCounterLogger {
	return &queryCounterLogger{Interface: gormlogger.Discard}
}

func (l *queryCounterLogger) LogMode(level gormlogger.LogLevel) gormlogger.Interface {
	l.Interface = l.Interface.LogMode(level)
	return l
}

func (l *queryCounterLogger) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	l.count.Add(1)
	l.Interface.Trace(ctx, begin, fc, err)
}

func (l *queryCounterLogger) Reset() {
	l.count.Store(0)
}

func (l *queryCounterLogger) Count() int {
	return int(l.count.Load())
}
