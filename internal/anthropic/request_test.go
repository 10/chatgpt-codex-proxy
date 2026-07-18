package anthropic

import (
	"encoding/base64"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"chatgpt-codex-proxy/internal/models"
)

func testCodexReasoningSignature() string {
	return testCodexReasoningSignatureWithMarker(0)
}

func testCodexReasoningSignatureWithMarker(marker byte) string {
	payload := make([]byte, 1+8+16+16+32)
	payload[0] = 0x80
	for index := 9; index < len(payload); index++ {
		payload[index] = byte(index)
	}
	payload[9] = marker
	return base64.RawURLEncoding.EncodeToString(payload)
}

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
	if normalized.Reasoning == nil || normalized.Reasoning.Effort != "low" || normalized.Reasoning.Summary != "auto" {
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

func TestNormalizeMessagesMapsThinkingBudgetToEffort(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		budget int
		want   string
	}{
		{name: "zero uses model default", budget: 0, want: "medium"},
		{name: "low upper bound", budget: 1024, want: "low"},
		{name: "medium lower bound", budget: 1025, want: "medium"},
		{name: "medium upper bound", budget: 8192, want: "medium"},
		{name: "high lower bound", budget: 8193, want: "high"},
		{name: "high upper bound", budget: 24576, want: "high"},
		{name: "xhigh lower bound", budget: 24577, want: "xhigh"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			maxTokens := 10
			normalized, err := Normalize(MessagesRequest{
				Model:     "gpt-5.4",
				MaxTokens: &maxTokens,
				Messages:  []Message{{Role: "user", Content: Content{{Type: "text", Text: "Think carefully"}}}},
				Thinking:  &Thinking{Type: "enabled", BudgetTokens: tc.budget},
			}, models.NewCatalog(models.BootstrapEntries()))
			if err != nil {
				t.Fatalf("Normalize() error = %v", err)
			}
			if normalized.Reasoning == nil || normalized.Reasoning.Effort != tc.want {
				t.Fatalf("reasoning = %#v, want %s effort", normalized.Reasoning, tc.want)
			}
		})
	}
}

func TestNormalizeMessagesClampsThinkingBudgetToSupportedEffort(t *testing.T) {
	t.Parallel()

	maxTokens := 10
	catalog := models.NewCatalog([]models.Entry{{
		ID:                     "gpt-test",
		DefaultReasoningEffort: "medium",
		SupportedReasoningEfforts: []models.ReasoningEffort{
			{ReasoningEffort: "low"},
			{ReasoningEffort: "medium"},
			{ReasoningEffort: "high"},
		},
	}})
	normalized, err := Normalize(MessagesRequest{
		Model:     "gpt-test",
		MaxTokens: &maxTokens,
		Messages:  []Message{{Role: "user", Content: Content{{Type: "text", Text: "Think as deeply as supported"}}}},
		Thinking:  &Thinking{Type: "enabled", BudgetTokens: 24577},
	}, catalog)
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if normalized.Reasoning == nil || normalized.Reasoning.Effort != "high" {
		t.Fatalf("reasoning = %#v, want high effort", normalized.Reasoning)
	}
}

func TestNormalizeMessagesPrefersExplicitEffortOverThinkingBudget(t *testing.T) {
	t.Parallel()

	maxTokens := 10
	normalized, err := Normalize(MessagesRequest{
		Model:        "gpt-5.4",
		MaxTokens:    &maxTokens,
		Messages:     []Message{{Role: "user", Content: Content{{Type: "text", Text: "Use the requested effort"}}}},
		Thinking:     &Thinking{Type: "enabled", BudgetTokens: 1024},
		OutputConfig: &OutputConfig{Effort: "high"},
	}, models.NewCatalog(models.BootstrapEntries()))
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if normalized.Reasoning == nil || normalized.Reasoning.Effort != "high" {
		t.Fatalf("reasoning = %#v, want explicit high effort", normalized.Reasoning)
	}
}

