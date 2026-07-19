package turn

import "encoding/json"

type ContentPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
	Detail   string `json:"detail,omitempty"`
	FileURL  string `json:"file_url,omitempty"`
	FileData string `json:"file_data,omitempty"`
	FileID   string `json:"file_id,omitempty"`
	Filename string `json:"filename,omitempty"`
}

type ReasoningPart struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type Reasoning struct {
	Effort  string `json:"effort,omitempty"`
	Summary string `json:"summary,omitempty"`
}

type TextFormat struct {
	Type   string         `json:"type"`
	Name   string         `json:"name,omitempty"`
	Schema map[string]any `json:"schema,omitempty"`
	Strict bool           `json:"strict,omitempty"`
}

type ToolDefinition struct {
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

func (t *ToolDefinition) UnmarshalJSON(data []byte) error {
	type alias ToolDefinition
	var decoded alias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	for _, key := range []string{"type", "function", "name", "description", "parameters", "format", "strict", "search_context_size", "user_location"} {
		delete(raw, key)
	}

	decoded.ExtraFields = raw
	*t = ToolDefinition(decoded)
	return nil
}

func (t ToolDefinition) MarshalJSON() ([]byte, error) {
	type alias ToolDefinition
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

type Usage struct {
	InputTokens     int64  `json:"input_tokens"`
	OutputTokens    int64  `json:"output_tokens"`
	CachedTokens    *int64 `json:"cached_tokens,omitempty"`
	ReasoningTokens *int64 `json:"reasoning_tokens,omitempty"`
}

type StreamEvent struct {
	Type string
	Raw  map[string]any
}

func (e *StreamEvent) IsTerminalResponse() bool {
	return e != nil && (e.Type == "response.completed" || e.Type == "response.incomplete")
}
