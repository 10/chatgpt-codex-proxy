package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"chatgpt-codex-proxy/internal/accounts"
	"chatgpt-codex-proxy/internal/models"
	"chatgpt-codex-proxy/internal/openai"
	"chatgpt-codex-proxy/internal/turn"
)

func (a *App) handleChatCompletions(c *gin.Context) {
	a.handlePublicRequest(
		c,
		"chat_completions",
		func(body []byte) (turn.NormalizedRequest, error) {
			return normalizeChatCompletionsBody(body, a.modelCatalog())
		},
		a.streamChatCompletion,
		(*turn.Accumulator).ChatCompletionObject,
		openai.PatchChatCompletionObjectForTuple,
	)
}

func (a *App) handleResponses(c *gin.Context) {
	a.handlePublicRequest(
		c,
		"responses",
		func(body []byte) (turn.NormalizedRequest, error) {
			return normalizeResponsesBody(body, a.modelCatalog())
		},
		a.streamResponses,
		(*turn.Accumulator).ResponsesObject,
		openai.PatchResponsesObjectForTuple,
	)
}

func (a *App) handlePublicRequest(
	c *gin.Context,
	endpoint string,
	normalize func([]byte) (turn.NormalizedRequest, error),
	stream func(*gin.Context, accounts.Record, turn.NormalizedRequest, eventStream),
	buildResponse func(*turn.Accumulator) map[string]any,
	patchTuple func(map[string]any, map[string]any) error,
) {
	body, err := readRequestBody(c.Request)
	if err != nil {
		a.respondOpenAIInvalidRequest(c, err)
		return
	}
	a.logIncomingPayload(c, endpoint, body)

	normalized, err := normalize(body)
	if err != nil {
		a.respondOpenAINormalizeError(c, err)
		return
	}

	opened, ok := a.resolveAndOpenRequest(c, endpoint, normalized)
	if !ok {
		return
	}
	defer opened.Stream.Close()

	if opened.Resolution.Request.Stream {
		stream(c, opened.Account, opened.Resolution.Request, opened.Stream)
		return
	}

	accumulator, err := a.collectEvents(c.Request.Context(), opened.Account, opened.Resolution.Request, opened.Stream)
	if err != nil {
		a.respondOpenAIUpstreamStreamError(c, endpoint, opened.Account.ID, "", err)
		return
	}
	response := buildResponse(accumulator)
	if err := patchTuple(response, normalized.TupleSchema); err != nil {
		a.logTupleReconversionWarning(c, endpoint, accumulator.ResponseID, err)
	}
	c.JSON(http.StatusOK, response)
}

func normalizeChatCompletionsBody(body []byte, catalog *models.Catalog) (turn.NormalizedRequest, error) {
	var chatReq openai.ChatCompletionsRequest
	if err := json.Unmarshal(body, &chatReq); err != nil {
		return turn.NormalizedRequest{}, err
	}

	if len(chatReq.Messages) > 0 {
		return openai.ChatCompletions(chatReq, catalog)
	}

	var envelope struct {
		Input              json.RawMessage `json:"input"`
		Instructions       json.RawMessage `json:"instructions"`
		PreviousResponseID string          `json:"previous_response_id"`
		Text               json.RawMessage `json:"text"`
		Reasoning          json.RawMessage `json:"reasoning"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return turn.NormalizedRequest{}, err
	}

	if len(bytes.TrimSpace(envelope.Input)) == 0 &&
		len(bytes.TrimSpace(envelope.Instructions)) == 0 &&
		strings.TrimSpace(envelope.PreviousResponseID) == "" &&
		len(bytes.TrimSpace(envelope.Text)) == 0 &&
		len(bytes.TrimSpace(envelope.Reasoning)) == 0 {
		return turn.NormalizedRequest{}, errors.New("request body must include chat messages or responses input")
	}

	return normalizeResponsesBody(body, catalog)
}

func normalizeResponsesBody(body []byte, catalog *models.Catalog) (turn.NormalizedRequest, error) {
	var req openai.ResponsesRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return turn.NormalizedRequest{}, err
	}
	return openai.Responses(req, catalog)
}
