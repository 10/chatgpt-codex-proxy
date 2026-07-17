package anthropic

import (
	"strings"
	"testing"

	"chatgpt-codex-proxy/internal/codex"
	"chatgpt-codex-proxy/internal/translate"
)

func TestStreamEncoderEmitsAnthropicEventOrder(t *testing.T) {
	t.Parallel()

	accumulator := translate.NewAccumulator(translate.NormalizedRequest{Request: codex.Request{Model: "gpt-5.4"}})
	encoder := NewStreamEncoder(0)
	events := applyAndEncode(accumulator, encoder,
		&codex.StreamEvent{Type: "response.created", Raw: map[string]any{"response": map[string]any{"id": "resp_stream", "model": "gpt-5.4"}}},
		&codex.StreamEvent{Type: "response.output_text.delta", Raw: map[string]any{"delta": "hello"}},
		&codex.StreamEvent{Type: "response.output_text.done", Raw: map[string]any{"text": "hello"}},
		&codex.StreamEvent{Type: "response.completed", Raw: map[string]any{"response": map[string]any{
			"id": "resp_stream", "model": "gpt-5.4", "status": "completed", "output_text": "hello",
			"usage": map[string]any{"input_tokens": 5, "output_tokens": 1},
		}}},
	)

	want := []string{"message_start", "content_block_start", "content_block_delta", "content_block_stop", "message_delta", "message_stop"}
	assertStreamTypes(t, events, want)
	message := events[0]["message"].(MessageResponse)
	if message.ID != "msg_stream" || message.StopReason != nil {
		t.Fatalf("message_start = %#v", message)
	}
	delta := events[len(events)-2]["delta"].(map[string]any)
	if delta["stop_reason"] != "end_turn" {
		t.Fatalf("message_delta = %#v", events[len(events)-2])
	}
}

func TestStreamEncoderStreamsFragmentedToolJSON(t *testing.T) {
	t.Parallel()

	accumulator := translate.NewAccumulator(translate.NormalizedRequest{Request: codex.Request{Model: "gpt-5.4"}})
	encoder := NewStreamEncoder(0)
	events := applyAndEncode(accumulator, encoder,
		&codex.StreamEvent{Type: "response.output_item.added", Raw: map[string]any{"output_index": 0, "item": map[string]any{"type": "function_call", "id": "item_1", "call_id": "call_1", "name": "weather", "arguments": ""}}},
		&codex.StreamEvent{Type: "response.function_call_arguments.delta", Raw: map[string]any{"item_id": "item_1", "output_index": 0, "delta": `{"city":`}},
		&codex.StreamEvent{Type: "response.function_call_arguments.delta", Raw: map[string]any{"item_id": "item_1", "output_index": 0, "delta": `"Paris"}`}},
		&codex.StreamEvent{Type: "response.function_call_arguments.done", Raw: map[string]any{"item_id": "item_1", "output_index": 0, "arguments": `{"city":"Paris"}`}},
		&codex.StreamEvent{Type: "response.output_item.done", Raw: map[string]any{"output_index": 0, "item": map[string]any{"type": "function_call", "id": "item_1", "call_id": "call_1", "name": "weather", "arguments": `{"city":"Paris"}`}}},
		&codex.StreamEvent{Type: "response.completed", Raw: map[string]any{"response": map[string]any{"id": "resp_tool", "status": "completed", "output": []any{map[string]any{"type": "function_call", "id": "item_1", "call_id": "call_1", "name": "weather", "arguments": `{"city":"Paris"}`}}}}},
	)

	want := []string{"message_start", "content_block_start", "content_block_delta", "content_block_delta", "content_block_stop", "message_delta", "message_stop"}
	assertStreamTypes(t, events, want)
	start := events[1]["content_block"].(map[string]any)
	if start["type"] != "tool_use" || start["id"] != "call_1" || start["name"] != "weather" {
		t.Fatalf("tool start = %#v", start)
	}
	if events[2]["delta"].(map[string]any)["partial_json"] != `{"city":` || events[3]["delta"].(map[string]any)["partial_json"] != `"Paris"}` {
		t.Fatalf("tool deltas = %#v %#v", events[2], events[3])
	}
	if events[len(events)-2]["delta"].(map[string]any)["stop_reason"] != "tool_use" {
		t.Fatalf("stop = %#v", events[len(events)-2])
	}
}

