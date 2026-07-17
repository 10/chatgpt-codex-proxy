package openai

import (
	"bytes"
	"encoding/json"

	"chatgpt-codex-proxy/internal/turn"
)

type ChatCompletionsRequest struct {
	Model              string                     `json:"model"`
	Messages           []ChatMessage              `json:"messages"`
	Stream             bool                       `json:"stream"`
	ReasoningEffort    string                     `json:"reasoning_effort,omitempty"`
	ServiceTier        string                     `json:"service_tier,omitempty"`
	PreviousResponseID string                     `json:"previous_response_id,omitempty"`
	PromptCacheKey     string                     `json:"prompt_cache_key,omitempty"`
	Tools              []ToolDefinition           `json:"tools,omitempty"`
	ToolChoice         json.RawMessage            `json:"tool_choice,omitempty"`
	ParallelToolCalls  *bool                      `json:"parallel_tool_calls,omitempty"`
	ResponseFormat     *ResponseFormat            `json:"response_format,omitempty"`
	Functions          []LegacyFunctionDefinition `json:"functions,omitempty"`
	FunctionCall       *LegacyFunctionCallChoice  `json:"function_call,omitempty"`
}

type ChatMessage struct {
	Role         string           `json:"role"`
	Content      MessageContent   `json:"content"`
	ToolCalls    []ToolCall       `json:"tool_calls,omitempty"`
	ToolCallID   string           `json:"tool_call_id,omitempty"`
	FunctionCall *FunctionPayload `json:"function_call,omitempty"`
}

type FunctionPayload struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type CustomToolPayload struct {
	Name  string `json:"name"`
	Input string `json:"input"`
}

type LegacyFunctionDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type LegacyFunctionCallChoice struct {
	Mode string
	Name string
}

func (l *LegacyFunctionCallChoice) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		return nil
	}
	if data[0] == '"' {
		return json.Unmarshal(data, &l.Mode)
	}
	var raw struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	l.Name = raw.Name
	return nil
}

type MessageContent []ContentPart

func (m *MessageContent) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*m = nil
		return nil
	}
	if len(data) > 0 && data[0] == '"' {
		var text string
		if err := json.Unmarshal(data, &text); err != nil {
			return err
		}
		*m = MessageContent{{Type: "text", Text: text}}
		return nil
	}
	var parts []ContentPart
	if err := json.Unmarshal(data, &parts); err != nil {
		return err
	}
	*m = parts
	return nil
}

type ContentPart struct {
	Type     string         `json:"type"`
	Text     string         `json:"text,omitempty"`
	ImageURL *ImageURLValue `json:"image_url,omitempty"`
	File     *FileValue     `json:"file,omitempty"`
	Detail   string         `json:"detail,omitempty"`
	FileURL  string         `json:"file_url,omitempty"`
	FileData string         `json:"file_data,omitempty"`
	FileID   string         `json:"file_id,omitempty"`
	Filename string         `json:"filename,omitempty"`
}

type ImageURLValue struct {
	URL    string `json:"url"`
	FileID string `json:"file_id,omitempty"`
	Detail string `json:"detail,omitempty"`
}

type FileValue struct {
	FileURL  string `json:"file_url,omitempty"`
	FileData string `json:"file_data,omitempty"`
	FileID   string `json:"file_id,omitempty"`
	Filename string `json:"filename,omitempty"`
}

func (i *ImageURLValue) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		return nil
	}
	if data[0] == '"' {
		return json.Unmarshal(data, &i.URL)
	}
	type imageURLAlias ImageURLValue
	var alias imageURLAlias
	if err := json.Unmarshal(data, &alias); err != nil {
		return err
	}
	*i = ImageURLValue(alias)
	return nil
}

type ToolDefinition = turn.ToolDefinition

type FunctionTool = turn.FunctionTool

type ToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"`
	Function FunctionPayload    `json:"function,omitempty"`
	Custom   *CustomToolPayload `json:"custom,omitempty"`
}

type ResponseFormat struct {
	Type       string          `json:"type"`
	JSONSchema *JSONSchemaSpec `json:"json_schema,omitempty"`
}

type JSONSchemaSpec struct {
	Name   string         `json:"name"`
	Schema map[string]any `json:"schema"`
	Strict bool           `json:"strict,omitempty"`
}

