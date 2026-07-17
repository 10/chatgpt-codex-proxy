package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"chatgpt-codex-proxy/internal/accounts"
	"chatgpt-codex-proxy/internal/codex"
	"chatgpt-codex-proxy/internal/config"
	"chatgpt-codex-proxy/internal/models"
)

type fakeResponsesWebSocketStream struct {
	mu       sync.Mutex
	headers  http.Header
	turns    [][]*codex.StreamEvent
	active   []*codex.StreamEvent
	requests []map[string]any
	connects int
	closed   bool
}

func (f *fakeResponsesWebSocketStream) begin(body any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	var request map[string]any
	if err := json.Unmarshal(payload, &request); err != nil {
		return err
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, request)
	turn := len(f.requests) - 1
	if turn < len(f.turns) {
		f.active = append([]*codex.StreamEvent(nil), f.turns[turn]...)
	} else {
		f.active = nil
	}
	return nil
}

func (f *fakeResponsesWebSocketStream) SendJSON(body any) error {
	return f.begin(body)
}

func (f *fakeResponsesWebSocketStream) NextEvent() (*codex.StreamEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.active) == 0 {
		return nil, io.EOF
	}
	event := f.active[0]
	f.active = f.active[1:]
	return event, nil
}

func (f *fakeResponsesWebSocketStream) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func (f *fakeResponsesWebSocketStream) Headers() http.Header {
	return f.headers.Clone()
}

func TestResponsesWebSocketReusesUpstreamConnectionForContinuation(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const model = "gpt-5.4"
	fakeStream := &fakeResponsesWebSocketStream{
		headers: http.Header{"X-Codex-Turn-State": []string{"turn-state"}},
		turns: [][]*codex.StreamEvent{
			responsesWebSocketTextEvents("resp_ws_1", model, "first"),
			responsesWebSocketTextEvents("resp_ws_2", model, "second"),
		},
	}
	app := newResponsesWebSocketTestApp(t, fakeStream)

	server := httptest.NewServer(app.Handler())
	defer server.Close()

	headers := http.Header{"Authorization": []string{"Bearer test-key"}}
	conn, response, err := websocket.DefaultDialer.Dial(websocketTestURL(server.URL)+"/v1/responses", headers)
	if err != nil {
		if response != nil {
			response.Body.Close()
		}
		t.Fatalf("Dial() error = %v", err)
	}
	defer conn.Close()

	if err := conn.WriteJSON(map[string]any{
		"type":  "response.create",
		"model": model,
		"input": "first prompt",
	}); err != nil {
		t.Fatalf("first WriteJSON() error = %v", err)
	}
	first := readResponsesWebSocketTurn(t, conn)
	assertResponsesWebSocketEventTypes(t, first, "response.created", "response.output_text.delta", "response.completed")

	if err := conn.WriteJSON(map[string]any{
		"type": "response.append",
		"input": []map[string]any{{
			"type":    "message",
			"role":    "user",
			"content": []map[string]any{{"type": "input_text", "text": "second prompt"}},
		}},
	}); err != nil {
		t.Fatalf("second WriteJSON() error = %v", err)
	}
	second := readResponsesWebSocketTurn(t, conn)
	assertResponsesWebSocketEventTypes(t, second, "response.created", "response.output_text.delta", "response.completed")

	fakeStream.mu.Lock()
	requests := append([]map[string]any(nil), fakeStream.requests...)
	connects := fakeStream.connects
	fakeStream.mu.Unlock()
	if connects != 1 {
		t.Fatalf("upstream connection count = %d, want 1", connects)
	}
	if len(requests) != 2 {
		t.Fatalf("upstream request count = %d, want 2", len(requests))
	}
	if got := requests[1]["previous_response_id"]; got != "resp_ws_1" {
		t.Fatalf("second previous_response_id = %#v, want resp_ws_1", got)
	}
	if got := requests[1]["model"]; got != model {
		t.Fatalf("second model = %#v, want inherited %q", got, model)
	}
	input, ok := requests[1]["input"].([]any)
	if !ok || len(input) != 1 {
		t.Fatalf("second input = %#v, want one incremental item", requests[1]["input"])
	}
}

