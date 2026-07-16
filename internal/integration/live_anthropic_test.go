//go:build live

package integration_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
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

func TestLiveAnthropicStreamingEventOrder(t *testing.T) {
	cfg := loadLiveConfig(t)
	body := postAnthropicJSON(t, cfg, "/messages", map[string]any{
		"model":      cfg.Model,
		"max_tokens": 64,
		"stream":     true,
		"messages": []map[string]any{{
			"role": "user", "content": "Reply with exactly STREAM_LIVE_OK",
		}},
	})
	text := string(body)
	for _, eventType := range []string{"message_start", "content_block_start", "content_block_delta", "content_block_stop", "message_delta", "message_stop"} {
		if !strings.Contains(text, "event: "+eventType) {
			t.Fatalf("stream missing %s: %s", eventType, text)
		}
	}
	if strings.Contains(text, "[DONE]") {
		t.Fatalf("Anthropic stream contained OpenAI sentinel: %s", text)
	}
}

func postAnthropicJSON(t *testing.T, cfg liveConfig, path string, payload any) []byte {
	t.Helper()

	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(cfg.BaseURL, "/")+path, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("X-API-Key", cfg.APIKey)
	req.Header.Set("Anthropic-Version", "2023-06-01")
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 90 * time.Second}
	response, err := client.Do(req)
	if err != nil {
		t.Fatalf("request %s failed: %v", path, err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	if response.StatusCode >= http.StatusBadRequest {
		t.Fatalf("request %s returned %d: %s", path, response.StatusCode, string(responseBody))
	}
	return responseBody
}