func TestStreamEncoderShortensLongToolUseIDs(t *testing.T) {
	t.Parallel()

	longID := "call_" + strings.Repeat("a", 80)
	accumulator := translate.NewAccumulator(translate.NormalizedRequest{Request: codex.Request{Model: "gpt-5.4"}})
	encoder := NewStreamEncoder(0)
	events := applyAndEncode(accumulator, encoder,
		&codex.StreamEvent{Type: "response.function_call_arguments.done", Raw: map[string]any{
			"item_id": "item_long", "call_id": longID, "name": "lookup", "arguments": `{}`,
		}},
		&codex.StreamEvent{Type: "response.completed", Raw: map[string]any{
			"response": map[string]any{"id": "resp_long_call", "status": "completed"},
		}},
	)

	start := events[1]["content_block"].(map[string]any)
	callID := start["id"].(string)
	if len(callID) > 64 || callID == longID {
		t.Fatalf("stream tool use ID = %q (len %d)", callID, len(callID))
	}
}

func TestStreamEncoderEmitsThinkingSignatureBeforeBlockStop(t *testing.T) {
	t.Parallel()

	accumulator := translate.NewAccumulator(translate.NormalizedRequest{Request: codex.Request{Model: "gpt-5.4", Include: []string{"reasoning.encrypted_content"}}})
	encoder := NewStreamEncoder(0)
	events := applyAndEncode(accumulator, encoder,
		&codex.StreamEvent{Type: "response.reasoning_summary_text.delta", Raw: map[string]any{"delta": "considering"}},
		&codex.StreamEvent{Type: "response.reasoning_summary_text.done", Raw: map[string]any{"text": "considering"}},
		&codex.StreamEvent{Type: "response.output_item.done", Raw: map[string]any{"item": map[string]any{
			"type": "reasoning", "summary": []any{map[string]any{"type": "summary_text", "text": "considering"}}, "encrypted_content": "signed",
		}}},
		&codex.StreamEvent{Type: "response.output_text.delta", Raw: map[string]any{"delta": "answer"}},
		&codex.StreamEvent{Type: "response.output_text.done", Raw: map[string]any{"text": "answer"}},
		&codex.StreamEvent{Type: "response.completed", Raw: map[string]any{"response": map[string]any{"id": "resp_reasoning", "status": "completed", "output_text": "answer"}}},
	)

	want := []string{
		"message_start",
		"content_block_start", "content_block_delta", "content_block_delta", "content_block_stop",
		"content_block_start", "content_block_delta", "content_block_stop",
		"message_delta", "message_stop",
	}
	assertStreamTypes(t, events, want)
	signature := events[3]["delta"].(map[string]any)
	if signature["type"] != "signature_delta" || signature["signature"] != "signed" {
		t.Fatalf("signature delta = %#v", signature)
	}
}

func TestStreamEncoderEmitsSignatureOnlyThinkingBlockBeforeText(t *testing.T) {
	t.Parallel()

	accumulator := translate.NewAccumulator(translate.NormalizedRequest{Request: codex.Request{Model: "gpt-5.4", Include: []string{"reasoning.encrypted_content"}}})
	encoder := NewStreamEncoder(0)
	events := applyAndEncode(accumulator, encoder,
		&codex.StreamEvent{Type: "response.output_item.done", Raw: map[string]any{"item": map[string]any{
			"type": "reasoning", "encrypted_content": "signed",
		}}},
		&codex.StreamEvent{Type: "response.output_text.delta", Raw: map[string]any{"delta": "answer"}},
		&codex.StreamEvent{Type: "response.output_text.done", Raw: map[string]any{"text": "answer"}},
		&codex.StreamEvent{Type: "response.completed", Raw: map[string]any{"response": map[string]any{"id": "resp_reasoning", "status": "completed", "output_text": "answer"}}},
	)

	want := []string{
		"message_start",
		"content_block_start", "content_block_delta", "content_block_stop",
		"content_block_start", "content_block_delta", "content_block_stop",
		"message_delta", "message_stop",
	}
	assertStreamTypes(t, events, want)
	firstBlock := events[1]["content_block"].(map[string]any)
	if firstBlock["type"] != "thinking" {
		t.Fatalf("first content block = %#v, want thinking", firstBlock)
	}
	if events[2]["delta"].(map[string]any)["type"] != "signature_delta" {
		t.Fatalf("thinking delta = %#v", events[2])
	}
}

