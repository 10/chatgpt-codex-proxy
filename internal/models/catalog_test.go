package models

import (
	"testing"

	"chatgpt-codex-proxy/internal/accounts"
	"chatgpt-codex-proxy/internal/codex"
)

func TestAstraCatalogPreservesAccountAccessAndMetadataAcrossCache(t *testing.T) {
	t.Parallel()

	allowed := accounts.Record{ID: "allowed", PlanType: "team"}
	other := accounts.Record{ID: "other", PlanType: "team"}
	catalog := NewCatalog(BootstrapEntries())
	catalog.RegisterRoute(RoutingKeyForRecord(other))
	entries := NormalizeBackendEntries([]codex.BackendModelEntry{{
		Slug: "gpt-6-astra", ContextWindow: 300000, MaxContextWindow: 900000,
		DefaultReasoningLevel:    "high",
		SupportedReasoningLevels: []codex.BackendReasoningEffort{{Effort: "high"}, {Effort: "max"}},
	}})
	catalog.ApplyRouteModels(RoutingKeyForRecord(allowed), entries)
	if entry, _ := catalog.Get("gpt-6-astra"); entry.DefaultReasoningEffort != "high" || entry.ContextWindow != 300000 {
		t.Fatalf("unrefreshed account overwrote fetched metadata: %#v", entry)
	}
	catalog.ApplyRouteModels(RoutingKeyForRecord(other), []Entry{{ID: "gpt-5.6-sol"}})

	dir := t.TempDir()
	if err := SaveCache(dir, catalog.Snapshot()); err != nil {
		t.Fatal(err)
	}
	snapshot, err := LoadCache(dir)
	if err != nil {
		t.Fatal(err)
	}
	restored := NewCatalog(BootstrapEntries())
	restored.LoadCache(snapshot)
	if !restored.SupportsRecord(allowed, "gpt-6-astra") || restored.SupportsRecord(other, "gpt-6-astra") {
		t.Fatal("Astra access was not isolated between accounts on the same plan")
	}
	entry, ok := restored.Get("gpt-6-astra")
	if !ok || entry.ContextWindow != 300000 || entry.MaxContextWindow != 900000 || len(entry.SupportedReasoningEfforts) != 2 {
		t.Fatalf("cached Astra metadata = %#v", entry)
	}
}

func TestRegisterRoutePreservesBootstrapVisibilityUntilRouteRefreshes(t *testing.T) {
	t.Parallel()

	catalog := NewCatalog(BootstrapEntries())
	catalog.RegisterRoute("acct:acct_free")
	catalog.ApplyRouteModels("acct:acct_plus", []Entry{
		{ID: "gpt-premium-only", IsDefault: true},
	})

	visible := catalog.List()
	seen := make(map[string]bool, len(visible))
	for _, entry := range visible {
		seen[entry.ID] = true
	}
	if !seen["gpt-premium-only"] {
		t.Fatal("premium model missing from visible list")
	}
	if !seen["gpt-5.6-terra"] {
		t.Fatal("bootstrap model missing while a known route remains unrefreshed")
	}

	freeRecord := accounts.Record{ID: "acct_free", PlanType: "free"}
	if !catalog.SupportsRecord(freeRecord, "gpt-5.6-terra") {
		t.Fatal("SupportsRecord(free, bootstrap) = false, want bootstrap fallback for unrefreshed route")
	}
	if catalog.SupportsRecord(freeRecord, "gpt-premium-only") {
		t.Fatal("SupportsRecord(free, premium) = true, want false")
	}
}
