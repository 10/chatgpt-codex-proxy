package codex

import "chatgpt-codex-proxy/internal/turn"

func WSCreatePayload(r turn.NormalizedRequest) map[string]any {
	payload := map[string]any{
		"type":         "response.create",
		"model":        r.Model,
		"input":        r.Input,
		"instructions": r.Instructions,
	}
	if len(r.Tools) > 0 {
		payload["tools"] = r.Tools
	}
	if len(r.ToolChoice) > 0 {
		payload["tool_choice"] = r.ToolChoice
	}
	if r.Text != nil {
		payload["text"] = r.Text
	}
	if r.Reasoning != nil {
		payload["reasoning"] = r.Reasoning
	}
	if r.ServiceTier != "" {
		payload["service_tier"] = r.ServiceTier
	}
	if r.PreviousResponseID != "" {
		payload["previous_response_id"] = r.PreviousResponseID
	}
	if r.PromptCacheKey != "" {
		payload["prompt_cache_key"] = r.PromptCacheKey
	}
	if len(r.Include) > 0 {
		payload["include"] = append([]string(nil), r.Include...)
	}
	if r.ParallelToolCalls != nil {
		payload["parallel_tool_calls"] = *r.ParallelToolCalls
	}
	if r.Generate != nil {
		payload["generate"] = *r.Generate
	}
	return payload
}