func TestResponsesWebSocketContinuesAfterIncompleteResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const model = "gpt-5.4"
	fakeStream := &fakeResponsesWebSocketStream{
		turns: [][]*codex.StreamEvent{
			{
				{
					Type: "response.created",
					Raw:  map[string]any{"response": map[string]any{"id": "resp_ws_incomplete", "model": model, "status": "in_progress"}},
				},
				{
					Type: "response.output_text.delta",
					Raw:  map[string]any{"response_id": "resp_ws_incomplete", "delta": "partial"},
				},
				{
					Type: "response.incomplete",
					Raw: map[string]any{"response": map[string]any{
						"id":                 "resp_ws_incomplete",
						"model":              model,
						"status":             "incomplete",
						"incomplete_details": map[string]any{"reason": "max_output_tokens"},
						"output_text":        "partial",
					}},
				},
			},
			responsesWebSocketTextEvents("resp_ws_after_incomplete", model, "continued"),
		},
	}
	app := newResponsesWebSocketTestApp(t, fakeStream)
	server := httptest.NewServer(app.Handler())
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial(websocketTestURL(server.URL)+"/v1/responses", http.Header{
		"Authorization": []string{"Bearer test-key"},
	})
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	defer conn.Close()

	if err := conn.WriteJSON(map[string]any{
		"type":  "response.create",
		"model": model,
		"input": "first prompt",
	}); err != nil {
		t.Fatalf("first WriteJSON() error = %v", err)
	}
	first := readResponsesWebSocketTurn(t, conn)
	assertResponsesWebSocketEventTypes(t, first, "response.created", "response.output_text.delta", "response.incomplete")
	incompleteResponse := nestedMapFromAny(first[len(first)-1]["response"])
	if incompleteResponse["status"] != "incomplete" || nestedMapFromAny(incompleteResponse["incomplete_details"])["reason"] != "max_output_tokens" {
		t.Fatalf("incomplete response = %#v", incompleteResponse)
	}

	if err := conn.WriteJSON(map[string]any{
		"type": "response.append",
		"input": []map[string]any{{
			"type":    "message",
			"role":    "user",
			"content": []map[string]any{{"type": "input_text", "text": "continue"}},
		}},
	}); err != nil {
		t.Fatalf("second WriteJSON() error = %v", err)
	}
	second := readResponsesWebSocketTurn(t, conn)
	assertResponsesWebSocketEventTypes(t, second, "response.created", "response.output_text.delta", "response.completed")

	fakeStream.mu.Lock()
	requests := append([]map[string]any(nil), fakeStream.requests...)
	fakeStream.mu.Unlock()
	if len(requests) != 2 || requests[1]["previous_response_id"] != "resp_ws_incomplete" {
		t.Fatalf("requests = %#v", requests)
	}
}

func TestResponsesWebSocketReturnsProtocolErrorsWithoutClosing(t *testing.T) {
	gin.SetMode(gin.TestMode)

	fakeStream := &fakeResponsesWebSocketStream{
		turns: [][]*codex.StreamEvent{responsesWebSocketTextEvents("resp_ws_valid", "gpt-5.4", "ok")},
	}
	app := newResponsesWebSocketTestApp(t, fakeStream)
	server := httptest.NewServer(app.Handler())
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial(websocketTestURL(server.URL)+"/v1/responses", http.Header{
		"Authorization": []string{"Bearer test-key"},
	})
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	defer conn.Close()

	if err := conn.WriteJSON(map[string]any{"type": "session.update"}); err != nil {
		t.Fatalf("invalid WriteJSON() error = %v", err)
	}
	var protocolError map[string]any
	if err := conn.ReadJSON(&protocolError); err != nil {
		t.Fatalf("error ReadJSON() error = %v", err)
	}
	if protocolError["type"] != "error" || protocolError["status"] != float64(http.StatusBadRequest) {
		t.Fatalf("protocol error = %#v", protocolError)
	}

	if err := conn.WriteJSON(map[string]any{
		"type":  "response.create",
		"model": "gpt-5.4",
		"input": "valid after error",
	}); err != nil {
		t.Fatalf("valid WriteJSON() error = %v", err)
	}
	events := readResponsesWebSocketTurn(t, conn)
	assertResponsesWebSocketEventTypes(t, events, "response.created", "response.output_text.delta", "response.completed")
}

func TestResponsesWebSocketRequiresAPIKeyBeforeUpgrade(t *testing.T) {
	gin.SetMode(gin.TestMode)

	app := newResponsesWebSocketTestApp(t, &fakeResponsesWebSocketStream{})
	server := httptest.NewServer(app.Handler())
	defer server.Close()

	conn, response, err := websocket.DefaultDialer.Dial(websocketTestURL(server.URL)+"/v1/responses", nil)
	if conn != nil {
		conn.Close()
	}
	if err == nil {
		t.Fatal("Dial() error = nil, want authentication failure")
	}
	if response == nil || response.StatusCode != http.StatusUnauthorized {
		status := 0
		if response != nil {
			status = response.StatusCode
			response.Body.Close()
		}
		t.Fatalf("upgrade status = %d, want %d", status, http.StatusUnauthorized)
	}
	response.Body.Close()
}

