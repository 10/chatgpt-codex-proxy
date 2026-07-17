package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"chatgpt-codex-proxy/internal/accounts"
	"chatgpt-codex-proxy/internal/codex"
	"chatgpt-codex-proxy/internal/middleware"
)

func TestAnthropicMessagesNonStreaming(t *testing.T) {
	t.Parallel()

	app := newFailoverTestApp(t)
	app.httpStream = func(_ context.Context, _ accounts.Record, request codex.Request, _ string) (eventStream, error) {
		if request.Instructions != "Be brief." || len(request.Input) != 1 || request.Input[0].Content[0].Text != "Hello" {
			t.Fatalf("upstream request = %#v", request)
		}
		return &fakeEventStream{events: []*codex.StreamEvent{{Type: "response.completed", Raw: map[string]any{
			"response": map[string]any{
				"id": "resp_anthropic", "model": "gpt-5.4", "status": "completed", "output_text": "Hi",
				"usage": map[string]any{"input_tokens": 8, "output_tokens": 2},
			},
		}}}}, nil
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Set(middleware.RequestIDKey, "req_test")
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{
		"model":"gpt-5.4","max_tokens":256,"system":"Be brief.",
		"messages":[{"role":"user","content":"Hello"}]
	}`))
	ctx.Request.Header.Set("anthropic-version", "2023-06-01")
	app.handleAnthropicMessages(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if response["id"] != "msg_anthropic" || response["type"] != "message" || response["role"] != "assistant" {
		t.Fatalf("response = %#v", response)
	}
	content := sliceOfMapsFromAny(response["content"])
	if len(content) != 1 || content[0]["type"] != "text" || content[0]["text"] != "Hi" {
		t.Fatalf("content = %#v", content)
	}
	if response["stop_reason"] != "end_turn" {
		t.Fatalf("stop_reason = %#v", response["stop_reason"])
	}
}

func TestAnthropicMessagesAcceptsCurrentClaudeCodeRequestShape(t *testing.T) {
	t.Parallel()

	app := newFailoverTestApp(t)
	app.httpStream = func(_ context.Context, _ accounts.Record, request codex.Request, _ string) (eventStream, error) {
		if request.Instructions != "You are Claude Code." {
			t.Fatalf("instructions = %q", request.Instructions)
		}
		if request.Reasoning == nil || request.Reasoning.Effort != "high" || request.Reasoning.Summary != "auto" {
			t.Fatalf("reasoning = %#v", request.Reasoning)
		}
		if len(request.Input) != 2 || request.Input[1].Role != "user" {
			t.Fatalf("input = %#v", request.Input)
		}
		if got := request.Input[1].Content[0].Text; got != "<system-reminder>\nUse the Workflow tool.\n</system-reminder>" {
			t.Fatalf("system reminder = %q", got)
		}
		return &fakeEventStream{events: []*codex.StreamEvent{{Type: "response.completed", Raw: map[string]any{
			"response": map[string]any{
				"id": "resp_claude_code", "model": "gpt-5.6-sol", "status": "completed", "output_text": "Ready",
				"usage": map[string]any{"input_tokens": 8, "output_tokens": 2},
			},
		}}}}, nil
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Set(middleware.RequestIDKey, "req_claude_code")
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{
		"model":"gpt-5.6-sol",
		"max_tokens":32000,
		"system":[
			{"type":"text","text":" x-anthropic-billing-header: cc_version=2.1.211"},
			{"type":"text","text":"You are Claude Code."}
		],
		"messages":[
			{"role":"user","content":"hello"},
			{"role":"system","content":"Use the Workflow tool."}
		],
		"thinking":{"type":"adaptive","display":"omitted"},
		"context_management":{"edits":[{"type":"clear_thinking_20251015","keep":"all"}]},
		"output_config":{"effort":"high"}
	}`))
	ctx.Request.Header.Set("anthropic-version", "2023-06-01")
	app.handleAnthropicMessages(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestAnthropicMessagesStreamingUsesNamedEventsWithoutDoneSentinel(t *testing.T) {
	t.Parallel()

	app := newFailoverTestApp(t)
	app.httpStream = func(_ context.Context, _ accounts.Record, _ codex.Request, _ string) (eventStream, error) {
		return &fakeEventStream{events: []*codex.StreamEvent{
			{Type: "response.created", Raw: map[string]any{"response": map[string]any{"id": "resp_stream", "model": "gpt-5.4"}}},
			{Type: "response.output_text.delta", Raw: map[string]any{"delta": "Hello"}},
			{Type: "response.output_text.done", Raw: map[string]any{"text": "Hello"}},
			{Type: "response.completed", Raw: map[string]any{"response": map[string]any{
				"id": "resp_stream", "model": "gpt-5.4", "status": "completed", "output_text": "Hello",
				"usage": map[string]any{"input_tokens": 4, "output_tokens": 1},
			}}},
		}}, nil
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"gpt-5.4","max_tokens":256,"stream":true,"messages":[{"role":"user","content":"Hi"}]}`))
	ctx.Request.Header.Set("anthropic-version", "2023-06-01")
	app.handleAnthropicMessages(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	events := parseSSEEvents(t, recorder.Body.String())
	assertEventTypes(t, events, "message_start", "content_block_start", "content_block_delta", "content_block_stop", "message_delta", "message_stop")
	if strings.Contains(recorder.Body.String(), "[DONE]") {
		t.Fatalf("body contains OpenAI sentinel: %s", recorder.Body.String())
	}
}

func TestAnthropicMessagesWritesMidstreamErrorEvent(t *testing.T) {
	t.Parallel()

	app := newFailoverTestApp(t)
	app.httpStream = func(_ context.Context, _ accounts.Record, _ codex.Request, _ string) (eventStream, error) {
		return &fakeEventStream{
			events:  []*codex.StreamEvent{{Type: "response.created", Raw: map[string]any{"response": map[string]any{"id": "resp_error", "model": "gpt-5.4"}}}},
			tailErr: errors.New("stream disconnected"),
		}, nil
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"gpt-5.4","max_tokens":256,"stream":true,"messages":[{"role":"user","content":"Hi"}]}`))
	ctx.Request.Header.Set("anthropic-version", "2023-06-01")
	app.handleAnthropicMessages(ctx)

	events := parseSSEEvents(t, recorder.Body.String())
	assertEventTypes(t, events, "message_start", "error")
	if events[1].Data["type"] != "error" || nestedMapFromAny(events[1].Data["error"])["type"] != "api_error" {
		t.Fatalf("error event = %#v", events[1])
	}
}