func TestNormalizeMessagesWithoutSystemStaysInstructionFree(t *testing.T) {
	t.Parallel()

	maxTokens := 10
	normalized, err := Normalize(MessagesRequest{
		Model:     "gpt-5.4",
		MaxTokens: &maxTokens,
		Messages: []Message{{
			Role:    "user",
			Content: Content{{Type: "text", Text: "Hello"}},
		}},
	}, models.NewCatalog(models.BootstrapEntries()))
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if normalized.Instructions != "" {
		t.Fatalf("instructions = %q, want empty", normalized.Instructions)
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

func TestNormalizeMessagesKeepsOnlyValidCodexThinkingSignatures(t *testing.T) {
	t.Parallel()

	maxTokens := 10
	normalized, err := Normalize(MessagesRequest{
		Model:     "gpt-5.4",
		MaxTokens: &maxTokens,
		Messages: []Message{
			{Role: "assistant", Content: Content{
				{Type: "thinking", Thinking: "discard me", Signature: "Eo8Canthropic-state"},
				{Type: "thinking", Thinking: "keep me", Signature: testCodexReasoningSignature()},
				{Type: "text", Text: "answer"},
			}},
			{Role: "user", Content: Content{{Type: "text", Text: "continue"}}},
		},
	}, models.NewCatalog(models.BootstrapEntries()))
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if len(normalized.Input) != 3 {
		t.Fatalf("input len = %d, want 3: %#v", len(normalized.Input), normalized.Input)
	}
	if normalized.Input[0].Type != "reasoning" || normalized.Input[0].EncryptedContent != testCodexReasoningSignature() {
		t.Fatalf("reasoning input = %#v", normalized.Input[0])
	}
	for _, item := range normalized.Input {
		if item.EncryptedContent == "Eo8Canthropic-state" {
			t.Fatalf("foreign thinking signature was forwarded: %#v", normalized.Input)
		}
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

func TestNormalizeMessagesMakesOptionalOutputFieldsStrictAndNullable(t *testing.T) {
	t.Parallel()

	maxTokens := 10
	normalized, err := Normalize(MessagesRequest{
		Model:     "gpt-5.4",
		MaxTokens: &maxTokens,
		Messages:  []Message{{Role: "user", Content: Content{{Type: "text", Text: "answer"}}}},
		OutputConfig: &OutputConfig{Format: &OutputFormat{
			Type: "json_schema",
			Schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"result":     map[string]any{"type": "string"},
					"impossible": map[string]any{"type": "boolean"},
					"state":      map[string]any{"type": "string", "enum": []any{"ready", "blocked"}},
					"details": map[string]any{
						"type": []any{"object", "null"},
						"properties": map[string]any{
							"note": map[string]any{"type": "string"},
						},
					},
				},
				"required": []any{"result"},
			},
		}},
	}, models.NewCatalog(models.BootstrapEntries()))
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}

	if !normalized.Text.Format.Strict {
		t.Fatal("strict = false, want true")
	}
	required, _ := normalized.Text.Format.Schema["required"].([]string)
	if !slices.Equal(required, []string{"details", "impossible", "result", "state"}) {
		t.Fatalf("upstream required = %#v, want every property", normalized.Text.Format.Schema["required"])
	}
	properties, _ := normalized.Text.Format.Schema["properties"].(map[string]any)
	impossible, _ := properties["impossible"].(map[string]any)
	if !newSchemaMatcher().matches(impossible, nil) || !newSchemaMatcher().matches(impossible, false) {
		t.Fatalf("impossible schema = %#v, want boolean or null", impossible)
	}
	state, _ := properties["state"].(map[string]any)
	if !newSchemaMatcher().matches(state, nil) || !newSchemaMatcher().matches(state, "ready") || newSchemaMatcher().matches(state, "other") {
		t.Fatalf("state schema = %#v, want enum or null", state)
	}
	details, _ := properties["details"].(map[string]any)
	detailsRequired, _ := details["required"].([]string)
	if !slices.Equal(detailsRequired, []string{"note"}) {
		t.Fatalf("details required = %#v, want nested properties", details["required"])
	}
	responseRequired, _ := normalized.ResponseSchema["required"].([]any)
	if !slices.Equal(responseRequired, []any{"result"}) {
		t.Fatalf("response required = %#v, want original required properties", normalized.ResponseSchema["required"])
	}
}

func TestNormalizeMessagesResolvesOptionalOutputSchemaRefs(t *testing.T) {
	t.Parallel()

	text, responseSchema, err := normalizeOutputFormat(&OutputConfig{Format: &OutputFormat{
		Type: "json_schema",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"result":  map[string]any{"type": "string"},
				"details": map[string]any{"$ref": "#/$defs/details"},
			},
			"required": []any{"result"},
			"$defs": map[string]any{
				"details": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"note": map[string]any{"type": "string"},
					},
				},
			},
		},
	}})
	if err != nil {
		t.Fatalf("normalizeOutputFormat() error = %v", err)
	}

	properties, _ := text.Format.Schema["properties"].(map[string]any)
	details, _ := properties["details"].(map[string]any)
	if !newSchemaMatcher().matches(details, nil) {
		t.Fatalf("strict details schema = %#v, want nullable", details)
	}
	responseProperties, _ := responseSchema["properties"].(map[string]any)
	responseDetails, _ := responseProperties["details"].(map[string]any)
	if responseDetails["$ref"] != nil || responseDetails["type"] != "object" {
		t.Fatalf("response details schema = %#v, want inlined object", responseDetails)
	}
}

