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
	cfg := config.Config{RefreshSkew: time.Minute, DefaultModel: "gpt-5.4", CodexBaseURL: "https://example.invalid"}
	catalog := models.NewCatalog(models.BootstrapEntries())
	return &App{
		cfg:           cfg,
		logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		accounts:      accountsSvc,
		accountMgr:    accountmanager.NewAccountManager(cfg, accountsSvc, nil, nil, catalog),
		continuations: conversation.NewContinuationManager(time.Minute),
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

	account, stream, _, err := app.openStream(nil, context.Background(), "responses", &sessionResolution{Request: turn.NormalizedRequest{Request: codex.Request{Model: "gpt-5.4", Stream: true}, ModelExplicit: true}})
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

	account, stream, _, err := app.openStream(nil, context.Background(), "responses", &sessionResolution{Request: turn.NormalizedRequest{Request: codex.Request{Model: "gpt-5.4", Stream: true}, ModelExplicit: true}})
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
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.4","input":"hello","stream":false}`))
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

func TestChatCompletionsNonStreamingAcceptsIncompleteResponse(t *testing.T) {
	t.Parallel()

	app := newFailoverTestApp(t)
	app.httpStream = func(_ context.Context, _ accounts.Record, _ codex.Request, _ string) (eventStream, error) {
		return &fakeEventStream{events: []*codex.StreamEvent{{
			Type: "response.incomplete",
			Raw: map[string]any{"response": map[string]any{
				"id":                 "resp_chat_incomplete",
				"model":              "gpt-5.4",
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
		"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}]
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
				"model":              "gpt-5.4",
				"status":             "incomplete",
				"incomplete_details": map[string]any{"reason": "content_filter"},
				"output_text":        "safe partial",
			}},
		}}}, nil
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
		"model":"gpt-5.4","input":"hello"
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

	account, _, _, err := app.openStream(nil, context.Background(), "responses", &sessionResolution{Request: turn.NormalizedRequest{Request: codex.Request{Model: "gpt-5.4"}, ModelExplicit: true}})
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

	_, _, _, err := app.openStream(nil, context.Background(), "responses", &sessionResolution{Request: turn.NormalizedRequest{Request: codex.Request{Model: "gpt-5.4"}, ModelExplicit: true}})
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

	_, _, _, err := app.openStream(nil, ctx, "responses", &sessionResolution{Request: turn.NormalizedRequest{Request: codex.Request{Model: "gpt-5.4", Stream: true}, ModelExplicit: true}})
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
		Request:            turn.NormalizedRequest{Request: codex.Request{Model: "gpt-5.4", PreviousResponseID: "resp_1"}, ModelExplicit: true},
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

func TestOpenStreamReplaysExplicitHTTPContinuation(t *testing.T) {
	t.Parallel()

	app := newFailoverTestApp(t)
	var upstreamRequest codex.Request
	app.httpStream = func(_ context.Context, _ accounts.Record, request codex.Request, _ string) (eventStream, error) {
		upstreamRequest = request
		return &fakeEventStream{events: []*codex.StreamEvent{{
			Type: "response.completed",
			Raw:  map[string]any{"response": map[string]any{"id": "resp_2", "status": "completed"}},
		}}}, nil
	}

	resolution := sessionResolution{
		Request: turn.NormalizedRequest{Request: codex.Request{
			Model: "gpt-5.4", PreviousResponseID: "resp_1", Input: []codex.InputItem{userText("second")},
		}, ModelExplicit: true},
		Original: turn.NormalizedRequest{Request: codex.Request{
			Model: "gpt-5.4", Input: []codex.InputItem{userText("first"), assistantText("first answer"), userText("second")},
		}, ModelExplicit: true},
		PreferredAccountID: "acct-a",
		ExplicitPrevious:   true,
		ReplayAvailable:    true,
	}
	account, stream, _, err := app.openStream(nil, context.Background(), "responses", &resolution)
	if err != nil {
		t.Fatalf("openStream() error = %v", err)
	}
	defer stream.Close()
	if account.ID != "acct-a" {
		t.Fatalf("account = %q, want acct-a", account.ID)
	}
	if upstreamRequest.PreviousResponseID != "" || len(upstreamRequest.Input) != 3 {
		t.Fatalf("upstream replay request = %#v", upstreamRequest)
	}
}

func TestObserveQuotaSnapshotUpdatesCachedQuota(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	accountsSvc := newServerAccounts(t, &accounts.Record{
		ID:        "acct_headers",
		AccountID: "upstream_headers",
		Status:    accounts.StatusActive,
		Token: accounts.OAuthToken{
			AccessToken: "token",
			ExpiresAt:   now.Add(time.Hour),
		},
		CreatedAt: now,
		UpdatedAt: now,
	})

	app := &App{
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		accounts: accountsSvc,
	}

	pct := 35.0
	resetAt := now.Add(15 * time.Minute)
	app.observeQuotaSnapshot("acct_headers", &accounts.QuotaSnapshot{
		Source:    "response_headers",
		FetchedAt: now,
		RateLimit: accounts.RateLimitWindow{
			Allowed:      true,
			LimitReached: false,
			UsedPercent:  &pct,
			ResetAt:      &resetAt,
		},
	})

	record := mustGetAccount(t, accountsSvc, "acct_headers")
	if record.CachedQuota == nil || record.CachedQuota.RateLimit.UsedPercent == nil {
		t.Fatal("cached quota missing used percent")
	}
	if *record.CachedQuota.RateLimit.UsedPercent != 35.0 {
		t.Fatalf("used_percent = %v, want 35", *record.CachedQuota.RateLimit.UsedPercent)
	}
}

func TestNormalizeChatCompletionsBodyAcceptsResponsesShape(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"model": "gpt-5.4",
		"instructions": "Be concise.",
		"input": {
			"role": "user",
			"content": [{"type": "text", "text": "hello"}]
		},
		"stream": true,
		"tools": [{
			"type": "function",
			"name": "Shell",
			"description": "Run a shell command",
			"parameters": {
				"type": "object"
			}
		}]
	}`)

	normalized, err := normalizeChatCompletionsBody(body, nil)
	if err != nil {
		t.Fatalf("normalizeChatCompletionsBody() error = %v", err)
	}

	if normalized.Instructions != "Be concise." {
		t.Fatalf("instructions = %q, want Be concise.", normalized.Instructions)
	}
	if !normalized.Stream {
		t.Fatal("stream = false, want true")
	}
	if len(normalized.Input) != 1 {
		t.Fatalf("len(input) = %d, want 1", len(normalized.Input))
	}
	if normalized.Input[0].Role != "user" {
		t.Fatalf("input role = %q, want user", normalized.Input[0].Role)
	}
	if got := normalized.Input[0].Content[0].Text; got != "hello" {
		t.Fatalf("input text = %q, want hello", got)
	}
	if len(normalized.Tools) != 1 || normalized.Tools[0].Name != "Shell" {
		t.Fatalf("tools = %#v, want Shell passthrough", normalized.Tools)
	}
}

func TestNormalizeChatCompletionsBodyLiftsInstructionRolesFromResponsesShape(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"model": "gpt-5.4",
		"input": [
			{"role": "system", "content": "You are GPT-5.4."},
			{"role": "user", "content": "Explain this repository."},
			{"role": "user", "content": [{"type": "input_text", "text": "<user_query>\nwhat does this project do\n</user_query>"}]}
		],
		"tools": [{
			"type": "function",
			"name": "Shell",
			"description": "Run a shell command",
			"parameters": {
				"type": "object"
			}
		}]
	}`)

	normalized, err := normalizeChatCompletionsBody(body, nil)
	if err != nil {
		t.Fatalf("normalizeChatCompletionsBody() error = %v", err)
	}

	if normalized.Instructions != "You are GPT-5.4." {
		t.Fatalf("instructions = %q, want lifted system instructions", normalized.Instructions)
	}
	if len(normalized.Input) != 2 {
		t.Fatalf("len(input) = %d, want 2", len(normalized.Input))
	}
	if normalized.Input[0].Role != "user" || normalized.Input[1].Role != "user" {
		t.Fatalf("input roles = %#v, want only user items", normalized.Input)
	}
}

func TestNormalizeChatCompletionsBodyAcceptsArrayToolOutputInResponsesShape(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"model": "gpt-5.4",
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

func TestNormalizeChatCompletionsBodyPrefersMessagesShape(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"model": "gpt-5.4",
		"messages": [{"role": "user", "content": "hello"}],
		"instructions": "ignored",
		"input": {"role": "user", "content": [{"type": "text", "text": "ignored"}]}
	}`)

	normalized, err := normalizeChatCompletionsBody(body, nil)
	if err != nil {
		t.Fatalf("normalizeChatCompletionsBody() error = %v", err)
	}

	if len(normalized.Input) != 1 {
		t.Fatalf("len(input) = %d, want 1", len(normalized.Input))
	}
	if got := normalized.Input[0].Content[0].Text; got != "hello" {
		t.Fatalf("input text = %q, want hello", got)
	}
}

func TestNormalizeChatCompletionsBodyRejectsEmptyPayload(t *testing.T) {
	t.Parallel()

	_, err := normalizeChatCompletionsBody([]byte(`{}`), nil)
	if err == nil {
		t.Fatal("normalizeChatCompletionsBody() error = nil, want empty payload error")
	}
}

func TestObserveQuotaEventUpdatesCachedQuota(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	accountsSvc := newServerAccounts(t, &accounts.Record{
		ID:        "acct_event",
		AccountID: "upstream_event",
		Status:    accounts.StatusActive,
		PlanType:  "plus",
		Token: accounts.OAuthToken{
			AccessToken: "token",
			ExpiresAt:   now.Add(time.Hour),
		},
		CreatedAt: now,
		UpdatedAt: now,
	})

	app := &App{
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		accounts: accountsSvc,
	}

	handled := app.observeQuotaEvent(accounts.Record{ID: "acct_event", PlanType: "plus"}, &codex.StreamEvent{
		Type: "codex.rate_limits",
		Raw: map[string]any{
			"rate_limits": map[string]any{
				"primary": map[string]any{
					"used_percent": 42.0,
					"reset_at":     float64(now.Add(30 * time.Minute).Unix()),
				},
			},
		},
	})
	if !handled {
		t.Fatal("observeQuotaEvent() = false, want true")
	}

	record := mustGetAccount(t, accountsSvc, "acct_event")
	if record.CachedQuota == nil || record.CachedQuota.RateLimit.UsedPercent == nil {
		t.Fatal("cached quota missing after event")
	}
	if *record.CachedQuota.RateLimit.UsedPercent != 42.0 {
		t.Fatalf("used_percent = %v, want 42", *record.CachedQuota.RateLimit.UsedPercent)
	}
}

func TestStreamChatCompletionClassifiesStructuredRateLimitFailureAndSetsCooldown(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil)
	ctx.Set(middleware.RequestIDKey, "req-test")

	now := time.Now().UTC()
	accountsSvc := newServerAccounts(t, &accounts.Record{
		ID:        "acct_rate_limit",
		AccountID: "upstream_rate_limit",
		Status:    accounts.StatusActive,
		Token: accounts.OAuthToken{
			AccessToken: "token",
			ExpiresAt:   now.Add(time.Hour),
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

	record := mustGetAccount(t, accountsSvc, "acct_rate_limit")
	stream := &fakeEventStream{
		events: []*codex.StreamEvent{{
			Type: "error",
			Raw: map[string]any{
				"error": map[string]any{
					"code":              "rate_limited",
					"message":           "Too many requests",
					"resets_in_seconds": 12,
					"usage": map[string]any{
						"input_tokens":  11,
						"output_tokens": 7,
					},
				},
			},
		}},
	}

	app.streamChatCompletion(ctx, record, turn.NormalizedRequest{
		Request: codex.Request{
			Model:  "gpt-5.4",
			Stream: true,
		},
	}, stream)

	if !strings.Contains(recorder.Body.String(), "upstream account rate limited") {
		t.Fatalf("stream body = %s, want rate limited message", recorder.Body.String())
	}

	updated := mustGetAccount(t, accountsSvc, "acct_rate_limit")
	if updated.CooldownUntil == nil {
		t.Fatal("cooldown_until = nil, want cooldown")
	}
	if updated.Status != accounts.StatusActive {
		t.Fatalf("status = %q, want active", updated.Status)
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
			Model:  "gpt-5.4",
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

	now := time.Now().UTC()
	accountsSvc := newServerAccounts(t, &accounts.Record{
		ID:        "acct_unauthorized",
		AccountID: "upstream_unauthorized",
		Status:    accounts.StatusActive,
		Token: accounts.OAuthToken{
			AccessToken: "token",
			ExpiresAt:   now.Add(time.Hour),
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

	record := mustGetAccount(t, accountsSvc, "acct_unauthorized")
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
			Model:  "gpt-5.4",
			Stream: true,
		},
	}, stream)

	updated := mustGetAccount(t, accountsSvc, "acct_unauthorized")
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

	now := time.Now().UTC()
	accountsSvc := newServerAccounts(t, &accounts.Record{
		ID:        "acct_stream_tools",
		AccountID: "upstream_stream_tools",
		Status:    accounts.StatusActive,
		Token: accounts.OAuthToken{
			AccessToken: "token",
			ExpiresAt:   now.Add(time.Hour),
		},
		CreatedAt: now,
		UpdatedAt: now,
	})

	app := &App{
		cfg:           config.Config{ContinuationTTL: time.Minute},
		logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		accounts:      accountsSvc,
		continuations: conversation.NewContinuationManager(time.Minute),
	}

	record := mustGetAccount(t, accountsSvc, "acct_stream_tools")
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
						"model":  "gpt-5.4",
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
			Model:  "gpt-5.4",
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

func TestStreamResponsesSynthesizesFunctionCallLifecycleWithoutDeltas(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	ctx.Set(middleware.RequestIDKey, "req-test")

	now := time.Now().UTC()
	accountsSvc := newServerAccounts(t, &accounts.Record{
		ID:        "acct_stream_done_only",
		AccountID: "upstream_stream_done_only",
		Status:    accounts.StatusActive,
		Token: accounts.OAuthToken{
			AccessToken: "token",
			ExpiresAt:   now.Add(time.Hour),
		},
		CreatedAt: now,
		UpdatedAt: now,
	})

	app := &App{
		cfg:           config.Config{ContinuationTTL: time.Minute},
		logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		accounts:      accountsSvc,
		continuations: conversation.NewContinuationManager(time.Minute),
	}

	record := mustGetAccount(t, accountsSvc, "acct_stream_done_only")
	stream := &fakeEventStream{
		events: []*codex.StreamEvent{
			{
				Type: "response.function_call_arguments.done",
				Raw: map[string]any{
					"response_id":  "resp_done_only",
					"item_id":      "fc_done",
					"output_index": 0,
					"name":         "demo__echo",
					"arguments":    `{"message":"hello"}`,
				},
			},
			{
				Type: "response.completed",
				Raw: map[string]any{
					"response": map[string]any{
						"id":     "resp_done_only",
						"model":  "gpt-5.4",
						"status": "completed",
					},
				},
			},
		},
	}

	app.streamResponses(ctx, record, turn.NormalizedRequest{
		Request: codex.Request{
			Model:  "gpt-5.4",
			Stream: true,
		},
	}, stream)

	events := parseSSEEvents(t, recorder.Body.String())
	assertEventTypes(t, events,
		"response.output_item.added",
		"response.function_call_arguments.done",
		"response.output_item.done",
		"response.completed",
	)
}

func TestStreamResponsesTextOnlyPassthroughRemainsUnchanged(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	ctx.Set(middleware.RequestIDKey, "req-test")

	now := time.Now().UTC()
	accountsSvc := newServerAccounts(t, &accounts.Record{
		ID:        "acct_stream_text",
		AccountID: "upstream_stream_text",
		Status:    accounts.StatusActive,
		Token: accounts.OAuthToken{
			AccessToken: "token",
			ExpiresAt:   now.Add(time.Hour),
		},
		CreatedAt: now,
		UpdatedAt: now,
	})

	app := &App{
		cfg:           config.Config{ContinuationTTL: time.Minute},
		logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		accounts:      accountsSvc,
		continuations: conversation.NewContinuationManager(time.Minute),
	}

	record := mustGetAccount(t, accountsSvc, "acct_stream_text")
	stream := &fakeEventStream{
		events: []*codex.StreamEvent{
			{
				Type: "response.output_text.delta",
				Raw: map[string]any{
					"response_id": "resp_text",
					"delta":       "hello",
				},
			},
			{
				Type: "response.completed",
				Raw: map[string]any{
					"response": map[string]any{
						"id":          "resp_text",
						"model":       "gpt-5.4",
						"status":      "completed",
						"output_text": "hello",
					},
				},
			},
		},
	}

	app.streamResponses(ctx, record, turn.NormalizedRequest{
		Request: codex.Request{
			Model:  "gpt-5.4",
			Stream: true,
		},
	}, stream)

	events := parseSSEEvents(t, recorder.Body.String())
	assertEventTypes(t, events,
		"response.output_text.delta",
		"response.completed",
	)
	for _, event := range events {
		if strings.HasPrefix(event.Event, "response.output_item.") {
			t.Fatalf("unexpected tool lifecycle event in text-only stream: %s", event.Event)
		}
	}
}

func TestStreamResponsesWebSearchPassthroughAndCompletedOutput(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	ctx.Set(middleware.RequestIDKey, "req-test")

	now := time.Now().UTC()
	accountsSvc := newServerAccounts(t, &accounts.Record{
		ID:        "acct_stream_web_search",
		AccountID: "upstream_stream_web_search",
		Status:    accounts.StatusActive,
		Token: accounts.OAuthToken{
			AccessToken: "token",
			ExpiresAt:   now.Add(time.Hour),
		},
		CreatedAt: now,
		UpdatedAt: now,
	})

	app := &App{
		cfg:           config.Config{ContinuationTTL: time.Minute},
		logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		accounts:      accountsSvc,
		continuations: conversation.NewContinuationManager(time.Minute),
	}

	record := mustGetAccount(t, accountsSvc, "acct_stream_web_search")
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
