package openai

import (
	"encoding/json"
	"strings"
	"testing"

	"chatgpt-codex-proxy/internal/codex"
	"chatgpt-codex-proxy/internal/turn"
)

func TestChatCompletionsTranslation(t *testing.T) {
	t.Parallel()

	request := ChatCompletionsRequest{
		Model:              "gpt-6-astra",
		ReasoningEffort:    "high",
		ServiceTier:        "fast",
		PreviousResponseID: "resp_prev_chat",
		PromptCacheKey:     "chat-cache-key",
		Messages: []ChatMessage{
			{
				Role:    "system",
				Content: MessageContent{{Type: "text", Text: "System rules"}},
			},
			{
				Role:    "developer",
				Content: MessageContent{{Type: "text", Text: "Developer rules"}},
			},
			{
				Role: "user",
				Content: MessageContent{
					{Type: "text", Text: "Hello"},
					{Type: "image_url", ImageURL: &ImageURLValue{URL: "https://example.com/image.png"}},
				},
			},
			{
				Role: "assistant",
				ToolCalls: []ToolCall{{
					ID:   "call_1",
					Type: "function",
					Function: FunctionPayload{
						Name:      "lookup_weather",
						Arguments: `{"city":"SF"}`,
					},
				}},
			},
			{
				Role:       "tool",
				ToolCallID: "call_1",
				Content:    MessageContent{{Type: "text", Text: `{"temp":72}`}},
			},
		},
		Tools: []ToolDefinition{{
			Type: "function",
			Function: &FunctionTool{
				Name:       "lookup_weather",
				Parameters: map[string]any{"type": "object"},
			},
		}},
	}

	normalized, err := ChatCompletions(request, nil)
	if err != nil {
		t.Fatalf("ChatCompletions() error = %v", err)
	}

	if normalized.Model != "gpt-6-astra" {
		t.Fatalf("model = %q, want explicit model passthrough", normalized.Model)
	}
	if normalized.Reasoning == nil || normalized.Reasoning.Effort != "high" {
		t.Fatalf("reasoning = %#v, want explicit effort override", normalized.Reasoning)
	}
	if normalized.ServiceTier != "priority" {
		t.Fatalf("service_tier = %q, want priority", normalized.ServiceTier)
	}
	if normalized.PreviousResponseID != "resp_prev_chat" {
		t.Fatalf("previous_response_id = %q, want resp_prev_chat", normalized.PreviousResponseID)
	}
	if normalized.PromptCacheKey != "chat-cache-key" {
		t.Fatalf("prompt_cache_key = %q, want chat-cache-key", normalized.PromptCacheKey)
	}
	if len(normalized.Include) != 1 || normalized.Include[0] != "reasoning.encrypted_content" {
		t.Fatalf("include = %#v, want reasoning.encrypted_content only", normalized.Include)
	}
	if normalized.Instructions != "" {
		t.Fatalf("instructions = %q, want empty (lifted to developer input item)", normalized.Instructions)
	}
	if len(normalized.Input) != 4 {
		t.Fatalf("input len = %d, want 4", len(normalized.Input))
	}
	if normalized.Input[0].Role != "developer" || len(normalized.Input[0].Content) != 1 || normalized.Input[0].Content[0].Text != "System rules\n\nDeveloper rules" {
		t.Fatalf("first input = %#v, want developer instructions item", normalized.Input[0])
	}
	if normalized.Input[1].Role != "user" {
		t.Fatalf("second input role = %q, want user", normalized.Input[1].Role)
	}
	if normalized.Input[2].Type != "function_call" {
		t.Fatalf("third input type = %q, want function_call", normalized.Input[2].Type)
	}
	if normalized.Input[3].Type != "function_call_output" {
		t.Fatalf("fourth input type = %q, want function_call_output", normalized.Input[3].Type)
	}
	if len(normalized.Tools) != 1 || normalized.Tools[0].Type != "function" {
		t.Fatalf("tools = %#v", normalized.Tools)
	}
	if normalized.Tools[0].Name != "lookup_weather" {
		t.Fatalf("tool name = %q, want lookup_weather", normalized.Tools[0].Name)
	}
}

