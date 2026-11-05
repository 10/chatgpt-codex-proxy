package server

import (
	"context"
	"strings"

	"github.com/gin-gonic/gin"

	"chatgpt-codex-proxy/internal/accounts"
	"chatgpt-codex-proxy/internal/conversation"
	"chatgpt-codex-proxy/internal/middleware"
)

func (a *App) acquireAccountForResolutionExcluding(ctx context.Context, resolution *sessionResolution, attempted map[string]struct{}) (accounts.Record, error) {
	if resolution.ExplicitPrevious || resolution.ImplicitResume {
		preferredID := strings.TrimSpace(resolution.PreferredAccountID)
		if preferredID == "" {
			return accounts.Record{}, errContinuationAccountUnavailable
		}
		record, err := a.accountMgr.EnsureReady(ctx, preferredID)
		if err != nil {
			return accounts.Record{}, errContinuationAccountUnavailable
		}
		if !a.modelCatalog().SupportsRecord(record, resolution.Request.Model) {
			return accounts.Record{}, errContinuationAccountUnavailable
		}
		return record, nil
	}
	record, modelID, err := a.acquireModelAccount(ctx, resolution.PreferredAccountID, resolution.Request.Model, attempted)
	if err != nil {
		return record, err
	}
	if resolution.Request.Model != modelID {
		resolution.Request.Model = modelID
		resolution.Original.Model = modelID
		if key := conversation.Derive(resolution.Request.Request); key != "" {
			resolution.ConversationKey = key
			resolution.Request.PromptCacheKey = key
			resolution.Original.PromptCacheKey = key
		}
	}
	return record, nil
}

func (a *App) acquireModelAccount(ctx context.Context, preferredID, modelID string, attempted map[string]struct{}) (accounts.Record, string, error) {
	record, err := a.selectReadyAccount(ctx, preferredID, modelID, attempted)
	if err != nil {
		return record, modelID, err
	}
	if strings.TrimSpace(modelID) == "" {
		modelID = a.modelCatalog().ResolveDefaultForRecord(record, a.cfg.DefaultModel)
		if strings.TrimSpace(modelID) == "" {
			return record, "", errContinuationAccountUnavailable
		}
	}
	return record, modelID, nil
}

func (a *App) selectReadyAccount(ctx context.Context, preferredID, modelID string, attempted map[string]struct{}) (accounts.Record, error) {
	record, err := a.accounts.AcquireMatching(preferredID, func(record accounts.Record) bool {
		if _, alreadyAttempted := attempted[record.ID]; alreadyAttempted {
			return false
		}
		return strings.TrimSpace(modelID) == "" || a.modelCatalog().SupportsRecord(record, modelID)
	})
	if err != nil {
		return record, err
	}
	ready, err := a.accountMgr.EnsureReady(ctx, record.ID)
	if err != nil {
		return record, err
	}
	return ready, nil
}

func (a *App) setRequestAccount(c *gin.Context, account accounts.Record) {
	if c == nil || account.ID == "" {
		return
	}
	c.Set(middleware.RequestAccountIDKey, account.ID)
	if account.AccountID != "" {
		c.Set(middleware.RequestUpstreamAccountIDKey, account.AccountID)
	}
}