func TestStreamEncoderBuffersInterleavedParallelToolCalls(t *testing.T) {
	t.Parallel()

	accumulator := translate.NewAccumulator(translate.NormalizedRequest{Request: codex.Request{Model: "gpt-5.4"}})
	encoder := NewStreamEncoder(0)
	events := applyAndEncode(accumulator, encoder,
		&codex.StreamEvent{Type: "response.function_call_arguments.delta", Raw: map[string]any{"item_id": "item_a", "call_id": "call_a", "name": "first", "delta": `{"a":`}},
		&codex.StreamEvent{Type: "response.function_call_arguments.delta", Raw: map[string]any{"item_id": "item_b", "call_id": "call_b", "name": "second", "delta": `{"b":`}},
		&codex.StreamEvent{Type: "response.function_call_arguments.delta", Raw: map[string]any{"item_id": "item_a", "call_id": "call_a", "delta": `1}`}},
		&codex.StreamEvent{Type: "response.function_call_arguments.done", Raw: map[string]any{"item_id": "item_a", "call_id": "call_a", "arguments": `{"a":1}`}},
		&codex.StreamEvent{Type: "response.function_call_arguments.delta", Raw: map[string]any{"item_id": "item_b", "call_id": "call_b", "delta": `2}`}},
		&codex.StreamEvent{Type: "response.function_call_arguments.done", Raw: map[string]any{"item_id": "item_b", "call_id": "call_b", "arguments": `{"b":2}`}},
		&codex.StreamEvent{Type: "response.completed", Raw: map[string]any{"response": map[string]any{"id": "resp_parallel", "status": "completed"}}},
	)

	want := []string{
		"message_start",
		"content_block_start", "content_block_delta", "content_block_delta", "content_block_stop",
		"content_block_start", "content_block_delta", "content_block_delta", "content_block_stop",
		"message_delta", "message_stop",
	}
	assertStreamTypes(t, events, want)
	firstStart := events[1]["content_block"].(map[string]any)
	secondStart := events[5]["content_block"].(map[string]any)
	if firstStart["id"] != "call_a" || secondStart["id"] != "call_b" {
		t.Fatalf("tool starts = %#v %#v", firstStart, secondStart)
	}
	firstJSON := events[2]["delta"].(map[string]any)["partial_json"].(string) + events[3]["delta"].(map[string]any)["partial_json"].(string)
	secondJSON := events[6]["delta"].(map[string]any)["partial_json"].(string) + events[7]["delta"].(map[string]any)["partial_json"].(string)
	if firstJSON != `{"a":1}` || secondJSON != `{"b":2}` {
		t.Fatalf("tool JSON = %q %q", firstJSON, secondJSON)
	}
}

func TestStreamEncoderFlushesActiveToolBeforeTerminalFallbackText(t *testing.T) {
	t.Parallel()

	accumulator := translate.NewAccumulator(translate.NormalizedRequest{Request: codex.Request{Model: "gpt-5.4"}})
	encoder := NewStreamEncoder(0)
	events := applyAndEncode(accumulator, encoder,
		&codex.StreamEvent{Type: "response.function_call_arguments.delta", Raw: map[string]any{"item_id": "item_1", "call_id": "call_1", "name": "lookup", "delta": `{"query":`}},
		&codex.StreamEvent{Type: "response.completed", Raw: map[string]any{"response": map[string]any{
			"id": "resp_terminal", "status": "completed", "output_text": "fallback",
			"output": []any{map[string]any{"type": "function_call", "id": "item_1", "call_id": "call_1", "name": "lookup", "arguments": `{"query":"docs"}`}},
		}}},
	)

	want := []string{
		"message_start",
		"content_block_start", "content_block_delta", "content_block_delta", "content_block_stop",
		"content_block_start", "content_block_delta", "content_block_stop",
		"message_delta", "message_stop",
	}
	assertStreamTypes(t, events, want)
	firstJSON := events[2]["delta"].(map[string]any)["partial_json"].(string) + events[3]["delta"].(map[string]any)["partial_json"].(string)
	if firstJSON != `{"query":"docs"}` {
		t.Fatalf("tool JSON = %q", firstJSON)
	}
	if block := events[5]["content_block"].(map[string]any); block["type"] != "text" {
		t.Fatalf("terminal fallback block = %#v, want text", block)
	}
}

func TestStreamEncoderSuppressesIncompleteTerminalToolCall(t *testing.T) {
	t.Parallel()

	accumulator := translate.NewAccumulator(translate.NormalizedRequest{Request: codex.Request{Model: "gpt-5.4"}})
	encoder := NewStreamEncoder(0)
	events := applyAndEncode(accumulator, encoder,
		&codex.StreamEvent{Type: "response.completed", Raw: map[string]any{"response": map[string]any{
			"id": "resp_partial_tool", "status": "incomplete",
			"incomplete_details": map[string]any{"reason": "max_output_tokens"},
			"output": []any{map[string]any{
				"type": "function_call", "id": "item_1", "call_id": "call_1", "name": "lookup", "arguments": `{"query":`, "status": "incomplete",
			}},
		}}},
	)

	want := []string{"message_start", "message_delta", "message_stop"}
	assertStreamTypes(t, events, want)
	if stop := events[1]["delta"].(map[string]any)["stop_reason"]; stop != "max_tokens" {
		t.Fatalf("stop reason = %#v", stop)
	}
}

