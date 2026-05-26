package embedding

import (
	"sync"
	"unicode/utf8"

	tiktoken "github.com/pkoukk/tiktoken-go"
	tiktokenloader "github.com/pkoukk/tiktoken-go-loader"
)

const (
	// MaxInputTokens is the OpenAI embeddings per-input token ceiling for
	// current embedding models such as text-embedding-3-small.
	MaxInputTokens = 8192

	fallbackMaxInputBytes = 32000
)

var (
	cl100kOnce      sync.Once
	cl100kTokenizer *tiktoken.Tiktoken
	cl100kErr       error
)

func init() {
	tiktoken.SetBpeLoader(tiktokenloader.NewOfflineLoader())
}

// TruncateInput keeps embedding inputs inside the model token limit.
func TruncateInput(text string) string {
	return TruncateInputTokens(text, MaxInputTokens)
}

// TruncateInputTokens returns text truncated to at most maxTokens under the
// cl100k_base encoding used by OpenAI's third-generation embedding models.
func TruncateInputTokens(text string, maxTokens int) string {
	if text == "" || maxTokens <= 0 {
		return ""
	}
	// Bound tokenizer CPU/heap by restoring the historical byte cap before
	// tokenization. The token-aware pass still enforces the model ceiling
	// inside that bounded window.
	text = truncateUTF8Bytes(text, fallbackMaxInputBytes)
	enc, err := inputTokenizer()
	if err != nil {
		return truncateUTF8Bytes(text, fallbackMaxInputBytes)
	}
	tokens := enc.EncodeOrdinary(text)
	if len(tokens) <= maxTokens {
		return text
	}
	return enc.Decode(tokens[:maxTokens])
}

// CountInputTokens counts tokens using the same encoding as TruncateInput.
func CountInputTokens(text string) (int, error) {
	enc, err := inputTokenizer()
	if err != nil {
		return 0, err
	}
	return len(enc.EncodeOrdinary(text)), nil
}

func inputTokenizer() (*tiktoken.Tiktoken, error) {
	cl100kOnce.Do(func() {
		cl100kTokenizer, cl100kErr = tiktoken.GetEncoding(tiktoken.MODEL_CL100K_BASE)
	})
	return cl100kTokenizer, cl100kErr
}

func truncateUTF8Bytes(text string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(text) <= maxBytes {
		return text
	}
	truncated := text[:maxBytes]
	for len(truncated) > 0 && !utf8.ValidString(truncated) {
		truncated = truncated[:len(truncated)-1]
	}
	return truncated
}
