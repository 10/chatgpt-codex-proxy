package anthropic

import (
	"encoding/json"
	"strings"
	"testing"

	"chatgpt-codex-proxy/internal/codex"
	"chatgpt-codex-proxy/internal/jsonutil"
	"chatgpt-codex-proxy/internal/openai"
	"chatgpt-codex-proxy/internal/turn"
)

func TestBuildMessageMapsTextThinkingToolsAndUsage(t *testing.T) {
	t.Parallel()

	normalized := turn.NormalizedRequest{Request: codex.Request{Model: "gpt-5.4", Include: []string{"reasoning.encrypted_content"}}, ToolNameAliases: map[string]string{"short": "original_long_name"}}
	accumulator := turn.NewAccumulator(normalized)
	accumulator.Apply(&codex.StreamEvent{Type: "response.completed", Raw: map[string]any{
		"response": map[string]any{
			"id":     "resp_123",
			"model":  "gpt-5.4",
			"status": "completed",
			"usage":  map[string]any{"input_tokens": 100, "output_tokens": 25, "input_tokens_details": map[string]any{"cached_tokens": 40}},
			"output": []any{
				map[string]any{"type": "reasoning", "summary": []any{map[string]any{"type": "summary_text", "text": "I considered it."}}, "encrypted_content": "signature"},
				map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "Calling a tool."}}},
				map[string]any{"type": "function_call", "call_id": "call_1", "name": "short", "arguments": `{"city":"Paris"}`},
			},
		},
	}})

	message := BuildMessage(accumulator)
	if message.ID != "msg_123" || message.Type != "message" || message.Role != "assistant" {
		t.Fatalf("message metadata = %#v", message)
	}
	if len(message.Content) != 3 {
		t.Fatalf("content = %#v", message.Content)
	}
	if message.Content[0].Type != "thinking" || message.Content[0].Signature != "signature" {
		t.Fatalf("thinking = %#v", message.Content[0])
	}
	if message.Content[1].Type != "text" || message.Content[1].Text != "Calling a tool." {
		t.Fatalf("text = %#v", message.Content[1])
	}
	if message.Content[2].Type != "tool_use" || message.Content[2].Name != "original_long_name" {
		t.Fatalf("tool = %#v", message.Content[2])
	}
	if message.StopReason == nil || *message.StopReason != "tool_use" {
		t.Fatalf("stop reason = %#v", message.StopReason)
	}
	if message.Usage.InputTokens != 60 || message.Usage.CacheReadInputTokens != 40 || message.Usage.OutputTokens != 25 {
		t.Fatalf("usage = %#v", message.Usage)
	}
}

func TestBuildMessageMapsIncompleteToMaxTokens(t *testing.T) {
	t.Parallel()

	accumulator := turn.NewAccumulator(turn.NormalizedRequest{Request: codex.Request{Model: "gpt-5.4"}})
	accumulator.Apply(&codex.StreamEvent{Type: "response.completed", Raw: map[string]any{
		"response": map[string]any{
			"id":                 "resp_limit",
			"status":             "incomplete",
			"incomplete_details": map[string]any{"reason": "max_output_tokens"},
			"output_text":        "partial",
		},
	}})

	message := BuildMessage(accumulator)
	if message.StopReason == nil || *message.StopReason != "max_tokens" {
		t.Fatalf("stop reason = %#v", message.StopReason)
	}
	if len(message.Content) != 1 || message.Content[0].Text != "partial" {
		t.Fatalf("content = %#v", message.Content)
	}
}

func TestBuildMessageDoesNotExposeIncompleteToolCallAsToolUse(t *testing.T) {
	t.Parallel()

	accumulator := turn.NewAccumulator(turn.NormalizedRequest{Request: codex.Request{Model: "gpt-5.4"}})
	accumulator.Apply(&codex.StreamEvent{Type: "response.completed", Raw: map[string]any{
		"response": map[string]any{
			"id":                 "resp_partial_tool",
			"status":             "incomplete",
			"incomplete_details": map[string]any{"reason": "max_output_tokens"},
			"output": []any{map[string]any{
				"type": "function_call", "call_id": "call_1", "name": "weather", "arguments": `{"city":`, "status": "incomplete",
			}},
		},
	}})

	message := BuildMessage(accumulator)
	if message.StopReason == nil || *message.StopReason != "max_tokens" {
		t.Fatalf("stop reason = %#v", message.StopReason)
	}
	if len(message.Content) != 0 {
		t.Fatalf("content = %#v, want no executable tool block", message.Content)
	}
}

