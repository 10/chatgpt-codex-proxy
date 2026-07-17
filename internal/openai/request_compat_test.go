package openai

import (
	"encoding/json"
	"testing"
)

func TestChatCompletionsTranslationAcceptsLegacyFunctionsAndChoice(t *testing.T) {
	t.Parallel()

	request := ChatCompletionsRequest{
		Model: "gpt-5.4",
		Messages: []ChatMessage{{
			Role:    "user",
			Content: MessageContent{{Type: "text", Text: "Call the function"}},
		}},
		Functions: []LegacyFunctionDefinition{{
			Name:       "lookup_weather",
			Parameters: map[string]any{"type": "object"},
		}},
		FunctionCall: &LegacyFunctionCallChoice{Name: "lookup_weather"},
	}

	normalized, err := ChatCompletions(request, nil)
	if err != nil {
		t.Fatalf("ChatCompletions() error = %v", err)
	}

	if len(normalized.Tools) != 1 {
		t.Fatalf("tools len = %d, want 1", len(normalized.Tools))
	}
	if normalized.Tools[0].Name != "lookup_weather" {
		t.Fatalf("tool name = %q, want lookup_weather", normalized.Tools[0].Name)
	}
	if string(normalized.ToolChoice) != `{"type":"function","name":"lookup_weather"}` {
		t.Fatalf("tool choice = %s", string(normalized.ToolChoice))
	}
}

func TestChatCompletionsTranslationPrefersModernToolsAndToolChoice(t *testing.T) {
	t.Parallel()

	rawToolChoice, err := json.Marshal(map[string]any{
		"type": "function",
		"name": "modern_tool",
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	request := ChatCompletionsRequest{
		Model: "gpt-5.4",
		Messages: []ChatMessage{{
			Role:    "user",
			Content: MessageContent{{Type: "text", Text: "Use the tool"}},
		}},
		Tools: []ToolDefinition{{
			Type: "function",
			Function: &FunctionTool{
				Name:       "modern_tool",
				Parameters: map[string]any{"type": "object"},
			},
		}},
		Functions: []LegacyFunctionDefinition{{
			Name: "legacy_tool",
		}},
		ToolChoice:   rawToolChoice,
		FunctionCall: &LegacyFunctionCallChoice{Name: "legacy_tool"},
	}

	normalized, err := ChatCompletions(request, nil)
	if err != nil {
		t.Fatalf("ChatCompletions() error = %v", err)
	}

	if len(normalized.Tools) != 1 {
		t.Fatalf("tools len = %d, want 1", len(normalized.Tools))
	}
	if normalized.Tools[0].Name != "modern_tool" {
		t.Fatalf("tool name = %q, want modern_tool", normalized.Tools[0].Name)
	}
	if string(normalized.ToolChoice) != `{"type":"function","name":"modern_tool"}` {
		t.Fatalf("tool choice = %s", string(normalized.ToolChoice))
	}
}

func TestChatCompletionsTranslationSupportsJSONObject(t *testing.T) {
	t.Parallel()

	request := ChatCompletionsRequest{
		Model: "gpt-5.4",
		Messages: []ChatMessage{{
			Role:    "user",
			Content: MessageContent{{Type: "text", Text: "Return JSON"}},
		}},
		ResponseFormat: &ResponseFormat{Type: "json_object"},
	}

	normalized, err := ChatCompletions(request, nil)
	if err != nil {
		t.Fatalf("ChatCompletions() error = %v", err)
	}

	if normalized.Text == nil || normalized.Text.Format.Type != "json_object" {
		t.Fatalf("text format = %#v, want json_object", normalized.Text)
	}
}

func TestChatCompletionsTranslationPreparesSchemaAndWarnings(t *testing.T) {
	t.Parallel()

	request := ChatCompletionsRequest{
		Model: "gpt-5.4",
		Messages: []ChatMessage{{
			Role:    "user",
			Content: MessageContent{{Type: "text", Text: "Return structured data"}},
		}},
		ResponseFormat: &ResponseFormat{
			Type: "json_schema",
			JSONSchema: &JSONSchemaSpec{
				Name: "tuple_payload",
				Schema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"items": map[string]any{
							"type": "array",
							"prefixItems": []any{
								map[string]any{"type": "string"},
								map[string]any{"type": "object"},
							},
						},
						"nested": map[string]any{
							"type": "object",
						},
					},
				},
			},
		},
	}

	normalized, err := ChatCompletions(request, nil)
	if err != nil {
		t.Fatalf("ChatCompletions() error = %v", err)
	}

	if normalized.TupleSchema == nil {
		t.Fatal("expected tuple schema to be preserved")
	}
	schema := normalized.Text.Format.Schema
	rootProps, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema.properties = %#v", schema["properties"])
	}
	nested, _ := rootProps["nested"].(map[string]any)
	if nested["additionalProperties"] != false {
		t.Fatalf("nested additionalProperties = %#v, want false", nested["additionalProperties"])
	}
	items, _ := rootProps["items"].(map[string]any)
	itemProps, _ := items["properties"].(map[string]any)
	if _, ok := itemProps["0"]; !ok {
		t.Fatalf("tuple item properties = %#v, want numeric keys", itemProps)
	}
}

func TestResponsesTranslationPreparesSchemaAndWarnings(t *testing.T) {
	t.Parallel()

	request := ResponsesRequest{
		Model: "gpt-5.4",
		Input: ResponsesInput{
			Items: []ResponsesInputItem{{
				Role: "user",
				Content: MessageContent{{
					Type: "text",
					Text: "Return structured data",
				}},
			}},
		},
		Text: &ResponsesText{
			Format: &ResponsesTextFormat{
				Type: "json_schema",
				Name: "tuple_payload",
				Schema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"pair": map[string]any{
							"type": "array",
							"prefixItems": []any{
								map[string]any{"type": "string"},
								map[string]any{"type": "number"},
							},
						},
					},
				},
			},
		},
	}

	normalized, err := Responses(request, nil)
	if err != nil {
		t.Fatalf("Responses() error = %v", err)
	}

	if normalized.TupleSchema == nil {
		t.Fatal("expected tuple schema to be preserved")
	}
	pair, _ := normalized.Text.Format.Schema["properties"].(map[string]any)["pair"].(map[string]any)
	if pair["type"] != "object" {
		t.Fatalf("pair.type = %#v, want object", pair["type"])
	}
}
