package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"chatgpt-codex-proxy/internal/accountmanager"
	"chatgpt-codex-proxy/internal/accounts"
	"chatgpt-codex-proxy/internal/anthropic"
	"chatgpt-codex-proxy/internal/codex"
	"chatgpt-codex-proxy/internal/config"
	"chatgpt-codex-proxy/internal/conversation"
	"chatgpt-codex-proxy/internal/middleware"
	"chatgpt-codex-proxy/internal/models"
	"chatgpt-codex-proxy/internal/turn"
)

func newFailoverTestApp(t *testing.T) *App {
	t.Helper()
	now := time.Now().UTC()
	accountsSvc := newServerAccounts(t,
		&accounts.Record{ID: "acct-a", AccountID: "upstream-a", Status: accounts.StatusActive, Token: accounts.OAuthToken{AccessToken: "token-a", ExpiresAt: now.Add(time.Hour)}, CreatedAt: now, UpdatedAt: now},
		&accounts.Record{ID: "acct-b", AccountID: "upstream-b", Status: accounts.StatusActive, Token: accounts.OAuthToken{AccessToken: "token-b", ExpiresAt: now.Add(time.Hour)}, CreatedAt: now, UpdatedAt: now},
	)
	cfg := config.Config{RefreshSkew: time.Minute, DefaultModel: "gpt-5.6-terra", CodexBaseURL: "https://example.invalid"}
	catalog := models.NewCatalog(models.BootstrapEntries())
	return &App{
		cfg:           cfg,
		logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		accounts:      accountsSvc,
		accountMgr:    accountmanager.NewAccountManager(cfg, accountsSvc, nil, nil, catalog.SupportsRecord),
		continuations: conversation.NewContinuationManager(time.Minute),
		claudeReplays: anthropic.NewReplayManager(time.Minute),
		models:        catalog,
	}
}

func TestOpenStreamFailsOverToAnotherAccount(t *testing.T) {
	t.Parallel()

	var attempts []string
	app := newFailoverTestApp(t)
	app.httpStream = func(_ context.Context, account accounts.Record, _ codex.Request, _ string) (eventStream, error) {
		attempts = append(attempts, account.ID)
		if account.ID == "acct-a" {
			return nil, &codex.UpstreamError{Op: "codex response", StatusCode: http.StatusTooManyRequests, RetryAfter: 30}
		}
		return &fakeEventStream{events: []*codex.StreamEvent{{Type: "response.completed"}}}, nil
	}

	account, stream, _, err := app.openStream(nil, context.Background(), "responses", &sessionResolution{Request: turn.NormalizedRequest{Request: codex.Request{Model: "gpt-5.6-terra", Stream: true}, ModelExplicit: true}})
	if err != nil {
		t.Fatalf("openStream() error = %v", err)
	}
	defer stream.Close()
	if account.ID != "acct-b" {
		t.Fatalf("account = %q, want acct-b", account.ID)
	}
	if len(attempts) != 2 || attempts[0] != "acct-a" || attempts[1] != "acct-b" {
		t.Fatalf("attempts = %#v, want acct-a then acct-b", attempts)
	}
}

func TestOpenStreamFailsOverWhenFirstEventIsRetryableFailure(t *testing.T) {
	t.Parallel()

	app := newFailoverTestApp(t)
	app.httpStream = func(_ context.Context, account accounts.Record, _ codex.Request, _ string) (eventStream, error) {
		if account.ID == "acct-a" {
			return &fakeEventStream{events: []*codex.StreamEvent{{
				Type: "response.failed",
				Raw: map[string]any{"response": map[string]any{"error": map[string]any{
					"code": "rate_limited", "message": "try another account",
				}}},
			}}}, nil
		}
		return &fakeEventStream{events: []*codex.StreamEvent{{Type: "response.created", Raw: map[string]any{"type": "response.created"}}}}, nil
	}

	account, stream, _, err := app.openStream(nil, context.Background(), "responses", &sessionResolution{Request: turn.NormalizedRequest{Request: codex.Request{Model: "gpt-5.6-terra", Stream: true}, ModelExplicit: true}})
	if err != nil {
		t.Fatalf("openStream() error = %v", err)
	}
	defer stream.Close()
	if account.ID != "acct-b" {
		t.Fatalf("account = %q, want acct-b", account.ID)
	}
	event, err := stream.NextEvent()
	if err != nil {
		t.Fatalf("NextEvent() error = %v", err)
	}
	if event.Type != "response.created" {
		t.Fatalf("event type = %q, want response.created", event.Type)
	}
}