func TestAnthropicMessagesDoesNotWriteErrorAfterClientCancellation(t *testing.T) {
	t.Parallel()

	app := newFailoverTestApp(t)
	var logs bytes.Buffer
	app.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	requestContext, cancel := context.WithCancel(context.Background())
	app.httpStream = func(_ context.Context, _ accounts.Record, _ codex.Request, _ string) (eventStream, error) {
		return &fakeEventStream{
			events: []*codex.StreamEvent{{
				Type: "response.created",
				Raw:  map[string]any{"response": map[string]any{"id": "resp_canceled", "model": "gpt-5.4"}},
			}},
			beforeTailErr: cancel,
			tailErr:       errors.New("H3_REQUEST_CANCELLED (local)"),
		}, nil
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"gpt-5.4","max_tokens":256,"stream":true,"messages":[{"role":"user","content":"Hi"}]}`)).WithContext(requestContext)
	ctx.Request.Header.Set("anthropic-version", "2023-06-01")
	app.handleAnthropicMessages(ctx)

	events := parseSSEEvents(t, recorder.Body.String())
	assertEventTypes(t, events, "message_start")
	if strings.Contains(logs.String(), "upstream stream failed") {
		t.Fatalf("client cancellation logged as upstream failure: %s", logs.String())
	}
	if got := ctx.GetString(middleware.RequestOutcomeKey); got != "client_canceled" {
		t.Fatalf("outcome = %q, want client_canceled", got)
	}
	if got := ctx.GetString(middleware.RequestErrorCodeKey); got != "client_canceled" {
		t.Fatalf("error code = %q, want client_canceled", got)
	}
	if got := ctx.GetString(middleware.RequestResponseIDKey); got != "resp_canceled" {
		t.Fatalf("response id = %q, want resp_canceled", got)
	}
}

func TestAnthropicErrorAddsRequestLogMetadata(t *testing.T) {
	t.Parallel()

	app := newFailoverTestApp(t)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	app.writeAnthropicError(ctx, http.StatusBadRequest, "messages must contain at least one message")

	if got := ctx.GetString(middleware.RequestErrorCodeKey); got != "invalid_request_error" {
		t.Fatalf("error code = %q, want invalid_request_error", got)
	}
	if got := ctx.GetString(middleware.RequestErrorMessageKey); got != "messages must contain at least one message" {
		t.Fatalf("error message = %q", got)
	}
}

func TestAnthropicMessagesKeepsPrestreamFailureAsJSON(t *testing.T) {
	t.Parallel()

	app := newFailoverTestApp(t)
	app.httpStream = func(_ context.Context, _ accounts.Record, _ codex.Request, _ string) (eventStream, error) {
		return &fakeEventStream{events: []*codex.StreamEvent{{
			Type: "response.failed",
			Raw: map[string]any{"response": map[string]any{"error": map[string]any{
				"code": "rate_limited", "message": "slow down",
			}}},
		}}}, nil
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"gpt-5.4","max_tokens":256,"stream":true,"messages":[{"role":"user","content":"Hi"}]}`))
	ctx.Request.Header.Set("anthropic-version", "2023-06-01")
	app.handleAnthropicMessages(ctx)

	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("prestream failure committed SSE: headers=%#v body=%s", recorder.Header(), recorder.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if nestedMapFromAny(response["error"])["type"] != "rate_limit_error" {
		t.Fatalf("response = %#v", response)
	}
}