func TestNormalizeResponsesWebSocketMessagePreservesGenerateAndIgnoresStream(t *testing.T) {
	generate := false
	normalized, err := normalizeResponsesWebSocketMessage([]byte(`{
		"type":"response.create",
		"model":"gpt-5.4",
		"stream":false,
		"generate":false,
		"input":"warm up"
	}`), models.NewCatalog(models.BootstrapEntries()))
	if err != nil {
		t.Fatalf("normalizeResponsesWebSocketMessage() error = %v", err)
	}
	if !normalized.Stream {
		t.Fatal("Stream = false, want implicit WebSocket streaming")
	}
	if normalized.Generate == nil || *normalized.Generate != generate {
		t.Fatalf("Generate = %#v, want pointer to false", normalized.Generate)
	}
	payload := normalized.ToCodexWSCreatePayload()
	if _, ok := payload["stream"]; ok {
		t.Fatalf("upstream payload stream = %#v, want omitted", payload["stream"])
	}
	if got, ok := payload["generate"]; !ok || got != false {
		t.Fatalf("upstream payload generate = %#v, want false", got)
	}
}

func TestNormalizeResponsesWebSocketAppendRequiresArrayInput(t *testing.T) {
	t.Parallel()

	normalized, err := normalizeResponsesWebSocketMessage([]byte(`{
		"type":"response.append",
		"input":[{"type":"message","role":"user","content":"continue"}]
	}`), models.NewCatalog(models.BootstrapEntries()))
	if err != nil {
		t.Fatalf("normalizeResponsesWebSocketMessage() error = %v", err)
	}
	if !normalized.WebSocketAppend {
		t.Fatal("WebSocketAppend = false, want true")
	}

	_, err = normalizeResponsesWebSocketMessage([]byte(`{"type":"response.append","input":"continue"}`), models.NewCatalog(models.BootstrapEntries()))
	if err == nil || !strings.Contains(err.Error(), "array field") {
		t.Fatalf("string input error = %v, want array field error", err)
	}
}

func newResponsesWebSocketTestApp(t *testing.T, stream *fakeResponsesWebSocketStream) *App {
	t.Helper()

	now := time.Now().UTC()
	accountsSvc := newServerAccounts(t, &accounts.Record{
		ID:        "acct_ws",
		AccountID: "upstream_ws",
		Status:    accounts.StatusActive,
		Token: accounts.OAuthToken{
			AccessToken: "token",
			ExpiresAt:   now.Add(time.Hour),
		},
		CreatedAt: now,
		UpdatedAt: now,
	})
	cfg := config.Config{
		ProxyAPIKey:     "test-key",
		CodexBaseURL:    "https://chatgpt.example/backend-api",
		DefaultModel:    "gpt-5.4",
		ContinuationTTL: time.Minute,
		RefreshSkew:     time.Minute,
	}
	catalog := models.NewCatalog(models.BootstrapEntries())
	httpClient := codex.NewHTTPClient(cfg)
	engine := gin.New()
	app := &App{
		cfg:           cfg,
		logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		engine:        engine,
		accounts:      accountsSvc,
		accountMgr:    codex.NewAccountManager(cfg, accountsSvc, nil, httpClient, catalog),
		httpClient:    httpClient,
		continuations: accounts.NewContinuationManager(time.Minute),
		models:        catalog,
	}
	app.wsConnector = func(_ context.Context, _ string, _ http.Header, body any) (responsesWebSocketStream, error) {
		stream.mu.Lock()
		stream.connects++
		stream.mu.Unlock()
		if err := stream.begin(body); err != nil {
			return nil, err
		}
		return stream, nil
	}
	app.routes()
	return app
}

func responsesWebSocketTextEvents(responseID, model, text string) []*codex.StreamEvent {
	return []*codex.StreamEvent{
		{
			Type: "response.created",
			Raw: map[string]any{
				"response": map[string]any{
					"id":     responseID,
					"model":  model,
					"status": "in_progress",
				},
			},
		},
		{
			Type: "response.output_text.delta",
			Raw: map[string]any{
				"response_id": responseID,
				"delta":       text,
			},
		},
		{
			Type: "response.completed",
			Raw: map[string]any{
				"response": map[string]any{
					"id":          responseID,
					"model":       model,
					"status":      "completed",
					"output_text": text,
				},
			},
		},
	}
}

func readResponsesWebSocketTurn(t *testing.T, conn *websocket.Conn) []map[string]any {
	t.Helper()
	events := make([]map[string]any, 0, 4)
	for {
		var event map[string]any
		if err := conn.ReadJSON(&event); err != nil {
			t.Fatalf("ReadJSON() error = %v", err)
		}
		events = append(events, event)
		if event["type"] == "response.completed" || event["type"] == "response.incomplete" || event["type"] == "error" {
			return events
		}
	}
}

func assertResponsesWebSocketEventTypes(t *testing.T, events []map[string]any, want ...string) {
	t.Helper()
	got := make([]string, 0, len(events))
	for _, event := range events {
		got = append(got, event["type"].(string))
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("event types = %v, want %v", got, want)
	}
}

func websocketTestURL(httpURL string) string {
	return "ws" + strings.TrimPrefix(httpURL, "http")
}
