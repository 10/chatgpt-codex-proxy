//go:build live

package integration_test

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLiveAnthropicMessagesAndTokenCount(t *testing.T) {
	cfg := loadLiveConfig(t)

	countBody := postAnthropicJSON(t, cfg, "/messages/count_tokens", map[string]any{
		"model": cfg.Model,
		"messages": []map[string]any{{
			"role": "user", "content": "Reply with exactly ANTHROPIC_LIVE_OK",
		}},
	})
	var countResponse struct {
		InputTokens int64 `json:"input_tokens"`
	}
	if err := json.Unmarshal(countBody, &countResponse); err != nil || countResponse.InputTokens <= 0 {
		t.Fatalf("decode token count: err=%v body=%s", err, string(countBody))
	}

	messageBody := postAnthropicJSON(t, cfg, "/messages", map[string]any{
		"model":      cfg.Model,
		"max_tokens": 64,
		"messages": []map[string]any{{
			"role": "user", "content": "Reply with exactly ANTHROPIC_LIVE_OK",
		}},
	})
	var message struct {
		Type       string `json:"type"`
		Role       string `json:"role"`
		StopReason string `json:"stop_reason"`
		Content    []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(messageBody, &message); err != nil {
		t.Fatalf("decode message: %v body=%s", err, string(messageBody))
	}
	if message.Type != "message" || message.Role != "assistant" || message.StopReason != "end_turn" {
		t.Fatalf("unexpected message envelope: %#v", message)
	}
	if len(message.Content) == 0 || strings.TrimSpace(message.Content[len(message.Content)-1].Text) != "ANTHROPIC_LIVE_OK" {
		t.Fatalf("unexpected message content: %#v", message.Content)
	}
}

func postAnthropicJSON(t *testing.T, cfg liveConfig, path string, payload any) []byte {
	return postJSON(t, cfg, path, payload, map[string]string{
		"X-API-Key": cfg.APIKey, "Anthropic-Version": "2023-06-01",
	})
}
