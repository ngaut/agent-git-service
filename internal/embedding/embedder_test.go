package embedding_test

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ngaut/agent-git-service/internal/embedding"
)

func TestNopEmbedder(t *testing.T) {
	var e embedding.NopEmbedder
	vec, err := e.Embed(context.Background(), "hello world")
	if err != nil {
		t.Fatalf("NopEmbedder.Embed returned error: %v", err)
	}
	if vec != nil {
		t.Fatalf("NopEmbedder.Embed returned non-nil: %v", vec)
	}
	if e.Dimensions() != 0 {
		t.Fatalf("NopEmbedder.Dimensions() = %d, want 0", e.Dimensions())
	}
}

func TestIsNop(t *testing.T) {
	if !embedding.IsNop(embedding.NopEmbedder{}) {
		t.Fatal("IsNop(NopEmbedder{}) = false, want true")
	}

	fake := embedding.NewOpenAI("key")
	if embedding.IsNop(fake) {
		t.Fatal("IsNop(OpenAI{}) = true, want false")
	}
}

func TestFormatVector(t *testing.T) {
	tests := []struct {
		in   []float32
		want string
	}{
		{nil, ""},
		{[]float32{}, ""},
		{[]float32{0.1, 0.2, 0.3}, "[0.1,0.2,0.3]"},
		{[]float32{1, -2.5, 0}, "[1,-2.5,0]"},
		// NaN/Inf sanitization tests (issue #236)
		{[]float32{0.1, float32(math.NaN()), 0.3}, "[0.1,0,0.3]"},
		{[]float32{float32(math.Inf(1)), 0.2}, "[0,0.2]"},
		{[]float32{float32(math.Inf(-1))}, "[0]"},
		{[]float32{float32(math.NaN()), float32(math.NaN())}, "[0,0]"},
	}
	for _, tt := range tests {
		got := embedding.FormatVector(tt.in)
		if got != tt.want {
			t.Errorf("FormatVector(%v) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestOpenAIEmbed(t *testing.T) {
	expectedVec := []float32{0.1, 0.2, 0.3}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/v1/embeddings" {
			t.Errorf("expected path /v1/embeddings, got %s", r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer test-key" {
			t.Errorf("expected Bearer test-key, got %s", auth)
		}

		resp := map[string]any{
			"data": []map[string]any{
				{"embedding": expectedVec},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	e := embedding.NewOpenAI("test-key",
		embedding.WithBaseURL(srv.URL),
		embedding.WithModel("test-model"),
		embedding.WithDimensions(3),
	)

	vec, err := e.Embed(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Embed returned error: %v", err)
	}
	if len(vec) != 3 {
		t.Fatalf("Embed returned %d dims, want 3", len(vec))
	}
	for i, v := range vec {
		if v != expectedVec[i] {
			t.Errorf("vec[%d] = %f, want %f", i, v, expectedVec[i])
		}
	}
	if e.Dimensions() != 3 {
		t.Errorf("Dimensions() = %d, want 3", e.Dimensions())
	}
}

func TestOpenAIEmbedFullEndpointURL(t *testing.T) {
	expectedVec := []float32{0.7, 0.8}

	newServer := func(t *testing.T, wantPath string) *httptest.Server {
		t.Helper()
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				t.Errorf("expected POST, got %s", r.Method)
			}
			if r.URL.Path != wantPath {
				t.Errorf("expected path %s, got %s", wantPath, r.URL.Path)
			}

			resp := map[string]any{
				"data": []map[string]any{
					{"embedding": expectedVec},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
		}))
	}

	t.Run("baseURL already includes embeddings path", func(t *testing.T) {
		srv := newServer(t, "/v1/embeddings")
		defer srv.Close()

		e := embedding.NewOpenAI("test-key",
			embedding.WithBaseURL(srv.URL+"/v1/embeddings"),
			embedding.WithModel("test-model"),
			embedding.WithDimensions(len(expectedVec)),
		)

		vec, err := e.Embed(context.Background(), "hello")
		if err != nil {
			t.Fatalf("Embed returned error: %v", err)
		}
		if len(vec) != len(expectedVec) {
			t.Fatalf("Embed returned %d dims, want %d", len(vec), len(expectedVec))
		}
		for i, v := range vec {
			if v != expectedVec[i] {
				t.Errorf("vec[%d] = %f, want %f", i, v, expectedVec[i])
			}
		}
	})

	t.Run("baseURL includes embeddings path with trailing slash", func(t *testing.T) {
		srv := newServer(t, "/v1/embeddings")
		defer srv.Close()

		e := embedding.NewOpenAI("test-key",
			embedding.WithBaseURL(srv.URL+"/v1/embeddings/"),
			embedding.WithModel("test-model"),
			embedding.WithDimensions(len(expectedVec)),
		)

		vec, err := e.Embed(context.Background(), "hello")
		if err != nil {
			t.Fatalf("Embed returned error: %v", err)
		}
		if len(vec) != len(expectedVec) {
			t.Fatalf("Embed returned %d dims, want %d", len(vec), len(expectedVec))
		}
		for i, v := range vec {
			if v != expectedVec[i] {
				t.Errorf("vec[%d] = %f, want %f", i, v, expectedVec[i])
			}
		}
	})

	t.Run("baseURL needs embeddings path appended", func(t *testing.T) {
		srv := newServer(t, "/v1/embeddings")
		defer srv.Close()

		e := embedding.NewOpenAI("test-key",
			embedding.WithBaseURL(srv.URL),
			embedding.WithModel("test-model"),
			embedding.WithDimensions(len(expectedVec)),
		)

		vec, err := e.Embed(context.Background(), "hello")
		if err != nil {
			t.Fatalf("Embed returned error: %v", err)
		}
		if len(vec) != len(expectedVec) {
			t.Fatalf("Embed returned %d dims, want %d", len(vec), len(expectedVec))
		}
		for i, v := range vec {
			if v != expectedVec[i] {
				t.Errorf("vec[%d] = %f, want %f", i, v, expectedVec[i])
			}
		}
	})

	t.Run("baseURL ends with v1", func(t *testing.T) {
		srv := newServer(t, "/v1/embeddings")
		defer srv.Close()

		e := embedding.NewOpenAI("test-key",
			embedding.WithBaseURL(srv.URL+"/v1"),
			embedding.WithModel("test-model"),
			embedding.WithDimensions(len(expectedVec)),
		)

		vec, err := e.Embed(context.Background(), "hello")
		if err != nil {
			t.Fatalf("Embed returned error: %v", err)
		}
		if len(vec) != len(expectedVec) {
			t.Fatalf("Embed returned %d dims, want %d", len(vec), len(expectedVec))
		}
		for i, v := range vec {
			if v != expectedVec[i] {
				t.Errorf("vec[%d] = %f, want %f", i, v, expectedVec[i])
			}
		}
	})
}

func TestOpenAIEmbedAutoDetectDimensions(t *testing.T) {
	expectedVec := []float32{0.5, 0.6}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		resp := map[string]any{
			"data": []map[string]any{
				{"embedding": expectedVec},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	e := embedding.NewOpenAI("test-key",
		embedding.WithBaseURL(srv.URL),
		embedding.WithModel("test-model"),
	)

	if dim := e.Dimensions(); dim != 0 {
		t.Fatalf("Dimensions() = %d, want 0 before first Embed", dim)
	}

	vec, err := e.Embed(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Embed returned error: %v", err)
	}
	if len(vec) != len(expectedVec) {
		t.Fatalf("Embed returned %d dims, want %d", len(vec), len(expectedVec))
	}
	if dim := e.Dimensions(); dim != len(expectedVec) {
		t.Fatalf("Dimensions() = %d, want %d after Embed", dim, len(expectedVec))
	}
}

func TestOpenAIEmbedAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
	}))
	defer srv.Close()

	e := embedding.NewOpenAI("bad-key", embedding.WithBaseURL(srv.URL))
	_, err := e.Embed(context.Background(), "hello")
	if err == nil {
		t.Fatal("expected error for 401 response, got nil")
	}
	var apiErr *embedding.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error type = %T, want *embedding.APIError", err)
	}
	if apiErr.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", apiErr.StatusCode, http.StatusUnauthorized)
	}
	if embedding.IsRetryableError(err) {
		t.Fatalf("401 error should not be retryable: %v", err)
	}
}