func TestResponsesFailsOverWhenNonStreamingResponseFailsAfterFirstEvent(t *testing.T) {
	t.Parallel()

	app := newFailoverTestApp(t)
	var attempts []string
	app.httpStream = func(_ context.Context, account accounts.Record, _ codex.Request, _ string) (eventStream, error) {
		attempts = append(attempts, account.ID)
		if account.ID == "acct-a" {
			return &fakeEventStream{events: []*codex.StreamEvent{
				{Type: "response.created", Raw: map[string]any{"type": "response.created"}},
				{Type: "response.failed", Raw: map[string]any{"response": map[string]any{"error": map[string]any{"code": "rate_limited", "message": "try another account"}}}},
			}}, nil
		}
		return &fakeEventStream{events: []*codex.StreamEvent{{Type: "response.completed", Raw: map[string]any{"response": map[string]any{"id": "resp_1", "status": "completed"}}}}}, nil
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.6-terra","input":"hello","stream":false}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	app.handleResponses(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if response["id"] != "resp_1" || response["status"] != "completed" {
		t.Fatalf("response = %#v, want completed resp_1", response)
	}
	if len(attempts) != 2 || attempts[0] != "acct-a" || attempts[1] != "acct-b" {
		t.Fatalf("attempts = %#v, want acct-a then acct-b", attempts)
	}
}

