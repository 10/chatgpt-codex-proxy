package server

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"chatgpt-codex-proxy/internal/accounts"
	"chatgpt-codex-proxy/internal/codex"
	"chatgpt-codex-proxy/internal/middleware"
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
