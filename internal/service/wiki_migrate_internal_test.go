package service

// Whitebox tests for migration internals that don't have a natural
// public entry point. Lives in package service (not _test) so it can
// call unexported helpers directly.

import (
	"strings"
	"testing"
	"time"
)

func TestParseCommitTime_AcceptsValidRFC3339(t *testing.T) {
	got, err := parseCommitTime("2026-05-17T12:34:56+00:00", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := time.Date(2026, 5, 17, 12, 34, 56, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestParseCommitTime_FallsBackToSecondaryFormat(t *testing.T) {
	got, err := parseCommitTime("garbage", "2026-05-17T12:34:56Z")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := time.Date(2026, 5, 17, 12, 34, 56, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestParseCommitTime_ErrorsOnAllUnparseable(t *testing.T) {
	_, err := parseCommitTime("not-a-date", "also-not")
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "unparseable") {
		t.Fatalf("error %q should mention 'unparseable'", err.Error())
	}
}

func TestParseCommitTime_ErrorsOnEmpty(t *testing.T) {
	_, err := parseCommitTime("", "")
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "no commit timestamp") {
		t.Fatalf("error %q should mention missing timestamp", err.Error())
	}
}
