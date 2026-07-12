package server

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"chatgpt-codex-proxy/internal/middleware"
)

const modelCreatedTimestamp int64 = 1700000000

var codexImageModels = []string{"gpt-image-1.5", "gpt-image-2"}

type modelListResponse struct {
	Object string          `json:"object"`
	Data   []modelResponse `json:"data"`
}

type modelResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

func (a *App) handleModels(c *gin.Context) {
	entries := a.modelCatalog().List()
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
	c.JSON(http.StatusOK, modelListResponse{Object: "list", Data: data})
}

func (a *App) handleModelByID(c *gin.Context) {
	modelID := c.Param("model_id")
	model, ok := a.modelCatalog().Get(modelID)
	if !ok && isCodexImageModel(modelID) {
		c.JSON(http.StatusOK, modelObject(modelID))
		return
	}
	if !ok {
		c.AbortWithStatusJSON(http.StatusNotFound, middleware.OpenAIErrorPayload("Model '"+c.Param("model_id")+"' not found", "invalid_request_error", "model_not_found", "model"))
		return
	}
	c.JSON(http.StatusOK, modelObject(model.ID))
}

func isCodexImageModel(model string) bool {
	for _, candidate := range codexImageModels {
		if model == candidate {
			return true
		}
	}
	return false
}

func modelObject(model string) modelResponse {
	return modelResponse{
		ID:      model,
		Object:  "model",
		Created: modelCreatedTimestamp,
		OwnedBy: "openai",
	}
}