func TestChatCompletionsTranslationPreservesTextVerbosity(t *testing.T) {
	t.Parallel()

	var request ChatCompletionsRequest
	if err := json.Unmarshal([]byte(`{
		"model": "gpt-5.6-terra",
		"messages": [{"role": "user", "content": "Hello"}],
		"response_format": {"type": "json_object"},
		"text": {"verbosity": "high"}
	}`), &request); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	normalized, err := ChatCompletions(request, nil)
	if err != nil {
		t.Fatalf("ChatCompletions() error = %v", err)
	}

	text := marshalTextConfig(t, normalized.Text)
	if got := text["verbosity"]; got != "high" {
		t.Fatalf("text.verbosity = %#v, want high", got)
	}
	format, _ := text["format"].(map[string]any)
	if got := format["type"]; got != "json_object" {
		t.Fatalf("text.format.type = %#v, want json_object", got)
	}
}

func TestResponsesTranslationPreservesVerbosityWithoutFormat(t *testing.T) {
	t.Parallel()

	var request ResponsesRequest
	if err := json.Unmarshal([]byte(`{
		"model": "gpt-5.6-terra",
		"input": "Hello",
		"text": {"verbosity": "low"}
	}`), &request); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	normalized, err := Responses(request, nil)
	if err != nil {
		t.Fatalf("Responses() error = %v", err)
	}

	text := marshalTextConfig(t, normalized.Text)
	if got := text["verbosity"]; got != "low" {
		t.Fatalf("text.verbosity = %#v, want low", got)
	}
	if _, ok := text["format"]; ok {
		t.Fatalf("text.format = %#v, want omitted", text["format"])
	}
}

func TestCompactTranslation(t *testing.T) {
	t.Parallel()

	request := ResponsesCompactRequest{
		PreviousResponseID: "resp_prev_compact",
		Reasoning: &Reasoning{
			Effort: "high",
		},
		Text: &ResponsesText{
			Format: &ResponsesTextFormat{
				Type:   "json_schema",
				Name:   "compact_summary",
				Schema: map[string]any{"type": "object"},
				Strict: true,
			},
		},
		Input: ResponsesInput{
			Items: []ResponsesInputItem{
				{
					Type:  "message",
					Role:  "assistant",
					Phase: "output",
					Content: MessageContent{{
						Type: "output_text",
						Text: "Existing assistant output",
					}},
				},
				{
					Type:             "compaction",
					ID:               "cmp_existing",
					EncryptedContent: "enc_existing",
				},
			},
		},
	}

	normalized, err := Compact(request, nil)
	if err != nil {
		t.Fatalf("Compact() error = %v", err)
	}

	if normalized.PreviousResponseID != "resp_prev_compact" {
		t.Fatalf("previous_response_id = %q, want resp_prev_compact", normalized.PreviousResponseID)
	}
	if normalized.Reasoning == nil || normalized.Reasoning.Effort != "high" || normalized.Reasoning.Summary != "auto" {
		t.Fatalf("reasoning = %#v, want explicit effort with auto summary", normalized.Reasoning)
	}
	if normalized.Text == nil || normalized.Text.Format.Type != "json_schema" {
		t.Fatalf("text = %#v, want json_schema text config", normalized.Text)
	}
	if len(normalized.Input) != 2 {
		t.Fatalf("len(input) = %d, want 2", len(normalized.Input))
	}
	if normalized.Input[0].Role != "assistant" || normalized.Input[0].Phase != "output" {
		t.Fatalf("input[0] = %#v, want assistant output-phase message", normalized.Input[0])
	}
	if normalized.Input[1].Type != "compaction" || normalized.Input[1].EncryptedContent != "enc_existing" {
		t.Fatalf("input[1] = %#v, want compaction passthrough", normalized.Input[1])
	}
}

