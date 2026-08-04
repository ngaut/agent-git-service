package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ngaut/agent-git-service/internal/httputil"
)

// OpenAI implements Embedder using an OpenAI-compatible embeddings API.
// It works with OpenAI, Azure OpenAI, Ollama, vLLM, or any server that
// implements the POST /v1/embeddings contract.
type OpenAI struct {
	apiKey  string
	baseURL string
	model   string
	dims    atomic.Int64
	client  *http.Client
}

// OpenAIOption configures an OpenAI embedder.
type OpenAIOption func(*OpenAI)

// WithBaseURL overrides the default base URL ("https://api.openai.com").
func WithBaseURL(url string) OpenAIOption {
	return func(o *OpenAI) { o.baseURL = url }
}

// WithModel overrides the default model ("text-embedding-3-small").
func WithModel(model string) OpenAIOption {
	return func(o *OpenAI) { o.model = model }
}

// WithDimensions overrides the embedding dimensions.
// Set to 0 to auto-detect from the first successful response.
func WithDimensions(dims int) OpenAIOption {
	return func(o *OpenAI) {
		if dims < 0 {
			dims = 0
		}
		o.dims.Store(int64(dims))
	}
}

// NewOpenAI creates an OpenAI-compatible embedder.
func NewOpenAI(apiKey string, opts ...OpenAIOption) *OpenAI {
	o := &OpenAI{
		apiKey:  apiKey,
		baseURL: "https://api.openai.com",
		model:   "text-embedding-3-small",
		client:  &http.Client{Timeout: 15 * time.Second},
	}
	for _, fn := range opts {
		fn(o)
	}
	return o
}

// embeddingRequest is the JSON body sent to the embeddings API.
type embeddingRequest struct {
	Input string `json:"input"`
	Model string `json:"model"`
}

// embeddingResponse is the expected JSON response from the embeddings API.
type embeddingResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// Embed sends text to the embeddings API and returns the resulting vector.
func (o *OpenAI) Embed(ctx context.Context, text string) ([]float32, error) {
	body, err := json.Marshal(embeddingRequest{Input: text, Model: o.model})
	if err != nil {
		return nil, fmt.Errorf("embedding: marshal request: %w", err)
	}

	endpoint, err := embeddingEndpoint(o.baseURL)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("embedding: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+o.apiKey)

	resp, err := o.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embedding: http call: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, newAPIError(resp.StatusCode, httputil.ErrorBody(resp.Body, 4096))
	}

	// Cap success-path read to 2MB. A typical embedding response is tens of KB.
	// This guards against a misconfigured proxy returning a massive body with status 200.
	const maxResponseSize = 2 * 1024 * 1024
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return nil, fmt.Errorf("embedding: read response: %w", err)
	}

	var result embeddingResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("embedding: unmarshal response: %w", err)
	}

	if result.Error != nil {
		return nil, fmt.Errorf("embedding: API error: %s", result.Error.Message)
	}

	if len(result.Data) == 0 || len(result.Data[0].Embedding) == 0 {
		return nil, fmt.Errorf("embedding: empty embedding in response")
	}

	// Critical: Validate dimensionality before returning. TiDB will reject mismatched vectors
	// with a fatal error. This protects against provider model switches or misconfiguration.
	respDims := len(result.Data[0].Embedding)
	expected := o.dims.Load()
	if expected == 0 {
		if o.dims.CompareAndSwap(0, int64(respDims)) {
			expected = int64(respDims)
		} else {
			expected = o.dims.Load()
		}
	}
	if expected != int64(respDims) {
		return nil, fmt.Errorf("embedding: model dimension mismatch: got %d, want %d", respDims, expected)
	}

	return result.Data[0].Embedding, nil
}

// Dimensions returns the vector dimensionality.
func (o *OpenAI) Dimensions() int { return int(o.dims.Load()) }

// APIKey returns the configured API key (for reuse by other services).
func (o *OpenAI) APIKey() string { return o.apiKey }

// BaseURL returns the configured base URL (for reuse by other services).
func (o *OpenAI) BaseURL() string { return o.baseURL }

func embeddingEndpoint(baseURL string) (string, error) {
	trimmed := strings.TrimSpace(baseURL)
	if trimmed == "" {
		return "", fmt.Errorf("embedding: base URL is empty")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("embedding: invalid base URL %q: %w", baseURL, err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("embedding: invalid base URL %q: missing scheme or host", baseURL)
	}

	path := strings.TrimRight(parsed.Path, "/")
	switch {
	case strings.HasSuffix(path, "/v1/embeddings"):
		parsed.Path = path
	case strings.HasSuffix(path, "/v1"):
		parsed.Path = path + "/embeddings"
	case path == "":
		parsed.Path = "/v1/embeddings"
	default:
		parsed.Path = path + "/v1/embeddings"
	}

	return parsed.String(), nil
}
