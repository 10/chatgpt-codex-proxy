package jsonutil

import "testing"

func TestFirstNonEmptySkipsWhitespaceOnlyStrings(t *testing.T) {
	t.Parallel()

	got := FirstNonEmpty("", "   ", " value ", "fallback")
	if got != " value " {
		t.Fatalf("FirstNonEmpty() = %q, want original first non-empty string", got)
	}
}

func TestMapValue(t *testing.T) {
	t.Parallel()

	tree := map[string]any{
		"response": map[string]any{
			"error": map[string]any{
				"message": "boom",
			},
		},
	}

	if got := MapValue(tree, "response"); got == nil {
		t.Fatalf("MapValue() = %#v, want nested map", got)
	}
}

func TestFirstMapSkipsEmptyMaps(t *testing.T) {
	t.Parallel()

	want := map[string]any{"id": "value"}
	if got := FirstMap(nil, map[string]any{}, want); StringValue(got["id"]) != "value" {
		t.Fatalf("FirstMap() = %#v, want %#v", got, want)
	}
}

func TestCloneMapDeepClonesNestedMaps(t *testing.T) {
	t.Parallel()

	src := map[string]any{
		"outer": map[string]any{
			"inner": "value",
		},
	}

	cloned := CloneMap(src)
	clonedOuter := cloned["outer"].(map[string]any)
	clonedOuter["inner"] = "changed"

	if src["outer"].(map[string]any)["inner"] != "value" {
		t.Fatal("CloneMap() did not isolate nested map mutations")
	}
}

func TestCloneMapDeepClonesMapsInsideSlices(t *testing.T) {
	t.Parallel()

	src := map[string]any{
		"items": []any{
			map[string]any{
				"nested": map[string]any{"name": "original"},
			},
		},
	}

	cloned := CloneMap(src)
	clonedItems := cloned["items"].([]any)
	clonedFirst := clonedItems[0].(map[string]any)
	clonedFirst["nested"].(map[string]any)["name"] = "changed"

	srcItems := src["items"].([]any)
	srcFirst := srcItems[0].(map[string]any)
	if srcFirst["nested"].(map[string]any)["name"] != "original" {
		t.Fatal("CloneMap() did not isolate maps inside slices")
	}
}

func TestSliceOfMaps(t *testing.T) {
	t.Parallel()

	raw := []any{
		map[string]any{"id": "a"},
		"skip",
		map[string]any{"id": "b"},
	}

	got := SliceOfMaps(raw)
	if len(got) != 2 {
		t.Fatalf("SliceOfMaps() len = %d, want 2", len(got))
	}
	if got[0]["id"] != "a" || got[1]["id"] != "b" {
		t.Fatalf("SliceOfMaps() = %#v, want maps in order", got)
	}
}