func TestResponsesTranslationExtractsInstructionRolesFromInput(t *testing.T) {
	t.Parallel()

	request := ResponsesRequest{
		Model:        "gpt-5.6-terra",
		Instructions: "Top-level instructions",
		Input: ResponsesInput{
			Items: []ResponsesInputItem{
				{
					Role: "system",
					Content: MessageContent{{
						Type: "text",
						Text: "System rules",
					}},
				},
				{
					Role: "developer",
					Content: MessageContent{{
						Type: "text",
						Text: "Developer rules",
					}},
				},
				{
					Role: "user",
					Content: MessageContent{{
						Type: "text",
						Text: "What does this project do?",
					}},
				},
			},
		},
	}

	normalized, err := Responses(request, nil)
	if err != nil {
		t.Fatalf("Responses() error = %v", err)
	}

	if normalized.Instructions != "" {
		t.Fatalf("instructions = %q, want empty (lifted to developer input item)", normalized.Instructions)
	}
	if len(normalized.Input) != 2 {
		t.Fatalf("input len = %d, want 2", len(normalized.Input))
	}
	if normalized.Input[0].Role != "developer" || len(normalized.Input[0].Content) != 1 || normalized.Input[0].Content[0].Text != "Top-level instructions\n\nSystem rules\n\nDeveloper rules" {
		t.Fatalf("first input = %#v, want collapsed developer instructions item", normalized.Input[0])
	}
	if normalized.Input[1].Role != "user" {
		t.Fatalf("input role = %q, want user", normalized.Input[1].Role)
	}
}

func TestChatCompletionsTranslationPreservesCustomToolShape(t *testing.T) {
	t.Parallel()

	request := ChatCompletionsRequest{
		Model: "gpt-5.6-terra",
		Messages: []ChatMessage{{
			Role:    "user",
			Content: MessageContent{{Type: "text", Text: "Patch the file"}},
		}},
		Tools: []ToolDefinition{{
			Type:        "custom",
			Name:        "ApplyPatch",
			Description: "Patch a file",
			Format: map[string]any{
				"type":       "grammar",
				"definition": "start: item+",
			},
			ExtraFields: map[string]json.RawMessage{
				"metadata": json.RawMessage(`{"origin":"cursor"}`),
			},
		}},
		ToolChoice: json.RawMessage(`{"type":"custom","name":"ApplyPatch"}`),
	}

	normalized, err := ChatCompletions(request, nil)
	if err != nil {
		t.Fatalf("ChatCompletions() error = %v", err)
	}

	if len(normalized.Tools) != 1 {
		t.Fatalf("tools len = %d, want 1", len(normalized.Tools))
	}
	if normalized.Tools[0].Type != "custom" {
		t.Fatalf("tool type = %q, want custom", normalized.Tools[0].Type)
	}
	if normalized.Tools[0].Name != "ApplyPatch" {
		t.Fatalf("tool name = %q, want ApplyPatch", normalized.Tools[0].Name)
	}
	if got := normalized.Tools[0].Format["type"]; got != "grammar" {
		t.Fatalf("tool format type = %#v, want grammar", got)
	}
	var metadata map[string]any
	if err := json.Unmarshal(normalized.Tools[0].ExtraFields["metadata"], &metadata); err != nil {
		t.Fatalf("metadata unmarshal error = %v", err)
	}
	if got := metadata["origin"]; got != "cursor" {
		t.Fatalf("tool metadata origin = %#v, want cursor", got)
	}
	if string(normalized.ToolChoice) != `{"type":"custom","name":"ApplyPatch"}` {
		t.Fatalf("tool choice = %s, want custom tool choice passthrough", string(normalized.ToolChoice))
	}
}