func TestResponsesRecoversFromInvalidReasoningSignature(t *testing.T) {
	t.Parallel()

	app := newFailoverTestApp(t)
	var attempts int
	var sawEncryptedReasoning []bool
	app.httpStream = func(_ context.Context, _ accounts.Record, req codex.Request, _ string) (eventStream, error) {
		attempts++
		hasEncryptedReasoning := false
		for _, item := range req.Input {
			if item.Type == "reasoning" && item.EncryptedContent != "" {
				hasEncryptedReasoning = true
			}
		}
		sawEncryptedReasoning = append(sawEncryptedReasoning, hasEncryptedReasoning)
		if hasEncryptedReasoning {
			return nil, &codex.UpstreamError{Op: "codex response", StatusCode: http.StatusBadRequest, Code: "invalid_encrypted_content"}
		}
		return &fakeEventStream{events: []*codex.StreamEvent{{Type: "response.completed", Raw: map[string]any{"response": map[string]any{"id": "resp_recovered", "status": "completed"}}}}}, nil
	}

	body := `{"model":"gpt-5.6-terra","stream":false,"input":[` +
		`{"type":"reasoning","encrypted_content":"foreign-signature","summary":[]},` +
		`{"role":"user","content":[{"type":"input_text","text":"hi"}]}]}`
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	app.handleResponses(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if response["id"] != "resp_recovered" || response["status"] != "completed" {
		t.Fatalf("response = %#v, want recovered completed", response)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2 (fail with signature, then retry sanitized)", attempts)
	}
	if len(sawEncryptedReasoning) != 2 || !sawEncryptedReasoning[0] || sawEncryptedReasoning[1] {
		t.Fatalf("sawEncryptedReasoning = %#v, want [true, false]", sawEncryptedReasoning)
	}
}

func TestChatCompletionsNonStreamingAcceptsIncompleteResponse(t *testing.T) {
	t.Parallel()

	app := newFailoverTestApp(t)
	app.httpStream = func(_ context.Context, _ accounts.Record, _ codex.Request, _ string) (eventStream, error) {
		return &fakeEventStream{events: []*codex.StreamEvent{{
			Type: "response.incomplete",
			Raw: map[string]any{"response": map[string]any{
				"id":                 "resp_chat_incomplete",
				"model":              "gpt-5.6-terra",
				"status":             "incomplete",
				"incomplete_details": map[string]any{"reason": "max_output_tokens"},
				"output_text":        "partial answer",
				"usage":              map[string]any{"input_tokens": 4, "output_tokens": 2},
			}},
		}}}, nil
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"gpt-5.6-terra","messages":[{"role":"user","content":"hello"}]
	}`))
	app.handleChatCompletions(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	choice := sliceOfMapsFromAny(response["choices"])[0]
	if choice["finish_reason"] != "length" || choice["native_finish_reason"] != "max_output_tokens" {
		t.Fatalf("choice = %#v", choice)
	}
	message := nestedMapFromAny(choice["message"])
	if message["content"] != "partial answer" {
		t.Fatalf("message = %#v", message)
	}
	if _, ok := app.continuations.Get("resp_chat_incomplete"); !ok {
		t.Fatal("incomplete response continuation was not saved")
	}
}

func TestResponsesNonStreamingPreservesIncompleteResponse(t *testing.T) {
	t.Parallel()

	app := newFailoverTestApp(t)
	app.httpStream = func(_ context.Context, _ accounts.Record, _ codex.Request, _ string) (eventStream, error) {
		return &fakeEventStream{events: []*codex.StreamEvent{{
			Type: "response.incomplete",
			Raw: map[string]any{"response": map[string]any{
				"id":                 "resp_responses_incomplete",
				"model":              "gpt-5.6-terra",
				"status":             "incomplete",
				"incomplete_details": map[string]any{"reason": "content_filter"},
				"output_text":        "safe partial",
			}},
		}}}, nil
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
		"model":"gpt-5.6-terra","input":"hello"
	}`))
	app.handleResponses(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if response["status"] != "incomplete" || response["output_text"] != "safe partial" {
		t.Fatalf("response = %#v", response)
	}
	incompleteDetails := nestedMapFromAny(response["incomplete_details"])
	if incompleteDetails["reason"] != "content_filter" {
		t.Fatalf("incomplete_details = %#v", incompleteDetails)
	}
}

func TestOpenStreamAttemptsEachAccountOnlyOnce(t *testing.T) {
	t.Parallel()

	app := newFailoverTestApp(t)
	var attempts []string
	app.httpStream = func(_ context.Context, account accounts.Record, _ codex.Request, _ string) (eventStream, error) {
		attempts = append(attempts, account.ID)
		return nil, &codex.UpstreamError{Op: "codex response", StatusCode: http.StatusServiceUnavailable}
	}

	account, _, _, err := app.openStream(nil, context.Background(), "responses", &sessionResolution{Request: turn.NormalizedRequest{Request: codex.Request{Model: "gpt-5.6-terra"}, ModelExplicit: true}})
	if err == nil {
		t.Fatal("openStream() error = nil, want upstream error")
	}
	if account.ID != "acct-b" {
		t.Fatalf("account = %q, want final attempted account acct-b", account.ID)
	}
	if len(attempts) != 2 || attempts[0] != "acct-a" || attempts[1] != "acct-b" {
		t.Fatalf("attempts = %#v, want each account once", attempts)
	}
}

