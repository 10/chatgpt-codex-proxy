package anthropic

import (
	"encoding/json"
	"strings"
	"testing"

	"chatgpt-codex-proxy/internal/models"
)

func TestNormalizeMessagesTextToolsAndThinking(t *testing.T) {
	t.Parallel()

	request, err := DecodeMessages([]byte(`{
		"model":"gpt-5.4",
		"max_tokens":2048,
		"system":[{"type":"text","text":"Be precise."}],
		"messages":[
			{"role":"user","content":"What is the weather?"},
			{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"weather","input":{"city":"Paris"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"Sunny"}]}
		],
		"tools":[{"name":"weather","description":"Get weather","input_schema":{"type":"object","properties":{"city":{"type":"string"}}}}],
		"tool_choice":{"type":"auto","disable_parallel_tool_use":true},
		"thinking":{"type":"enabled","budget_tokens":1024},
		"stream":true
	}`))
	if err != nil {
		t.Fatalf("DecodeMessages() error = %v", err)
	}

	normalized, err := Normalize(request, models.NewCatalog(models.BootstrapEntries()))
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if normalized.Model != "gpt-5.4" || normalized.Instructions != "Be precise." || !normalized.Stream {
		t.Fatalf("normalized metadata = %#v", normalized)
	}
	if normalized.Reasoning == nil || normalized.Reasoning.Effort != "medium" || normalized.Reasoning.Summary != "auto" {
		t.Fatalf("reasoning = %#v", normalized.Reasoning)
	}
	if normalized.ParallelToolCalls == nil || *normalized.ParallelToolCalls {
		t.Fatalf("parallel_tool_calls = %#v, want false", normalized.ParallelToolCalls)
	}
	if len(normalized.Tools) != 1 || normalized.Tools[0].Name != "weather" {
		t.Fatalf("tools = %#v", normalized.Tools)
	}
	if got := normalized.Tools[0].Parameters["additionalProperties"]; got != false {
		t.Fatalf("additionalProperties = %#v, want false", got)
	}
	if len(normalized.Input) != 3 {
		t.Fatalf("input len = %d, want 3: %#v", len(normalized.Input), normalized.Input)
	}
	if normalized.Input[1].Type != "function_call" || normalized.Input[1].Arguments != `{"city":"Paris"}` {
		t.Fatalf("tool use = %#v", normalized.Input[1])
	}
	if normalized.Input[2].Type != "function_call_output" || normalized.Input[2].OutputText != "Sunny" {
		t.Fatalf("tool result = %#v", normalized.Input[2])
	}
	var toolChoice string
	if err := json.Unmarshal(normalized.ToolChoice, &toolChoice); err != nil || toolChoice != "auto" {
		t.Fatalf("tool choice = %s, err = %v", normalized.ToolChoice, err)
	}
}

func TestNormalizeMessagesMapsImagesAndLongToolNames(t *testing.T) {
	t.Parallel()

	longName := "mcp__server__" + strings.Repeat("very_long_tool_name_", 5)
	maxTokens := 1
	disableParallel := false
	request := MessagesRequest{
		Model:     "gpt-5.4",
		MaxTokens: &maxTokens,
		Messages: []Message{{Role: "user", Content: Content{
			{Type: "image", Source: &ImageSource{Type: "base64", MediaType: "image/png", Data: "YWJj"}},
			{Type: "text", Text: "describe"},
		}}},
		Tools:      []Tool{{Name: longName, InputSchema: map[string]any{"type": "object"}}},
		ToolChoice: &ToolChoice{Type: "tool", Name: longName, DisableParallelToolUse: &disableParallel},
	}

	normalized, err := Normalize(request, models.NewCatalog(models.BootstrapEntries()))
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if len(normalized.Input) != 1 || normalized.Input[0].Content[0].ImageURL != "data:image/png;base64,YWJj" {
		t.Fatalf("input = %#v", normalized.Input)
	}
	shortened := normalized.Tools[0].Name
	if len(shortened) > 64 || normalized.ToolNameAliases[shortened] != longName {
		t.Fatalf("tool mapping = %q aliases=%#v", shortened, normalized.ToolNameAliases)
	}
	if normalized.ParallelToolCalls == nil || !*normalized.ParallelToolCalls {
		t.Fatalf("parallel_tool_calls = %#v, want true", normalized.ParallelToolCalls)
	}
}

