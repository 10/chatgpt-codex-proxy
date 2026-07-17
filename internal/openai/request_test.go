package openai

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"chatgpt-codex-proxy/internal/codex"
	"chatgpt-codex-proxy/internal/turn"
)

func TestChatCompletionsTranslation(t *testing.T) {
	t.Parallel()

	request := ChatCompletionsRequest{
		Model:              "gpt-5.4",
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

	if normalized.Model != "gpt-5.4" {
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
	if normalized.Instructions != "System rules\n\nDeveloper rules" {
		t.Fatalf("instructions = %q", normalized.Instructions)
	}
	if len(normalized.Input) != 3 {
		t.Fatalf("input len = %d, want 3", len(normalized.Input))
	}
	if normalized.Input[0].Role != "user" {
		t.Fatalf("first input role = %q, want user", normalized.Input[0].Role)
	}
	if normalized.Input[1].Type != "function_call" {
		t.Fatalf("second input type = %q, want function_call", normalized.Input[1].Type)
	}
	if normalized.Input[2].Type != "function_call_output" {
		t.Fatalf("third input type = %q, want function_call_output", normalized.Input[2].Type)
	}
	if len(normalized.Tools) != 1 || normalized.Tools[0].Type != "function" {
		t.Fatalf("tools = %#v", normalized.Tools)
	}
	if normalized.Tools[0].Name != "lookup_weather" {
		t.Fatalf("tool name = %q, want lookup_weather", normalized.Tools[0].Name)
	}
}

func TestInstructionFreeOpenAIRequestsStayInstructionFree(t *testing.T) {
	t.Parallel()

	chat, err := ChatCompletions(ChatCompletionsRequest{
		Model: "gpt-5.4",
		Messages: []ChatMessage{{
			Role:    "user",
			Content: MessageContent{{Type: "text", Text: "Hello"}},
		}},
	}, nil)
	if err != nil {
		t.Fatalf("ChatCompletions() error = %v", err)
	}
	if chat.Instructions != "" {
		t.Fatalf("ChatCompletions() instructions = %q, want empty", chat.Instructions)
	}

	responses, err := Responses(ResponsesRequest{
		Model: "gpt-5.4",
		Input: ResponsesInput{Items: []ResponsesInputItem{{
			Role:    "user",
			Content: MessageContent{{Type: "text", Text: "Hello"}},
		}}},
	}, nil)
	if err != nil {
		t.Fatalf("Responses() error = %v", err)
	}
	if responses.Instructions != "" {
		t.Fatalf("Responses() instructions = %q, want empty", responses.Instructions)
	}

	compact, err := Compact(ResponsesCompactRequest{
		Model: "gpt-5.4",
		Input: ResponsesInput{Items: []ResponsesInputItem{{
			Role:    "user",
			Content: MessageContent{{Type: "text", Text: "Hello"}},
		}}},
	}, nil)
	if err != nil {
		t.Fatalf("Compact() error = %v", err)
	}
	if compact.Instructions != "" {
		t.Fatalf("Compact() instructions = %q, want empty", compact.Instructions)
	}
}

func TestResponsesTranslation(t *testing.T) {
	t.Parallel()

	toolChoice, _ := json.Marshal(map[string]any{"type": "function", "name": "lookup"})
	request := ResponsesRequest{
		Model:              "gpt-5.4",
		PreviousResponseID: "resp_prev",
		PromptCacheKey:     "client-cache-key",
		ServiceTier:        "priority",
		Instructions:       "Be terse",
		Input: ResponsesInput{
			Items: []ResponsesInputItem{
				{
					Role: "user",
					Content: MessageContent{
						{Type: "text", Text: "Summarize this"},
					},
				},
				{
					Type:       "function_call_output",
					CallID:     "call_1",
					OutputText: `{"ok":true}`,
				},
			},
		},
		ToolChoice: toolChoice,
		Text: &ResponsesText{
			Format: &ResponsesTextFormat{
				Type:   "json_schema",
				Name:   "summary",
				Schema: map[string]any{"type": "object"},
				Strict: true,
			},
		},
	}

	normalized, err := Responses(request, nil)
	if err != nil {
		t.Fatalf("Responses() error = %v", err)
	}

	if normalized.Model != "gpt-5.4" {
		t.Fatalf("model = %q", normalized.Model)
	}
	if normalized.ServiceTier != "priority" {
		t.Fatalf("service_tier = %q, want priority", normalized.ServiceTier)
	}
	if normalized.PreviousResponseID != "resp_prev" {
		t.Fatalf("previous_response_id = %q", normalized.PreviousResponseID)
	}
	if normalized.PromptCacheKey != "client-cache-key" {
		t.Fatalf("prompt_cache_key = %q, want client-cache-key", normalized.PromptCacheKey)
	}
	if normalized.Text == nil || normalized.Text.Format.Type != "json_schema" {
		t.Fatalf("text format = %#v", normalized.Text)
	}
	if len(normalized.Input) != 2 {
		t.Fatalf("input len = %d", len(normalized.Input))
	}
	if len(normalized.Include) != 0 {
		t.Fatalf("include = %#v, want empty when reasoning disabled", normalized.Include)
	}
}

func TestOpenAIServiceTierAutoUsesCodexDefault(t *testing.T) {
	t.Parallel()

	chat, err := ChatCompletions(ChatCompletionsRequest{
		Model:       "gpt-5.4",
		ServiceTier: "auto",
		Messages: []ChatMessage{{
			Role:    "user",
			Content: MessageContent{{Type: "text", Text: "Hello"}},
		}},
	}, nil)
	if err != nil {
		t.Fatalf("ChatCompletions() error = %v", err)
	}
	if chat.ServiceTier != "default" {
		t.Fatalf("ChatCompletions() service_tier = %q, want default", chat.ServiceTier)
	}

	response, err := Responses(ResponsesRequest{
		Model:       "gpt-5.4",
		ServiceTier: "auto",
		Input:       ResponsesInput{String: "Hello"},
	}, nil)
	if err != nil {
		t.Fatalf("Responses() error = %v", err)
	}
	if response.ServiceTier != "default" {
		t.Fatalf("Responses() service_tier = %q, want default", response.ServiceTier)
	}
}

func TestResponsesTranslationUsesReasoningObject(t *testing.T) {
	t.Parallel()

	request := ResponsesRequest{
		Model: "gpt-5.4",
		Reasoning: &Reasoning{
			Effort: "high",
		},
		Input: ResponsesInput{
			String: "Think carefully",
		},
	}

	normalized, err := Responses(request, nil)
	if err != nil {
		t.Fatalf("Responses() error = %v", err)
	}

	if normalized.Reasoning == nil {
		t.Fatalf("reasoning = nil, want explicit reasoning object to populate it")
	}
	if normalized.Reasoning.Effort != "high" {
		t.Fatalf("reasoning effort = %q, want high", normalized.Reasoning.Effort)
	}
	if normalized.Reasoning.Summary != "auto" {
		t.Fatalf("reasoning summary = %q, want auto", normalized.Reasoning.Summary)
	}
	if len(normalized.Include) != 1 || normalized.Include[0] != "reasoning.encrypted_content" {
		t.Fatalf("include = %#v, want reasoning.encrypted_content only", normalized.Include)
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

func TestCompactTranslationRejectsUnsupportedContentPart(t *testing.T) {
	t.Parallel()

	_, err := Compact(ResponsesCompactRequest{
		Input: ResponsesInput{
			Items: []ResponsesInputItem{{
				Role: "user",
				Content: MessageContent{{
					Type: "input_audio",
				}},
			}},
		},
	}, nil)

	if err == nil {
		t.Fatal("Compact() error = nil, want unsupported content part error")
	}

	var contentErr *UnsupportedContentPartError
	if !errors.As(err, &contentErr) {
		t.Fatalf("Compact() error = %T, want UnsupportedContentPartError", err)
	}
	if contentErr.PartType != "input_audio" {
		t.Fatalf("PartType = %q, want input_audio", contentErr.PartType)
	}
}

func TestResponsesTranslationExtractsInstructionRolesFromInput(t *testing.T) {
	t.Parallel()

	request := ResponsesRequest{
		Model:        "gpt-5.4",
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

	if normalized.Instructions != "Top-level instructions\n\nSystem rules\n\nDeveloper rules" {
		t.Fatalf("instructions = %q", normalized.Instructions)
	}
	if len(normalized.Input) != 1 {
		t.Fatalf("input len = %d, want 1", len(normalized.Input))
	}
	if normalized.Input[0].Role != "user" {
		t.Fatalf("input role = %q, want user", normalized.Input[0].Role)
	}
}

func TestResponsesTranslationAcceptsModernFunctionToolShape(t *testing.T) {
	t.Parallel()

	request := ResponsesRequest{
		Model: "gpt-5.4",
		Input: ResponsesInput{
			Items: []ResponsesInputItem{{
				Role: "user",
				Content: MessageContent{{
					Type: "text",
					Text: "Use the tool",
				}},
			}},
		},
		Tools: []ToolDefinition{{
			Type:        "function",
			Name:        "ping_tool",
			Description: "Echo a message",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"message": map[string]any{"type": "string"},
				},
			},
			Strict: true,
		}},
	}

	normalized, err := Responses(request, nil)
	if err != nil {
		t.Fatalf("Responses() error = %v", err)
	}

	if len(normalized.Tools) != 1 {
		t.Fatalf("tools len = %d, want 1", len(normalized.Tools))
	}
	if normalized.Tools[0].Name != "ping_tool" {
		t.Fatalf("tool name = %q, want ping_tool", normalized.Tools[0].Name)
	}
	if !normalized.Tools[0].Strict {
		t.Fatal("expected strict function tool passthrough")
	}
}

func TestChatCompletionsTranslationPreservesCustomToolShape(t *testing.T) {
	t.Parallel()

	request := ChatCompletionsRequest{
		Model: "gpt-5.4",
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
		Model: "gpt-5.4",
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
		Model: "gpt-5.4",
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
				Model: "gpt-5.4",
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

func TestResponsesTranslationAcceptsAssistantOutputTextReplay(t *testing.T) {
	t.Parallel()

	request := ResponsesRequest{
		Model: "gpt-5.4",
		Input: ResponsesInput{
			Items: []ResponsesInputItem{
				{
					Role: "assistant",
					Content: MessageContent{{
						Type: "output_text",
						Text: "remembered response",
					}},
				},
				{
					Role: "user",
					Content: MessageContent{{
						Type: "text",
						Text: "follow up",
					}},
				},
			},
		},
	}

	normalized, err := Responses(request, nil)
	if err != nil {
		t.Fatalf("Responses() error = %v", err)
	}

	if len(normalized.Input) != 2 {
		t.Fatalf("input len = %d, want 2", len(normalized.Input))
	}
	parts := normalized.Input[0].Content
	if len(parts) != 1 {
		t.Fatalf("assistant replay content = %#v", normalized.Input[0].Content)
	}
	if parts[0].Type != "output_text" {
		t.Fatalf("assistant replay part type = %q, want output_text", parts[0].Type)
	}
}

func TestResponsesTranslationPreservesWebSearchCallReplayIdentity(t *testing.T) {
	t.Parallel()

	request := ResponsesRequest{
		Model: "gpt-5.4",
		Input: ResponsesInput{
			Items: []ResponsesInputItem{
				{
					Role: "user",
					Content: MessageContent{{
						Type: "text",
						Text: "hello",
					}},
				},
				{
					Type:   "web_search_call",
					ID:     "ws_1",
					Status: "completed",
				},
			},
		},
	}

	normalized, err := Responses(request, nil)
	if err != nil {
		t.Fatalf("Responses() error = %v", err)
	}

	if len(normalized.Input) != 2 {
		t.Fatalf("input len = %d, want 2", len(normalized.Input))
	}
	if normalized.Input[1].Type != "web_search_call" {
		t.Fatalf("input[1].Type = %q, want web_search_call", normalized.Input[1].Type)
	}
	if normalized.Input[1].ID != "ws_1" {
		t.Fatalf("input[1].ID = %q, want ws_1", normalized.Input[1].ID)
	}
	if normalized.Input[1].Status != "completed" {
		t.Fatalf("input[1].Status = %q, want completed", normalized.Input[1].Status)
	}
}

func TestResponsesTranslationPreservesToolOutputContentTypes(t *testing.T) {
	t.Parallel()

	request := ResponsesRequest{
		Model: "gpt-5.4",
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

func TestResponsesTranslationAcceptsInputFilePart(t *testing.T) {
	t.Parallel()

	request := ResponsesRequest{
		Model: "gpt-5.4",
		Input: ResponsesInput{
			Items: []ResponsesInputItem{{
				Role: "user",
				Content: MessageContent{
					{Type: "text", Text: "Read the attachment"},
					{
						Type:     "input_file",
						FileData: "data:application/pdf;base64,SGVsbG8=",
						Filename: "sample.pdf",
					},
				},
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
	parts := normalized.Input[0].Content
	if len(parts) != 2 {
		t.Fatalf("file input content = %#v", normalized.Input[0].Content)
	}
	if parts[1].Type != "input_file" {
		t.Fatalf("file part type = %q, want input_file", parts[1].Type)
	}
	if parts[1].FileData == "" || parts[1].Filename != "sample.pdf" {
		t.Fatalf("file part = %#v", parts[1])
	}
}

func TestChatCompletionsTranslationAcceptsCanonicalFilePart(t *testing.T) {
	t.Parallel()

	var request ChatCompletionsRequest
	err := json.Unmarshal([]byte(`{
		"model":"gpt-5.4",
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
		Model: "gpt-5.4",
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
		Model: "gpt-5.4",
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

func TestResponsesShortensCustomToolNameAndChoice(t *testing.T) {
	t.Parallel()

	name := "custom_tool_with_a_name_that_is_deliberately_longer_than_sixty_four_characters_for_codex"
	choice, _ := json.Marshal(map[string]any{"type": "custom", "name": name})
	normalized, err := Responses(ResponsesRequest{
		Model: "gpt-5.4",
		Input: ResponsesInput{Items: []ResponsesInputItem{{
			Type: "custom_tool_call", CallID: "call_1", Name: name, Input: "input",
		}}},
		Tools:      []ToolDefinition{{Type: "custom", Name: name}},
		ToolChoice: choice,
	}, nil)
	if err != nil {
		t.Fatalf("Responses() error = %v", err)
	}

	shortName := normalized.Tools[0].Name
	if len(shortName) > 64 || shortName == name {
		t.Fatalf("short name = %q", shortName)
	}
	if normalized.Input[0].Name != shortName {
		t.Fatalf("input name = %q, want %q", normalized.Input[0].Name, shortName)
	}
	if string(normalized.ToolChoice) != `{"type":"custom","name":"`+shortName+`"}` {
		t.Fatalf("tool choice = %s", normalized.ToolChoice)
	}
	if normalized.ToolNameAliases[shortName] != name {
		t.Fatalf("tool aliases = %#v", normalized.ToolNameAliases)
	}
}

func TestResponsesTranslationAcceptsReasoningItemReplay(t *testing.T) {
	t.Parallel()

	request := ResponsesRequest{
		Model: "gpt-5.4",
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
		Model: "gpt-5.4",
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

func TestChatCompletionsTranslationRejectsInvalidToolCallContent(t *testing.T) {
	t.Parallel()

	_, err := ChatCompletions(ChatCompletionsRequest{
		Model: "gpt-5.4",
		Messages: []ChatMessage{{
			Role: "assistant",
			Content: MessageContent{{
				Type: "image_url",
			}},
			ToolCalls: []ToolCall{{
				ID:   "call_1",
				Type: "function",
				Function: FunctionPayload{
					Name:      "lookup_weather",
					Arguments: `{"city":"SF"}`,
				},
			}},
		}},
	}, nil)

	if err == nil {
		t.Fatal("expected invalid tool call content error")
	}
	if got := err.Error(); got != "image_url.url or image_url.file_id is required" {
		t.Fatalf("error = %q, want image_url URL or file ID requirement", got)
	}
}

func TestUnsupportedModelRejected(t *testing.T) {
	t.Parallel()

	_, err := ChatCompletions(ChatCompletionsRequest{
		Model: "codex",
		Messages: []ChatMessage{{
			Role:    "user",
			Content: MessageContent{{Type: "text", Text: "hello"}},
		}},
	}, nil)

	if err == nil {
		t.Fatal("expected unsupported model error")
	}
}

func TestResponsesTranslationLeavesModelEmptyWhenOmitted(t *testing.T) {
	t.Parallel()

	normalized, err := Responses(ResponsesRequest{
		Input: ResponsesInput{
			String: "hello",
		},
	}, nil)

	if err != nil {
		t.Fatalf("Responses() error = %v", err)
	}

	if normalized.Model != "" {
		t.Fatalf("model = %q, want empty when omitted", normalized.Model)
	}
	if normalized.ModelExplicit {
		t.Fatal("ModelExplicit = true, want false when model is omitted")
	}
}

func TestToCodexWSCreatePayloadIncludesTurnControls(t *testing.T) {
	t.Parallel()

	generate := false
	parallel := false
	request := turn.NormalizedRequest{
		Request: codex.Request{
			Model:             "gpt-5.4",
			Input:             []codex.InputItem{{Role: "user"}},
			ParallelToolCalls: &parallel,
		},
		Generate: &generate,
	}
	payload := request.ToCodexWSCreatePayload()
	if payload["generate"] != false || payload["parallel_tool_calls"] != false {
		t.Fatalf("payload = %#v", payload)
	}
}
