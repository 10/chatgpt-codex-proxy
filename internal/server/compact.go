package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"chatgpt-codex-proxy/internal/accounts"
	"chatgpt-codex-proxy/internal/codex"
	"chatgpt-codex-proxy/internal/jsonutil"
	"chatgpt-codex-proxy/internal/middleware"
	"chatgpt-codex-proxy/internal/models"
	"chatgpt-codex-proxy/internal/openai"
	"chatgpt-codex-proxy/internal/turn"
)

func (a *App) handleResponsesCompact(c *gin.Context) {
	body, err := readRequestBody(c.Request)
	if err != nil {
		a.respondOpenAIInvalidRequest(c, err)
		return
	}
	a.logIncomingPayload(c, "responses_compact", body)

	normalized, err := normalizeResponsesCompactBody(body, a.modelCatalog())
	if err != nil {
		a.respondOpenAINormalizeError(c, err)
		return
	}

	normalized, preferredAccountID, err := a.resolveCompactRequest(normalized)
	if err != nil {
		if a.writeRequestError(c, err) {
			return
		}
		a.respondOpenAINormalizeError(c, err)
		return
	}

	account, err := a.acquireAccountForCompact(c.Request.Context(), preferredAccountID, &normalized)
	if err != nil {
		a.handleOpenStreamError(c, "responses_compact", "", preferredAccountID, err)
		return
	}
	a.setRequestAccount(c, account)

	payload := normalized.CompactRequest
	a.logUpstreamPayload(c, "responses_compact", "http", account.ID, payload)
	caller := a.compactCaller
	if caller == nil {
		caller = a.httpClient.CompactResponse
	}
	upstream, quota, err := caller(c.Request.Context(), account, payload)
	if err != nil {
		if a.recordRequestCancellation(c, account.ID, "", err) {
			return
		}
		status, code, message := a.classifyUpstreamError(account.ID, err)
		a.logUpstreamRequestFailure(c, "responses_compact", account.ID, status, code, err)
		middleware.SetRequestOutcome(c, "upstream_error")
		a.writeOpenAIError(c, status, code, message, "api_error")
		return
	}

	a.observeQuotaSnapshot(account.ID, quota)
	a.accounts.NoteSuccess(account.ID)

	response := compactResponseObject(upstream)
	if err := openai.PatchResponsesObjectForTuple(response, normalized.TupleSchema); err != nil {
		a.logTupleReconversionWarning(c, "responses_compact", jsonutil.StringValue(response["id"]), err)
	}
	c.JSON(http.StatusOK, response)
}

func normalizeResponsesCompactBody(body []byte, catalog *models.Catalog) (turn.NormalizedCompactRequest, error) {
	var req openai.ResponsesCompactRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return turn.NormalizedCompactRequest{}, err
	}
	return openai.Compact(req, catalog)
}

func (a *App) resolveCompactRequest(normalized turn.NormalizedCompactRequest) (turn.NormalizedCompactRequest, string, error) {
	if strings.TrimSpace(normalized.PreviousResponseID) == "" {
		return normalized, "", nil
	}

	record, ok := a.continuations.Get(normalized.PreviousResponseID)
	if !ok {
		return turn.NormalizedCompactRequest{}, "", errInvalidPreviousResponseID
	}
	if strings.TrimSpace(normalized.Model) == "" {
		normalized.Model = record.Model
	}
	normalized.ToolNameAliases = turn.MergeToolNameAliases(normalized.ToolNameAliases, record.ToolNameAliases)

	history := cloneContinuationInputItems(record.InputHistory)
	if len(history) > 0 {
		normalized.Input = slices.Concat(history, normalized.Input)
	}

	return normalized, strings.TrimSpace(record.AccountID), nil
}

func (a *App) acquireAccountForCompact(ctx context.Context, preferredAccountID string, normalized *turn.NormalizedCompactRequest) (accounts.Record, error) {
	if !normalized.ModelExplicit && strings.TrimSpace(normalized.Model) == "" {
		account, err := a.accountMgr.AcquireReady(ctx, preferredAccountID)
		if err != nil {
			return accounts.Record{}, err
		}
		modelID := a.modelCatalog().ResolveDefaultForRecord(account, a.cfg.DefaultModel)
		if strings.TrimSpace(modelID) == "" {
			return accounts.Record{}, errContinuationAccountUnavailable
		}
		normalized.Model = modelID
		return account, nil
	}
	return a.accountMgr.AcquireReadyForModel(ctx, preferredAccountID, normalized.Model)
}

func compactResponseObject(upstream codex.CompactResponse) map[string]any {
	createdAt := upstream.CreatedAt
	if createdAt == 0 {
		createdAt = time.Now().UTC().Unix()
	}

	response := map[string]any{
		"id":         jsonutil.FirstNonEmpty(strings.TrimSpace(upstream.ID), fmt.Sprintf("resp_compact_%d", time.Now().UTC().UnixNano())),
		"object":     jsonutil.FirstNonEmpty(strings.TrimSpace(upstream.Object), "response.compaction"),
		"created_at": createdAt,
	}
	response["output"] = jsonutil.CloneValue(upstream.Output)
	if len(upstream.Usage) > 0 {
		response["usage"] = upstream.Usage
	}
	return response
}