func TestAnthropicMessagesRequiresVersion(t *testing.T) {
	t.Parallel()

	app := newFailoverTestApp(t)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	app.handleAnthropicMessages(ctx)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response map[string]any
	_ = json.Unmarshal(recorder.Body.Bytes(), &response)
	if nestedMapFromAny(response["error"])["type"] != "invalid_request_error" {
		t.Fatalf("response = %#v", response)
	}
}

func TestAnthropicCountTokensDoesNotOpenUpstream(t *testing.T) {
	t.Parallel()

	app := newFailoverTestApp(t)
	app.httpStream = func(context.Context, accounts.Record, codex.Request, string) (eventStream, error) {
		t.Fatal("count_tokens opened an upstream request")
		return nil, io.EOF
	}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", strings.NewReader(`{"model":"gpt-5.4","messages":[{"role":"user","content":"Count me"}]}`))
	ctx.Request.Header.Set("anthropic-version", "2023-06-01")
	app.handleAnthropicCountTokens(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response map[string]any
	_ = json.Unmarshal(recorder.Body.Bytes(), &response)
	if response["input_tokens"].(float64) <= 0 {
		t.Fatalf("response = %#v", response)
	}
}

func TestAnthropicModelsUseAnthropicEnvelope(t *testing.T) {
	t.Parallel()

	app := newFailoverTestApp(t)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	ctx.Request.Header.Set("anthropic-version", "2023-06-01")
	app.handleModels(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response map[string]any
	_ = json.Unmarshal(recorder.Body.Bytes(), &response)
	data := sliceOfMapsFromAny(response["data"])
	if len(data) == 0 || data[0]["type"] != "model" || data[0]["display_name"] == "" {
		t.Fatalf("response = %#v", response)
	}
}

func TestAnthropicZeroMaxTokensUsesWebSocketGenerateFalse(t *testing.T) {
	t.Parallel()

	app := newFailoverTestApp(t)
	app.httpStream = func(context.Context, accounts.Record, codex.Request, string) (eventStream, error) {
		t.Fatal("max_tokens zero used HTTP instead of WebSocket")
		return nil, io.EOF
	}
	fakeStream := &fakeResponsesWebSocketStream{turns: [][]*codex.StreamEvent{{{
		Type: "response.completed",
		Raw:  map[string]any{"response": map[string]any{"id": "resp_cached", "model": "gpt-5.4", "status": "completed"}},
	}}}}
	app.wsConnector = func(_ context.Context, _ string, _ http.Header, body any) (responsesWebSocketStream, error) {
		fakeStream.connects++
		if err := fakeStream.begin(body); err != nil {
			return nil, err
		}
		return fakeStream, nil
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"gpt-5.4","max_tokens":0,"messages":[{"role":"user","content":"warm"}]}`))
	ctx.Request.Header.Set("anthropic-version", "2023-06-01")
	app.handleAnthropicMessages(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if fakeStream.connects != 1 || len(fakeStream.requests) != 1 || fakeStream.requests[0]["generate"] != false {
		t.Fatalf("connects=%d requests=%#v", fakeStream.connects, fakeStream.requests)
	}
}

func TestAnthropicRouteAuthenticationUsesAnthropicErrorAndRequestID(t *testing.T) {
	t.Parallel()

	app := newFailoverTestApp(t)
	app.cfg.ProxyAPIKey = "secret"
	app.engine = gin.New()
	app.engine.Use(middleware.RequestID())
	app.routes()

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	request.Header.Set("anthropic-version", "2023-06-01")
	app.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if nestedMapFromAny(response["error"])["type"] != "authentication_error" {
		t.Fatalf("response = %#v", response)
	}
	if recorder.Header().Get("request-id") == "" || response["request_id"] != recorder.Header().Get("request-id") {
		t.Fatalf("request id header=%q response=%#v", recorder.Header().Get("request-id"), response)
	}
}
