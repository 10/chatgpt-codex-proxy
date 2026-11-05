package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/gin-gonic/gin"

	"chatgpt-codex-proxy/internal/accounts"
	"chatgpt-codex-proxy/internal/codex"
	"chatgpt-codex-proxy/internal/jsonutil"
	"chatgpt-codex-proxy/internal/turn"
)

type eventStream interface {
	NextEvent() (*turn.StreamEvent, error)
	Close() error
	Headers() http.Header
}

var errIncompleteResponse = errors.New("upstream stream ended before a terminal response event")

type openedRequest struct {
	Resolution sessionResolution
	Account    accounts.Record
	Stream     eventStream
}

type bufferedEventStream struct {
	events []*turn.StreamEvent
	eventStream
}

func (s *bufferedEventStream) NextEvent() (*turn.StreamEvent, error) {
	if len(s.events) > 0 {
		event := s.events[0]
		s.events = s.events[1:]
		return event, nil
	}
	return s.eventStream.NextEvent()
}

func (a *App) resolveAndOpenRequest(c *gin.Context, endpoint string, normalized turn.NormalizedRequest) (openedRequest, bool) {
	resolution, err := a.resolveSession(normalized)
	if err != nil {
		if a.writeRequestError(c, err) {
			return openedRequest{}, false
		}
		a.respondOpenAINormalizeError(c, err)
		return openedRequest{}, false
	}

	account, stream, quota, err := a.openStream(c, c.Request.Context(), endpoint, &resolution)
	if err != nil && codex.IsInvalidReasoningSignatureError(err) {
		if sanitized, changed := resolution.Original.StripReasoningEncryptedContent(); changed {
			resolution = sessionResolution{
				Request:            sanitized,
				Original:           sanitized,
				PreferredAccountID: account.ID,
			}
			account, stream, quota, err = a.openStream(c, c.Request.Context(), endpoint, &resolution)
		}
	}
	if err != nil {
		a.setRequestAccount(c, account)
		reportedAccountID := jsonutil.FirstNonEmpty(account.ID, resolution.PreferredAccountID)
		a.handleOpenStreamError(c, endpoint, account.ID, reportedAccountID, err)
		return openedRequest{}, false
	}

	a.setRequestAccount(c, account)
	a.observeQuotaSnapshot(account.ID, quota)

	return openedRequest{
		Resolution: resolution,
		Account:    account,
		Stream:     stream,
	}, true
}

func (a *App) openStream(c *gin.Context, ctx context.Context, endpoint string, resolution *sessionResolution) (accounts.Record, eventStream, *accounts.QuotaSnapshot, error) {
	if resolution.Request.PreviousResponseID != "" {
		if resolution.ExplicitPrevious && resolution.ReplayAvailable {
			replay := *resolution
			replay.Request = resolution.Original
			replay.TurnState = ""
			replay.ExplicitPrevious = false
			if requestUsesHostedWebSearch(replay.Request) {
				return a.openStreamWithFailover(c, ctx, endpoint, &replay, a.openWSStream)
			}
			return a.openStreamWithFailover(c, ctx, endpoint, &replay, a.openHTTPStream)
		}
		account, stream, quota, err := a.openWSStream(c, ctx, endpoint, resolution, nil)
		if err == nil || !resolution.ImplicitResume {
			return account, stream, quota, err
		}
		fallback := *resolution
		fallback.Request = resolution.Original
		fallback.PreferredAccountID = ""
		fallback.TurnState = ""
		fallback.ImplicitResume = false
		return a.openStreamWithFailover(c, ctx, endpoint, &fallback, a.openHTTPStream)
	}
	if resolution.Request.Generate != nil {
		return a.openStreamWithFailover(c, ctx, endpoint, resolution, a.openWSStream)
	}
	if requestUsesHostedWebSearch(resolution.Request) {
		return a.openStreamWithFailover(c, ctx, endpoint, resolution, a.openWSStream)
	}
	return a.openStreamWithFailover(c, ctx, endpoint, resolution, a.openHTTPStream)
}

type streamOpenAttempt func(*gin.Context, context.Context, string, *sessionResolution, map[string]struct{}) (accounts.Record, eventStream, *accounts.QuotaSnapshot, error)

func (a *App) openStreamWithFailover(c *gin.Context, ctx context.Context, endpoint string, resolution *sessionResolution, open streamOpenAttempt) (accounts.Record, eventStream, *accounts.QuotaSnapshot, error) {
	attempted := make(map[string]struct{})
	var lastAccount accounts.Record
	var lastErr error
	for {
		account, stream, quota, err := open(c, ctx, endpoint, resolution, attempted)
		err = normalizeRequestContextError(ctx, err)
		if err == nil {
			prepared, prepareErr := a.prepareStreamForDelivery(ctx, account, stream, resolution.Request.Stream)
			if prepareErr == nil {
				return account, prepared, quota, nil
			}
			_ = stream.Close()
			err = prepareErr
		}
		if account.ID == "" {
			if lastErr != nil && errors.Is(err, accounts.ErrNoAvailableAccounts) {
				return lastAccount, nil, nil, lastErr
			}
			return account, nil, nil, err
		}
		if !shouldFailoverRequest(err) {
			return account, nil, nil, err
		}

		attempted[account.ID] = struct{}{}
		lastAccount = account
		lastErr = err
		a.classifyUpstreamError(account.ID, err)
	}
}

