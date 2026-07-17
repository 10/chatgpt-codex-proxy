package openai

import (
	"testing"

	"chatgpt-codex-proxy/internal/jsonutil"
)

func TestPatchChatCompletionObjectForTuple(t *testing.T) {
	t.Parallel()

	object := map[string]any{
		"choices": []any{
			map[string]any{
				"message": map[string]any{
					"role":    "assistant",
					"content": `{"pair":{"0":"left","1":2}}`,
				},
			},
		},
	}
	schema := map[string]any{
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
	}

	if err := PatchChatCompletionObjectForTuple(object, schema); err != nil {
		t.Fatalf("PatchChatCompletionObjectForTuple() error = %v", err)
	}

	choice := jsonutil.SliceOfMaps(object["choices"])[0]
	message, _ := choice["message"].(map[string]any)
	if message["content"] != `{"pair":["left",2]}` {
		t.Fatalf("message.content = %#v, want reconverted tuple JSON", message["content"])
	}
}

func TestPatchResponsesObjectForTuple(t *testing.T) {
	t.Parallel()

	object := map[string]any{
		"output_text": `{"pair":{"0":"left","1":2}}`,
		"output": []any{
			map[string]any{
				"type": "message",
				"content": []any{
					map[string]any{
						"type": "output_text",
						"text": `{"pair":{"0":"left","1":2}}`,
					},
				},
			},
		},
	}
	schema := map[string]any{
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
	}

	if err := PatchResponsesObjectForTuple(object, schema); err != nil {
		t.Fatalf("PatchResponsesObjectForTuple() error = %v", err)
	}

	if object["output_text"] != `{"pair":["left",2]}` {
		t.Fatalf("output_text = %#v, want reconverted tuple JSON", object["output_text"])
	}
	content := jsonutil.SliceOfMaps(jsonutil.SliceOfMaps(object["output"])[0]["content"])
	if content[0]["text"] != `{"pair":["left",2]}` {
		t.Fatalf("content[0].text = %#v, want reconverted tuple JSON", content[0]["text"])
	}
}
