package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"chatgpt-codex-proxy/internal/accounts"
	"chatgpt-codex-proxy/internal/codex"
	"chatgpt-codex-proxy/internal/jsonutil"
	"chatgpt-codex-proxy/internal/middleware"
	"chatgpt-codex-proxy/internal/models"
	"chatgpt-codex-proxy/internal/translate"
)

var responsesWebSocketUpgrader = websocket.Upgrader{
	CheckOrigin: func(*http.Request) bool { return true },
}

type responsesWebSocketStream interface {
	eventStream
	SendJSON(any) error
}

type responsesWebSocketConnector func(context.Context, string, http.Header, any) (responsesWebSocketStream, error)

type responsesWebSocketSession struct {
	stream         responsesWebSocketStream
	account        accounts.Record
	lastResponseID string
}

func (a *App) connectResponsesWebSocket(ctx context.Context, endpoint string, headers http.Header, body any) (responsesWebSocketStream, error) {
	if a.wsConnector != nil {
		return a.wsConnector(ctx, endpoint, headers, body)
	}
	return codex.ConnectWS(ctx, endpoint, headers, body)
}

func (a *App) handleResponsesWebSocket(c *gin.Context) {
	conn, err := responsesWebSocketUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	session := responsesWebSocketSession{}
	defer func() {
		if session.stream != nil {
			_ = session.stream.Close()
		}
	}()

	for {
		messageType, message, err := conn.ReadMessage()
		if err != nil {
			return
		}
		if messageType != websocket.TextMessage && messageType != websocket.BinaryMessage {
			if !writeResponsesWebSocketError(conn, http.StatusBadRequest, "invalid_websocket_event", "Responses WebSocket events must contain JSON", "invalid_request_error", "") {
				return
			}
			continue
		}

		a.logIncomingPayload(c, "responses_websocket", message)
		normalized, err := normalizeResponsesWebSocketMessage(message, a.modelCatalog())
		if err != nil {
			status, code, message, param := responsesWebSocketRequestError(err)
			if !writeResponsesWebSocketError(conn, status, code, message, "invalid_request_error", param) {
				return
			}
			continue
		}

		if !a.handleResponsesWebSocketTurn(c, conn, normalized, &session) {
			return
		}
	}
}

type responsesWebSocketEnvelope struct {
	Type       string          `json:"type"`
	Background bool            `json:"background"`
	Generate   *bool           `json:"generate"`
	Input      json.RawMessage `json:"input"`
}

var errResponsesWebSocketBackground = errors.New("background responses are not supported over WebSocket")

func normalizeResponsesWebSocketMessage(body []byte, catalog *models.Catalog) (translate.NormalizedRequest, error) {
	var envelope responsesWebSocketEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return translate.NormalizedRequest{}, err
	}
	eventType := strings.TrimSpace(envelope.Type)
	if eventType != "response.create" && eventType != "response.append" {
		return translate.NormalizedRequest{}, fmt.Errorf("unsupported websocket event type %q", envelope.Type)
	}
	if envelope.Background {
		return translate.NormalizedRequest{}, errResponsesWebSocketBackground
	}

	// The regular Responses normalizer intentionally ignores transport-only
	// fields. WebSocket streaming is implicit, while generate belongs only to
	// the upstream WebSocket payload.
	normalized, err := normalizeResponsesBody(body, catalog)
	if err != nil {
		return translate.NormalizedRequest{}, err
	}
	normalized.Stream = true
	normalized.Generate = envelope.Generate
	normalized.WebSocketAppend = eventType == "response.append"
	if normalized.WebSocketAppend {
		input := bytes.TrimSpace(envelope.Input)
		if len(input) == 0 || input[0] != '[' {
			return translate.NormalizedRequest{}, errors.New("response.append requires array field: input")
		}
	}
	return normalized, nil
}

