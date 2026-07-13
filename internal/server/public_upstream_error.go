package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"chatgpt-codex-proxy/internal/codex"
	"chatgpt-codex-proxy/internal/jsonutil"
)

func upstreamEventError(event *codex.StreamEvent) error {
	if event == nil {
		return nil
	}
	if event.Type != "error" && event.Type != "response.failed" {
		return nil
	}
	details := extractUpstreamEventDetails(event)
	if details == nil {
		return fmt.Errorf("upstream %s", event.Type)
	}
	return details
}

func extractUpstreamEventDetails(event *codex.StreamEvent) *codex.UpstreamError {
	if event == nil || event.Raw == nil {
		return nil
	}

	nested := jsonutil.MapValue(event.Raw, "error")
	if nested == nil {
		nested = jsonutil.MapValue(jsonutil.MapValue(event.Raw, "response"), "error")
	}
	message := jsonutil.FirstNonEmpty(
		jsonutil.StringValue(nested["message"]),
		jsonutil.StringValue(nested["detail"]),
		jsonutil.StringValue(event.Raw["message"]),
		jsonutil.StringValue(event.Raw["detail"]),
	)
	if message == "" {
		message = fmt.Sprintf("upstream %s", event.Type)
	}

	code := jsonutil.FirstNonEmpty(
		jsonutil.StringValue(nested["code"]),
		jsonutil.StringValue(nested["type"]),
		jsonutil.StringValue(event.Raw["code"]),
		jsonutil.StringValue(event.Raw["type"]),
	)
	statusCode := 0
	for _, value := range []any{nested["status_code"], nested["status"], event.Raw["status_code"], event.Raw["status"]} {
		if parsed, ok := serverIntValue(value); ok {
			statusCode = parsed
			break
		}
	}
	if statusCode == 0 {
		statusCode = upstreamStatusCodeFromCode(code)
	}

	return &codex.UpstreamError{
		Op:         "codex stream",
		StatusCode: statusCode,
		Body:       message,
		Code:       code,
		RetryAfter: firstRetryAfterSeconds(nested, event.Raw),
	}
}

func upstreamStatusCodeFromCode(code string) int {
	switch normalized := strings.ToLower(strings.TrimSpace(code)); normalized {
	case "rate_limited", "rate_limit_exceeded", "too_many_requests":
		return http.StatusTooManyRequests
	case "quota_exhausted", "usage_limit_reached", "payment_required", "subscription_required":
		return http.StatusPaymentRequired
	case "invalid_api_key", "unauthorized", "authentication_error", "invalid_token":
		return http.StatusUnauthorized
	default:
		switch {
		case strings.Contains(normalized, "rate_limit"), strings.Contains(normalized, "too_many"):
			return http.StatusTooManyRequests
		case strings.Contains(normalized, "quota"), strings.Contains(normalized, "usage_limit"), strings.Contains(normalized, "payment"):
			return http.StatusPaymentRequired
		case strings.Contains(normalized, "unauthorized"), strings.Contains(normalized, "auth"):
			return http.StatusUnauthorized
		default:
			return 0
		}
	}
}

func firstRetryAfterSeconds(values ...map[string]any) int {
	now := time.Now().UTC()
	for _, value := range values {
		if value == nil {
			continue
		}
		if seconds, ok := serverIntValue(value["resets_in_seconds"]); ok && seconds > 0 {
			return seconds
		}
		if resetAt, ok := serverIntValue(value["resets_at"]); ok && resetAt > 0 {
			diff := resetAt - int(now.Unix())
			if diff > 0 {
				return diff
			}
		}
	}
	return 0
}

func serverIntValue(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	case json.Number:
		parsed, err := typed.Int64()
		if err == nil {
			return int(parsed), true
		}
		floatValue, floatErr := typed.Float64()
		if floatErr == nil {
			return int(floatValue), true
		}
		return 0, false
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		return parsed, err == nil
	default:
		return 0, false
	}
}