type ResponsesRequest struct {
	Model              string           `json:"model"`
	Input              ResponsesInput   `json:"input"`
	Instructions       string           `json:"instructions,omitempty"`
	Stream             bool             `json:"stream"`
	Tools              []ToolDefinition `json:"tools,omitempty"`
	ToolChoice         json.RawMessage  `json:"tool_choice,omitempty"`
	ParallelToolCalls  *bool            `json:"parallel_tool_calls,omitempty"`
	PreviousResponseID string           `json:"previous_response_id,omitempty"`
	PromptCacheKey     string           `json:"prompt_cache_key,omitempty"`
	ServiceTier        string           `json:"service_tier,omitempty"`
	Text               *ResponsesText   `json:"text,omitempty"`
	Reasoning          *Reasoning       `json:"reasoning,omitempty"`
}

type ResponsesCompactRequest struct {
	Model              string         `json:"model"`
	Input              ResponsesInput `json:"input"`
	Instructions       string         `json:"instructions,omitempty"`
	PreviousResponseID string         `json:"previous_response_id,omitempty"`
	Text               *ResponsesText `json:"text,omitempty"`
	Reasoning          *Reasoning     `json:"reasoning,omitempty"`
}

type Reasoning = turn.Reasoning

type ResponsesText struct {
	Format *ResponsesTextFormat `json:"format,omitempty"`
}

type ResponsesTextFormat = turn.TextFormat

type ResponsesInput struct {
	String string
	Items  []ResponsesInputItem
}

func (r *ResponsesInput) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		return nil
	}
	switch data[0] {
	case '"':
		return json.Unmarshal(data, &r.String)
	case '{':
		var item ResponsesInputItem
		if err := json.Unmarshal(data, &item); err != nil {
			return err
		}
		r.Items = []ResponsesInputItem{item}
		return nil
	}
	return json.Unmarshal(data, &r.Items)
}

type ResponsesInputItem struct {
	Type             string          `json:"type,omitempty"`
	Role             string          `json:"role,omitempty"`
	Phase            string          `json:"phase,omitempty"`
	Content          MessageContent  `json:"content,omitempty"`
	CallID           string          `json:"call_id,omitempty"`
	OutputText       string          `json:"-"`
	OutputContent    MessageContent  `json:"-"`
	Name             string          `json:"name,omitempty"`
	Input            string          `json:"input,omitempty"`
	Arguments        string          `json:"arguments,omitempty"`
	ID               string          `json:"id,omitempty"`
	Status           string          `json:"status,omitempty"`
	Summary          []ReasoningPart `json:"summary,omitempty"`
	EncryptedContent string          `json:"encrypted_content,omitempty"`
}

func (r *ResponsesInputItem) UnmarshalJSON(data []byte) error {
	type alias ResponsesInputItem
	var raw struct {
		alias
		Output json.RawMessage `json:"output,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*r = ResponsesInputItem(raw.alias)

	outputText, outputContent, err := decodeResponsesOutput(raw.Output)
	if err != nil {
		return err
	}
	r.OutputText = outputText
	r.OutputContent = outputContent
	return nil
}

func decodeResponsesOutput(raw json.RawMessage) (string, MessageContent, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return "", nil, nil
	}

	if trimmed[0] == '"' {
		var text string
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return "", nil, err
		}
		return text, nil, nil
	}

	if content, ok := parseResponsesOutputContent(trimmed); ok {
		return "", content, nil
	}

	var decoded any
	if err := json.Unmarshal(trimmed, &decoded); err != nil {
		return "", nil, err
	}
	normalized, err := json.Marshal(decoded)
	if err != nil {
		return "", nil, err
	}
	return string(normalized), nil, nil
}

func parseResponsesOutputContent(raw json.RawMessage) (MessageContent, bool) {
	var content MessageContent
	if err := json.Unmarshal(raw, &content); err == nil && len(content) > 0 {
		return content, true
	}

	var part ContentPart
	if err := json.Unmarshal(raw, &part); err == nil && isResponseOutputContentPart(part) {
		return MessageContent{part}, true
	}

	return nil, false
}

func isResponseOutputContentPart(part ContentPart) bool {
	return part.Type == "input_text" || part.Type == "output_text" || part.Type == "input_image" || part.Type == "input_file"
}

type ReasoningPart = turn.ReasoningPart