func (a *App) prepareStreamForDelivery(ctx context.Context, account accounts.Record, stream eventStream, streaming bool) (eventStream, error) {
	events := make([]*turn.StreamEvent, 0, 1)
	for {
		event, err := stream.NextEvent()
		if err != nil {
			err = normalizeRequestContextError(ctx, err)
			if err == io.EOF {
				return nil, errIncompleteResponse
			}
			return nil, err
		}
		if event == nil {
			return nil, errIncompleteResponse
		}
		if a.observeQuotaEvent(account, event) {
			continue
		}
		if err := codex.StreamEventError(event); err != nil {
			return nil, err
		}
		events = append(events, event)
		if streaming || event.IsTerminalResponse() {
			return &bufferedEventStream{events: events, eventStream: stream}, nil
		}
	}
}

func shouldFailoverRequest(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var upstreamErr *codex.UpstreamError
	if !errors.As(err, &upstreamErr) {
		return true
	}
	switch upstreamErr.StatusCode {
	case http.StatusUnauthorized,
		http.StatusPaymentRequired,
		http.StatusForbidden,
		http.StatusRequestTimeout,
		http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func requestUsesHostedWebSearch(request turn.NormalizedRequest) bool {
	return slices.ContainsFunc(request.Tools, func(tool turn.ToolDefinition) bool {
		return strings.TrimSpace(tool.Type) == "web_search"
	})
}

func (a *App) openHTTPStream(c *gin.Context, ctx context.Context, endpoint string, resolution *sessionResolution, attempted map[string]struct{}) (accounts.Record, eventStream, *accounts.QuotaSnapshot, error) {
	account, err := a.acquireAccountForResolutionExcluding(ctx, resolution, attempted)
	if err != nil {
		return account, nil, nil, err
	}
	request := resolution.Request.Request
	a.logUpstreamPayload(c, endpoint, "http", account.ID, codex.StreamRequestPayload(request))
	var stream eventStream
	if a.httpStream != nil {
		stream, err = a.httpStream(ctx, account, request, resolution.TurnState)
	} else {
		stream, err = a.httpClient.StreamResponse(ctx, account, request, resolution.TurnState)
	}
	if err != nil {
		return account, nil, nil, err
	}
	return account, stream, codex.ParseQuotaFromHeaders(stream.Headers()), nil
}

func (a *App) openWSStream(c *gin.Context, ctx context.Context, endpoint string, resolution *sessionResolution, attempted map[string]struct{}) (accounts.Record, eventStream, *accounts.QuotaSnapshot, error) {
	account, err := a.acquireAccountForResolutionExcluding(ctx, resolution, attempted)
	if err != nil {
		return account, nil, nil, err
	}
	body := codex.WSCreatePayload(resolution.Request)
	a.logUpstreamPayload(c, endpoint, "websocket", account.ID, body)
	stream, err := a.connectResponsesWebSocket(ctx, account, resolution.Request, resolution.TurnState)
	if err != nil {
		return account, nil, nil, err
	}
	return account, stream, codex.ParseQuotaFromHeaders(stream.Headers()), nil
}

func (a *App) collectEvents(ctx context.Context, account accounts.Record, normalized turn.NormalizedRequest, stream eventStream) (*turn.Accumulator, error) {
	accumulator := turn.NewAccumulator(normalized)
	for {
		event, _, err := a.nextStreamEvent(ctx, account, accumulator, stream)
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		if event.IsTerminalResponse() {
			break
		}
	}

	if accumulator.ResponseID == "" || !accumulator.IsTerminal() {
		return nil, errIncompleteResponse
	}

	a.finalizeSuccessfulStream(account.ID, accumulator, stream)
	return accumulator, nil
}

func (a *App) nextStreamEvent(ctx context.Context, account accounts.Record, accumulator *turn.Accumulator, stream eventStream) (*turn.StreamEvent, bool, error) {
	for {
		event, err := stream.NextEvent()
		if err != nil {
			return nil, false, normalizeRequestContextError(ctx, err)
		}
		if a.observeQuotaEvent(account, event) {
			continue
		}
		accumulator.Apply(event)
		if upstreamErr := codex.StreamEventError(event); upstreamErr != nil {
			return nil, true, upstreamErr
		}
		return event, false, nil
	}
}

func (a *App) finalizeSuccessfulStream(accountID string, accumulator *turn.Accumulator, stream eventStream) {
	a.accounts.NoteSuccess(accountID)
	a.rememberContinuation(accountID, accumulator, stream.Headers().Get("x-codex-turn-state"))
}

func (a *App) observeQuotaSnapshot(accountID string, quota *accounts.QuotaSnapshot) {
	if quota == nil || strings.TrimSpace(accountID) == "" {
		return
	}
	if err := a.accounts.ObserveQuota(accountID, quota); err != nil {
		a.logger.Warn("persist quota snapshot failed", "account_id", accountID, "error", err.Error())
	}
}

func (a *App) observeQuotaEvent(account accounts.Record, event *turn.StreamEvent) bool {
	if event == nil || event.Type != "codex.rate_limits" {
		return false
	}
	quota := codex.ParseQuotaFromEvent(event, account.PlanType)
	a.observeQuotaSnapshot(account.ID, quota)
	return true
}
