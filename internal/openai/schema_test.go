package openai

import "testing"

func TestNormalizeSchemaInlinesRefsAndInjectsAdditionalProperties(t *testing.T) {
	t.Parallel()

	normalized := NormalizeSchema(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"candidates": map[string]any{
				"type": "array",
				"items": map[string]any{
					"$ref": "#/$defs/LeadCandidateIdentity",
				},
			},
		},
		"$defs": map[string]any{
			"LeadCandidateIdentity": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"business_name": map[string]any{"type": "string"},
					"website": map[string]any{
						"anyOf": []any{
							map[string]any{"type": "string"},
							map[string]any{"type": "null"},
						},
					},
				},
			},
		},
	})

	if _, ok := normalized["$defs"]; ok {
		t.Fatalf("$defs still present in normalized schema: %#v", normalized["$defs"])
	}
	rootProps, _ := normalized["properties"].(map[string]any)
	candidates, _ := rootProps["candidates"].(map[string]any)
	items, _ := candidates["items"].(map[string]any)
	if items["$ref"] != nil {
		t.Fatalf("items.$ref = %#v, want inlined schema", items["$ref"])
	}
	if items["additionalProperties"] != false {
		t.Fatalf("items.additionalProperties = %#v, want false", items["additionalProperties"])
	}
	itemProps, _ := items["properties"].(map[string]any)
	website, _ := itemProps["website"].(map[string]any)
	anyOf, _ := website["anyOf"].([]any)
	if len(anyOf) != 2 {
		t.Fatalf("website.anyOf len = %d, want 2", len(anyOf))
	}
}

func TestNormalizeSchemaStopsAtRecursiveRefs(t *testing.T) {
	t.Parallel()

	normalized := NormalizeSchema(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"head": map[string]any{"$ref": "#/$defs/node"},
		},
		"$defs": map[string]any{
			"node": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"next": map[string]any{"$ref": "#/$defs/node"},
				},
			},
		},
	})

	rootProperties, _ := normalized["properties"].(map[string]any)
	head, _ := rootProperties["head"].(map[string]any)
	headProperties, _ := head["properties"].(map[string]any)
	next, _ := headProperties["next"].(map[string]any)
	if next["$ref"] != "#/$defs/node" {
		t.Fatalf("next.$ref = %#v, want recursive ref", next["$ref"])
	}
}
