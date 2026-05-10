// Mock embedding server for E2E vector search tests.
// Returns deterministic vectors based on input text hash.
// Usage: go run ./e2e/cmd/mock-embedding-server :8888
package main

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	port := ":8888"
	if len(os.Args) > 1 {
		port = os.Args[1]
	}

	http.HandleFunc("/v1/embeddings", handleEmbeddings)
	fmt.Printf("Mock embedding server listening on %s\n", port)
	if err := http.ListenAndServe(port, nil); err != nil {
		fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
		os.Exit(1)
	}
}

func handleEmbeddings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Input string `json:"input"`
		Model string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	switch {
	case strings.Contains(req.Input, "__e2e_rate_limit__"):
		http.Error(w, "rate limited", http.StatusTooManyRequests)
		return
	case strings.Contains(req.Input, "__e2e_outage__"):
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
		return
	case strings.Contains(req.Input, "__e2e_timeout__"):
		// Sleep long enough to trigger the embedder's HTTP client timeout.
		time.Sleep(20 * time.Second)
	}

	// Generate deterministic vector based on input hash
	vector := generateVector(req.Input, 1536)

	resp := map[string]any{
		"object": "list",
		"data": []map[string]any{
			{
				"object":    "embedding",
				"index":     0,
				"embedding": vector,
			},
		},
		"usage": map[string]int{
			"prompt_tokens": len(req.Input) / 4,
			"total_tokens":  len(req.Input) / 4,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// generateVector creates a deterministic 1536-dim vector based on input text.
// Similar inputs produce similar vectors for semantic search testing.
func generateVector(input string, dims int) []float32 {
	h := fnv.New32a()
	h.Write([]byte(input))
	hash := h.Sum32()

	vector := make([]float32, dims)
	for i := 0; i < dims; i++ {
		// Use hash + position to generate deterministic but varied values
		seed := hash + uint32(i)
		// Normalize to [-1, 1] range
		value := float32(int32(seed)%1000-500) / 500.0
		vector[i] = value
	}

	// Normalize vector to unit length
	var norm float32
	for _, v := range vector {
		norm += v * v
	}
	norm = float32(math.Sqrt(float64(norm)))
	for i := range vector {
		vector[i] /= norm
	}

	return vector
}
