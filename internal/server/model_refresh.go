package server

import (
	"context"
	"slices"
	"strings"
	"time"

	"chatgpt-codex-proxy/internal/accounts"
	"chatgpt-codex-proxy/internal/models"
)

func (a *App) refreshModels(ctx context.Context) {
	delay := time.Second
	hasFetched := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		if ctx.Err() != nil {
			return
		}
		hasFetched = a.refreshModelCatalog(ctx) || hasFetched
		delay = 10 * time.Second
		if hasFetched {
			delay = time.Hour
		}
	}
}

func (a *App) refreshModelCatalog(ctx context.Context) bool {
	records, err := a.accounts.List()
	if err != nil {
		a.logger.Warn("route account discovery failed", "error", err.Error())
		return false
	}
	now := time.Now().UTC()
	records = slices.DeleteFunc(records, func(record accounts.Record) bool { return !record.Eligible(now) })
	if len(records) == 0 {
		return false
	}
	slices.SortFunc(records, func(a, b accounts.Record) int { return strings.Compare(a.ID, b.ID) })
	for _, record := range records {
		a.models.RegisterRoute(models.RoutingKeyForRecord(record))
	}

	anySuccess := false
	for _, record := range records {
		key := models.RoutingKeyForRecord(record)
		ready, err := a.accountMgr.EnsureReady(ctx, record.ID)
		if err != nil {
			a.logger.Warn("codex model fetch ensure-ready failed", "route_key", key, "account_id", record.ID, "error", err.Error())
			continue
		}
		entries, err := a.httpClient.GetCodexModels(ctx, ready)
		if err != nil {
			a.logger.Warn("codex model fetch failed", "route_key", key, "account_id", ready.ID, "error", err.Error())
			continue
		}
		if len(entries) == 0 {
			continue
		}
		a.models.ApplyRouteModels(key, entries)
		anySuccess = true
	}

	if anySuccess {
		if err := models.SaveCache(a.cfg.DataDir, a.models.Snapshot()); err != nil {
			a.logger.Warn("save models cache failed", "error", err.Error())
		}
	}
	return anySuccess
}
