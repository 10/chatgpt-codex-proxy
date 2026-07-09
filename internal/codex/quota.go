package codex

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"chatgpt-codex-proxy/internal/accounts"
	"chatgpt-codex-proxy/internal/jsonutil"
)

func ParseQuotaFromHeaders(headers http.Header) *accounts.QuotaSnapshot {
	primary := parseRateWindow(headers, "x-codex-primary")
	secondary := parseRateWindow(headers, "x-codex-secondary")
	credits := parseCredits(headers, "x-codex")
	if primary == nil && secondary == nil {
		return nil
	}
	snapshot := &accounts.QuotaSnapshot{
		PlanType:  "unknown",
		Source:    "response_headers",
		FetchedAt: time.Now().UTC(),
		RateLimit: accounts.RateLimitWindow{
			Allowed:      true,
			LimitReached: primary != nil && primary.UsedPercent != nil && *primary.UsedPercent >= 100,
		},
	}
	if primary != nil {
		snapshot.RateLimit = *primary
	}
	if secondary != nil {
		snapshot.SecondaryRateLimit = secondary
	}
	if credits != nil {
		snapshot.Credits = credits
	}
	return snapshot
}

func QuotaFromUsageResponse(payload UsageResponse) *accounts.QuotaSnapshot {
	snapshot := &accounts.QuotaSnapshot{
		PlanType:  payload.PlanType,
		Source:    "usage_endpoint",
		FetchedAt: time.Now().UTC(),
		RateLimit: usageWindowRateLimit(payload.RateLimit.PrimaryWindow, payload.RateLimit.Allowed, payload.RateLimit.LimitReached),
	}
	if payload.RateLimit.SecondaryWindow != nil {
		window := usageWindowRateLimit(payload.RateLimit.SecondaryWindow, true, payload.RateLimit.SecondaryWindow.UsedPercent >= 100)
		snapshot.SecondaryRateLimit = &window
	}
	if payload.CodeReviewRateLimit != nil {
		window := usageWindowRateLimit(payload.CodeReviewRateLimit.PrimaryWindow, payload.CodeReviewRateLimit.Allowed, payload.CodeReviewRateLimit.LimitReached)
		snapshot.CodeReviewRateLimit = &window
	}
	if payload.Credits != nil {
		snapshot.Credits = parseCreditsFromUsage(payload.Credits)
	}
	return snapshot
}

func ParseQuotaFromEvent(event *StreamEvent, planType string) *accounts.QuotaSnapshot {
	if event == nil || event.Type != "codex.rate_limits" {
		return nil
	}
	rateLimits := jsonutil.MapValue(event.Raw, "rate_limits")
	if rateLimits == nil {
		return nil
	}

	primary := parseEventRateWindow(jsonutil.MapValue(rateLimits, "primary"))
	secondary := parseEventRateWindow(jsonutil.MapValue(rateLimits, "secondary"))
	codeReview := parseEventRateWindow(jsonutil.FirstMapValue(rateLimits, "code_review", "code_review_rate_limit"))
	if primary == nil && secondary == nil && codeReview == nil {
		return nil
	}

	snapshot := &accounts.QuotaSnapshot{
		PlanType:  jsonutil.FirstNonEmpty(planType, "unknown"),
		Source:    "response_event",
		FetchedAt: time.Now().UTC(),
		RateLimit: accounts.RateLimitWindow{
			Allowed: true,
		},
	}
	if primary != nil {
		snapshot.RateLimit = *primary
	}
	if secondary != nil {
		snapshot.SecondaryRateLimit = secondary
	}
	if codeReview != nil {
		snapshot.CodeReviewRateLimit = codeReview
	}
	return snapshot
}

func parseRateWindow(headers http.Header, prefix string) *accounts.RateLimitWindow {
	pctRaw := headers.Get(prefix + "-used-percent")
	if pctRaw == "" {
		return nil
	}
	pct, err := strconv.ParseFloat(pctRaw, 64)
	if err != nil {
		return nil
	}
	window := &accounts.RateLimitWindow{
		Allowed:      true,
		LimitReached: pct >= 100,
		UsedPercent:  &pct,
	}
	if resetRaw := headers.Get(prefix + "-reset-at"); resetRaw != "" {
		if seconds, err := strconv.ParseInt(resetRaw, 10, 64); err == nil {
			ts := time.Unix(seconds, 0).UTC()
			window.ResetAt = &ts
		}
	}
	if windowRaw := headers.Get(prefix + "-window-minutes"); windowRaw != "" {
		if minutes, err := strconv.Atoi(windowRaw); err == nil {
			seconds := minutes * 60
			window.LimitWindowSeconds = &seconds
		}
	}
	return window
}