func TestBuildMessagePreservesLargeToolArgumentNumbers(t *testing.T) {
	t.Parallel()

	accumulator := turn.NewAccumulator(turn.NormalizedRequest{Request: codex.Request{Model: "gpt-5.4"}})
	accumulator.Apply(&codex.StreamEvent{Type: "response.completed", Raw: map[string]any{
		"response": map[string]any{
			"id": "resp_large_id", "status": "completed",
			"output": []any{map[string]any{
				"type": "function_call", "call_id": "call_1", "name": "lookup", "arguments": `{"id":9007199254740993}`, "status": "completed",
			}},
		},
	}})

	encoded, err := json.Marshal(BuildMessage(accumulator))
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if !strings.Contains(string(encoded), `"input":{"id":9007199254740993}`) {
		t.Fatalf("encoded message = %s", encoded)
	}
}

func TestBuildMessageShortensLongToolUseIDs(t *testing.T) {
	t.Parallel()

	longID := "call_" + strings.Repeat("a", 80)
	accumulator := turn.NewAccumulator(turn.NormalizedRequest{Request: codex.Request{Model: "gpt-5.4"}})
	accumulator.Apply(&codex.StreamEvent{Type: "response.completed", Raw: map[string]any{
		"response": map[string]any{
			"id": "resp_long_call", "status": "completed",
			"output": []any{map[string]any{
				"type": "function_call", "call_id": longID, "name": "lookup", "arguments": `{}`, "status": "completed",
			}},
		},
	}})

	message := BuildMessage(accumulator)
	if len(message.Content) != 1 {
		t.Fatalf("content = %#v", message.Content)
	}
	callID := message.Content[0].ID
	if len(callID) > 64 || callID == longID {
		t.Fatalf("tool use ID = %q (len %d)", callID, len(callID))
	}
}

func TestBuildMessageSerializesSignatureOnlyThinkingWithEmptyText(t *testing.T) {
	t.Parallel()

	accumulator := turn.NewAccumulator(turn.NormalizedRequest{Request: codex.Request{
		Model: "gpt-5.4", Include: []string{"reasoning.encrypted_content"},
	}})
	accumulator.Apply(&codex.StreamEvent{Type: "response.completed", Raw: map[string]any{
		"response": map[string]any{
			"id": "resp_omitted_thinking", "status": "completed",
			"output": []any{map[string]any{"type": "reasoning", "encrypted_content": "signed"}},
		},
	}})

	encoded, err := json.Marshal(BuildMessage(accumulator))
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if !strings.Contains(string(encoded), `{"type":"thinking","thinking":"","signature":"signed"}`) {
		t.Fatalf("encoded message = %s", encoded)
	}
}

func TestBuildMessageOmitsUnsignedThinkingSummary(t *testing.T) {
	t.Parallel()

	accumulator := turn.NewAccumulator(turn.NormalizedRequest{Request: codex.Request{
		Model: "gpt-5.4", Include: []string{"reasoning.encrypted_content"},
	}})
	accumulator.Apply(&codex.StreamEvent{Type: "response.completed", Raw: map[string]any{
		"response": map[string]any{
			"id": "resp_unsigned_thinking", "status": "completed", "output_text": "answer",
			"output": []any{map[string]any{"type": "reasoning", "summary": []any{map[string]any{"type": "summary_text", "text": "unsigned"}}}},
		},
	}})

	message := BuildMessage(accumulator)
	if len(message.Content) != 1 || message.Content[0].Type != "text" || message.Content[0].Text != "answer" {
		t.Fatalf("content = %#v", message.Content)
	}
}