func TestOpenStreamDoesNotFailOverNonRetryableRequestError(t *testing.T) {
	t.Parallel()

	app := newFailoverTestApp(t)
	var attempts []string
	app.httpStream = func(_ context.Context, account accounts.Record, _ codex.Request, _ string) (eventStream, error) {
		attempts = append(attempts, account.ID)
		return nil, &codex.UpstreamError{Op: "codex response", StatusCode: http.StatusBadRequest}
	}

	_, _, _, err := app.openStream(nil, context.Background(), "responses", &sessionResolution{Request: turn.NormalizedRequest{Request: codex.Request{Model: "gpt-5.6-terra"}, ModelExplicit: true}})
	if err == nil {
		t.Fatal("openStream() error = nil, want upstream error")
	}
	if len(attempts) != 1 || attempts[0] != "acct-a" {
		t.Fatalf("attempts = %#v, want only acct-a", attempts)
	}
}

func TestOpenStreamDoesNotFailOverAfterRequestCancellation(t *testing.T) {
	t.Parallel()

	app := newFailoverTestApp(t)
	ctx, cancel := context.WithCancel(context.Background())
	var attempts []string
	app.httpStream = func(_ context.Context, account accounts.Record, _ codex.Request, _ string) (eventStream, error) {
		attempts = append(attempts, account.ID)
		return &fakeEventStream{
			beforeTailErr: cancel,
			tailErr:       errors.New("H3_REQUEST_CANCELLED (local)"),
		}, nil
	}

	_, _, _, err := app.openStream(nil, ctx, "responses", &sessionResolution{Request: turn.NormalizedRequest{Request: codex.Request{Model: "gpt-5.6-terra", Stream: true}, ModelExplicit: true}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("openStream() error = %v, want context.Canceled", err)
	}
	if len(attempts) != 1 || attempts[0] != "acct-a" {
		t.Fatalf("attempts = %#v, want only acct-a", attempts)
	}
}

func TestOpenStreamKeepsExplicitContinuationPinned(t *testing.T) {
	t.Parallel()

	app := newFailoverTestApp(t)
	connects := 0
	app.wsConnector = func(_ context.Context, _ string, _ http.Header, _ any) (responsesWebSocketStream, error) {
		connects++
		return nil, &codex.UpstreamError{Op: "codex websocket", StatusCode: http.StatusServiceUnavailable}
	}

	account, _, _, err := app.openStream(nil, context.Background(), "responses", &sessionResolution{
		Request:            turn.NormalizedRequest{Request: codex.Request{Model: "gpt-5.6-terra", PreviousResponseID: "resp_1"}, ModelExplicit: true},
		PreferredAccountID: "acct-a",
		ExplicitPrevious:   true,
	})
	if err == nil {
		t.Fatal("openStream() error = nil, want upstream error")
	}
	if account.ID != "acct-a" || connects != 1 {
		t.Fatalf("account = %q, connects = %d; want pinned acct-a and one attempt", account.ID, connects)
	}
}

func TestResponsesReplaysExplicitHTTPContinuation(t *testing.T) {
	t.Parallel()

	app := newFailoverTestApp(t)
	app.continuations.Put(conversation.ContinuationRecord{
		ResponseID: "resp_1", AccountID: "acct-a", Model: "gpt-5.6-terra",
		InputHistory:    []turn.InputItem{userText("first"), assistantText("first answer")},
		ToolNameAliases: map[string]string{"short": "original_tool_name"},
	})
	var upstreamRequest codex.Request
	app.httpStream = func(_ context.Context, account accounts.Record, request codex.Request, _ string) (eventStream, error) {
		if account.ID != "acct-a" {
			t.Errorf("continuation routed to %q, want acct-a", account.ID)
		}
		upstreamRequest = request
		return &fakeEventStream{events: []*codex.StreamEvent{{
			Type: "response.completed",
			Raw: map[string]any{"response": map[string]any{
				"id": "resp_2", "status": "completed",
				"output": []any{map[string]any{"type": "function_call", "call_id": "call_1", "name": "short", "arguments": `{}`}},
			}},
		}}}, nil
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"previous_response_id":"resp_1","input":"second"}`))
	app.handleResponses(ctx)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if upstreamRequest.Model != "gpt-5.6-terra" || upstreamRequest.PreviousResponseID != "" || len(upstreamRequest.Input) != 3 || upstreamRequest.Input[2].Content[0].Text != "second" {
		t.Fatalf("upstream replay request = %#v", upstreamRequest)
	}
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	output := sliceOfMapsFromAny(response["output"])
	if len(output) != 1 || output[0]["name"] != "original_tool_name" {
		t.Fatalf("continuation lost tool aliases: %#v", output)
	}
}

func TestNormalizeChatCompletionsBodyAcceptsArrayToolOutputInResponsesShape(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"model": "gpt-5.6-terra",
		"input": [
			{"role": "assistant", "type": "function_call", "call_id": "call_1", "name": "Glob", "arguments": "{\"glob_pattern\":\"README*\"}"},
			{"type": "function_call_output", "call_id": "call_1", "output": [
				{"type": "output_text", "text": "Result of search"},
				{"type": "output_text", "text": "README.md"}
			]},
			{"role": "user", "content": "explain this project"}
		]
	}`)

	normalized, err := normalizeChatCompletionsBody(body, nil)
	if err != nil {
		t.Fatalf("normalizeChatCompletionsBody() error = %v", err)
	}

	if len(normalized.Input) != 3 {
		t.Fatalf("len(input) = %d, want 3", len(normalized.Input))
	}
	if normalized.Input[1].Type != "function_call_output" {
		t.Fatalf("input[1].Type = %q, want function_call_output", normalized.Input[1].Type)
	}
	if normalized.Input[1].OutputText != "" {
		t.Fatalf("input[1].OutputText = %q, want empty", normalized.Input[1].OutputText)
	}
	if len(normalized.Input[1].OutputContent) != 2 {
		t.Fatalf("len(input[1].OutputContent) = %d, want 2", len(normalized.Input[1].OutputContent))
	}
	if normalized.Input[1].OutputContent[0].Text != "Result of search" || normalized.Input[1].OutputContent[1].Text != "README.md" {
		t.Fatalf("input[1].OutputContent = %#v, want preserved output parts", normalized.Input[1].OutputContent)
	}
}

