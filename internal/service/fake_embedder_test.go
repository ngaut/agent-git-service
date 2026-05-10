package service

import (
	"context"
	"testing"
)

// FakeEmbedder is a test embedder that returns deterministic vectors.
// Shared across all service tests to avoid duplicate declarations.
type FakeEmbedder struct {
	Vec      []float32
	Err      error
	Called   int
	LastText string
}

func (f *FakeEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	f.Called++
	f.LastText = text
	return f.Vec, f.Err
}

func (f *FakeEmbedder) Dimensions() int { return len(f.Vec) }

// TestFakeEmbedder_Smoke is a package-level smoke test that verifies
// the FakeEmbedder helper compiles and functions correctly.
// This ensures the shared test helper is properly available across all service tests.
func TestFakeEmbedder_Smoke(t *testing.T) {
	embedder := &FakeEmbedder{Vec: []float32{0.1, 0.2, 0.3}}

	// Test Embed method
	vec, err := embedder.Embed(context.Background(), "test text")
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}
	if len(vec) != 3 {
		t.Errorf("Expected vector length 3, got %d", len(vec))
	}
	if embedder.Called != 1 {
		t.Errorf("Expected Called=1, got %d", embedder.Called)
	}
	if embedder.LastText != "test text" {
		t.Errorf("Expected LastText='test text', got %q", embedder.LastText)
	}

	// Test Dimensions method
	if dim := embedder.Dimensions(); dim != 3 {
		t.Errorf("Expected Dimensions=3, got %d", dim)
	}
}