func TestOpenAIEmbedInsufficientQuotaIsPermanent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"message":"quota exhausted","type":"insufficient_quota","code":"insufficient_quota"}}`))
	}))
	defer srv.Close()

	e := embedding.NewOpenAI("exhausted-key", embedding.WithBaseURL(srv.URL))
	_, err := e.Embed(context.Background(), "hello")
	if err == nil {
		t.Fatal("expected error for exhausted quota, got nil")
	}
	var apiErr *embedding.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error type = %T, want *embedding.APIError", err)
	}
	if apiErr.Code != "insufficient_quota" || apiErr.Type != "insufficient_quota" {
		t.Fatalf("structured quota fields = code:%q type:%q", apiErr.Code, apiErr.Type)
	}
	if embedding.IsRetryableError(err) {
		t.Fatalf("insufficient quota should not be retryable: %v", err)
	}
}

func TestAPIErrorRetryability(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "temporary rate limit",
			err:  &embedding.APIError{StatusCode: http.StatusTooManyRequests, Code: "rate_limit_exceeded"},
			want: true,
		},
		{
			name: "insufficient quota",
			err:  &embedding.APIError{StatusCode: http.StatusTooManyRequests, Code: "insufficient_quota", Type: "insufficient_quota"},
			want: false,
		},
		{
			name: "server error",
			err:  &embedding.APIError{StatusCode: http.StatusServiceUnavailable},
			want: true,
		},
		{
			name: "invalid key",
			err:  &embedding.APIError{StatusCode: http.StatusUnauthorized, Code: "invalid_api_key"},
			want: false,
		},
		{
			name: "connection refused",
			err:  &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := embedding.IsRetryableError(tt.err); got != tt.want {
				t.Fatalf("IsRetryableError() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestOpenAIEmbedEmptyResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
	}))
	defer srv.Close()

	e := embedding.NewOpenAI("key", embedding.WithBaseURL(srv.URL))
	_, err := e.Embed(context.Background(), "hello")
	if err == nil {
		t.Fatal("expected error for empty embedding, got nil")
	}
}
