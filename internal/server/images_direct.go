package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"chatgpt-codex-proxy/internal/accounts"
	"chatgpt-codex-proxy/internal/codex"
)

func prepareDirectImagePayload(body []byte, model string, stream bool) ([]byte, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	payload["model"] = mustRawJSON(model)
	if stream {
		payload["stream"] = json.RawMessage("true")
	} else {
		delete(payload, "stream")
	}
	return json.Marshal(payload)
}

func (a *App) handleDirectImageResponse(c *gin.Context, endpoint, path string, payload []byte, stream bool) bool {
	account, response, err := a.openDirectImageWithFailover(c.Request.Context(), c, endpoint, path, payload, stream)
	if err != nil {
		if directImageEndpointUnavailable(err) {
			return false
		}
		a.setRequestAccount(c, account)
		a.handleOpenStreamError(c, endpoint, account.ID, account.ID, err)
		return true
	}
	defer response.Body.Close()

	a.setRequestAccount(c, account)
	a.observeQuotaSnapshot(account.ID, codex.ParseQuotaFromHeaders(response.Header))
	contentType := strings.TrimSpace(response.Header.Get("Content-Type"))
	if stream {
		prepareStreamResponse(c)
		if contentType != "" {
			c.Header("Content-Type", contentType)
		}
		buffer := make([]byte, 32*1024)
		for {
			n, readErr := response.Body.Read(buffer)
			if n > 0 {
				_, _ = c.Writer.Write(buffer[:n])
				c.Writer.Flush()
			}
			if readErr != nil {
				if readErr != io.EOF {
					a.respondStreamError(c, endpoint, account.ID, "", "error", readErr, false)
					return true
				}
				break
			}
		}
	} else {
		body, readErr := io.ReadAll(response.Body)
		if readErr != nil {
			a.handleOpenStreamError(c, endpoint, account.ID, account.ID, readErr)
			return true
		}
		if contentType == "" {
			contentType = "application/json"
		}
		c.Data(http.StatusOK, contentType, body)
	}
	a.accounts.NoteSuccess(account.ID)
	return true
}

func (a *App) openDirectImageWithFailover(ctx context.Context, c *gin.Context, endpoint, path string, payload []byte, stream bool) (accounts.Record, *http.Response, error) {
	attempted := make(map[string]struct{})
	var lastAccount accounts.Record
	var lastErr error
	for {
		account, err := a.accounts.AcquireMatching("", func(record accounts.Record) bool {
			_, alreadyAttempted := attempted[record.ID]
			return !alreadyAttempted
		})
		if err != nil {
			if lastErr != nil && strings.Contains(strings.ToLower(err.Error()), "no active accounts") {
				return lastAccount, nil, lastErr
			}
			return account, nil, err
		}
		selected := account
		account, err = a.accountMgr.EnsureReady(ctx, selected.ID)
		if err != nil {
			account = selected
		}
		if err == nil {
			a.logUpstreamPayload(c, endpoint, "http", account.ID, json.RawMessage(payload))
			open := a.directImageOpen
			if open == nil {
				open = a.httpClient.OpenImage
			}
			var response *http.Response
			response, err = open(ctx, account, path, payload, stream)
			if err == nil {
				return account, response, nil
			}
		}
		err = normalizeRequestContextError(ctx, err)
		if directImageEndpointUnavailable(err) || !shouldFailoverRequest(err) {
			return account, nil, err
		}
		attempted[account.ID] = struct{}{}
		lastAccount = account
		lastErr = err
		a.classifyUpstreamError(account.ID, err)
	}
}

func directImageEndpointUnavailable(err error) bool {
	var upstreamErr *codex.UpstreamError
	if !errors.As(err, &upstreamErr) {
		return false
	}
	switch upstreamErr.StatusCode {
	case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented:
		return true
	default:
		return false
	}
}
