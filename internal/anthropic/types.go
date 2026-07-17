package anthropic

import (
	"bytes"
	"encoding/json"
)

const Version = "2023-06-01"

type MessagesRequest struct {
	Model             string          `json:"model"`
	MaxTokens         *int            `json:"max_tokens"`
	Messages          []Message       `json:"messages"`
	System            Content         `json:"system,omitempty"`
	Stream            bool            `json:"stream,omitempty"`
	StopSequences     []string        `json:"stop_sequences,omitempty"`
	Temperature       *float64        `json:"temperature,omitempty"`
	TopP              *float64        `json:"top_p,omitempty"`
	TopK              *int            `json:"top_k,omitempty"`
	Tools             []Tool          `json:"tools,omitempty"`
	ToolChoice        *ToolChoice     `json:"tool_choice,omitempty"`
	Thinking          *Thinking       `json:"thinking,omitempty"`
	ContextManagement json.RawMessage `json:"context_management,omitempty"`
	OutputConfig      *OutputConfig   `json:"output_config,omitempty"`
	ServiceTier       string          `json:"service_tier,omitempty"`
	Metadata          json.RawMessage `json:"metadata,omitempty"`
}

type Message struct {
	Role    string  `json:"role"`
	Content Content `json:"content"`
}

type Content []Block

func (c *Content) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		*c = nil
		return nil
	}
	if trimmed[0] == '"' {
		var value string
		if err := json.Unmarshal(trimmed, &value); err != nil {
			return err
		}
		*c = Content{{Type: "text", Text: value}}
		return nil
	}
	var blocks []Block
	if err := json.Unmarshal(trimmed, &blocks); err != nil {
		return err
	}
	*c = blocks
	return nil
}

type Block struct {
	Type             string          `json:"type"`
	Text             string          `json:"text,omitempty"`
	Thinking         string          `json:"thinking,omitempty"`
	Signature        string          `json:"signature,omitempty"`
	Data             string          `json:"data,omitempty"`
	Source           *ImageSource    `json:"source,omitempty"`
	ID               string          `json:"id,omitempty"`
	Name             string          `json:"name,omitempty"`
	Input            json.RawMessage `json:"input,omitempty"`
	ToolUseID        string          `json:"tool_use_id,omitempty"`
	Content          Content         `json:"content,omitempty"`
	IsError          bool            `json:"is_error,omitempty"`
	Title            string          `json:"title,omitempty"`
	URL              string          `json:"url,omitempty"`
	EncryptedContent string          `json:"encrypted_content,omitempty"`
	PageAge          json.RawMessage `json:"page_age,omitempty"`
	Cache            json.RawMessage `json:"cache_control,omitempty"`
}

type ImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
	URL       string `json:"url,omitempty"`
}

type Tool struct {
	Type           string          `json:"type,omitempty"`
	Name           string          `json:"name,omitempty"`
	Description    string          `json:"description,omitempty"`
	InputSchema    map[string]any  `json:"input_schema,omitempty"`
	MaxUses        *int            `json:"max_uses,omitempty"`
	AllowedDomains []string        `json:"allowed_domains,omitempty"`
	BlockedDomains []string        `json:"blocked_domains,omitempty"`
	UserLocation   map[string]any  `json:"user_location,omitempty"`
	Cache          json.RawMessage `json:"cache_control,omitempty"`
}

type ToolChoice struct {
	Type                   string `json:"type"`
	Name                   string `json:"name,omitempty"`
	DisableParallelToolUse *bool  `json:"disable_parallel_tool_use,omitempty"`
}

type Thinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens,omitempty"`
	Display      string `json:"display,omitempty"`
}

type OutputConfig struct {
	Effort string        `json:"effort,omitempty"`
	Format *OutputFormat `json:"format,omitempty"`
}

type OutputFormat struct {
	Type   string         `json:"type"`
	Name   string         `json:"name,omitempty"`
	Schema map[string]any `json:"schema,omitempty"`
}
