package server

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"chatgpt-codex-proxy/internal/accounts"
	"chatgpt-codex-proxy/internal/codex"
	"chatgpt-codex-proxy/internal/jsonutil"
	"chatgpt-codex-proxy/internal/middleware"
	"chatgpt-codex-proxy/internal/models"
	"chatgpt-codex-proxy/internal/openai"
)

func (a *App) writeOpenAIError(c *gin.Context, status int, code, message, errType string) {
	middleware.SetRequestError(c, code, message)
	c.AbortWithStatusJSON(status, middleware.OpenAIErrorPayload(message, errType, code, ""))
}

func (a *App) writeAdminError(c *gin.Context, status int, code, message string) {
	middleware.SetRequestError(c, code, message)
	c.AbortWithStatusJSON(status, gin.H{"error": code, "message": message})
}

func (a *App) classifyUpstreamError(accountID string, err error) (int, string, string) {
	var upstreamErr *codex.UpstreamError
	if errors.As(err, &upstreamErr) {
		text := strings.ToLower(upstreamErr.Message())
		switch upstreamErr.StatusCode {
		case http.StatusUnauthorized:
			a.markAccountError(accountID, accounts.StatusExpired, err)
			return http.StatusUnauthorized, "upstream_unauthorized", "upstream account unauthorized"
		case http.StatusForbidden:
			if !looksLikeCloudflareBlock(text) {
				a.setAccountCooldown(accountID, fallbackUntil(time.Now().UTC(), accounts.DefaultRateLimitFallback), err)
			}
		case http.StatusPaymentRequired:
			a.setAccountCooldown(accountID, a.quotaCooldownUntil(accountID, time.Now().UTC()), err)
			return http.StatusPaymentRequired, "quota_exhausted", "upstream account quota exhausted"
		case http.StatusTooManyRequests:
			a.setAccountCooldown(accountID, a.rateLimitCooldownUntil(accountID, err, time.Now().UTC()), err)
			return http.StatusTooManyRequests, "rate_limited", "upstream account rate limited"
		}
		return clampUpstreamStatus(upstreamErr.StatusCode), "upstream_error", upstreamErr.Message()
	}
	return http.StatusBadGateway, "upstream_error", err.Error()
}

func (a *App) markAccountError(accountID string, status accounts.Status, cause error) {
	if err := a.accounts.MarkError(accountID, status, cause.Error()); err != nil {
		a.logger.Error("persist account error status failed",
			"account_id", accountID,
			"status", string(status),
			"error", err.Error(),
		)
	}
}

func (a *App) setAccountCooldown(accountID string, until *time.Time, cause error) {
	if strings.TrimSpace(accountID) == "" {
		return
	}
	if err := a.accounts.SetCooldown(accountID, until, cause.Error()); err != nil {
		a.logger.Error("persist account cooldown failed",
			"account_id", accountID,
			"error", err.Error(),
		)
	}
}

func (a *App) rateLimitCooldownUntil(accountID string, cause error, now time.Time) *time.Time {
	var upstreamErr *codex.UpstreamError
	if errors.As(cause, &upstreamErr) && upstreamErr.RetryAfter > 0 {
		return fallbackUntil(now, time.Duration(upstreamErr.RetryAfter)*time.Second)
	}
	record, ok, err := a.accounts.Get(accountID)
	if err != nil {
		a.logger.Warn("load account cooldown failed", "account_id", accountID, "error", err.Error())
		return fallbackUntil(now, accounts.DefaultRateLimitFallback)
	}
	if ok {
		if reset := accounts.PrimaryRateLimitReset(record.CachedQuota, now); reset != nil {
			return reset
		}
	}
	return fallbackUntil(now, accounts.DefaultRateLimitFallback)
}

func (a *App) quotaCooldownUntil(accountID string, now time.Time) *time.Time {
	record, ok, err := a.accounts.Get(accountID)
	if err != nil {
		a.logger.Warn("load account cooldown failed", "account_id", accountID, "error", err.Error())
		return fallbackUntil(now, accounts.DefaultQuotaFallback)
	}
	if ok {
		if reset := accounts.QuotaReset(record.CachedQuota, now); reset != nil {
			return reset
		}
	}
	return fallbackUntil(now, accounts.DefaultQuotaFallback)
}

func clampUpstreamStatus(status int) int {
	if status >= 400 && status < 600 {
		return status
	}
	return http.StatusBadGateway
}

func looksLikeCloudflareBlock(text string) bool {
	normalized := strings.ToLower(strings.TrimSpace(text))
	return strings.Contains(normalized, "cf_chl") ||
		strings.Contains(normalized, "<!doctype") ||
		strings.Contains(normalized, "<html")
}

