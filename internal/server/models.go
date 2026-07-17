package server

import (
	"net/http"
	"slices"
	"strings"

	"github.com/gin-gonic/gin"

	"chatgpt-codex-proxy/internal/middleware"
	"chatgpt-codex-proxy/internal/models"
)

const modelCreatedTimestamp int64 = 1700000000

var codexImageModels = []string{"gpt-image-1.5", "gpt-image-2"}

type modelResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

func (a *App) handleModels(c *gin.Context) {
	entries := a.modelCatalog().List()
	if strings.TrimSpace(c.GetHeader("anthropic-version")) != "" {
		a.prepareAnthropicHeaders(c)
		if !a.validateAnthropicVersion(c) {
			return
		}
		data := make([]map[string]any, 0, len(entries))
		for _, entry := range entries {
			data = append(data, anthropicModelObject(entry.ID, entry.DisplayName))
		}
		response := gin.H{"data": data, "has_more": false}
		if len(data) > 0 {
			response["first_id"] = data[0]["id"]
			response["last_id"] = data[len(data)-1]["id"]
		}
		c.JSON(http.StatusOK, response)
		return
	}
	if strings.TrimSpace(c.Query("client_version")) != "" {
		c.JSON(http.StatusOK, codexClientModelsResponse(entries))
		return
	}
	data := make([]modelResponse, 0, len(entries)+len(codexImageModels))
	seen := make(map[string]bool, len(entries)+len(codexImageModels))
	for _, entry := range entries {
		data = append(data, modelObject(entry.ID))
		seen[entry.ID] = true
	}
	for _, model := range codexImageModels {
		if !seen[model] {
			data = append(data, modelObject(model))
		}
	}
	c.JSON(http.StatusOK, gin.H{"object": "list", "data": data})
}

func codexClientModelsResponse(entries []models.Entry) map[string]any {
	data := make([]map[string]any, 0, len(entries)+len(codexImageModels))
	seen := make(map[string]bool, len(entries))
	for index, entry := range entries {
		levels := make([]map[string]any, 0, len(entry.SupportedReasoningEfforts))
		for _, effort := range entry.SupportedReasoningEfforts {
			levels = append(levels, map[string]any{
				"effort":      effort.ReasoningEffort,
				"description": effort.Description,
			})
		}
		data = append(data, map[string]any{
			"slug":                       entry.ID,
			"display_name":               entry.DisplayName,
			"description":                entry.Description,
			"prefer_websockets":          true,
			"support_verbosity":          true,
			"default_verbosity":          "low",
			"input_modalities":           []string{"text", "image"},
			"context_window":             272000,
			"max_context_window":         1000000,
			"default_reasoning_level":    entry.DefaultReasoningEffort,
			"supported_reasoning_levels": levels,
			"visibility":                 "list",
			"supported_in_api":           true,
			"priority":                   index + 1,
		})
		seen[entry.ID] = true
	}
	for _, id := range codexImageModels {
		if seen[id] {
			continue
		}
		data = append(data, map[string]any{
			"slug":              id,
			"display_name":      id,
			"description":       id,
			"prefer_websockets": false,
			"visibility":        "hide",
			"supported_in_api":  true,
			"priority":          len(data) + 1,
		})
	}
	return map[string]any{"models": data}
}

func (a *App) handleModelByID(c *gin.Context) {
	modelID := c.Param("model_id")
	model, ok := a.modelCatalog().Get(modelID)
	if strings.TrimSpace(c.GetHeader("anthropic-version")) != "" {
		a.prepareAnthropicHeaders(c)
		if !a.validateAnthropicVersion(c) {
			return
		}
		if !ok {
			a.writeAnthropicError(c, http.StatusNotFound, "Model '"+modelID+"' not found")
			return
		}
		c.JSON(http.StatusOK, anthropicModelObject(model.ID, model.DisplayName))
		return
	}
	if !ok && isCodexImageModel(modelID) {
		c.JSON(http.StatusOK, modelObject(modelID))
		return
	}
	if !ok {
		middleware.SetRequestError(c, "model_not_found", "Model '"+modelID+"' not found")
		c.AbortWithStatusJSON(http.StatusNotFound, middleware.OpenAIErrorPayload("Model '"+c.Param("model_id")+"' not found", "invalid_request_error", "model_not_found", "model"))
		return
	}
	c.JSON(http.StatusOK, modelObject(model.ID))
}

func anthropicModelObject(id, displayName string) map[string]any {
	return map[string]any{
		"type":         "model",
		"id":           id,
		"display_name": displayName,
		"created_at":   "2023-11-06T00:00:00Z",
	}
}

func isCodexImageModel(model string) bool {
	return slices.Contains(codexImageModels, model)
}

func modelObject(model string) modelResponse {
	return modelResponse{
		ID:      model,
		Object:  "model",
		Created: modelCreatedTimestamp,
		OwnedBy: "openai",
	}
}
