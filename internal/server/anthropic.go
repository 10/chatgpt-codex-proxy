package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"chatgpt-codex-proxy/internal/accounts"
	"chatgpt-codex-proxy/internal/anthropic"
	"chatgpt-codex-proxy/internal/jsonutil"
	"chatgpt-codex-proxy/internal/middleware"
	"chatgpt-codex-proxy/internal/translate"
)

const anthropicMessagesBodyLimit = 32 << 20

func (a *App) handleAnthropicCountTokens(c *gin.Context) {
	request, ok := a.decodeAnthropicRequest(c, "anthropic_count_tokens")
	if !ok {
		return
	}
	if request.MaxTokens == nil {
		zero := 0
		request.MaxTokens = &zero
	}
	normalized, err := anthropic.Normalize(request, a.modelCatalog())
	if err != nil {
		a.respondAnthropicNormalizeError(c, err)
		return
	}
	count, err := anthropic.CountInputTokens(normalized)
	if err != nil {
		a.writeAnthropicError(c, http.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"input_tokens": count})
}

func (a *App) handleAnthropicMessages(c *gin.Context) {
	request, ok := a.decodeAnthropicRequest(c, "anthropic_messages")
	if !ok {
		return
	}
	normalized, err := anthropic.Normalize(request, a.modelCatalog())
	if err != nil {
		a.respondAnthropicNormalizeError(c, err)
		return
	}

	resolution := sessionResolution{Request: normalized, Original: normalized}
	account, stream, quota, err := a.openStream(c, c.Request.Context(), "anthropic_messages", &resolution)
	if err != nil {
		a.setRequestAccount(c, account)
		a.respondAnthropicOpenError(c, account.ID, resolution.PreferredAccountID, err)
		return
	}
	a.setRequestAccount(c, account)
	a.observeQuotaSnapshot(account.ID, quota)
	defer stream.Close()

	if normalized.Stream {
		a.streamAnthropicMessage(c, account, normalized, stream)
		return
	}
	accumulator, err := a.collectEvents(c.Request.Context(), account, normalized, stream)
	if err != nil {
		a.respondAnthropicStreamFailure(c, account.ID, "", err)
		return
	}
	c.JSON(http.StatusOK, anthropic.BuildMessage(accumulator))
}

func (a *App) decodeAnthropicRequest(c *gin.Context, logLabel string) (anthropic.MessagesRequest, bool) {
	a.prepareAnthropicHeaders(c)
	if !a.validateAnthropicVersion(c) {
		return anthropic.MessagesRequest{}, false
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, anthropicMessagesBodyLimit)
	body, err := readRequestBody(c.Request)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			a.writeAnthropicError(c, http.StatusRequestEntityTooLarge, "request body exceeds 32 MB")
			return anthropic.MessagesRequest{}, false
		}
		a.writeAnthropicError(c, http.StatusBadRequest, err.Error())
		return anthropic.MessagesRequest{}, false
	}
	if len(body) > anthropicMessagesBodyLimit {
		a.writeAnthropicError(c, http.StatusRequestEntityTooLarge, "request body exceeds 32 MB")
		return anthropic.MessagesRequest{}, false
	}
	a.logIncomingPayload(c, logLabel, body)

	request, err := anthropic.DecodeMessages(body)
	if err != nil {
		a.writeAnthropicError(c, http.StatusBadRequest, err.Error())
		return anthropic.MessagesRequest{}, false
	}
	return request, true
}

func (a *App) streamAnthropicMessage(c *gin.Context, account accounts.Record, normalized translate.NormalizedRequest, stream eventStream) {
	prepareStreamResponse(c)
	accumulator := translate.NewAccumulator(normalized)
	inputTokens, _ := anthropic.CountInputTokens(normalized)
	encoder := anthropic.NewStreamEncoder(inputTokens)

	for {
		event, upstreamErr, err := a.nextStreamEvent(c.Request.Context(), account, accumulator, stream)
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = errIncompleteResponse
				upstreamErr = false
			}
			a.writeAnthropicStreamError(c, account.ID, accumulator.ResponseID, err, upstreamErr)
			return
		}
		for _, outgoing := range encoder.Events(event, accumulator) {
			payload, marshalErr := json.Marshal(outgoing)
			if marshalErr != nil {
				a.writeAnthropicStreamError(c, account.ID, accumulator.ResponseID, marshalErr, false)
				return
			}
			writeSSE(c.Writer, jsonutil.StringValue(outgoing["type"]), payload)
		}
		c.Writer.Flush()
		if event.Type == "response.completed" {
			break
		}
	}
	a.finalizeSuccessfulStream(account.ID, accumulator, stream)
	c.Writer.Flush()
}

