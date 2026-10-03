package codex

import (
	"testing"

	"chatgpt-codex-proxy/internal/config"
)

func TestParseCodexModelsResponseAcceptsLiveShape(t *testing.T) {
	t.Parallel()

	payload := `{
		"models": [
			{
				"slug": "gpt-5.6-terra",
				"display_name": "gpt-5.6-terra",
				"description": "Flagship",
				"default_reasoning_level": "medium",
				"supported_reasoning_levels": [
					{"effort": "low", "description": "Fastest"},
					{"effort": "medium", "description": "Balanced"}
				]
			},
			{
				"slug": "gpt-5.6-luna",
				"display_name": "GPT-5.4-Mini",
				"default_reasoning_level": "medium"
			}
		]
	}`

	models, err := parseCodexModelsResponse(payload)
	if err != nil {
		t.Fatalf("parseCodexModelsResponse() error = %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("len(models) = %d, want 2", len(models))
	}
	if models[0].Slug != "gpt-5.6-terra" {
		t.Fatalf("models[0].Slug = %q, want gpt-5.6-terra", models[0].Slug)
	}
	if models[0].DisplayName != "gpt-5.6-terra" {
		t.Fatalf("models[0].DisplayName = %q, want gpt-5.6-terra", models[0].DisplayName)
	}
	if models[0].DefaultReasoningLevel != "medium" {
		t.Fatalf("models[0].DefaultReasoningLevel = %q, want medium", models[0].DefaultReasoningLevel)
	}
}

func TestParseCodexModelsResponseRejectsUnsupportedShapes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		payload string
	}{
		{
			name:    "bare array",
			payload: `[{"slug":"gpt-5.6-terra"}]`,
		},
		{
			name:    "data field",
			payload: `{"data":[{"slug":"gpt-5.6-terra"}]}`,
		},
		{
			name:    "chat_models field",
			payload: `{"chat_models":{"models":[{"slug":"gpt-5.6-terra"}]}}`,
		},
		{
			name:    "categories field",
			payload: `{"categories":[{"models":[{"slug":"gpt-5.6-terra"}]}]}`,
		},
		{
			name:    "missing models",
			payload: `{}`,
		},
		{
			name:    "empty models",
			payload: `{"models":[]}`,
		},
		{
			name:    "nested models tree",
			payload: `{"models":[{"models":[{"slug":"gpt-5.6-terra"}]}]}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			models, err := parseCodexModelsResponse(tc.payload)
			if err == nil {
				t.Fatalf("parseCodexModelsResponse() models = %#v, want error", models)
			}
		})
	}
}

func TestCodexModelsURL(t *testing.T) {
	t.Parallel()

	client := NewHTTPClient(config.Config{CodexBaseURL: "https://chatgpt.com/backend-api"})
	want := "https://chatgpt.com/backend-api/codex/models?client_version=26.930.21537"
	if got := client.codexModelsURL(); got != want {
		t.Fatalf("codexModelsURL() = %q, want %q", got, want)
	}
}