func TestStreamResponsesClassifiesStructuredQuotaFailureAndSetsCooldown(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	ctx.Set(middleware.RequestIDKey, "req-test")

	now := time.Now().UTC()
	resetAt := now.Add(20 * time.Minute)
	accountsSvc := newServerAccounts(t, &accounts.Record{
		ID:        "acct_quota",
		AccountID: "upstream_quota",
		Status:    accounts.StatusActive,
		Token: accounts.OAuthToken{
			AccessToken: "token",
			ExpiresAt:   now.Add(time.Hour),
		},
		CachedQuota: &accounts.QuotaSnapshot{
			RateLimit: accounts.RateLimitWindow{
				Allowed:      true,
				LimitReached: true,
				ResetAt:      &resetAt,
			},
		},
		CreatedAt: now,
		UpdatedAt: now,
	})

	app := &App{
		cfg: config.Config{
			ContinuationTTL: time.Minute,
		},
		logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		accounts:      accountsSvc,
		continuations: conversation.NewContinuationManager(time.Minute),
	}

	record := mustGetAccount(t, accountsSvc, "acct_quota")
	stream := &fakeEventStream{
		events: []*codex.StreamEvent{{
			Type: "response.failed",
			Raw: map[string]any{
				"response": map[string]any{
					"id": "resp_quota",
					"usage": map[string]any{
						"input_tokens":  13,
						"output_tokens": 2,
					},
					"error": map[string]any{
						"code":    "usage_limit_reached",
						"message": "Billing period exhausted",
					},
				},
			},
		}},
	}

	app.streamResponses(ctx, record, turn.NormalizedRequest{
		Request: codex.Request{
			Model:  "gpt-5.6-terra",
			Stream: true,
		},
	}, stream)

	if !strings.Contains(recorder.Body.String(), "upstream account quota exhausted") {
		t.Fatalf("stream body = %s, want quota exhausted message", recorder.Body.String())
	}

	updated := mustGetAccount(t, accountsSvc, "acct_quota")
	if updated.CooldownUntil == nil {
		t.Fatal("cooldown_until = nil, want cooldown")
	}
	if updated.Status != accounts.StatusActive {
		t.Fatalf("status = %q, want active", updated.Status)
	}
}