func TestNormalizeMessagesRejectsInvalidOutputSchemas(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		schema map[string]any
		want   string
	}{
		{
			name:   "implicit object root",
			schema: map[string]any{"properties": map[string]any{"result": map[string]any{"type": "string"}}},
			want:   "root must have type object",
		},
		{
			name: "undeclared required property",
			schema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"result": map[string]any{"type": "string"}},
				"required":   []any{"result", "undeclared"},
			},
			want: `required property "undeclared" must be declared`,
		},
		{
			name: "malformed required properties",
			schema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"result": map[string]any{"type": "string"}},
				"required":   "result",
			},
			want: "output schema is invalid",
		},
		{
			name: "boolean property schema",
			schema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"never": false},
			},
			want: `property "never" must use an object schema`,
		},
		{
			name: "unsupported composition",
			schema: map[string]any{
				"type": "object",
				"allOf": []any{map[string]any{
					"type":       "object",
					"properties": map[string]any{"value": map[string]any{"type": "string"}},
				}},
			},
			want: `keyword "allOf" is not supported`,
		},
		{
			name: "unsupported keyword",
			schema: map[string]any{
				"type": "object",
				"properties": map[string]any{"values": map[string]any{
					"type":     "array",
					"items":    map[string]any{"type": "string"},
					"contains": map[string]any{"const": "required"},
				}},
			},
			want: `keyword "contains" is not supported`,
		},
		{
			name: "unsupported format",
			schema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"link": map[string]any{"type": "string", "format": "uri"}},
			},
			want: `format "uri" is not supported`,
		},
		{
			name: "non-object root",
			schema: map[string]any{
				"anyOf": []any{
					map[string]any{"type": "string"},
					map[string]any{"type": "null"},
				},
			},
			want: "root must have type object",
		},
		{
			name: "additional properties",
			schema: map[string]any{
				"type":                 "object",
				"properties":           map[string]any{"result": map[string]any{"type": "string"}},
				"additionalProperties": true,
			},
			want: "must set additionalProperties to false",
		},
		{
			name: "nested boolean schema",
			schema: map[string]any{
				"type": "object",
				"properties": map[string]any{"choice": map[string]any{
					"anyOf": []any{false, map[string]any{"type": "string"}},
				}},
			},
			want: `"anyOf" entries must use object schemas`,
		},
		{
			name: "empty anyOf",
			schema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"choice": map[string]any{"anyOf": []any{}}},
			},
			want: `"anyOf" must contain at least one schema`,
		},
		{
			name: "array without items",
			schema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"values": map[string]any{"type": "array"}},
			},
			want: "array nodes must define items",
		},
		{
			name: "typed object anyOf",
			schema: map[string]any{
				"type": "object",
				"properties": map[string]any{"choice": map[string]any{
					"type": "object",
					"anyOf": []any{
						map[string]any{
							"type":       "object",
							"properties": map[string]any{"a": map[string]any{"type": "string"}},
							"required":   []any{"a"},
						},
						map[string]any{
							"type":       "object",
							"properties": map[string]any{"b": map[string]any{"type": "string"}},
							"required":   []any{"b"},
						},
					},
				}},
			},
			want: `"anyOf" cannot be combined with type object`,
		},
		{
			name: "unresolved reference",
			schema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"head": map[string]any{"$ref": "#/$defs/node"}},
				"$defs": map[string]any{"node": map[string]any{
					"type":       "object",
					"properties": map[string]any{"next": map[string]any{"$ref": "#/$defs/node"}},
				}},
			},
			want: `keyword "$ref" is not supported`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, _, err := normalizeOutputFormat(&OutputConfig{Format: &OutputFormat{
				Type:   "json_schema",
				Schema: tc.schema,
			}})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("normalizeOutputFormat() error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestNormalizeMessagesStrictifiesEmptyNullableObjects(t *testing.T) {
	t.Parallel()

	text, _, err := normalizeOutputFormat(&OutputConfig{Format: &OutputFormat{
		Type: "json_schema",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"details": map[string]any{"type": []any{"object", "null"}},
			},
		},
	}})
	if err != nil {
		t.Fatalf("normalizeOutputFormat() error = %v", err)
	}
	properties, _ := text.Format.Schema["properties"].(map[string]any)
	details, _ := properties["details"].(map[string]any)
	if details["additionalProperties"] != false {
		t.Fatalf("details = %#v", details)
	}
	if _, ok := details["properties"].(map[string]any); !ok {
		t.Fatalf("details.properties = %#v, want object", details["properties"])
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

func TestNormalizeMessagesMapsHostedWebSearch(t *testing.T) {
	t.Parallel()

	for _, toolType := range []string{"web_search_20250305", "web_search_20260209"} {
		t.Run(toolType, func(t *testing.T) {
			t.Parallel()

			request, err := DecodeMessages([]byte(`{
				"model":"gpt-5.4",
				"max_tokens":10,
				"messages":[{"role":"user","content":"search"}],
				"tools":[{
					"type":"` + toolType + `",
					"name":"browser_search",
					"allowed_domains":["openai.com","anthropic.com"],
					"user_location":{"type":"approximate","country":"US"}
				}],
				"tool_choice":{"type":"tool","name":"browser_search","disable_parallel_tool_use":true}
			}`))
			if err != nil {
				t.Fatalf("DecodeMessages() error = %v", err)
			}

			normalized, err := Normalize(request, models.NewCatalog(models.BootstrapEntries()))
			if err != nil {
				t.Fatalf("Normalize() error = %v", err)
			}
			if len(normalized.Tools) != 1 || normalized.Tools[0].Type != "web_search" {
				t.Fatalf("tools = %#v, want one web_search tool", normalized.Tools)
			}
			encoded, err := json.Marshal(normalized.Tools[0])
			if err != nil {
				t.Fatalf("json.Marshal(tool) error = %v", err)
			}
			var tool map[string]any
			if err := json.Unmarshal(encoded, &tool); err != nil {
				t.Fatalf("json.Unmarshal(tool) error = %v", err)
			}
			filters, _ := tool["filters"].(map[string]any)
			allowedDomains, _ := filters["allowed_domains"].([]any)
			if len(allowedDomains) != 2 || allowedDomains[0] != "openai.com" || allowedDomains[1] != "anthropic.com" {
				t.Fatalf("filters = %#v", filters)
			}
			if country := normalized.Tools[0].UserLocation["country"]; country != "US" {
				t.Fatalf("user_location.country = %#v, want US", country)
			}
			var toolChoice map[string]any
			if err := json.Unmarshal(normalized.ToolChoice, &toolChoice); err != nil || toolChoice["type"] != "web_search" {
				t.Fatalf("tool choice = %s, err = %v", normalized.ToolChoice, err)
			}
			if normalized.ParallelToolCalls == nil || *normalized.ParallelToolCalls {
				t.Fatalf("parallel_tool_calls = %#v, want false", normalized.ParallelToolCalls)
			}
		})
	}
}

func TestNormalizeMessagesReplaysHostedWebSearchBlocks(t *testing.T) {
	t.Parallel()

	request, err := DecodeMessages([]byte(`{
		"model":"gpt-5.4",
		"max_tokens":10,
		"messages":[
			{"role":"user","content":"search"},
			{"role":"assistant","content":[
				{"type":"server_tool_use","id":"ws_1","name":"web_search","input":{"query":"Codex API"}},
				{"type":"web_search_tool_result","tool_use_id":"ws_1","content":[
					{"type":"web_search_result","title":"OpenAI Codex","url":"https://openai.com/codex","encrypted_content":"opaque","page_age":null}
				]},
				{"type":"text","text":"Here is what I found."}
			]},
			{"role":"user","content":"Tell me more."}
		],
		"tools":[{"type":"web_search_20250305","name":"web_search"}]
	}`))
	if err != nil {
		t.Fatalf("DecodeMessages() error = %v", err)
	}

	normalized, err := Normalize(request, models.NewCatalog(models.BootstrapEntries()))
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if len(normalized.Input) != 4 {
		t.Fatalf("input len = %d, want 4: %#v", len(normalized.Input), normalized.Input)
	}
	search := normalized.Input[1]
	if search.Type != "web_search_call" || search.ID != "ws_1" || search.Status != "completed" {
		t.Fatalf("search replay = %#v", search)
	}
	if normalized.Input[2].Role != "assistant" || normalized.Input[2].Content[0].Text != "Here is what I found." {
		t.Fatalf("assistant replay = %#v", normalized.Input[2])
	}
}

func TestNormalizeMessagesRejectsUnsupportedHostedWebSearchLimits(t *testing.T) {
	t.Parallel()

	maxTokens := 10
	maxUses := 1
	for name, tool := range map[string]Tool{
		"max_uses":        {Type: "web_search_20250305", Name: "web_search", MaxUses: &maxUses},
		"blocked_domains": {Type: "web_search_20250305", Name: "web_search", BlockedDomains: []string{"example.com"}},
	} {
		name, tool := name, tool
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := Normalize(MessagesRequest{
				Model:     "gpt-5.4",
				MaxTokens: &maxTokens,
				Messages:  []Message{{Role: "user", Content: Content{{Type: "text", Text: "search"}}}},
				Tools:     []Tool{tool},
			}, models.NewCatalog(models.BootstrapEntries()))
			if err == nil || !strings.Contains(err.Error(), name+" is not supported") {
				t.Fatalf("Normalize() error = %v", err)
			}
		})
	}
}
