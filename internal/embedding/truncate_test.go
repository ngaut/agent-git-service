package embedding_test

import (
	"strings"
	"testing"

	"gh-server/internal/embedding"
)

func TestTruncateInputTokens(t *testing.T) {
	longText := strings.Repeat(" token", embedding.MaxInputTokens+512)
	if tokens, err := embedding.CountInputTokens(longText); err != nil {
		t.Fatalf("CountInputTokens(longText): %v", err)
	} else if tokens <= embedding.MaxInputTokens {
		t.Fatalf("test fixture has %d tokens, want > %d", tokens, embedding.MaxInputTokens)
	}

	truncated := embedding.TruncateInput(longText)
	tokens, err := embedding.CountInputTokens(truncated)
	if err != nil {
		t.Fatalf("CountInputTokens(truncated): %v", err)
	}
	if tokens > embedding.MaxInputTokens {
		t.Fatalf("truncated tokens = %d, want <= %d", tokens, embedding.MaxInputTokens)
	}
	if len(truncated) >= len(longText) {
		t.Fatalf("expected truncated text to be shorter")
	}
	if !strings.HasPrefix(truncated, " token") {
		t.Fatalf("truncated text lost expected prefix: %q", truncated[:min(len(truncated), 16)])
	}
}

func TestTruncateInputTokensKeepsShortText(t *testing.T) {
	text := "short wiki page"
	if got := embedding.TruncateInput(text); got != text {
		t.Fatalf("TruncateInput(%q) = %q", text, got)
	}
}

func TestTruncateInputTokensCapsByteBudgetBeforeTokenization(t *testing.T) {
	text := strings.Repeat("a", 40000)

	truncated := embedding.TruncateInput(text)
	if len(truncated) > 32000 {
		t.Fatalf("truncated bytes = %d, want <= 32000", len(truncated))
	}
	if !strings.HasPrefix(truncated, strings.Repeat("a", min(len(truncated), 32))) {
		t.Fatalf("truncated text lost expected prefix")
	}
}
