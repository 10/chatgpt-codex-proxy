package codex

import "chatgpt-codex-proxy/internal/turn"

type Reasoning = turn.Reasoning
type TextFormat = turn.TextFormat
type Tool = turn.ToolDefinition
type Request = turn.Request
type CompactRequest = turn.CompactRequest
type TextConfig = turn.TextConfig
type InputItem = turn.InputItem
type ContentPart = turn.ContentPart
type StreamEvent = turn.StreamEvent

type CompactResponse struct {
	ID        string           `json:"id,omitempty"`
	Object    string           `json:"object,omitempty"`
	CreatedAt int64            `json:"created_at,omitempty"`
	Output    []map[string]any `json:"output,omitempty"`
	Usage     map[string]any   `json:"usage,omitempty"`
}
