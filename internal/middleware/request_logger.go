package middleware

import (
	"log/slog"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	RequestOutcomeKey      = "request_outcome"
	RequestErrorCodeKey    = "request_error_code"
	RequestErrorMessageKey = "request_error_message"
	RequestResponseIDKey   = "request_response_id"
	RequestUsageKey        = "request_usage"
)

const maxLoggedErrorLength = 2048

func SetRequestOutcome(c *gin.Context, outcome string) {
	c.Set(RequestOutcomeKey, strings.TrimSpace(outcome))
}

func SetRequestError(c *gin.Context, code, message string) {
	c.Set(RequestErrorCodeKey, strings.TrimSpace(code))
	c.Set(RequestErrorMessageKey, truncateLogValue(message))
}

func SetRequestResponseID(c *gin.Context, responseID string) {
	c.Set(RequestResponseIDKey, strings.TrimSpace(responseID))
}

type requestUsage struct {
	input, cached, output int64
}

func SetRequestUsage(c *gin.Context, input, cached, output int64) {
	c.Set(RequestUsageKey, requestUsage{input: input, cached: cached, output: output})
}

func RequestLogger(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Request.URL.Path
		if path == "/health/live" {
			c.Next()
			return
		}

		start := time.Now()
		c.Next()

		status := c.Writer.Status()
		outcome := c.GetString(RequestOutcomeKey)
		if outcome == "" {
			outcome = defaultRequestOutcome(status)
		}
		attrs := []any{
			"request_id", GetRequestID(c),
			"method", c.Request.Method,
			"path", path,
			"route", c.FullPath(),
			"status", status,
			"outcome", outcome,
			"latency_ms", time.Since(start).Milliseconds(),
			"client_ip", c.ClientIP(),
		}

		if size := c.Writer.Size(); size >= 0 {
			attrs = append(attrs, "bytes_written", size)
		}
		if userAgent := c.Request.UserAgent(); userAgent != "" {
			attrs = append(attrs, "user_agent", userAgent)
		}
		if query := c.Request.URL.RawQuery; query != "" {
			attrs = append(attrs, "query", query)
		}
		if accountID := c.GetString(RequestAccountIDKey); accountID != "" {
			attrs = append(attrs, "account_id", accountID)
		}
		if upstreamAccountID := c.GetString(RequestUpstreamAccountIDKey); upstreamAccountID != "" {
			attrs = append(attrs, "upstream_account_id", upstreamAccountID)
		}
		if responseID := c.GetString(RequestResponseIDKey); responseID != "" {
			attrs = append(attrs, "response_id", responseID)
		}
		if usage, ok := c.Value(RequestUsageKey).(requestUsage); ok {
			attrs = append(attrs, "input_tokens", usage.input, "cached_tokens", usage.cached, "output_tokens", usage.output)
		}
		if errorCode := c.GetString(RequestErrorCodeKey); errorCode != "" {
			attrs = append(attrs, "error_code", errorCode)
		}
		if errorMessage := c.GetString(RequestErrorMessageKey); errorMessage != "" {
			attrs = append(attrs, "error", errorMessage)
		}

		logger.Log(c.Request.Context(), requestLogLevel(status, outcome), "http request completed", attrs...)
	}
}

func defaultRequestOutcome(status int) string {
	switch {
	case status >= 500:
		return "server_error"
	case status >= 400:
		return "request_error"
	default:
		return "success"
	}
}

func requestLogLevel(status int, outcome string) slog.Level {
	switch outcome {
	case "client_canceled":
		return slog.LevelInfo
	case "request_timeout":
		return slog.LevelWarn
	case "upstream_error", "stream_error":
		return slog.LevelError
	}
	switch {
	case status >= 500:
		return slog.LevelError
	case status == 401 || status == 403 || status == 429:
		return slog.LevelWarn
	default:
		return slog.LevelInfo
	}
}

func truncateLogValue(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= maxLoggedErrorLength {
		return value
	}
	return value[:maxLoggedErrorLength] + "…"
}