func TestStreamResponsesClassifiesStructuredUnauthorizedFailure(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	ctx.Set(middleware.RequestIDKey, "req-test")

	app := newFailoverTestApp(t)
	record := mustGetAccount(t, app.accounts, "acct-a")
	stream := &fakeEventStream{
		events: []*codex.StreamEvent{{
			Type: "response.failed",
			Raw: map[string]any{
				"response": map[string]any{
					"id": "resp_unauthorized",
					"error": map[string]any{
						"code":    "invalid_token",
						"message": "Session expired",
					},
				},
			},
		}},
	}

	app.streamResponses(ctx, record, turn.NormalizedRequest{
		Request: codex.Request{
			Model:  "gpt-5.6-terra",
			Stream: true,
		},
	}, stream)

	updated := mustGetAccount(t, app.accounts, "acct-a")
	if updated.Status != accounts.StatusExpired {
		t.Fatalf("status = %q, want expired", updated.Status)
	}
}

func TestStreamResponsesSynthesizesFunctionCallLifecycle(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	ctx.Set(middleware.RequestIDKey, "req-test")

	app := newFailoverTestApp(t)
	record := mustGetAccount(t, app.accounts, "acct-a")
	stream := &fakeEventStream{
		events: []*codex.StreamEvent{
			{
				Type: "response.function_call_arguments.delta",
				Raw: map[string]any{
					"response_id":  "resp_tool",
					"item_id":      "fc_1",
					"output_index": 0,
					"name":         "demo__echo",
					"delta":        `{"message":"hel`,
				},
			},
			{
				Type: "response.function_call_arguments.done",
				Raw: map[string]any{
					"response_id":  "resp_tool",
					"item_id":      "fc_1",
					"output_index": 0,
					"name":         "demo__echo",
					"arguments":    `{"message":"hello"}`,
				},
			},
			{
				Type: "response.completed",
				Raw: map[string]any{
					"response": map[string]any{
						"id":     "resp_tool",
						"model":  "gpt-5.6-terra",
						"status": "completed",
						"output": []any{
							map[string]any{
								"id":     "msg_1",
								"type":   "message",
								"role":   "assistant",
								"status": "completed",
								"content": []any{
									map[string]any{
										"type": "output_text",
										"text": "tool ready",
									},
								},
							},
						},
						"output_text": "tool ready",
					},
				},
			},
		},
	}

	app.streamResponses(ctx, record, turn.NormalizedRequest{
		Request: codex.Request{
			Model:  "gpt-5.6-terra",
			Stream: true,
		},
	}, stream)

	events := parseSSEEvents(t, recorder.Body.String())
	assertEventTypes(t, events,
		"response.output_item.added",
		"response.function_call_arguments.delta",
		"response.function_call_arguments.done",
		"response.output_item.done",
		"response.completed",
	)

	added := events[0].Data
	if added["response_id"] != "resp_tool" {
		t.Fatalf("added response_id = %#v, want resp_tool", added["response_id"])
	}
	if added["output_index"] != float64(0) {
		t.Fatalf("added output_index = %#v, want 0", added["output_index"])
	}
	item := nestedMapFromAny(added["item"])
	if item["type"] != "function_call" {
		t.Fatalf("added item.type = %#v, want function_call", item["type"])
	}
	if item["status"] != "in_progress" {
		t.Fatalf("added item.status = %#v, want in_progress", item["status"])
	}

	completed := events[4].Data
	response := nestedMapFromAny(completed["response"])
	output := sliceOfMapsFromAny(response["output"])
	if len(output) != 2 {
		t.Fatalf("completed response.output len = %d, want 2", len(output))
	}
	if output[0]["type"] != "function_call" {
		t.Fatalf("completed output[0].type = %#v, want function_call", output[0]["type"])
	}
	if output[0]["call_id"] != "fc_1" {
		t.Fatalf("completed output[0].call_id = %#v, want fc_1", output[0]["call_id"])
	}
	if output[1]["type"] != "message" {
		t.Fatalf("completed output[1].type = %#v, want message", output[1]["type"])
	}
}

