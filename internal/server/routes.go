package server

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"chatgpt-codex-proxy/internal/anthropic"
	"chatgpt-codex-proxy/internal/middleware"
)

func (a *App) routes() {
	a.engine.GET("/health/live", a.handleHealthLive)

	protected := a.engine.Group("/")
	protected.Use(middleware.APIKeyWithUnauthorized(a.cfg.ProxyAPIKey, func(c *gin.Context) {
		if strings.TrimSpace(c.GetHeader("anthropic-version")) != "" || strings.HasPrefix(c.Request.URL.Path, "/v1/messages") {
			a.prepareAnthropicHeaders(c)
			middleware.SetRequestError(c, "authentication_error", "invalid x-api-key")
			c.AbortWithStatusJSON(http.StatusUnauthorized, anthropic.ErrorPayload("authentication_error", "invalid x-api-key", middleware.GetRequestID(c)))
			return
		}
		middleware.SetRequestError(c, "invalid_api_key", "invalid_api_key")
		c.AbortWithStatusJSON(http.StatusUnauthorized, middleware.OpenAIErrorPayload("invalid_api_key", "authentication_error", "invalid_api_key", ""))
	}))
	protected.GET("/health", a.handleHealth)
	protected.GET("/v1/models", a.handleModels)
	protected.GET("/v1/models/:model_id", a.handleModelByID)
	protected.POST("/v1/completions", a.handleCompletions)
	protected.POST("/v1/chat/completions", a.handleChatCompletions)
	protected.GET("/v1/responses", a.handleResponsesWebSocket)
	protected.POST("/v1/responses", a.handleResponses)
	protected.POST("/v1/responses/compact", a.handleResponsesCompact)
	protected.POST("/v1/images/generations", a.handleImageGenerations)
	protected.POST("/v1/images/edits", a.handleImageEdits)
	protected.POST("/v1/messages", a.handleAnthropicMessages)
	protected.POST("/v1/messages/count_tokens", a.handleAnthropicCountTokens)

	adminGroup := protected.Group("/admin")
	adminGroup.GET("/accounts", a.handleAdminAccounts)
	adminGroup.POST("/accounts/device-login/start", a.handleAdminDeviceLoginStart)
	adminGroup.GET("/accounts/device-login/:login_id", a.handleAdminDeviceLoginGet)
	adminGroup.DELETE("/accounts/:account_id", a.handleAdminAccountDelete)
	adminGroup.PATCH("/accounts/:account_id", a.handleAdminAccountPatch)
	adminGroup.GET("/accounts/:account_id/usage", a.handleAdminAccountUsage)
	adminGroup.POST("/accounts/:account_id/refresh", a.handleAdminAccountRefresh)
	adminGroup.GET("/rotation", a.handleAdminRotationGet)
	adminGroup.PUT("/rotation", a.handleAdminRotationPut)
}
