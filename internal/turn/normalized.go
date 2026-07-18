package turn

type NormalizedRequest struct {
	Request
	ModelExplicit   bool
	Generate        *bool
	WebSocketAppend bool
	TupleSchema     map[string]any
	ResponseSchema  map[string]any
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
	CompactRequest
	ModelExplicit      bool
	PreviousResponseID string
	TupleSchema        map[string]any
	ToolNameAliases    map[string]string
}

// StripReasoningEncryptedContent returns a copy of the request with every
// reasoning input item that carries encrypted reasoning state removed, plus a
// flag reporting whether anything was dropped.
//
// Codex rejects replayed reasoning encrypted_content that was not produced by
// the account/session now serving the request ("invalid_encrypted_content" /
// "thinking_signature_invalid"). Dropping those items lets the request be
// retried statelessly, mirroring how the Anthropic path drops thinking blocks.
func (r NormalizedRequest) StripReasoningEncryptedContent() (NormalizedRequest, bool) {
	changed := false
	filtered := make([]InputItem, 0, len(r.Input))
	for _, item := range r.Input {
		if item.Type == "reasoning" && item.EncryptedContent != "" {
			changed = true
			continue
		}
		filtered = append(filtered, item)
	}
	if !changed {
		return r, false
	}
	r.Input = filtered
	return r, true
}