func TestBuildMessageMapsHostedWebSearch(t *testing.T) {
	t.Parallel()

	accumulator := turn.NewAccumulator(turn.NormalizedRequest{Request: codex.Request{Model: "gpt-5.4"}})
	accumulator.Apply(&codex.StreamEvent{Type: "response.completed", Raw: map[string]any{
		"response": map[string]any{
			"id": "resp_search", "model": "gpt-5.4", "status": "completed",
			"output": []any{
				map[string]any{
					"type": "web_search_call", "id": "ws_1", "status": "completed",
					"action": map[string]any{"type": "search", "query": "Codex API"},
					"results": []any{
						map[string]any{"title": "OpenAI Codex", "url": "https://openai.com/codex", "page_age": "2 days"},
						map[string]any{"url": "https://platform.openai.com/docs"},
					},
				},
				map[string]any{
					"type": "message", "role": "assistant", "status": "completed",
					"content": []any{map[string]any{"type": "output_text", "text": "Here is what I found."}},
				},
			},
		},
	}})

	message := BuildMessage(accumulator)
	if len(message.Content) != 3 {
		t.Fatalf("content = %#v, want server tool use, result, and text", message.Content)
	}
	use := message.Content[0]
	if use.Type != "server_tool_use" || use.ID != "ws_1" || use.Name != "web_search" || string(use.Input) != `{"query":"Codex API"}` {
		t.Fatalf("server tool use = %#v", use)
	}
	result := message.Content[1]
	if result.Type != "web_search_tool_result" || result.ToolUseID != "ws_1" || len(result.Content) != 2 {
		t.Fatalf("web search result = %#v", result)
	}
	if result.Content[0]["type"] != "web_search_result" || result.Content[0]["title"] != "OpenAI Codex" || result.Content[0]["page_age"] != "2 days" {
		t.Fatalf("first search result = %#v", result.Content[0])
	}
	if result.Content[1]["title"] != "https://platform.openai.com/docs" || result.Content[1]["page_age"] != nil {
		t.Fatalf("second search result = %#v", result.Content[1])
	}
	if message.Content[2].Type != "text" || message.Content[2].Text != "Here is what I found." {
		t.Fatalf("text = %#v", message.Content[2])
	}
	if message.StopReason == nil || *message.StopReason != "end_turn" {
		t.Fatalf("stop reason = %#v, want end_turn", message.StopReason)
	}
}

func TestBuildMessageDoesNotReportFailedHostedWebSearchAsEmptySuccess(t *testing.T) {
	t.Parallel()

	accumulator := turn.NewAccumulator(turn.NormalizedRequest{Request: codex.Request{Model: "gpt-5.4"}})
	accumulator.Apply(&codex.StreamEvent{Type: "response.completed", Raw: map[string]any{
		"response": map[string]any{
			"id": "resp_search_failed", "model": "gpt-5.4", "status": "completed",
			"output": []any{
				map[string]any{
					"type": "web_search_call", "id": "ws_failed", "status": "failed",
					"action": map[string]any{"type": "search", "query": "Codex API"},
				},
				map[string]any{
					"type": "message", "role": "assistant", "status": "completed",
					"content": []any{map[string]any{"type": "output_text", "text": "Search was unavailable."}},
				},
			},
		},
	}})

	message := BuildMessage(accumulator)
	if len(message.Content) != 1 || message.Content[0].Type != "text" || message.Content[0].Text != "Search was unavailable." {
		t.Fatalf("content = %#v, want only the explanatory text", message.Content)
	}
}

func TestBuildMessageRemovesSyntheticNullOptionalFields(t *testing.T) {
	t.Parallel()

	accumulator := structuredOutputAccumulator(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"result":        map[string]any{"type": "string"},
			"impossible":    map[string]any{"type": "boolean"},
			"explicit_null": map[string]any{"type": []any{"string", "null"}},
			"maybe":         map[string]any{"type": []any{"string", "null"}},
			"details": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"note": map[string]any{"type": "string"},
				},
			},
		},
		"required": []any{"result", "explicit_null", "details"},
	})
	accumulator.Apply(&codex.StreamEvent{Type: "response.completed", Raw: map[string]any{
		"response": map[string]any{
			"id": "resp_structured", "status": "completed",
			"output": []any{map[string]any{
				"type": "message",
				"content": []any{map[string]any{
					"type": "output_text",
					"text": `{"result":"ok","impossible":null,"explicit_null":null,"maybe":null,"details":{"note":null}}`,
				}},
			}},
		},
	}})

	message := BuildMessage(accumulator)
	if len(message.Content) != 1 || message.Content[0].Text != `{"details":{},"explicit_null":null,"maybe":null,"result":"ok"}` {
		t.Fatalf("content = %#v", message.Content)
	}
}

