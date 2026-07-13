package codex

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
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

	nested := upstreamErrorFields{
		Code:            payload.Code,
		ResetsInSeconds: payload.ResetsInSeconds,
		RetryAfter:      payload.RetryAfter,
		ResetsAt:        payload.ResetsAt,
	}
	if payload.Error != nil {
		if nested.Code == "" {
			nested.Code = payload.Error.Code
		}
		if nested.ResetsInSeconds == nil {
			nested.ResetsInSeconds = payload.Error.ResetsInSeconds
		}
		if nested.RetryAfter == nil {
			nested.RetryAfter = payload.Error.RetryAfter
		}
		if nested.ResetsAt == nil {
			nested.ResetsAt = payload.Error.ResetsAt
		}
	}

	code := strings.TrimSpace(nested.Code)
	if retryAfter, ok := parsePositiveInt64(nested.ResetsInSeconds); ok {
		return code, int(retryAfter)
	}
	if retryAfter, ok := parsePositiveInt64(nested.RetryAfter); ok {
		return code, int(retryAfter)
	}
	if resetAt, ok := parsePositiveInt64(nested.ResetsAt); ok {
		return code, max(int(time.Until(time.Unix(resetAt, 0)).Seconds()), 0)
	}
	return code, 0
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

func parsePositiveInt64(raw json.RawMessage) (int64, bool) {
	var parsed flexibleInt64
	if err := parsed.UnmarshalJSON(raw); err != nil {
		return 0, false
	}
	value, ok := parsed.Int64()
	return value, ok && value > 0
}
