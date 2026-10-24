package turn

import (
	"testing"
)

func TestResponsesObjectMergesFunctionCallsWithExistingOutputInOrder(t *testing.T) {
	t.Parallel()

	accumulator := NewAccumulator(NormalizedRequest{
		Request: Request{
			Model: "gpt-5.4",
		},
	})
	accumulator.ResponseID = "resp_merge"
	accumulator.ToolCalls = []*ToolCallState{
		{
			ItemID:      "fc_late",
			CallID:      "call_late",
			Name:        "late_tool",
			Arguments:   `{"value":2}`,
			OutputIndex: 1,
			Status:      "completed",
		},
		{
			ItemID:      "fc_early",
			CallID:      "call_early",
			Name:        "early_tool",
			Arguments:   `{"value":1}`,
			OutputIndex: 0,
			Status:      "completed",
		},
	}
	accumulator.OutputItems = []*outputItemState{{
		OutputIndex: 2,
		Item: map[string]any{
			"id":     "msg_123",
			"type":   "message",
			"role":   "assistant",
			"status": "completed",
			"content": []map[string]any{{
				"type": "output_text",
				"text": "done",
			}},
		},
	}}

	response := accumulator.ResponsesObject()
	output := response["output"].([]map[string]any)
	if len(output) != 3 {
		t.Fatalf("output len = %d, want 3", len(output))
	}
	if output[0]["type"] != "function_call" {
		t.Fatalf("output[0].type = %#v, want function_call", output[0]["type"])
	}
	if output[0]["call_id"] != "call_early" {
		t.Fatalf("output[0].call_id = %#v, want call_early", output[0]["call_id"])
	}
	if output[0]["status"] != "completed" {
		t.Fatalf("output[0].status = %#v, want completed", output[0]["status"])
	}
	if output[0]["id"] != "fc_early" {
		t.Fatalf("output[0].id = %#v, want fc_early", output[0]["id"])
	}
	if output[0]["name"] != "early_tool" {
		t.Fatalf("output[0].name = %#v, want early_tool", output[0]["name"])
	}
	if output[0]["arguments"] != `{"value":1}` {
		t.Fatalf("output[0].arguments = %#v", output[0]["arguments"])
	}
	if output[1]["call_id"] != "call_late" {
		t.Fatalf("output[1].call_id = %#v, want call_late", output[1]["call_id"])
	}
	if output[2]["type"] != "message" {
		t.Fatalf("output[2].type = %#v, want message", output[2]["type"])
	}
}

func TestResponsesObjectPreservesNativeResponseFields(t *testing.T) {
	t.Parallel()

	accumulator := NewAccumulator(NormalizedRequest{
		Request: Request{Model: "gpt-5.4"},
	})
	accumulator.Apply(&StreamEvent{
		Type: "response.completed",
		Raw: map[string]any{
			"response": map[string]any{
				"id":         "resp_native",
				"object":     "response",
				"created_at": int64(1_700_000_000),
				"model":      "gpt-5.4",
				"status":     "completed",
				"metadata":   map[string]any{"request_kind": "compatibility_test"},
				"future_field": map[string]any{
					"enabled": true,
				},
				"output_text": "done",
				"usage": map[string]any{
					"input_tokens":  3,
					"output_tokens": 2,
					"future_detail": "preserve-me",
				},
			},
		},
	})

	response := accumulator.ResponsesObject()
	if response["created_at"] != int64(1_700_000_000) {
		t.Fatalf("created_at = %#v, want preserved timestamp", response["created_at"])
	}
	metadata, _ := response["metadata"].(map[string]any)
	if metadata["request_kind"] != "compatibility_test" {
		t.Fatalf("metadata = %#v, want preserved metadata", metadata)
	}
	futureField, _ := response["future_field"].(map[string]any)
	if futureField["enabled"] != true {
		t.Fatalf("future_field = %#v, want preserved unknown field", futureField)
	}
	usage, _ := response["usage"].(map[string]any)
	if usage["future_detail"] != "preserve-me" {
		t.Fatalf("usage.future_detail = %#v, want preserved unknown usage field", usage["future_detail"])
	}
}

func TestIncompleteResponseDoesNotCompletePartialToolCall(t *testing.T) {
	t.Parallel()

	accumulator := NewAccumulator(NormalizedRequest{Request: Request{Model: "gpt-5.4"}})
	accumulator.Apply(&StreamEvent{
		Type: "response.function_call_arguments.delta",
		Raw: map[string]any{
			"response_id":  "resp_partial_tool",
			"item_id":      "fc_partial",
			"call_id":      "call_partial",
			"output_index": 0,
			"name":         "lookup",
			"delta":        `{"query":`,
		},
	})
	accumulator.Apply(&StreamEvent{
		Type: "response.incomplete",
		Raw: map[string]any{"response": map[string]any{
			"id":                 "resp_partial_tool",
			"model":              "gpt-5.4",
			"status":             "incomplete",
			"incomplete_details": map[string]any{"reason": "max_output_tokens"},
			"output": []any{map[string]any{
				"id":        "fc_partial",
				"type":      "function_call",
				"call_id":   "call_partial",
				"name":      "lookup",
				"arguments": `{"query":`,
				"status":    "incomplete",
			}},
		}},
	})

	if events := accumulator.PendingResponseToolCallCompletionEvents(); len(events) != 0 {
		t.Fatalf("completion events = %#v, want none", events)
	}
	output := accumulator.ResponsesObject()["output"].([]map[string]any)
	if len(output) != 1 || output[0]["status"] != "incomplete" {
		t.Fatalf("output = %#v", output)
	}
}

func TestChatCompletionObjectIncludesGeneratedImages(t *testing.T) {
	t.Parallel()

	accumulator := NewAccumulator(NormalizedRequest{Request: Request{Model: "gpt-5.4"}})
	accumulator.Apply(&StreamEvent{
		Type: "response.completed",
		Raw: map[string]any{"response": map[string]any{
			"id":     "resp_image",
			"model":  "gpt-5.4",
			"status": "completed",
			"output": []any{map[string]any{
				"id":            "ig_1",
				"type":          "image_generation_call",
				"output_format": "jpeg",
				"result":        "aGVsbG8=",
			}},
		}},
	})

	response := accumulator.ChatCompletionObject()
	choices, _ := response["choices"].([]map[string]any)
	message, _ := choices[0]["message"].(map[string]any)
	images, _ := message["images"].([]map[string]any)
	if len(images) != 1 {
		t.Fatalf("images = %#v, want one image", message["images"])
	}
	imageURL, _ := images[0]["image_url"].(map[string]any)
	if imageURL["url"] != "data:image/jpeg;base64,aGVsbG8=" {
		t.Fatalf("image URL = %#v", imageURL["url"])
	}
}
