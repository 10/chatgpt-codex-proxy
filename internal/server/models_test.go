package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"chatgpt-codex-proxy/internal/config"
	"chatgpt-codex-proxy/internal/models"
)

func TestHandleModelsIncludesCreatedTimestamp(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)

	app := &App{cfg: config.Config{DefaultModel: "gpt-5.4"}}
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
	if len(body.Data) == 0 {
		t.Fatal("expected model list")
	}
	if body.Data[0]["created"] != float64(modelCreatedTimestamp) {
		t.Fatalf("created = %#v, want %d", body.Data[0]["created"], modelCreatedTimestamp)
	}
}

func TestHandleModelsIncludesCodexImageModels(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)

	app := &App{cfg: config.Config{DefaultModel: "gpt-5.4"}}
	app.handleModels(ctx)

	var body struct {
		Data []modelResponse `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	ids := make(map[string]bool, len(body.Data))
	for _, model := range body.Data {
		ids[model.ID] = true
	}
	for _, id := range []string{"gpt-image-1.5", "gpt-image-2"} {
		if !ids[id] {
			t.Fatalf("model list missing %q", id)
		}
	}
}

func TestHandleModelsReturnsCodexClientMetadata(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v1/models?client_version=0.98.0", nil)

	app := &App{cfg: config.Config{DefaultModel: "gpt-5.4"}}
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

func TestHandleModelByID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		modelID    string
		wantStatus int
		assertBody func(*testing.T, []byte)
	}{
		{
			name:       "returns supported model",
			modelID:    "gpt-5.4",
			wantStatus: http.StatusOK,
			assertBody: func(t *testing.T, bodyBytes []byte) {
				t.Helper()

				var body map[string]any
				if err := json.Unmarshal(bodyBytes, &body); err != nil {
					t.Fatalf("json.Unmarshal() error = %v", err)
				}
				if body["id"] != "gpt-5.4" {
					t.Fatalf("id = %#v, want gpt-5.4", body["id"])
				}
				if body["created"] != float64(modelCreatedTimestamp) {
					t.Fatalf("created = %#v, want %d", body["created"], modelCreatedTimestamp)
				}
			},
		},
		{
			name:       "returns image model",
			modelID:    "gpt-image-2",
			wantStatus: http.StatusOK,
			assertBody: func(t *testing.T, bodyBytes []byte) {
				t.Helper()

				var body map[string]any
				if err := json.Unmarshal(bodyBytes, &body); err != nil {
					t.Fatalf("json.Unmarshal() error = %v", err)
				}
				if body["id"] != "gpt-image-2" {
					t.Fatalf("id = %#v, want gpt-image-2", body["id"])
				}
			},
		},
		{
			name:       "returns not found",
			modelID:    "unknown",
			wantStatus: http.StatusNotFound,
			assertBody: func(t *testing.T, bodyBytes []byte) {
				t.Helper()

				var body map[string]map[string]any
				if err := json.Unmarshal(bodyBytes, &body); err != nil {
					t.Fatalf("json.Unmarshal() error = %v", err)
				}
				if body["error"]["code"] != "model_not_found" {
					t.Fatalf("error.code = %#v, want model_not_found", body["error"]["code"])
				}
			},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			gin.SetMode(gin.TestMode)
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodGet, "/v1/models/"+tc.modelID, nil)
			ctx.Params = gin.Params{{Key: "model_id", Value: tc.modelID}}

			app := &App{cfg: config.Config{DefaultModel: "gpt-5.4"}}
			app.handleModelByID(ctx)

			if recorder.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", recorder.Code, tc.wantStatus)
			}
			tc.assertBody(t, recorder.Body.Bytes())
		})
	}
}

func TestHandleModelsUsesRuntimeCatalog(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)

	catalog := models.NewCatalog(models.BootstrapEntries())
	catalog.ApplyRouteModels("plan:plus", []models.Entry{{
		ID:        "gpt-dynamic-test",
		Source:    models.SourceUpstream,
		IsDefault: true,
	}}, time.Now().UTC())

	app := &App{
		cfg:    config.Config{DefaultModel: "gpt-5.4"},
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