func TestNormalizedOutputTextRestoresNestedFieldsInOptionalObjects(t *testing.T) {
	t.Parallel()

	accumulator := structuredOutputAccumulator(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"details": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"note": map[string]any{"type": "string"},
				},
			},
			"entries": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"note": map[string]any{"type": "string"},
					},
				},
			},
		},
	})

	if got := normalizedOutputText(accumulator, `{"details":{"note":null},"entries":[{"note":null}]}`); got != `{"details":{},"entries":[{}]}` {
		t.Fatalf("normalized output = %s", got)
	}
}

func TestNormalizedOutputTextUsesMatchingUnionBranch(t *testing.T) {
	t.Parallel()

	accumulator := structuredOutputAccumulator(map[string]any{
		"anyOf": []any{
			map[string]any{
				"type": "object",
				"properties": map[string]any{
					"kind":  map[string]any{"type": "string", "const": "a"},
					"value": map[string]any{"type": "string"},
				},
				"required": []any{"kind"},
			},
			map[string]any{
				"type": "object",
				"properties": map[string]any{
					"kind":  map[string]any{"type": "string", "const": "b"},
					"value": map[string]any{"type": []any{"string", "null"}},
				},
				"required": []any{"kind", "value"},
			},
		},
	})

	if got := normalizedOutputText(accumulator, `{"kind":"a","value":null}`); got != `{"kind":"a"}` {
		t.Fatalf("optional branch = %s", got)
	}
	if got := normalizedOutputText(accumulator, `{"kind":"b","value":null}`); got != `{"kind":"b","value":null}` {
		t.Fatalf("nullable branch = %s", got)
	}
}

func TestNormalizedOutputTextUsesSchemaConstraintsToMatchUnionBranch(t *testing.T) {
	t.Parallel()

	accumulator := structuredOutputAccumulator(map[string]any{
		"anyOf": []any{
			map[string]any{
				"type": "object",
				"properties": map[string]any{
					"kind":  map[string]any{"type": "string", "pattern": "^a$"},
					"value": map[string]any{"type": "string"},
				},
				"required": []any{"kind"},
			},
			map[string]any{
				"type": "object",
				"properties": map[string]any{
					"kind":  map[string]any{"type": "string", "pattern": "^b$"},
					"value": map[string]any{"type": []any{"string", "null"}},
				},
				"required": []any{"kind", "value"},
			},
		},
	})

	if got := normalizedOutputText(accumulator, `{"kind":"a","value":null}`); got != `{"kind":"a"}` {
		t.Fatalf("optional branch = %s", got)
	}
	if got := normalizedOutputText(accumulator, `{"kind":"b","value":null}`); got != `{"kind":"b","value":null}` {
		t.Fatalf("nullable branch = %s", got)
	}
}

func TestSchemaMatchesValueUsesJSONNumericSemantics(t *testing.T) {
	t.Parallel()

	for _, value := range []json.Number{"1", "1.0", "1e0"} {
		if !newSchemaMatcher().matches(map[string]any{"const": float64(1)}, value) {
			t.Fatalf("const 1 did not match %q", value)
		}
		if !newSchemaMatcher().matches(map[string]any{"type": "integer"}, value) {
			t.Fatalf("integer did not match %q", value)
		}
	}
	if newSchemaMatcher().matches(map[string]any{"type": "integer"}, json.Number("1.5")) {
		t.Fatal("integer matched 1.5")
	}
}

func TestSchemaMatcherCompilesEachSchemaOnce(t *testing.T) {
	t.Parallel()

	schema := map[string]any{"type": "integer", "minimum": 1}
	matcher := newSchemaMatcher()
	if !matcher.matches(schema, json.Number("1")) || !matcher.matches(schema, json.Number("2")) {
		t.Fatal("schema did not match valid integers")
	}
	if len(matcher.compiled) != 1 {
		t.Fatalf("compiled schemas = %d, want 1", len(matcher.compiled))
	}
}

func structuredOutputAccumulator(schema map[string]any) *turn.Accumulator {
	responseSchema := openai.NormalizeSchema(schema)
	strictSchema := jsonutil.CloneMap(responseSchema)
	if err := makeOpenAIStrictSchemaNode(strictSchema, newSchemaMatcher()); err != nil {
		panic(err)
	}
	return turn.NewAccumulator(turn.NormalizedRequest{
		Request: codex.Request{
			Model: "gpt-5.4",
			Text: &codex.TextConfig{Format: codex.TextFormat{
				Type: "json_schema", Schema: strictSchema, Strict: true,
			}},
		},
		ResponseSchema: responseSchema,
	})
}
