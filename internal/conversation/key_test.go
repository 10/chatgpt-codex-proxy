package conversation

import (
	"testing"

	"chatgpt-codex-proxy/internal/turn"
)

func TestDeriveIgnoresLeadingSystemReminderBlocks(t *testing.T) {
	t.Parallel()

	base := turn.Request{
		Model:        "gpt-5.4",
		Instructions: "Be concise.",
		Input: []turn.InputItem{{
			Role: "user",
			Content: []turn.ContentPart{{
				Type: "input_text",
				Text: "hello world",
			}},
		}},
	}
	withReminder := base
	withReminder.Input = []turn.InputItem{{
		Role: "user",
		Content: []turn.ContentPart{{
			Type: "input_text",
			Text: "<system-reminder>internal</system-reminder>\nhello world",
		}},
	}}

	if Derive(base) != Derive(withReminder) {
		t.Fatal("Derive() changed when only a leading system-reminder block was added")
	}
}

func TestDeriveChangesForDifferentFirstUserText(t *testing.T) {
	t.Parallel()

	first := turn.Request{
		Model:        "gpt-5.4",
		Instructions: "Be concise.",
		Input: []turn.InputItem{{
			Role: "user",
			Content: []turn.ContentPart{{
				Type: "input_text",
				Text: "hello world",
			}},
		}},
	}
	second := first
	second.Input = []turn.InputItem{{
		Role: "user",
		Content: []turn.ContentPart{{
			Type: "input_text",
			Text: "different text",
		}},
	}}

	if Derive(first) == Derive(second) {
		t.Fatal("Derive() stayed the same for different first user text")
	}
}

func TestDeriveChangesForDifferentEstablishedConversationHistory(t *testing.T) {
	t.Parallel()

	first := turn.Request{
		Model:        "gpt-5.4",
		Instructions: "Be concise.",
		Input: []turn.InputItem{
			{
				Role: "user",
				Content: []turn.ContentPart{{
					Type: "input_text",
					Text: "hello world",
				}},
			},
			{
				Role: "assistant",
				Content: []turn.ContentPart{{
					Type: "output_text",
					Text: "assistant one",
				}},
			},
			{
				Role: "user",
				Content: []turn.ContentPart{{
					Type: "input_text",
					Text: "follow up",
				}},
			},
		},
	}
	second := first
	second.Input = []turn.InputItem{
		first.Input[0],
		{
			Role: "assistant",
			Content: []turn.ContentPart{{
				Type: "output_text",
				Text: "assistant two",
			}},
		},
		first.Input[2],
	}

	if Derive(first) == Derive(second) {
		t.Fatal("Derive() stayed the same for different established assistant history")
	}
}
