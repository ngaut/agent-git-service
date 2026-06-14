package server

import (
	"log/slog"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
	code := m.Run()
	slog.SetDefault(prev)
	os.Exit(code)
}