func TestChatCompletionsTranslationPreservesCustomToolCallsAndOutputs(t *testing.T) {
	t.Parallel()

	request := ChatCompletionsRequest{
		Model: "gpt-5.6-terra",
		Messages: []ChatMessage{
			{
				Role: "assistant",
				ToolCalls: []ToolCall{{
					ID:   "call_patch",
					Type: "custom",
					Custom: &CustomToolPayload{
						Name:  "ApplyPatch",
						Input: "*** Begin Patch\n*** Add File: dummy.txt\n+hello\n*** End Patch\n",
					},
				}},
			},
			{
				Role:       "tool",
				ToolCallID: "call_patch",
				Content:    MessageContent{{Type: "text", Text: "patched"}},
			},
		},
	}

	normalized, err := ChatCompletions(request, nil)
	if err != nil {
		t.Fatalf("ChatCompletions() error = %v", err)
	}

	if len(normalized.Input) != 2 {
		t.Fatalf("input len = %d, want 2", len(normalized.Input))
	}
	if normalized.Input[0].Type != "custom_tool_call" {
		t.Fatalf("input[0].Type = %q, want custom_tool_call", normalized.Input[0].Type)
	}
	if normalized.Input[0].Input == "" {
		t.Fatal("expected custom tool input to be preserved")
	}
	if normalized.Input[1].Type != "custom_tool_call_output" {
		t.Fatalf("input[1].Type = %q, want custom_tool_call_output", normalized.Input[1].Type)
	}
	if normalized.Input[1].OutputText != "patched" {
		t.Fatalf("input[1].OutputText = %q, want patched", normalized.Input[1].OutputText)
	}
}

func TestChatCompletionsTranslationMapsFunctionShapedReplayBackToCustomTool(t *testing.T) {
	t.Parallel()

	request := ChatCompletionsRequest{
		Model: "gpt-5.6-terra",
		Messages: []ChatMessage{
			{
				Role: "assistant",
				ToolCalls: []ToolCall{{
					ID:   "call_patch",
					Type: "function",
					Function: FunctionPayload{
						Name:      "ApplyPatch",
						Arguments: "*** Begin Patch\n*** Add File: dummy.txt\n+hello\n*** End Patch\n",
					},
				}},
			},
			{
				Role:       "tool",
				ToolCallID: "call_patch",
				Content:    MessageContent{{Type: "text", Text: "patched"}},
			},
		},
		Tools: []ToolDefinition{{
			Type: "custom",
			Name: "ApplyPatch",
			Format: map[string]any{
				"type":       "grammar",
				"definition": "start: patch",
				"syntax":     "lark",
			},
		}},
	}

	normalized, err := ChatCompletions(request, nil)
	if err != nil {
		t.Fatalf("ChatCompletions() error = %v", err)
	}

	if len(normalized.Input) != 2 {
		t.Fatalf("input len = %d, want 2", len(normalized.Input))
	}
	if normalized.Input[0].Type != "custom_tool_call" {
		t.Fatalf("input[0].Type = %q, want custom_tool_call", normalized.Input[0].Type)
	}
	if normalized.Input[0].Name != "ApplyPatch" {
		t.Fatalf("input[0].Name = %q, want ApplyPatch", normalized.Input[0].Name)
	}
	if normalized.Input[0].Input != "*** Begin Patch\n*** Add File: dummy.txt\n+hello\n*** End Patch\n" {
		t.Fatalf("input[0].Input = %q, want raw custom input", normalized.Input[0].Input)
	}
	if normalized.Input[1].Type != "custom_tool_call_output" {
		t.Fatalf("input[1].Type = %q, want custom_tool_call_output", normalized.Input[1].Type)
	}
}

