package openai

import (
	"encoding/json"
	"testing"
)

func TestToolDefinitionRoundTripPreservesCustomFields(t *testing.T) {
	t.Parallel()

	var tool ToolDefinition
	err := json.Unmarshal([]byte(`{
		"type": "custom",
		"name": "ApplyPatch",
		"description": "Patch a file",
		"format": {
			"type": "grammar",
			"definition": "start: item+"
		},
		"metadata": {
			"origin": "cursor"
		}
	}`), &tool)
	if err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	if tool.Type != "custom" {
		t.Fatalf("Type = %q, want custom", tool.Type)
	}
	if tool.Name != "ApplyPatch" {
		t.Fatalf("Name = %q, want ApplyPatch", tool.Name)
	}
	if got := tool.Format["type"]; got != "grammar" {
		t.Fatalf("Format[type] = %#v, want grammar", got)
	}
	var metadata map[string]any
	if err := json.Unmarshal(tool.ExtraFields["metadata"], &metadata); err != nil {
		t.Fatalf("metadata unmarshal error = %v", err)
	}
	if got := metadata["origin"]; got != "cursor" {
		t.Fatalf("ExtraFields[metadata][origin] = %#v, want cursor", got)
	}

	data, err := json.Marshal(tool)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("round-trip unmarshal error = %v", err)
	}
	if got := payload["type"]; got != "custom" {
		t.Fatalf("payload[type] = %#v, want custom", got)
	}
	format, _ := payload["format"].(map[string]any)
	if got := format["definition"]; got != "start: item+" {
		t.Fatalf("payload[format][definition] = %#v, want grammar definition", got)
	}
	roundTripMetadata, _ := payload["metadata"].(map[string]any)
	if got := roundTripMetadata["origin"]; got != "cursor" {
		t.Fatalf("payload[metadata][origin] = %#v, want cursor", got)
	}
}
