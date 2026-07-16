package generation

import (
	"encoding/json"

	"chatgpt-codex-proxy/internal/conversation"
)

// Reasoning describes backend reasoning controls independently of any public
// compatibility protocol.
type Reasoning struct {
	Effort  string `json:"effort,omitempty"`
	Summary string `json:"summary,omitempty"`
}

// TextFormat is the backend structured-output format.
type TextFormat struct {
	Type   string         `json:"type"`
	Name   string         `json:"name,omitempty"`
	Schema map[string]any `json:"schema,omitempty"`
	Strict bool           `json:"strict,omitempty"`
}

// Tool is the canonical tool definition accepted by the Codex backend.
type Tool struct {
	Type              string                     `json:"type"`
	Function          *FunctionTool              `json:"function,omitempty"`
	Name              string                     `json:"name,omitempty"`
	Description       string                     `json:"description,omitempty"`
	Parameters        map[string]any             `json:"parameters,omitempty"`
	Format            map[string]any             `json:"format,omitempty"`
	Strict            bool                       `json:"strict,omitempty"`
	SearchContextSize string                     `json:"search_context_size,omitempty"`
	UserLocation      map[string]any             `json:"user_location,omitempty"`
	ExtraFields       map[string]json.RawMessage `json:"-"`
}

func (t *Tool) UnmarshalJSON(data []byte) error {
	type alias Tool
	var decoded alias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	delete(raw, "type")
	delete(raw, "function")
	delete(raw, "name")
	delete(raw, "description")
	delete(raw, "parameters")
	delete(raw, "format")
	delete(raw, "strict")
	delete(raw, "search_context_size")
	delete(raw, "user_location")

	decoded.ExtraFields = raw
	*t = Tool(decoded)
	return nil
}

func (t Tool) MarshalJSON() ([]byte, error) {
	type alias Tool
	base, err := json.Marshal(alias(t))
	if err != nil {
		return nil, err
	}
	if len(t.ExtraFields) == 0 {
		return base, nil
	}

	var payload map[string]json.RawMessage
	if err := json.Unmarshal(base, &payload); err != nil {
		return nil, err
	}
	for key, value := range t.ExtraFields {
		if _, exists := payload[key]; exists {
			continue
		}
		payload[key] = append(json.RawMessage(nil), value...)
	}
	return json.Marshal(payload)
}

type FunctionTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
	Strict      bool           `json:"strict,omitempty"`
}

type ReasoningPart = conversation.ReasoningPart
