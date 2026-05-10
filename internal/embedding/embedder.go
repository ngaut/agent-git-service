// Package embedding provides a pluggable text-to-vector embedding interface.
//
// When an embedding API key is configured, the server generates embeddings
// for issue/PR text and stores them in TiDB VECTOR columns. When no API key
// is present, NopEmbedder is used and search falls back to SQL LIKE.
package embedding

import (
	"context"
	"math"
	"strconv"
	"strings"
)

// Embedder converts text into a dense float32 vector.
type Embedder interface {
	// Embed returns a dense vector representation of the input text.
	// Returns (nil, nil) if embedding is not available (NopEmbedder).
	Embed(ctx context.Context, text string) ([]float32, error)

	// Dimensions returns the dimensionality of the embedding vectors.
	// It may return 0 when dimensions are unknown (auto-detect) or embedding is disabled.
	Dimensions() int
}

// NopEmbedder is a no-op implementation used when no embedding provider is configured.
// It always returns (nil, nil) so callers fall back to LIKE-based search.
type NopEmbedder struct{}

func (NopEmbedder) Embed(context.Context, string) ([]float32, error) { return nil, nil }
func (NopEmbedder) Dimensions() int                                  { return 0 }

// IsNop reports whether the given Embedder is a NopEmbedder.
func IsNop(e Embedder) bool {
	_, ok := e.(NopEmbedder)
	return ok
}

// FormatVector formats a float32 slice as a TiDB VECTOR literal string.
// Example: [0.1, 0.2, 0.3] → "[0.1,0.2,0.3]"
// NaN and Inf values are replaced with 0 to prevent TiDB VECTOR parse errors.
func FormatVector(v []float32) string {
	if len(v) == 0 {
		return ""
	}
	var b strings.Builder
	// Preallocate: '[' + ']' + (len-1)*',' + approx 10 chars per float
	b.Grow(2 + (len(v) - 1) + len(v)*10)
	b.WriteByte('[')
	for i, f := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		// Guard against corrupt provider responses: NaN/Inf would produce
		// invalid VECTOR literals like "[NaN,0.2,...]", crashing TiDB.
		f64 := float64(f)
		if math.IsNaN(f64) || math.IsInf(f64, 0) {
			b.WriteByte('0')
		} else {
			b.WriteString(strconv.FormatFloat(f64, 'g', -1, 32))
		}
	}
	b.WriteByte(']')
	return b.String()
}