func TestNormalizeMessagesShortensLongToolUseIDs(t *testing.T) {
	t.Parallel()

	longID := "toolu_" + strings.Repeat("a", 80)
	maxTokens := 10
	normalized, err := Normalize(MessagesRequest{
		Model:     "gpt-5.4",
		MaxTokens: &maxTokens,
		Messages: []Message{
			{Role: "assistant", Content: Content{{Type: "tool_use", ID: longID, Name: "lookup", Input: json.RawMessage(`{}`)}}},
			{Role: "user", Content: Content{{Type: "tool_result", ToolUseID: longID, Content: Content{{Type: "text", Text: "done"}}}}},
		},
	}, models.NewCatalog(models.BootstrapEntries()))
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if len(normalized.Input) != 2 {
		t.Fatalf("input len = %d, want 2: %#v", len(normalized.Input), normalized.Input)
	}
	callID := normalized.Input[0].CallID
	if len(callID) > 64 || callID == longID {
		t.Fatalf("shortened call ID = %q (len %d)", callID, len(callID))
	}
	if normalized.Input[1].CallID != callID {
		t.Fatalf("tool result call ID = %q, want %q", normalized.Input[1].CallID, callID)
	}
}

func TestNormalizeMessagesRejectsUnknownToolResult(t *testing.T) {
	t.Parallel()

	maxTokens := 10
	_, err := Normalize(MessagesRequest{
		Model:     "gpt-5.4",
		MaxTokens: &maxTokens,
		Messages:  []Message{{Role: "user", Content: Content{{Type: "tool_result", ToolUseID: "missing", Content: Content{{Type: "text", Text: "nope"}}}}}},
	}, models.NewCatalog(models.BootstrapEntries()))
	if err == nil || !strings.Contains(err.Error(), "unknown tool_use") {
		t.Fatalf("Normalize() error = %v", err)
	}
}

func TestNormalizeMessagesMapsZeroMaxTokensToGenerateFalse(t *testing.T) {
	t.Parallel()

	maxTokens := 0
	normalized, err := Normalize(MessagesRequest{
		Model:     "gpt-5.4",
		MaxTokens: &maxTokens,
		Messages:  []Message{{Role: "user", Content: Content{{Type: "text", Text: "warm cache"}}}},
	}, models.NewCatalog(models.BootstrapEntries()))
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if normalized.Generate == nil || *normalized.Generate {
		t.Fatalf("Generate = %#v, want false", normalized.Generate)
	}
}

func TestNormalizeMessagesPreservesToolErrorSemantics(t *testing.T) {
	t.Parallel()

	maxTokens := 10
	normalized, err := Normalize(MessagesRequest{
		Model:     "gpt-5.4",
		MaxTokens: &maxTokens,
		Messages: []Message{
			{
				Role: "assistant",
				Content: Content{{
					Type: "tool_use", ID: "call_1", Name: "weather", Input: json.RawMessage(`{}`),
				}},
			},
			{
				Role: "user",
				Content: Content{{
					Type: "tool_result", ToolUseID: "call_1", IsError: true,
					Content: Content{{Type: "text", Text: "network unavailable"}},
				}},
			},
		},
	}, models.NewCatalog(models.BootstrapEntries()))
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if got := normalized.Input[1].OutputText; got != "Tool error: network unavailable" {
		t.Fatalf("tool output = %q", got)
	}
}

func TestNormalizeMessagesPreservesLargeToolInputNumbers(t *testing.T) {
	t.Parallel()

	maxTokens := 10
	normalized, err := Normalize(MessagesRequest{
		Model:     "gpt-5.4",
		MaxTokens: &maxTokens,
		Messages: []Message{{
			Role: "assistant",
			Content: Content{{
				Type: "tool_use", ID: "call_1", Name: "lookup", Input: json.RawMessage(`{"id":9007199254740993}`),
			}},
		}},
	}, models.NewCatalog(models.BootstrapEntries()))
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if got := normalized.Input[0].Arguments; got != `{"id":9007199254740993}` {
		t.Fatalf("tool arguments = %q", got)
	}
}

