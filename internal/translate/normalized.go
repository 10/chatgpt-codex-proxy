package translate

import "chatgpt-codex-proxy/internal/codex"

type NormalizedRequest struct {
	codex.Request
	ModelExplicit   bool
	Generate        *bool
	WebSocketAppend bool
	TupleSchema     map[string]any
	ToolNameAliases map[string]string
}

func (r NormalizedRequest) ToCodexWSCreatePayload() map[string]any {
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

type NormalizedCompactRequest struct {
	codex.CompactRequest
	ModelExplicit      bool
	PreviousResponseID string
	TupleSchema        map[string]any
	ToolNameAliases    map[string]string
}
