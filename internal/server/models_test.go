package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/gin-gonic/gin"

	"chatgpt-codex-proxy/internal/config"
	"chatgpt-codex-proxy/internal/models"
)

func TestHandleModelsReturnsCodexClientMetadata(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	app := &App{cfg: config.Config{ProxyAPIKey: "test-key", DefaultModel: "gpt-6.2-sol"}, engine: gin.New()}
	app.routes()
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/models?client_version=0.166.0", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	app.Handler().ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

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
	for slug, contextWindow := range map[string]float64{"gpt-6.2-sol": 500000, "gpt-6.2-astra": 272000, "gpt-6.1-terra": 500000} {
		index := slices.IndexFunc(body.Models, func(model map[string]any) bool { return model["slug"] == slug })
		if index < 0 {
			t.Fatalf("Codex model catalog is missing %s", slug)
		}
		model := body.Models[index]
		if model["context_window"] != contextWindow || model["max_context_window"] != float64(872000) || model["default_reasoning_level"] != "low" {
			t.Fatalf("%s metadata = %#v", slug, model)
		}
		var efforts []string
		for _, level := range model["supported_reasoning_levels"].([]any) {
			efforts = append(efforts, level.(map[string]any)["effort"].(string))
		}
		if !slices.Equal(efforts, []string{"low", "medium", "high", "xhigh", "max", "ultra"}) {
			t.Fatalf("%s reasoning levels = %v", slug, efforts)
		}
	}
}

func TestHandleModelsFollowsUpstreamOrderAndVisibility(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	catalog := models.NewCatalog(models.BootstrapEntries())
	catalog.ApplyRouteModels("acct:acct_pro", []models.Entry{
		{ID: "codex-auto-review", Priority: 3, Hidden: true},
		{ID: "gpt-6-astra", Priority: 2},
		{ID: "gpt-6.1-sol", Priority: 1},
	})
	app := &App{cfg: config.Config{ProxyAPIKey: "test-key", DefaultModel: "gpt-6.1-sol"}, engine: gin.New(), models: catalog}
	app.routes()
	get := func(path, anthropicVersion string) []byte {
		t.Helper()
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer test-key")
		if anthropicVersion != "" {
			req.Header.Set("anthropic-version", anthropicVersion)
		}
		app.Handler().ServeHTTP(recorder, req)
		if recorder.Code != http.StatusOK {
			t.Fatalf("GET %s: status = %d, body = %s", path, recorder.Code, recorder.Body.String())
		}
		return recorder.Body.Bytes()
	}

	for _, anthropicVersion := range []string{"", "2023-06-01"} {
		var list struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(get("/v1/models", anthropicVersion), &list); err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, model := range list.Data {
			ids = append(ids, model.ID)
		}
		if len(ids) < 2 || ids[0] != "gpt-6.1-sol" || ids[1] != "gpt-6-astra" || slices.Contains(ids, "codex-auto-review") {
			t.Fatalf("anthropic-version %q: model list = %v", anthropicVersion, ids)
		}
	}

	var codexList struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(get("/v1/models?client_version=0.169.0", ""), &codexList); err != nil {
		t.Fatal(err)
	}
	slugs := make([]any, 0, 3)
	for _, model := range codexList.Models[:3] {
		slugs = append(slugs, model["slug"], model["visibility"])
	}
	if want := []any{"gpt-6.1-sol", "list", "gpt-6-astra", "list", "codex-auto-review", "hide"}; !slices.Equal(slugs, want) {
		t.Fatalf("Codex client models = %v, want %v", slugs, want)
	}
	if _, ok := catalog.Get("codex-auto-review"); !ok {
		t.Fatal("hidden model is no longer routable")
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
