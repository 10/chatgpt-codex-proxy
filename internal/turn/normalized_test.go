package turn

import "testing"

func TestStripReasoningEncryptedContent(t *testing.T) {
	t.Parallel()

	req := NormalizedRequest{
		Request: Request{
			Model: "gpt-5.4",
			Input: []InputItem{
				{Type: "reasoning", EncryptedContent: "foreign-signature", Summary: []ReasoningPart{{Type: "summary_text", Text: "s"}}},
				{Role: "user", Content: []ContentPart{{Type: "input_text", Text: "hi"}}},
				{Type: "reasoning", ID: "rs_1"}, // no encrypted_content -> kept
			},
		},
	}

	sanitized, changed := req.StripReasoningEncryptedContent()
	if !changed {
		t.Fatal("changed = false, want true")
	}
	if len(sanitized.Input) != 2 {
		t.Fatalf("len(input) = %d, want 2", len(sanitized.Input))
	}
	for _, item := range sanitized.Input {
		if item.Type == "reasoning" && item.EncryptedContent != "" {
			t.Fatalf("encrypted reasoning item survived: %+v", item)
		}
	}
	// Original request is untouched (pure function).
	if len(req.Input) != 3 {
		t.Fatalf("original input mutated: len = %d, want 3", len(req.Input))
	}
}

func TestStripReasoningEncryptedContentNoChange(t *testing.T) {
	t.Parallel()

	req := NormalizedRequest{
		Request: Request{
			Input: []InputItem{
				{Role: "user", Content: []ContentPart{{Type: "input_text", Text: "hi"}}},
				{Type: "reasoning", ID: "rs_1"},
			},
		},
	}

	sanitized, changed := req.StripReasoningEncryptedContent()
	if changed {
		t.Fatal("changed = true, want false")
	}
	if len(sanitized.Input) != 2 {
		t.Fatalf("len(input) = %d, want 2", len(sanitized.Input))
	}
}
