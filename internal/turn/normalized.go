package turn

import "slices"

type NormalizedRequest struct {
	Request
	ModelExplicit   bool
	Generate        *bool
	WebSocketAppend bool
	TupleSchema     map[string]any
	ResponseSchema  map[string]any
	ToolNameAliases map[string]string
}

type NormalizedCompactRequest struct {
	CompactRequest
	ModelExplicit      bool
	PreviousResponseID string
	TupleSchema        map[string]any
	ToolNameAliases    map[string]string
}

// Codex rejects replayed reasoning encrypted_content that was not produced by
// the account/session now serving the request ("invalid_encrypted_content" /
// "thinking_signature_invalid"). Dropping those items lets the request be
// retried statelessly, mirroring how the Anthropic path drops thinking blocks.
func (r NormalizedRequest) StripReasoningEncryptedContent() (NormalizedRequest, bool) {
	filtered := slices.DeleteFunc(slices.Clone(r.Input), func(item InputItem) bool {
		return item.Type == "reasoning" && item.EncryptedContent != ""
	})
	if len(filtered) == len(r.Input) {
		return r, false
	}
	r.Input = filtered
	return r, true
}
