package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"

	"chatgpt-codex-proxy/internal/models"
)

func TestHandleResponsesDecodesZstdRequestBody(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", zstdRequestBody(t, `{"model":"not-a-real-model","input":"hello"}`))
	ctx.Request.Header.Set("Content-Encoding", "zstd")

	app := &App{models: models.NewCatalog(models.BootstrapEntries())}
	app.handleResponses(ctx)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", recorder.Code, recorder.Body.String())
	}
	assertOpenAIErrorCode(t, recorder.Body.Bytes(), "model_not_found")
}

func zstdRequestBody(t *testing.T, payload string) *bytes.Buffer {
	t.Helper()

	var body bytes.Buffer
	encoder, err := zstd.NewWriter(&body)
	if err != nil {
		t.Fatalf("zstd.NewWriter() error = %v", err)
	}
	if _, err := encoder.Write([]byte(payload)); err != nil {
		t.Fatalf("encoder.Write() error = %v", err)
	}
	if err := encoder.Close(); err != nil {
		t.Fatalf("encoder.Close() error = %v", err)
	}
	return &body
}
