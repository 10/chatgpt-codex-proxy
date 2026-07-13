package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"chatgpt-codex-proxy/internal/accounts"
	"chatgpt-codex-proxy/internal/codex"
)

func TestNormalizeCompletionsBody(t *testing.T) {
	t.Parallel()

	normalized, err := normalizeCompletionsBody([]byte(`{"model":"gpt-5.4","prompt":"hello","stream":true}`), nil)
	if err != nil {
		t.Fatalf("normalizeCompletionsBody() error = %v", err)
	}
	if !normalized.Stream || len(normalized.Input) != 1 || normalized.Input[0].Content[0].Text != "hello" {
		t.Fatalf("normalized request = %#v", normalized)
	}

	_, err = normalizeCompletionsBody([]byte(`{"model":"gpt-5.4","prompt":["one","two"]}`), nil)
	if err == nil || !strings.Contains(err.Error(), "multiple prompts") {
		t.Fatalf("multiple prompt error = %v", err)
	}
}

func TestHandleCompletionsReturnsTextCompletion(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	app := newFailoverTestApp(t)
	app.httpStream = func(_ context.Context, _ accounts.Record, _ codex.Request, _ string) (eventStream, error) {
		return &fakeEventStream{events: []*codex.StreamEvent{{
			Type: "response.completed",
			Raw: map[string]any{"response": map[string]any{
				"id": "resp_completion", "model": "gpt-5.4", "status": "completed", "output_text": "completed text",
			}},
		}}}, nil
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/completions", strings.NewReader(`{"model":"gpt-5.4","prompt":"complete me"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	app.handleCompletions(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if response["object"] != "text_completion" {
		t.Fatalf("object = %#v, want text_completion", response["object"])
	}
	choices, _ := response["choices"].([]any)
	choice, _ := choices[0].(map[string]any)
	if choice["text"] != "completed text" {
		t.Fatalf("choice text = %#v", choice["text"])
	}
}
