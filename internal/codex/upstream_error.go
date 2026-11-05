package codex

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"chatgpt-codex-proxy/internal/jsonutil"
)

type UpstreamError struct {
	Op         string
	StatusCode int
	Body       string
	Code       string
	RetryAfter int
}

func (e *UpstreamError) Error() string {
	if e == nil {
		return ""
	}
	op := strings.TrimSpace(e.Op)
	if op == "" {
		op = "upstream request"
	}
	if body := strings.TrimSpace(e.Body); body != "" {
		return fmt.Sprintf("%s failed (%d): %s", op, e.StatusCode, body)
	}
	if statusText := strings.TrimSpace(http.StatusText(e.StatusCode)); statusText != "" {
		return fmt.Sprintf("%s failed (%d): %s", op, e.StatusCode, statusText)
	}
	return fmt.Sprintf("%s failed (%d)", op, e.StatusCode)
}

func (e *UpstreamError) Message() string {
	if e == nil {
		return ""
	}
	if body := strings.TrimSpace(e.Body); body != "" {
		return body
	}
	if statusText := strings.TrimSpace(http.StatusText(e.StatusCode)); statusText != "" {
		return statusText
	}
	return "upstream request failed"
}

func NewUpstreamError(op string, statusCode int, body string, headers http.Header) *UpstreamError {
	err := &UpstreamError{
		Op:         strings.TrimSpace(op),
		StatusCode: statusCode,
		Body:       strings.TrimSpace(body),
	}
	err.Code, err.RetryAfter = parseUpstreamErrorBody(err.Body)
	err.RetryAfter = max(err.RetryAfter, parseRetryAfterHeaders(headers))
	return err
}

func parseUpstreamErrorBody(body string) (string, int) {
	if strings.TrimSpace(body) == "" {
		return "", 0
	}

	var payload upstreamErrorEnvelope
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		return "", 0
	}

	nested := upstreamErrorFields{}
	if payload.Error != nil {
		nested = *payload.Error
	}
	nested.Code = cmp.Or(payload.Code, nested.Code)
	if len(payload.ResetsInSeconds) > 0 {
		nested.ResetsInSeconds = payload.ResetsInSeconds
	}
	if len(payload.RetryAfter) > 0 {
		nested.RetryAfter = payload.RetryAfter
	}
	if len(payload.ResetsAt) > 0 {
		nested.ResetsAt = payload.ResetsAt
	}

	return strings.TrimSpace(nested.Code), retryAfterSeconds(nested.ResetsInSeconds, nested.RetryAfter, nested.ResetsAt)
}

func retryAfterSeconds(resetsInSeconds, retryAfter, resetsAt any) int {
	for _, value := range []any{resetsInSeconds, retryAfter} {
		if seconds, ok := parsePositiveInt64(value); ok {
			return int(seconds)
		}
	}
	if resetAt, ok := parsePositiveInt64(resetsAt); ok {
		return max(int(time.Until(time.Unix(resetAt, 0)).Seconds()), 0)
	}
	return 0
}

func parseRetryAfterHeaders(headers http.Header) int {
	raw := strings.TrimSpace(headers.Get("Retry-After"))
	if raw == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(raw); err == nil && seconds > 0 {
		return seconds
	}
	if when, err := http.ParseTime(raw); err == nil {
		seconds := int(time.Until(when).Seconds())
		if seconds > 0 {
			return seconds
		}
	}
	return 0
}

type upstreamErrorEnvelope struct {
	Error           *upstreamErrorFields `json:"error"`
	Code            string               `json:"code"`
	ResetsInSeconds json.RawMessage      `json:"resets_in_seconds"`
	RetryAfter      json.RawMessage      `json:"retry_after"`
	ResetsAt        json.RawMessage      `json:"resets_at"`
}

type upstreamErrorFields struct {
	Code            string          `json:"code"`
	ResetsInSeconds json.RawMessage `json:"resets_in_seconds"`
	RetryAfter      json.RawMessage `json:"retry_after"`
	ResetsAt        json.RawMessage `json:"resets_at"`
}

func parsePositiveInt64(value any) (int64, bool) {
	raw, err := json.Marshal(value)
	if err != nil {
		return 0, false
	}
	var parsed jsonutil.FlexibleInt64
	if err := parsed.UnmarshalJSON(raw); err != nil {
		return 0, false
	}
	number, ok := parsed.Int64()
	return number, ok && number > 0
}

func IsInvalidReasoningSignatureError(err error) bool {
	var upstreamErr *UpstreamError
	if !errors.As(err, &upstreamErr) {
		return false
	}
	code := strings.ToLower(strings.TrimSpace(upstreamErr.Code))
	message := strings.ToLower(strings.TrimSpace(upstreamErr.Message()))
	return code == "thinking_signature_invalid" ||
		strings.Contains(code, "invalid_encrypted_content") ||
		strings.Contains(message, "invalid signature in thinking block") ||
		strings.Contains(message, "thinking_signature_invalid") ||
		strings.Contains(message, "invalid_encrypted_content")
}
