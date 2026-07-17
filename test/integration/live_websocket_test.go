//go:build live

package integration_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

func TestLiveResponsesWebSocketContinuation(t *testing.T) {
	cfg := loadLiveConfig(t)
	endpoint := strings.TrimRight(cfg.BaseURL, "/") + "/responses"
	endpoint = strings.Replace(endpoint, "https://", "wss://", 1)
	endpoint = strings.Replace(endpoint, "http://", "ws://", 1)

	conn, response, err := websocket.DefaultDialer.Dial(endpoint, http.Header{
		"Authorization": []string{"Bearer " + cfg.APIKey},
	})
	if err != nil {
		if response != nil {
			response.Body.Close()
		}
		t.Fatalf("connect responses websocket: %v", err)
	}
	defer conn.Close()

	firstID := runLiveResponsesWebSocketTurn(t, conn, map[string]any{
		"type":  "response.create",
		"model": cfg.Model,
		"input": "Reply with exactly ALPHA.",
	})
	if firstID == "" {
		t.Fatal("first WebSocket turn returned no response ID")
	}

	secondID := runLiveResponsesWebSocketTurn(t, conn, map[string]any{
		"type":                 "response.create",
		"model":                cfg.Model,
		"previous_response_id": firstID,
		"input":                "Reply with exactly BETA.",
	})
	if secondID == "" || secondID == firstID {
		t.Fatalf("second response ID = %q, want a new non-empty ID", secondID)
	}
}

func runLiveResponsesWebSocketTurn(t *testing.T, conn *websocket.Conn, request map[string]any) string {
	t.Helper()
	if err := conn.WriteJSON(request); err != nil {
		t.Fatalf("write response.create: %v", err)
	}

	responseID := ""
	for {
		var event map[string]any
		if err := conn.ReadJSON(&event); err != nil {
			t.Fatalf("read responses websocket event: %v", err)
		}
		eventType, _ := event["type"].(string)
		if eventType == "error" {
			t.Fatalf("responses websocket returned error: %#v", event)
		}
		if id, _ := event["response_id"].(string); id != "" {
			responseID = id
		}
		if response, _ := event["response"].(map[string]any); response != nil {
			if id, _ := response["id"].(string); id != "" {
				responseID = id
			}
		}
		if eventType == "response.completed" {
			return responseID
		}
	}
}
