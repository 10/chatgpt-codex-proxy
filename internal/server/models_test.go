package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"chatgpt-codex-proxy/internal/config"
	"chatgpt-codex-proxy/internal/models"
)

func TestHandleModelsReturnsCodexClientMetadata(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v1/models?client_version=0.98.0", nil)

	app := &App{cfg: config.Config{DefaultModel: "gpt-5.6-terra"}}
	app.handleModels(ctx)

	var body struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if len(body.Models) == 0 {
		t.Fatal("models = empty")
	}
	model := body.Models[0]
	if model["slug"] == "" || model["context_window"] == nil || model["supported_reasoning_levels"] == nil {
		t.Fatalf("Codex client model metadata = %#v", model)
	}
	if _, hasOpenAIList := model["object"]; hasOpenAIList {
		t.Fatalf("Codex client model metadata unexpectedly uses OpenAI list shape: %#v", model)
	}
}

func TestHandleModelsUsesRuntimeCatalog(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)

	catalog := models.NewCatalog(models.BootstrapEntries())
	catalog.ApplyRouteModels("acct:acct_plus", []models.Entry{{
		ID:        "gpt-dynamic-test",
		IsDefault: true,
	}})

	app := &App{
		cfg:    config.Config{DefaultModel: "gpt-5.6-terra"},
		models: catalog,
	}
	app.handleModels(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}

	var body struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if len(body.Data) != 3 {
		t.Fatalf("len(data) = %d, want dynamic model plus two image models", len(body.Data))
	}
	found := false
	for _, model := range body.Data {
		if model["id"] == "gpt-dynamic-test" {
			found = true
		}
	}
	if !found {
		t.Fatal("model list missing gpt-dynamic-test")
	}
}