func TestChatCompletionsTranslationSupportsWebSearchVariants(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		tool       ToolDefinition
		toolChoice json.RawMessage
		assertTool func(*testing.T, ToolDefinition, turn.NormalizedRequest)
	}{
		{
			name: "native web_search tool",
			tool: ToolDefinition{
				Type: "web_search",
			},
			toolChoice: json.RawMessage(`{"type":"web_search"}`),
			assertTool: func(t *testing.T, _ ToolDefinition, normalized turn.NormalizedRequest) {
				t.Helper()

				if normalized.Tools[0].Type != "web_search" {
					t.Fatalf("tool type = %q, want web_search", normalized.Tools[0].Type)
				}
			},
		},
		{
			name: "web_search_preview alias",
			tool: ToolDefinition{
				Type:              "web_search_preview",
				SearchContextSize: "high",
				UserLocation: map[string]any{
					"type":    "approximate",
					"country": "US",
				},
			},
			toolChoice: json.RawMessage(`{"type":"web_search_preview"}`),
			assertTool: func(t *testing.T, _ ToolDefinition, normalized turn.NormalizedRequest) {
				t.Helper()

				if normalized.Tools[0].Type != "web_search" {
					t.Fatalf("tool type = %q, want web_search", normalized.Tools[0].Type)
				}
				if normalized.Tools[0].SearchContextSize != "high" {
					t.Fatalf("search_context_size = %q, want high", normalized.Tools[0].SearchContextSize)
				}
				if country := normalized.Tools[0].UserLocation["country"]; country != "US" {
					t.Fatalf("user_location.country = %#v, want US", country)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			request := ChatCompletionsRequest{
				Model: "gpt-5.6-terra",
				Messages: []ChatMessage{{
					Role:    "user",
					Content: MessageContent{{Type: "text", Text: "Search the web"}},
				}},
				Tools:      []ToolDefinition{tc.tool},
				ToolChoice: tc.toolChoice,
			}

			normalized, err := ChatCompletions(request, nil)
			if err != nil {
				t.Fatalf("ChatCompletions() error = %v", err)
			}

			if len(normalized.Tools) != 1 {
				t.Fatalf("tools len = %d, want 1", len(normalized.Tools))
			}
			tc.assertTool(t, tc.tool, normalized)
			if string(normalized.ToolChoice) != `{"type":"web_search"}` {
				t.Fatalf("tool choice = %s, want web_search", string(normalized.ToolChoice))
			}
		})
	}
}

