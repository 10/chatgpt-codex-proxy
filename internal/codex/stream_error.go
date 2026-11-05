package codex

import (
	"fmt"
	"net/http"
	"strings"

	"chatgpt-codex-proxy/internal/jsonutil"
	"chatgpt-codex-proxy/internal/turn"
)

func StreamEventError(event *turn.StreamEvent) error {
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

func extractUpstreamEventDetails(event *turn.StreamEvent) *UpstreamError {
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
		if parsed, ok := jsonutil.IntValue(value); ok {
			statusCode = parsed
			break
		}
	}
	if statusCode == 0 {
		statusCode = upstreamStatusCodeFromCode(code)
	}

	return &UpstreamError{
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
	for _, value := range values {
		if value == nil {
			continue
		}
		if seconds := retryAfterSeconds(value["resets_in_seconds"], value["retry_after"], value["resets_at"]); seconds > 0 {
			return seconds
		}
	}
	return 0
}
