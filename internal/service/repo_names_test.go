package service

import (
	"regexp"
	"testing"
)

func TestGenerateRepoName(t *testing.T) {
	seen := make(map[string]bool)

	for i := 0; i < 100; i++ {
		name := GenerateRepoName()

		// Must match adjective-noun pattern (lowercase alpha, hyphen separated).
		if !regexp.MustCompile(`^[a-z]+-[a-z]+$`).MatchString(name) {
			t.Fatalf("GenerateRepoName returned invalid format: %q", name)
		}

		seen[name] = true
	}

	// With 48×49 = 2352 combinations, 100 draws should produce at least 80 unique names.
	if len(seen) < 80 {
		t.Fatalf("expected at least 80 unique names from 100 draws, got %d", len(seen))
	}
}

func TestRepoDisplayName(t *testing.T) {
	tests := []struct {
		locale string
		want   string
	}{
		{"zh-CN", "我的记忆空间"},
		{"zh-TW", "我的记忆空间"},
		{"zh", "我的记忆空间"},
		{"ja", "マイメモリースペース"},
		{"en", "My Memory Space"},
		{"en-US", "My Memory Space"},
		{"", "My Memory Space"},
		{"fr", "My Memory Space"},
	}
	for _, tt := range tests {
		got := RepoDisplayName(tt.locale)
		if got != tt.want {
			t.Errorf("RepoDisplayName(%q) = %q, want %q", tt.locale, got, tt.want)
		}
	}
}

func TestSequentialRepoName(t *testing.T) {
	if got := SequentialRepoName("amber-chronicle", 2); got != "amber-chronicle-2" {
		t.Fatalf("got %q, want %q", got, "amber-chronicle-2")
	}
	if got := SequentialRepoName("my-memory", 15); got != "my-memory-15" {
		t.Fatalf("got %q, want %q", got, "my-memory-15")
	}
}
