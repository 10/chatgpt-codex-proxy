package translate

import (
	"encoding/json"
	"strings"

	"chatgpt-codex-proxy/internal/codex"
	"chatgpt-codex-proxy/internal/jsonutil"
)

func (a *Accumulator) ChatUsageObject() map[string]any {
	return usageObject(
		a.Usage,
		"prompt_tokens",
		"completion_tokens",
		"prompt_tokens_details",
		"completion_tokens_details",
	)
}

func (a *Accumulator) ResponsesUsageObject() map[string]any {
	return usageObject(
		a.Usage,
		"input_tokens",
		"output_tokens",
		"input_tokens_details",
		"output_tokens_details",
	)
}

func usageObject(usage *codex.Usage, inputKey, outputKey, inputDetailsKey, outputDetailsKey string) map[string]any {
	if usage == nil {
		return nil
	}
	result := map[string]any{
		inputKey:       usage.InputTokens,
		outputKey:      usage.OutputTokens,
		"total_tokens": usage.InputTokens + usage.OutputTokens,
	}
	if usage.CachedTokens != nil {
		result[inputDetailsKey] = map[string]any{
			"cached_tokens": *usage.CachedTokens,
		}
	}
	if usage.ReasoningTokens != nil {
		result[outputDetailsKey] = map[string]any{
			"reasoning_tokens": *usage.ReasoningTokens,
		}
	}
	return result
}

func usageFromRaw(value any) *codex.Usage {
	if value == nil {
		return nil
	}
	switch mapped := value.(type) {
	case map[string]any:
		cachedTokens := optionalInt64Value(mapped["cached_tokens"])
		if details := jsonutil.MapValue(mapped, "input_tokens_details"); details != nil {
			cachedTokens = firstInt64Ptr(cachedTokens, optionalInt64Value(details["cached_tokens"]))
		}
		reasoningTokens := optionalInt64Value(mapped["reasoning_tokens"])
		if details := jsonutil.MapValue(mapped, "output_tokens_details"); details != nil {
			reasoningTokens = firstInt64Ptr(reasoningTokens, optionalInt64Value(details["reasoning_tokens"]))
		}
		return &codex.Usage{
			InputTokens:     int64(numberValue(mapped["input_tokens"])),
			OutputTokens:    int64(numberValue(mapped["output_tokens"])),
			CachedTokens:    cachedTokens,
			ReasoningTokens: reasoningTokens,
		}
	case codex.Usage:
		cloned := mapped
		return &cloned
	case *codex.Usage:
		return mapped
	default:
		return nil
	}
}

func numberValue(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case float32:
		return float64(typed)
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	case json.Number:
		number, _ := typed.Float64()
		return number
	default:
		return 0
	}
}

func intValue(value any) (int, bool) {
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
		number, err := typed.Int64()
		return int(number), err == nil
	default:
		return 0, false
	}
}

func optionalInt64Value(value any) *int64 {
	switch typed := value.(type) {
	case int:
		number := int64(typed)
		return &number
	case int32:
		number := int64(typed)
		return &number
	case int64:
		number := typed
		return &number
	case float64:
		number := int64(typed)
		return &number
	case json.Number:
		number, err := typed.Int64()
		if err == nil {
			return &number
		}
	case string:
		parsed, err := json.Number(strings.TrimSpace(typed)).Int64()
		if err == nil {
			return &parsed
		}
	}
	return nil
}

func firstInt64Ptr(values ...*int64) *int64 {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}
