package codex

import (
	"cmp"
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

type flexibleInt64 struct {
	value int64
	set   bool
}

func (f *flexibleInt64) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		return nil
	}
	var number json.Number
	if err := json.Unmarshal(data, &number); err == nil {
		value, err := number.Int64()
		if err != nil {
			floatValue, floatErr := number.Float64()
			if floatErr != nil {
				return fmt.Errorf("parse numeric value %q: %w", number.String(), err)
			}
			value = int64(floatValue)
		}
		f.value = value
		f.set = true
		return nil
	}
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		trimmed := strings.TrimSpace(text)
		if trimmed == "" {
			return nil
		}
		value, err := strconv.ParseInt(trimmed, 10, 64)
		if err == nil {
			f.value = value
			f.set = true
			return nil
		}
		floatValue, floatErr := strconv.ParseFloat(trimmed, 64)
		if floatErr != nil {
			return fmt.Errorf("parse numeric value %q: %w", text, err)
		}
		f.value = int64(floatValue)
		f.set = true
		return nil
	}
	return fmt.Errorf("unsupported numeric value %q", string(data))
}

func (f flexibleInt64) Int64() (int64, bool) {
	return f.value, f.set
}

func parsePositiveInt64(raw json.RawMessage) (int64, bool) {
	var parsed flexibleInt64
	if err := parsed.UnmarshalJSON(raw); err != nil {
		return 0, false
	}
	value, ok := parsed.Int64()
	return value, ok && value > 0
}