func parseEventRateWindow(raw map[string]any) *accounts.RateLimitWindow {
	if raw == nil {
		return nil
	}
	usedPercent, ok := eventFloat(raw["used_percent"])
	if !ok {
		return nil
	}
	window := &accounts.RateLimitWindow{
		Allowed:      true,
		LimitReached: usedPercent >= 100,
		UsedPercent:  &usedPercent,
	}
	if resetValue, ok := eventInt64(raw["reset_at"]); ok {
		resetAt := time.Unix(resetValue, 0).UTC()
		window.ResetAt = &resetAt
	}
	if minutes, ok := eventInt(raw["window_minutes"]); ok {
		seconds := minutes * 60
		window.LimitWindowSeconds = &seconds
	} else if limitSeconds, ok := eventInt(raw["limit_window_seconds"]); ok {
		seconds := limitSeconds
		window.LimitWindowSeconds = &seconds
	}
	return window
}

func parseCredits(headers http.Header, prefix string) *accounts.CreditsSnapshot {
	hasAny := false
	credits := &accounts.CreditsSnapshot{}
	if value, ok := parseBoolHeader(headers.Get(prefix + "-credits-has-credits")); ok {
		credits.HasCredits = value
		hasAny = true
	}
	if value, ok := parseBoolHeader(headers.Get(prefix + "-credits-unlimited")); ok {
		credits.Unlimited = value
		hasAny = true
	}
	if value, ok := parseFloatHeader(headers.Get(prefix + "-credits-balance")); ok {
		credits.Balance = &value
		hasAny = true
	}
	if value := headers.Get(prefix + "-active-limit"); value != "" {
		credits.ActiveLimit = value
		hasAny = true
	}
	if !hasAny {
		return nil
	}
	return credits
}

func parseCreditsFromUsage(value *UsageResponseCredits) *accounts.CreditsSnapshot {
	if value == nil {
		return nil
	}
	hasAny := false
	credits := &accounts.CreditsSnapshot{}
	if value.HasCredits != nil {
		credits.HasCredits = *value.HasCredits
		hasAny = true
	}
	if value.Unlimited != nil {
		credits.Unlimited = *value.Unlimited
		hasAny = true
	}
	if value.Balance != nil {
		balance := *value.Balance
		credits.Balance = &balance
		hasAny = true
	}
	if value.ActiveLimit != nil && strings.TrimSpace(*value.ActiveLimit) != "" {
		credits.ActiveLimit = strings.TrimSpace(*value.ActiveLimit)
		hasAny = true
	}
	if !hasAny {
		return nil
	}
	return credits
}

func parseBoolHeader(raw string) (bool, bool) {
	if raw == "" {
		return false, false
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, false
	}
	return value, true
}

func parseFloatHeader(raw string) (float64, bool) {
	if raw == "" {
		return 0, false
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, false
	}
	return value, true
}

func usageWindowRateLimit(window *UsageWindow, allowed, limitReached bool) accounts.RateLimitWindow {
	out := accounts.RateLimitWindow{
		Allowed:      allowed,
		LimitReached: limitReached,
	}
	if window == nil {
		return out
	}
	usedPercent := window.UsedPercent
	resetAt := time.Unix(window.ResetAt, 0).UTC()
	limitWindowSeconds := window.LimitWindowSeconds
	out.UsedPercent = &usedPercent
	out.ResetAt = &resetAt
	out.LimitWindowSeconds = &limitWindowSeconds
	return out
}

func eventFloat(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	default:
		return 0, false
	}
}

func eventInt(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	case json.Number:
		parsed, err := typed.Int64()
		return int(parsed), err == nil
	default:
		return 0, false
	}
}

func eventInt64(value any) (int64, bool) {
	switch typed := value.(type) {
	case int:
		return int64(typed), true
	case int64:
		return typed, true
	case float64:
		return int64(typed), true
	case json.Number:
		parsed, err := typed.Int64()
		return parsed, err == nil
	default:
		return 0, false
	}
}