func TestStreamResponsesWebSearchPassthroughAndCompletedOutput(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	ctx.Set(middleware.RequestIDKey, "req-test")

	app := newFailoverTestApp(t)
	record := mustGetAccount(t, app.accounts, "acct-a")
	stream := &fakeEventStream{
		events: []*codex.StreamEvent{
			{
				Type: "response.output_item.added",
				Raw: map[string]any{
					"response_id":  "resp_search",
					"output_index": 0,
					"item": map[string]any{
						"id":     "ws_1",
						"type":   "web_search_call",
						"status": "in_progress",
					},
				},
			},
			{
				Type: "response.web_search_call.in_progress",
				Raw: map[string]any{
					"response_id":  "resp_search",
					"output_index": 0,
					"item_id":      "ws_1",
				},
			},
			{
				Type: "response.web_search_call.searching",
				Raw: map[string]any{
					"response_id":  "resp_search",
					"output_index": 0,
					"item_id":      "ws_1",
				},
			},
			{
				Type: "response.web_search_call.completed",
				Raw: map[string]any{
					"response_id":  "resp_search",
					"output_index": 0,
					"item_id":      "ws_1",
				},
			},
			{
				Type: "response.output_item.done",
				Raw: map[string]any{
					"response_id":  "resp_search",
					"output_index": 0,
					"item": map[string]any{
						"id":     "ws_1",
						"type":   "web_search_call",
						"status": "completed",
						"action": map[string]any{
							"type":  "search",
							"query": "longevity clinic San Francisco",
						},
					},
				},
			},
			{
				Type: "response.output_item.done",
				Raw: map[string]any{
					"response_id":  "resp_search",
					"output_index": 1,
					"item": map[string]any{
						"id":     "msg_1",
						"type":   "message",
						"role":   "assistant",
						"status": "completed",
						"content": []any{
							map[string]any{
								"type": "output_text",
								"text": `{"leads":[]}`,
							},
						},
					},
				},
			},
			{
				Type: "response.completed",
				Raw: map[string]any{
					"response": map[string]any{
						"id":     "resp_search",
						"model":  "gpt-5.5",
						"status": "completed",
						"output": []any{},
					},
				},
			},
		},
	}

	app.streamResponses(ctx, record, turn.NormalizedRequest{
		Request: codex.Request{
			Model:  "gpt-5.5",
			Stream: true,
		},
	}, stream)

	events := parseSSEEvents(t, recorder.Body.String())
	assertEventTypes(t, events,
		"response.output_item.added",
		"response.web_search_call.in_progress",
		"response.web_search_call.searching",
		"response.web_search_call.completed",
		"response.output_item.done",
		"response.output_item.done",
		"response.completed",
	)

	completed := events[6].Data
	response := nestedMapFromAny(completed["response"])
	output := sliceOfMapsFromAny(response["output"])
	if len(output) != 2 {
		t.Fatalf("completed response.output len = %d, want 2", len(output))
	}
	if output[0]["type"] != "web_search_call" {
		t.Fatalf("completed output[0].type = %#v, want web_search_call", output[0]["type"])
	}
	if output[1]["type"] != "message" {
		t.Fatalf("completed output[1].type = %#v, want message", output[1]["type"])
	}
	if response["output_text"] != `{"leads":[]}` {
		t.Fatalf("completed response.output_text = %#v, want structured text", response["output_text"])
	}
}
