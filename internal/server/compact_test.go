package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"chatgpt-codex-proxy/internal/accountmanager"
	"chatgpt-codex-proxy/internal/accounts"
	"chatgpt-codex-proxy/internal/codex"
	"chatgpt-codex-proxy/internal/config"
	"chatgpt-codex-proxy/internal/conversation"
	"chatgpt-codex-proxy/internal/models"
	"chatgpt-codex-proxy/internal/turn"
)

func TestHandleResponsesCompactReturnsCompactedWindow(t *testing.T) {
	t.Parallel()

	var received codex.CompactRequest
	app := newCompactTestApp(t, func(ctx context.Context, record accounts.Record, req codex.CompactRequest) (codex.CompactResponse, *accounts.QuotaSnapshot, error) {
		received = req
		return codex.CompactResponse{
			ID: "resp_compact", Object: "response.compaction", CreatedAt: 123,
			Output: []map[string]any{{
				"type":              "compaction",
				"id":                "cmp_1",
				"encrypted_content": "enc_1",
			}},
		}, nil, nil
	})

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses/compact", bytes.NewBufferString(`{"input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", "test-key")
	app.Handler().ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
	}

	if received.Model == "" {
		t.Fatal("received.Model = empty, want resolved default model")
	}

	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if got := body["object"]; got != "response.compaction" {
		t.Fatalf("object = %#v, want response.compaction", got)
	}
	if body["id"] != "resp_compact" || body["created_at"] != float64(123) {
		t.Fatalf("response metadata = %#v", body)
	}
	output, _ := body["output"].([]any)
	if len(output) != 1 {
		t.Fatalf("len(output) = %d, want 1", len(output))
	}
}

func TestHandleResponsesCompactPreservesUsageAndExpandsPreviousResponse(t *testing.T) {
	t.Parallel()

	var received codex.CompactRequest
	app := newCompactTestApp(t, func(ctx context.Context, record accounts.Record, req codex.CompactRequest) (codex.CompactResponse, *accounts.QuotaSnapshot, error) {
		received = req
		return codex.CompactResponse{
			ID:        "cmp_resp_1",
			Object:    "response.compaction",
			CreatedAt: 123,
			Usage: map[string]any{
				"input_tokens":  10,
				"output_tokens": 4,
			},
			Output: []map[string]any{{
				"type":              "compaction",
				"id":                "cmp_2",
				"encrypted_content": "enc_2",
			}},
		}, nil, nil
	})

	app.continuations.Put(conversation.ContinuationRecord{
		ResponseID: "resp_prev_compact",
		AccountID:  "acct_compact",
		Model:      "gpt-5.6-terra",
		InputHistory: []turn.InputItem{{
			Role:  "assistant",
			Type:  "message",
			Phase: "output",
			Content: []turn.ContentPart{{
				Type: "output_text",
				Text: "Earlier compacted output",
			}},
		}},
		CreatedAt: time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(time.Minute),
	})

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses/compact", bytes.NewBufferString(`{
		"previous_response_id":"resp_prev_compact",
		"input":{"role":"user","content":"next turn"}
	}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", "test-key")
	app.Handler().ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
	}
	if received.Model != "gpt-5.6-terra" {
		t.Fatalf("model = %q, want gpt-5.6-terra", received.Model)
	}
	if len(received.Input) != 2 {
		t.Fatalf("len(input) = %d, want 2", len(received.Input))
	}
	if received.Input[0].Role != "assistant" || received.Input[0].Phase != "output" {
		t.Fatalf("input[0] = %#v, want expanded assistant output-phase history", received.Input[0])
	}

	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if got := body["id"]; got != "cmp_resp_1" {
		t.Fatalf("id = %#v, want cmp_resp_1", got)
	}
	usage, _ := body["usage"].(map[string]any)
	if usage["input_tokens"] != float64(10) {
		t.Fatalf("usage[input_tokens] = %#v, want 10", usage["input_tokens"])
	}
}

func newCompactTestApp(t *testing.T, caller func(context.Context, accounts.Record, codex.CompactRequest) (codex.CompactResponse, *accounts.QuotaSnapshot, error)) *App {
	t.Helper()

	gin.SetMode(gin.TestMode)

	now := time.Now().UTC()
	accountsSvc := newServerAccounts(t, &accounts.Record{
		ID:        "acct_compact",
		AccountID: "upstream_compact",
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
		CodexBaseURL:    "https://example.invalid",
		DefaultModel:    "gpt-5.6-terra",
		ContinuationTTL: time.Minute,
		RequestTimeout:  5 * time.Second,
	}
	modelCatalog := models.NewCatalog(models.BootstrapEntries())
	app := &App{
		cfg:           cfg,
		logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		engine:        gin.New(),
		accounts:      accountsSvc,
		compactCaller: caller,
		continuations: conversation.NewContinuationManager(time.Minute),
		models:        modelCatalog,
	}
	app.accountMgr = accountmanager.NewAccountManager(cfg, accountsSvc, nil, nil, modelCatalog.SupportsRecord)
	app.routes()
	return app
}