func fallbackUntil(now time.Time, duration time.Duration) *time.Time {
	until := now.Add(duration).UTC()
	return &until
}

func (a *App) respondOpenAIInvalidRequest(c *gin.Context, err error) {
	a.writeOpenAIError(c, http.StatusBadRequest, "invalid_request_error", err.Error(), "invalid_request_error")
}

func (a *App) respondOpenAINormalizeError(c *gin.Context, err error) {
	var modelErr *models.NotFoundError
	if errors.As(err, &modelErr) {
		message := "Model '" + strings.TrimSpace(modelErr.Model) + "' not found"
		a.writeOpenAIError(c, http.StatusNotFound, "model_not_found", message, "invalid_request_error")
		return
	}
	var contentErr *openai.UnsupportedContentPartError
	if errors.As(err, &contentErr) {
		a.writeOpenAIError(c, http.StatusBadRequest, "unsupported_content_part", contentErr.Error(), "invalid_request_error")
		return
	}
	a.respondOpenAIInvalidRequest(c, err)
}

func (a *App) handleOpenStreamError(c *gin.Context, endpoint, actualAccountID, reportedAccountID string, err error) {
	if a.recordRequestCancellation(c, actualAccountID, "", err) {
		return
	}
	if errors.Is(err, errContinuationAccountUnavailable) {
		a.writeOpenAIError(c, http.StatusServiceUnavailable, "continuation_account_unavailable", "continuation account unavailable", "api_error")
		return
	}
	if errors.Is(err, accounts.ErrNoAvailableAccounts) {
		a.writeOpenAIError(c, http.StatusServiceUnavailable, "no_available_accounts", "no available accounts", "api_error")
		return
	}
	status, code, message := a.classifyUpstreamError(strings.TrimSpace(actualAccountID), err)
	logAccountID := jsonutil.FirstNonEmpty(actualAccountID, reportedAccountID)
	a.logUpstreamRequestFailure(c, endpoint, logAccountID, status, code, err)
	middleware.SetRequestOutcome(c, "upstream_error")
	a.writeOpenAIError(c, status, code, message, "api_error")
}

func (a *App) respondOpenAIUpstreamStreamError(c *gin.Context, endpoint, accountID, responseID string, err error) {
	if a.recordRequestCancellation(c, accountID, responseID, err) {
		return
	}
	status, code, message := a.classifyUpstreamError(accountID, err)
	a.logUpstreamStreamFailure(c, endpoint, accountID, responseID, err)
	middleware.SetRequestOutcome(c, "upstream_error")
	middleware.SetRequestResponseID(c, responseID)
	a.writeOpenAIError(c, status, code, message, "api_error")
}

func (a *App) respondStreamError(c *gin.Context, endpoint, accountID, responseID, eventName string, err error, classify bool) {
	if a.recordRequestCancellation(c, accountID, responseID, err) {
		return
	}
	status, code, message := http.StatusInternalServerError, "api_error", err.Error()
	if classify {
		status, code, message = a.classifyUpstreamError(accountID, err)
	}
	a.logUpstreamStreamFailure(c, endpoint, accountID, responseID, err)
	middleware.SetRequestOutcome(c, "upstream_error")
	middleware.SetRequestError(c, code, message)
	middleware.SetRequestResponseID(c, responseID)
	if endpoint == "responses" {
		writeResponsesStreamError(c.Writer, status, message)
		c.Writer.Flush()
		return
	}
	writeSSE(c.Writer, eventName, mustJSON(middleware.OpenAIErrorPayload(message, "api_error", code, "")))
	c.Writer.Flush()
}

func writeResponsesStreamError(writer io.Writer, status int, message string) {
	code := "unknown_error"
	switch status {
	case http.StatusUnauthorized:
		code = "invalid_api_key"
	case http.StatusForbidden:
		code = "insufficient_quota"
	case http.StatusTooManyRequests:
		code = "rate_limit_exceeded"
	case http.StatusNotFound:
		code = "model_not_found"
	case http.StatusRequestTimeout:
		code = "request_timeout"
	default:
		if status >= http.StatusInternalServerError {
			code = "internal_server_error"
		} else if status >= http.StatusBadRequest {
			code = "invalid_request_error"
		}
	}
	writeSSE(writer, "error", mustJSON(map[string]any{
		"type":            "error",
		"code":            code,
		"message":         strings.TrimSpace(message),
		"sequence_number": 0,
	}))
}
