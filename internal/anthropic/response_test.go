package anthropic

import (
	"encoding/json"
	"strings"
	"testing"

	"chatgpt-codex-proxy/internal/codex"
	"chatgpt-codex-proxy/internal/translate"
)

func TestBuildMessageMapsTextThinkingToolsAndUsage(t *testing.T) {
	t.Parallel()

	normalized := translate.NormalizedRequest{Request: codex.Request{Model: "gpt-5.4", Include: []string{"reasoning.encrypted_content"}}, ToolNameAliases: map[string]string{"short": "original_long_name"}}
	accumulator := translate.NewAccumulator(normalized)
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

	accumulator := translate.NewAccumulator(translate.NormalizedRequest{Request: codex.Request{Model: "gpt-5.4"}})
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

	accumulator := translate.NewAccumulator(translate.NormalizedRequest{Request: codex.Request{Model: "gpt-5.4"}})
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

	accumulator := translate.NewAccumulator(translate.NormalizedRequest{Request: codex.Request{Model: "gpt-5.4"}})
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
	accumulator := translate.NewAccumulator(translate.NormalizedRequest{Request: codex.Request{Model: "gpt-5.4"}})
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

	accumulator := translate.NewAccumulator(translate.NormalizedRequest{Request: codex.Request{
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

	accumulator := translate.NewAccumulator(translate.NormalizedRequest{Request: codex.Request{
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
