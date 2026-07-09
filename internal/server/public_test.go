package server

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"chatgpt-codex-proxy/internal/accounts"
	"chatgpt-codex-proxy/internal/codex"
	"chatgpt-codex-proxy/internal/config"
	"chatgpt-codex-proxy/internal/middleware"
	"chatgpt-codex-proxy/internal/translate"
)

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
		continuations: accounts.NewContinuationManager(time.Minute),
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

	app.streamChatCompletion(ctx, record, translate.NormalizedRequest{
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
		continuations: accounts.NewContinuationManager(time.Minute),
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

	app.streamResponses(ctx, record, translate.NormalizedRequest{
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
		continuations: accounts.NewContinuationManager(time.Minute),
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

	app.streamResponses(ctx, record, translate.NormalizedRequest{
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
		continuations: accounts.NewContinuationManager(time.Minute),
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

	app.streamResponses(ctx, record, translate.NormalizedRequest{
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
		"done",
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
		continuations: accounts.NewContinuationManager(time.Minute),
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

	app.streamResponses(ctx, record, translate.NormalizedRequest{
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
		"done",
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
		continuations: accounts.NewContinuationManager(time.Minute),
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

	app.streamResponses(ctx, record, translate.NormalizedRequest{
		Request: codex.Request{
			Model:  "gpt-5.4",
			Stream: true,
		},
	}, stream)

	events := parseSSEEvents(t, recorder.Body.String())
	assertEventTypes(t, events,
		"response.output_text.delta",
		"response.completed",
		"done",
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
		continuations: accounts.NewContinuationManager(time.Minute),
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

	app.streamResponses(ctx, record, translate.NormalizedRequest{
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
		"done",
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