func (a *App) handleResponsesWebSocketTurn(c *gin.Context, conn *websocket.Conn, normalized translate.NormalizedRequest, session *responsesWebSocketSession) bool {
	if normalized.WebSocketAppend && session.lastResponseID == "" {
		return writeResponsesWebSocketError(conn, http.StatusBadRequest, "invalid_request_error", "response.append received before response.create", "invalid_request_error", "")
	}
	if normalized.PreviousResponseID == "" && session.lastResponseID != "" && (normalized.WebSocketAppend || !hasPriorAssistantOrToolHistory(normalized.Input)) {
		normalized.PreviousResponseID = session.lastResponseID
	}
	resolution, err := a.resolveSession(normalized)
	if err != nil {
		code := "invalid_request_error"
		param := ""
		if errors.Is(err, errInvalidPreviousResponseID) {
			code = "previous_response_not_found"
			param = "previous_response_id"
		}
		return writeResponsesWebSocketError(conn, http.StatusBadRequest, code, err.Error(), "invalid_request_error", param)
	}
	if session.stream != nil && resolution.PreferredAccountID == "" {
		resolution.PreferredAccountID = session.account.ID
	}

	account, err := a.acquireAccountForResolution(c.Request.Context(), &resolution)
	if err != nil {
		status, code, message := a.responsesWebSocketOpenError(c, "", err)
		return writeResponsesWebSocketError(conn, status, code, message, "api_error", "")
	}
	a.setRequestAccount(c, account)

	body := resolution.Request.ToCodexWSCreatePayload()
	if session.stream == nil || session.account.ID != account.ID {
		if session.stream != nil {
			_ = session.stream.Close()
		}
		headers := codex.BuildHeaders(account.Token.AccessToken, codex.HeaderOptions{
			AccountID:   account.AccountID,
			Cookies:     account.Cookies,
			TurnState:   resolution.TurnState,
			RequestID:   codex.NewRequestID(),
			IncludeBeta: true,
		})
		a.logUpstreamPayload(c, "responses_websocket", "websocket", account.ID, body)
		stream, connectErr := a.connectResponsesWebSocket(c.Request.Context(), websocketEndpoint(a.cfg.CodexBaseURL), headers, body)
		if connectErr != nil {
			session.stream = nil
			session.account = accounts.Record{}
			status, code, message := a.responsesWebSocketOpenError(c, account.ID, connectErr)
			return writeResponsesWebSocketError(conn, status, code, message, "api_error", "")
		}
		session.stream = stream
		session.account = account
		a.observeQuotaSnapshot(account.ID, codex.ParseQuotaFromHeaders(stream.Headers()))
	} else {
		a.logUpstreamPayload(c, "responses_websocket", "websocket", account.ID, body)
		if err := session.stream.SendJSON(body); err != nil {
			_ = session.stream.Close()
			session.stream = nil
			session.account = accounts.Record{}
			status, code, message := a.responsesWebSocketOpenError(c, account.ID, err)
			return writeResponsesWebSocketError(conn, status, code, message, "api_error", "")
		}
	}

	accumulator := translate.NewAccumulator(resolution.Request)
	var tupleTextBuffer strings.Builder
	for {
		event, upstreamErr, err := a.nextStreamEvent(c.Request.Context(), account, accumulator, session.stream)
		if err != nil {
			if err == io.EOF {
				err = errIncompleteResponse
			}
			if a.recordRequestCancellation(c, account.ID, accumulator.ResponseID, err) {
				return false
			}
			if !upstreamErr {
				_ = session.stream.Close()
				session.stream = nil
				session.account = accounts.Record{}
			}
			status, code, message := a.classifyUpstreamError(account.ID, err)
			a.logUpstreamStreamFailure(c, "responses_websocket", account.ID, accumulator.ResponseID, err)
			middleware.SetRequestOutcome(c, "upstream_error")
			middleware.SetRequestError(c, code, message)
			middleware.SetRequestResponseID(c, accumulator.ResponseID)
			return writeResponsesWebSocketError(conn, status, code, message, "api_error", "")
		}

		for _, outgoing := range a.responsesStreamEvents(c, accumulator, resolution.Request, &tupleTextBuffer, event) {
			payload := translate.ResponseEventJSON(outgoing.Type, accumulator.ResponseID, outgoing.Payload)
			if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
				return false
			}
		}
		if event.IsTerminalResponse() {
			break
		}
	}

	a.finalizeSuccessfulStream(account.ID, accumulator, session.stream)
	session.lastResponseID = accumulator.ResponseID
	return true
}

