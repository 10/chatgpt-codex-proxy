package anthropic

import (
	"strings"
	"testing"

	"chatgpt-codex-proxy/internal/codex"
	"chatgpt-codex-proxy/internal/turn"
)

func TestCountInputTokensCountsEffectiveCodexInput(t *testing.T) {
	t.Parallel()

	count, err := CountInputTokens(turn.NormalizedRequest{Request: codex.Request{
		Model:        "gpt-5.4",
		Instructions: "Be concise.",
		Input:        []codex.InputItem{{Role: "user", Content: []codex.ContentPart{{Type: "input_text", Text: "Hello, world."}}}},
		Tools:        []codex.Tool{{Type: "function", Name: "weather", Description: "Get weather", Parameters: map[string]any{"type": "object"}}},
	}})
	if err != nil {
		t.Fatalf("CountInputTokens() error = %v", err)
	}
	if count <= 0 {
		t.Fatalf("count = %d, want positive", count)
	}
}

func TestCountInputTokensDoesNotTokenizeBase64ImagePayloads(t *testing.T) {
	t.Parallel()

	count := func(payload string) int64 {
		value, err := CountInputTokens(turn.NormalizedRequest{Request: codex.Request{
			Model: "gpt-5.4",
			Input: []codex.InputItem{{Role: "user", Content: []codex.ContentPart{{
				Type: "input_image", ImageURL: "data:image/png;base64," + payload,
			}}}},
		}})
		if err != nil {
			t.Fatalf("CountInputTokens() error = %v", err)
		}
		return value
	}

	small := count(strings.Repeat("A", 16))
	large := count(strings.Repeat("A", 100_000))
	if small != large || small < estimatedImageTokens {
		t.Fatalf("image token estimates = small:%d large:%d", small, large)
	}
}