func (a *App) validateAnthropicVersion(c *gin.Context) bool {
	version := strings.TrimSpace(c.GetHeader("anthropic-version"))
	if version == "" {
		a.writeAnthropicError(c, http.StatusBadRequest, "anthropic-version header is required")
		return false
	}
	if version != anthropic.Version {
		a.writeAnthropicError(c, http.StatusBadRequest, "unsupported anthropic-version "+version)
		return false
	}
	return true
}

func (a *App) respondAnthropicNormalizeError(c *gin.Context, err error) {
	var modelErr *translate.ModelNotFoundError
	if errors.As(err, &modelErr) {
		a.writeAnthropicError(c, http.StatusNotFound, "Model '"+strings.TrimSpace(modelErr.Model)+"' not found")
		return
	}
	a.writeAnthropicError(c, http.StatusBadRequest, err.Error())
}

func (a *App) respondAnthropicOpenError(c *gin.Context, actualAccountID, reportedAccountID string, err error) {
	if a.recordRequestCancellation(c, actualAccountID, "", err) {
		return
	}
	if strings.Contains(strings.ToLower(err.Error()), "no active accounts") {
		a.writeAnthropicError(c, http.StatusServiceUnavailable, "no available accounts")
		return
	}
	status, _, message := a.classifyUpstreamError(strings.TrimSpace(actualAccountID), err)
	logAccountID := jsonutil.FirstNonEmpty(actualAccountID, reportedAccountID)
	a.logUpstreamRequestFailure(c, "anthropic_messages", logAccountID, status, anthropic.ErrorTypeForStatus(status), err)
	middleware.SetRequestOutcome(c, "upstream_error")
	a.writeAnthropicError(c, status, message)
}

func (a *App) respondAnthropicStreamFailure(c *gin.Context, accountID, responseID string, err error) {
	if a.recordRequestCancellation(c, accountID, responseID, err) {
		return
	}
	status, _, message := a.classifyUpstreamError(accountID, err)
	a.logUpstreamStreamFailure(c, "anthropic_messages", accountID, responseID, err)
	middleware.SetRequestOutcome(c, "upstream_error")
	middleware.SetRequestResponseID(c, responseID)
	a.writeAnthropicError(c, status, message)
}

func (a *App) writeAnthropicError(c *gin.Context, status int, message string) {
	a.prepareAnthropicHeaders(c)
	errorType := anthropic.ErrorTypeForStatus(status)
	message = strings.TrimSpace(message)
	middleware.SetRequestError(c, errorType, message)
	c.AbortWithStatusJSON(status, anthropic.ErrorPayload(errorType, message, middleware.GetRequestID(c)))
}

func (a *App) prepareAnthropicHeaders(c *gin.Context) {
	if requestID := middleware.GetRequestID(c); requestID != "" {
		c.Header("request-id", requestID)
	}
}

func (a *App) writeAnthropicStreamError(c *gin.Context, accountID, responseID string, err error, classify bool) {
	if a.recordRequestCancellation(c, accountID, responseID, err) {
		return
	}
	status, message := http.StatusInternalServerError, err.Error()
	if classify {
		status, _, message = a.classifyUpstreamError(accountID, err)
	}
	a.logUpstreamStreamFailure(c, "anthropic_messages", accountID, responseID, err)
	errorType := anthropic.ErrorTypeForStatus(status)
	message = strings.TrimSpace(message)
	middleware.SetRequestOutcome(c, "upstream_error")
	middleware.SetRequestError(c, errorType, message)
	middleware.SetRequestResponseID(c, responseID)
	payload, _ := json.Marshal(anthropic.ErrorPayload(errorType, message, middleware.GetRequestID(c)))
	writeSSE(c.Writer, "error", payload)
	c.Writer.Flush()
}
