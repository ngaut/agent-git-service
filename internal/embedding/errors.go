package embedding

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
)

// APIError preserves provider status and structured error fields so retry
// policy does not have to infer quota or authentication failures from text.
type APIError struct {
	StatusCode int
	Code       string
	Type       string
	Message    string
	Detail     string
}

func (e *APIError) Error() string {
	detail := strings.TrimSpace(e.Detail)
	if detail == "" {
		detail = strings.TrimSpace(e.Message)
	}
	if detail == "" {
		detail = "empty response body"
	}
	return fmt.Sprintf("embedding: API returned %d: %s", e.StatusCode, detail)
}

// Retryable distinguishes temporary throttling from permanent quota failures.
func (e *APIError) Retryable() bool {
	if e == nil {
		return false
	}
	classification := strings.ToLower(strings.Join([]string{e.Code, e.Type, e.Message, e.Detail}, " "))
	for _, marker := range []string{
		"insufficient_quota",
		"billing_hard_limit",
		"invalid_api_key",
		"invalid api key",
	} {
		if strings.Contains(classification, marker) {
			return false
		}
	}
	switch e.StatusCode {
	case 408, 409, 425, 429:
		return true
	default:
		return e.StatusCode >= 500
	}
}

// IsRetryableError classifies provider, transport, and compatibility errors.
func IsRetryableError(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Retryable()
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) && (netErr.Timeout() || netErr.Temporary()) {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}

	// Preserve compatibility with custom embedders that have not adopted
	// APIError yet. Permanent markers must be checked before status text.
	message := strings.ToLower(err.Error())
	for _, marker := range []string{"insufficient_quota", "billing_hard_limit", "invalid_api_key", "invalid api key"} {
		if strings.Contains(message, marker) {
			return false
		}
	}
	for _, marker := range []string{
		"429",
		"rate_concurrency_limit",
		"500",
		"502",
		"503",
		"504",
		"timeout",
		"deadline exceeded",
		"connection reset",
		"connection refused",
		"eof",
		"unexpected eof",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func newAPIError(statusCode int, detail string) *APIError {
	result := &APIError{StatusCode: statusCode, Detail: strings.TrimSpace(detail)}
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(detail), &envelope) == nil {
		result.Code = envelope.Error.Code
		result.Type = envelope.Error.Type
		result.Message = envelope.Error.Message
	}
	return result
}