func TestStreamEncoderSuppressesIncompleteToolCallAfterDeltas(t *testing.T) {
	t.Parallel()

	accumulator := translate.NewAccumulator(translate.NormalizedRequest{Request: codex.Request{Model: "gpt-5.4"}})
	encoder := NewStreamEncoder(0)
	events := applyAndEncode(accumulator, encoder,
		&codex.StreamEvent{Type: "response.function_call_arguments.delta", Raw: map[string]any{
			"item_id": "item_1", "call_id": "call_1", "name": "lookup", "delta": `{"query":`,
		}},
		&codex.StreamEvent{Type: "response.completed", Raw: map[string]any{"response": map[string]any{
			"id": "resp_partial_tool", "status": "incomplete",
			"incomplete_details": map[string]any{"reason": "max_output_tokens"},
			"output": []any{map[string]any{
				"type": "function_call", "id": "item_1", "call_id": "call_1", "name": "lookup", "arguments": `{"query":`, "status": "incomplete",
			}},
		}}},
	)

	want := []string{"message_start", "message_delta", "message_stop"}
	assertStreamTypes(t, events, want)
}

func TestStreamEncoderSuppressesThinkingWhenNotEnabled(t *testing.T) {
	t.Parallel()

	accumulator := translate.NewAccumulator(translate.NormalizedRequest{Request: codex.Request{
		Model: "gpt-5.4", Reasoning: &codex.Reasoning{Effort: "low"},
	}})
	encoder := NewStreamEncoder(0)
	events := applyAndEncode(accumulator, encoder,
		&codex.StreamEvent{Type: "response.reasoning_summary_text.delta", Raw: map[string]any{"delta": "hidden"}},
		&codex.StreamEvent{Type: "response.output_item.done", Raw: map[string]any{"item": map[string]any{
			"type": "reasoning", "summary": []any{map[string]any{"type": "summary_text", "text": "hidden"}}, "encrypted_content": "signed",
		}}},
		&codex.StreamEvent{Type: "response.output_text.delta", Raw: map[string]any{"delta": "answer"}},
		&codex.StreamEvent{Type: "response.output_text.done", Raw: map[string]any{"text": "answer"}},
		&codex.StreamEvent{Type: "response.completed", Raw: map[string]any{"response": map[string]any{"id": "resp_hidden_reasoning", "status": "completed", "output_text": "answer"}}},
	)

	want := []string{"message_start", "content_block_start", "content_block_delta", "content_block_stop", "message_delta", "message_stop"}
	assertStreamTypes(t, events, want)
	if block := events[1]["content_block"].(map[string]any); block["type"] != "text" {
		t.Fatalf("content block = %#v, want text", block)
	}
}

func TestStreamEncoderSuppressesUnsignedThinking(t *testing.T) {
	t.Parallel()

	accumulator := translate.NewAccumulator(translate.NormalizedRequest{Request: codex.Request{
		Model: "gpt-5.4", Include: []string{"reasoning.encrypted_content"},
	}})
	encoder := NewStreamEncoder(0)
	events := applyAndEncode(accumulator, encoder,
		&codex.StreamEvent{Type: "response.reasoning_summary_text.delta", Raw: map[string]any{"delta": "unsigned"}},
		&codex.StreamEvent{Type: "response.output_item.done", Raw: map[string]any{"item": map[string]any{
			"type": "reasoning", "summary": []any{map[string]any{"type": "summary_text", "text": "unsigned"}},
		}}},
		&codex.StreamEvent{Type: "response.output_text.delta", Raw: map[string]any{"delta": "answer"}},
		&codex.StreamEvent{Type: "response.output_text.done", Raw: map[string]any{"text": "answer"}},
		&codex.StreamEvent{Type: "response.completed", Raw: map[string]any{"response": map[string]any{"id": "resp_unsigned", "status": "completed", "output_text": "answer"}}},
	)

	want := []string{"message_start", "content_block_start", "content_block_delta", "content_block_stop", "message_delta", "message_stop"}
	assertStreamTypes(t, events, want)
	if block := events[1]["content_block"].(map[string]any); block["type"] != "text" {
		t.Fatalf("content block = %#v, want text", block)
	}
}

func applyAndEncode(accumulator *translate.Accumulator, encoder *StreamEncoder, input ...*codex.StreamEvent) []StreamEvent {
	var result []StreamEvent
	for _, event := range input {
		accumulator.Apply(event)
		result = append(result, encoder.Events(event, accumulator)...)
	}
	return result
}

func assertStreamTypes(t *testing.T, events []StreamEvent, want []string) {
	t.Helper()
	if len(events) != len(want) {
		t.Fatalf("event count = %d, want %d: %#v", len(events), len(want), events)
	}
	for index := range want {
		if events[index]["type"] != want[index] {
			t.Fatalf("event[%d] = %q, want %q", index, events[index]["type"], want[index])
		}
	}
}