func TestNormalizeMessagesKeepsThinkingDisabledWhenEffortIsSet(t *testing.T) {
	t.Parallel()

	maxTokens := 10
	normalized, err := Normalize(MessagesRequest{
		Model:        "gpt-5.4",
		MaxTokens:    &maxTokens,
		Messages:     []Message{{Role: "user", Content: Content{{Type: "text", Text: "answer"}}}},
		Thinking:     &Thinking{Type: "disabled"},
		OutputConfig: &OutputConfig{Effort: "low"},
	}, models.NewCatalog(models.BootstrapEntries()))
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if normalized.Reasoning == nil || normalized.Reasoning.Effort != "low" || normalized.Reasoning.Summary != "" {
		t.Fatalf("reasoning = %#v", normalized.Reasoning)
	}
	if len(normalized.Include) != 0 {
		t.Fatalf("include = %#v, want no exposed thinking", normalized.Include)
	}
}

func TestDecodeMessagesAcceptsCurrentClaudeCodeBetaFields(t *testing.T) {
	t.Parallel()

	_, err := DecodeMessages([]byte(`{
		"model":"gpt-5.6-sol",
		"max_tokens":32000,
		"messages":[{"role":"user","content":"hello"}],
		"thinking":{"type":"adaptive","display":"omitted"},
		"context_management":{"edits":[{"type":"clear_thinking_20251015","keep":"all"}]},
		"output_config":{"effort":"high"}
	}`))
	if err != nil {
		t.Fatalf("DecodeMessages() error = %v", err)
	}
}

func TestNormalizeMessagesWrapsMessageSystemRoleAsUserReminder(t *testing.T) {
	t.Parallel()

	maxTokens := 10
	normalized, err := Normalize(MessagesRequest{
		Model:     "gpt-5.6-sol",
		MaxTokens: &maxTokens,
		Messages: []Message{
			{Role: "user", Content: Content{{Type: "text", Text: "hello"}}},
			{Role: "system", Content: Content{{Type: "text", Text: "Use the Workflow tool."}}},
		},
	}, models.NewCatalog(models.BootstrapEntries()))
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if len(normalized.Input) != 2 {
		t.Fatalf("input len = %d, want 2: %#v", len(normalized.Input), normalized.Input)
	}
	reminder := normalized.Input[1]
	if reminder.Role != "user" || len(reminder.Content) != 1 {
		t.Fatalf("reminder = %#v", reminder)
	}
	if got := reminder.Content[0].Text; got != "<system-reminder>\nUse the Workflow tool.\n</system-reminder>" {
		t.Fatalf("reminder text = %q", got)
	}
}

func TestNormalizeMessagesFiltersClaudeCodeAttributionSystemText(t *testing.T) {
	t.Parallel()

	maxTokens := 10
	normalized, err := Normalize(MessagesRequest{
		Model:     "gpt-5.6-sol",
		MaxTokens: &maxTokens,
		System: Content{
			{Type: "text", Text: " \n x-anthropic-billing-header: cc_version=2.1.211"},
			{Type: "text", Text: "Be precise."},
		},
		Messages: []Message{{Role: "user", Content: Content{{Type: "text", Text: "hello"}}}},
	}, models.NewCatalog(models.BootstrapEntries()))
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if normalized.Instructions != "Be precise." {
		t.Fatalf("instructions = %q, want %q", normalized.Instructions, "Be precise.")
	}
}

func TestNormalizeMessagesRejectsHostedWebSearch(t *testing.T) {
	t.Parallel()

	maxTokens := 10
	_, err := Normalize(MessagesRequest{
		Model:     "gpt-5.4",
		MaxTokens: &maxTokens,
		Messages:  []Message{{Role: "user", Content: Content{{Type: "text", Text: "search"}}}},
		Tools:     []Tool{{Type: "web_search_20250305", Name: "web_search"}},
	}, models.NewCatalog(models.BootstrapEntries()))
	if err == nil || !strings.Contains(err.Error(), "hosted web search") {
		t.Fatalf("Normalize() error = %v", err)
	}
}