func (a *App) responsesStreamEvents(c *gin.Context, accumulator *translate.Accumulator, normalized translate.NormalizedRequest, tupleTextBuffer *strings.Builder, event *codex.StreamEvent) []translate.ResponseStreamEvent {
	if normalized.TupleSchema != nil {
		switch event.Type {
		case "response.output_text.delta":
			tupleTextBuffer.WriteString(jsonutil.StringValue(event.Raw["delta"]))
			return nil
		case "response.output_text.done":
			if text := jsonutil.StringValue(event.Raw["text"]); text != "" {
				tupleTextBuffer.Reset()
				tupleTextBuffer.WriteString(text)
			}
			return nil
		}
	}

	if toolEvents, handled := accumulator.ResponsesStreamEventsForEvent(event); handled {
		return toolEvents
	}

	events := make([]translate.ResponseStreamEvent, 0, 3)
	if event.IsTerminalResponse() {
		if normalized.TupleSchema != nil && strings.TrimSpace(tupleTextBuffer.String()) != "" {
			reconverted := tupleTextBuffer.String()
			if patched, err := translate.ReconvertJSONText(reconverted, normalized.TupleSchema); err != nil {
				a.logTupleReconversionWarning(c, "responses", accumulator.ResponseID, err)
			} else {
				reconverted = patched
			}
			events = append(events, translate.ResponseStreamEvent{
				Type:    "response.output_text.delta",
				Payload: map[string]any{"delta": reconverted},
			})
		}
		events = append(events, accumulator.PendingResponseToolCallCompletionEvents()...)
	}

	payload := responseStreamPayload(event, accumulator)
	if normalized.TupleSchema != nil && event.IsTerminalResponse() {
		if err := translate.PatchResponseCompletedPayloadForTuple(payload, normalized.TupleSchema); err != nil {
			a.logTupleReconversionWarning(c, "responses", accumulator.ResponseID, err)
		}
	}
	return append(events, translate.ResponseStreamEvent{Type: event.Type, Payload: payload})
}

func responsesWebSocketRequestError(err error) (int, string, string, string) {
	var modelErr *translate.ModelNotFoundError
	if errors.As(err, &modelErr) {
		return http.StatusNotFound, "model_not_found", "Model '" + strings.TrimSpace(modelErr.Model) + "' not found", "model"
	}
	var contentErr *translate.UnsupportedContentPartError
	if errors.As(err, &contentErr) {
		return http.StatusBadRequest, "unsupported_content_part", contentErr.Error(), "input"
	}
	if errors.Is(err, errResponsesWebSocketBackground) {
		return http.StatusBadRequest, "unsupported_value", err.Error(), "background"
	}
	return http.StatusBadRequest, "invalid_request_error", err.Error(), ""
}

func (a *App) responsesWebSocketOpenError(c *gin.Context, accountID string, err error) (int, string, string) {
	if errors.Is(err, errContinuationAccountUnavailable) {
		return http.StatusServiceUnavailable, "continuation_account_unavailable", "continuation account unavailable"
	}
	if strings.Contains(strings.ToLower(err.Error()), "no active accounts") {
		return http.StatusServiceUnavailable, "no_available_accounts", "no available accounts"
	}
	status, code, message := a.classifyUpstreamError(accountID, err)
	a.logUpstreamRequestFailure(c, "responses_websocket", accountID, status, code, err)
	return status, code, message
}

func writeResponsesWebSocketError(conn *websocket.Conn, status int, code, message, errorType, param string) bool {
	payload := map[string]any{
		"type":   "error",
		"status": status,
		"error": middleware.OpenAIErrorBody{
			Message: message,
			Type:    errorType,
			Code:    code,
			Param:   param,
		},
	}
	return conn.WriteJSON(payload) == nil
}