func TestResponsesTranslationForwardsUntranslatedItemsVerbatim(t *testing.T) {
	t.Parallel()

	items := []string{
		`{"type":"additional_tools","role":"developer","tools":[{"type":"namespace","name":"functions","description":"","tools":[]}]}`,
		`{"type":"compaction_trigger"}`,
		`{"type":"agent_message","author":"/root/worker","recipient":"/root","content":[{"type":"input_text","text":"done"}]}`,
	}
	var request ResponsesRequest
	if err := json.Unmarshal([]byte(`{"model":"gpt-5.6-terra","input":[`+strings.Join(items, ",")+`]}`), &request); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	normalized, err := Responses(request, nil)
	if err != nil {
		t.Fatalf("Responses() error = %v", err)
	}
	payload, err := json.Marshal(normalized.Request)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var outgoing struct {
		Input []json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(payload, &outgoing); err != nil {
		t.Fatalf("outgoing request unmarshal error = %v", err)
	}
	if len(outgoing.Input) != len(items) {
		t.Fatalf("outgoing input len = %d, want %d", len(outgoing.Input), len(items))
	}
	for index, want := range items {
		if got := string(outgoing.Input[index]); got != want {
			t.Fatalf("outgoing input[%d] = %s, want %s", index, got, want)
		}
	}
}

func TestResponsesTranslationPreservesToolOutputContentTypes(t *testing.T) {
	t.Parallel()

	request := ResponsesRequest{
		Model: "gpt-5.6-terra",
		Input: ResponsesInput{
			Items: []ResponsesInputItem{
				{
					Type:   "function_call_output",
					CallID: "call_1",
					OutputContent: MessageContent{{
						Type: "input_text",
						Text: "tool result",
					}},
				},
				{
					Type:   "custom_tool_call_output",
					CallID: "call_2",
					OutputContent: MessageContent{{
						Type: "text",
						Text: "custom result",
					}},
				},
			},
		},
	}

	normalized, err := Responses(request, nil)
	if err != nil {
		t.Fatalf("Responses() error = %v", err)
	}

	if normalized.Input[0].OutputContent[0].Type != "input_text" {
		t.Fatalf("function output part type = %q, want input_text", normalized.Input[0].OutputContent[0].Type)
	}
	if normalized.Input[1].OutputContent[0].Type != "input_text" {
		t.Fatalf("custom output part type = %q, want input_text", normalized.Input[1].OutputContent[0].Type)
	}
}

func TestChatCompletionsTranslationAcceptsCanonicalFilePart(t *testing.T) {
	t.Parallel()

	var request ChatCompletionsRequest
	err := json.Unmarshal([]byte(`{
		"model":"gpt-5.6-terra",
		"messages":[{
			"role":"user",
			"content":[
				{"type":"file","file":{"file_data":"data:text/plain;base64,SGVsbG8=","filename":"note.txt"}}
			]
		}]
	}`), &request)
	if err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	normalized, err := ChatCompletions(request, nil)
	if err != nil {
		t.Fatalf("ChatCompletions() error = %v", err)
	}
	parts := normalized.Input[0].Content
	if len(parts) != 1 {
		t.Fatalf("content = %#v, want one file part", parts)
	}
	if parts[0].Type != "input_file" || parts[0].FileData == "" || parts[0].Filename != "note.txt" {
		t.Fatalf("file part = %#v", parts[0])
	}
}

func TestChatCompletionsTranslationPreservesMultimodalToolOutput(t *testing.T) {
	t.Parallel()

	request := ChatCompletionsRequest{
		Model: "gpt-5.6-terra",
		Messages: []ChatMessage{
			{
				Role: "assistant",
				ToolCalls: []ToolCall{{
					ID:   "call_1",
					Type: "function",
					Function: FunctionPayload{
						Name:      "inspect_asset",
						Arguments: `{}`,
					},
				}},
			},
			{
				Role:       "tool",
				ToolCallID: "call_1",
				Content: MessageContent{
					{Type: "text", Text: "asset"},
					{Type: "image_url", ImageURL: &ImageURLValue{URL: "data:image/png;base64,aW1hZ2U="}},
					{Type: "file", File: &FileValue{FileData: "data:text/plain;base64,ZmlsZQ==", Filename: "asset.txt"}},
				},
			},
		},
	}

	normalized, err := ChatCompletions(request, nil)
	if err != nil {
		t.Fatalf("ChatCompletions() error = %v", err)
	}
	if len(normalized.Input) != 2 {
		t.Fatalf("input = %#v, want call and output", normalized.Input)
	}
	output := normalized.Input[1]
	if output.Type != "function_call_output" || len(output.OutputContent) != 3 || output.OutputText != "" {
		t.Fatalf("tool output = %#v", output)
	}
	if output.OutputContent[1].Type != "input_image" || output.OutputContent[2].Type != "input_file" {
		t.Fatalf("tool output content = %#v", output.OutputContent)
	}
}

func TestToolNamesAreShortenedConsistentlyAndRestored(t *testing.T) {
	t.Parallel()

	first := "mcp__filesystem__" + strings.Repeat("read_project_file_", 4)
	second := "mcp__workspace__" + strings.Repeat("read_project_file_", 4)
	choice, _ := json.Marshal(map[string]any{
		"type":     "function",
		"function": map[string]any{"name": second},
	})
	request := ChatCompletionsRequest{
		Model: "gpt-5.6-terra",
		Messages: []ChatMessage{{
			Role: "assistant",
			ToolCalls: []ToolCall{{
				ID: "call_1", Type: "function",
				Function: FunctionPayload{Name: first, Arguments: `{}`},
			}},
		}},
		Tools: []ToolDefinition{
			{Type: "function", Function: &FunctionTool{Name: first, Parameters: map[string]any{"type": "object"}}},
			{Type: "function", Function: &FunctionTool{Name: second, Parameters: map[string]any{"type": "object"}}},
		},
		ToolChoice: choice,
	}

	normalized, err := ChatCompletions(request, nil)
	if err != nil {
		t.Fatalf("ChatCompletions() error = %v", err)
	}
	shortFirst := normalized.Tools[0].Name
	shortSecond := normalized.Tools[1].Name
	if len(shortFirst) > 64 || len(shortSecond) > 64 || shortFirst == shortSecond {
		t.Fatalf("short names = %q, %q", shortFirst, shortSecond)
	}
	if normalized.Input[0].Name != shortFirst {
		t.Fatalf("replayed call name = %q, want %q", normalized.Input[0].Name, shortFirst)
	}
	if string(normalized.ToolChoice) != `{"type":"function","name":"`+shortSecond+`"}` {
		t.Fatalf("tool choice = %s", normalized.ToolChoice)
	}
	if normalized.ToolNameAliases[shortFirst] != first || normalized.ToolNameAliases[shortSecond] != second {
		t.Fatalf("tool aliases = %#v", normalized.ToolNameAliases)
	}

	accumulator := turn.NewAccumulator(normalized)
	accumulator.Apply(&codex.StreamEvent{
		Type: "response.output_item.added",
		Raw: map[string]any{
			"output_index": 0,
			"item": map[string]any{
				"id": "fc_1", "call_id": "call_2", "type": "function_call",
				"name": shortFirst, "arguments": `{}`, "status": "in_progress",
			},
		},
	})
	if accumulator.ToolCalls[0].Name != first {
		t.Fatalf("restored response tool name = %q, want %q", accumulator.ToolCalls[0].Name, first)
	}
}

func TestResponsesTranslationAcceptsReasoningItemReplay(t *testing.T) {
	t.Parallel()

	request := ResponsesRequest{
		Model: "gpt-5.6-terra",
		Input: ResponsesInput{
			Items: []ResponsesInputItem{{
				Type:             "reasoning",
				ID:               "rs_123",
				Status:           "completed",
				EncryptedContent: "encrypted-reasoning",
				Summary: []ReasoningPart{{
					Type: "summary_text",
					Text: "Summarized reasoning",
				}},
				Content: MessageContent{{
					Type: "reasoning_text",
					Text: "Full reasoning text",
				}},
			}},
		},
	}

	normalized, err := Responses(request, nil)
	if err != nil {
		t.Fatalf("Responses() error = %v", err)
	}

	if len(normalized.Input) != 1 {
		t.Fatalf("input len = %d, want 1", len(normalized.Input))
	}
	item := normalized.Input[0]
	if item.Type != "reasoning" {
		t.Fatalf("item.Type = %q, want reasoning", item.Type)
	}
	if item.ID != "rs_123" {
		t.Fatalf("item.ID = %q, want rs_123", item.ID)
	}
	if item.Status != "completed" {
		t.Fatalf("item.Status = %q, want completed", item.Status)
	}
	if item.EncryptedContent != "encrypted-reasoning" {
		t.Fatalf("item.EncryptedContent = %q, want encrypted-reasoning", item.EncryptedContent)
	}
	if len(item.Summary) != 1 || item.Summary[0].Text != "Summarized reasoning" {
		t.Fatalf("item.Summary = %#v", item.Summary)
	}
	if len(item.Content) != 1 || item.Content[0].Type != "reasoning_text" {
		t.Fatalf("item.Content = %#v", item.Content)
	}
}

func TestUnsupportedContentPartRejected(t *testing.T) {
	t.Parallel()

	_, err := ChatCompletions(ChatCompletionsRequest{
		Model: "gpt-5.6-terra",
		Messages: []ChatMessage{{
			Role: "user",
			Content: MessageContent{{
				Type: "audio",
			}},
		}},
	}, nil)

	if err == nil {
		t.Fatal("expected unsupported content part error")
	}
}

func marshalTextConfig(t *testing.T, text *codex.TextConfig) map[string]any {
	t.Helper()
	if text == nil {
		t.Fatal("text config is nil")
	}
	payload, err := json.Marshal(text)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	return decoded
}
