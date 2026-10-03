package models

import (
	"slices"
	"testing"

	"chatgpt-codex-proxy/internal/accounts"
	"chatgpt-codex-proxy/internal/codex"
)

func TestBootstrapEntriesMatchSupportedModels(t *testing.T) {
	t.Parallel()

	entries := make(map[string]Entry)
	var modelIDs []string
	var defaults []string
	for _, entry := range BootstrapEntries() {
		entries[entry.ID] = entry
		modelIDs = append(modelIDs, entry.ID)
		if entry.IsDefault {
			defaults = append(defaults, entry.ID)
		}
	}
	wantModelIDs := []string{
		"gpt-6.1-sol",
		"gpt-6-astra",
		"gpt-6-sol",
		"gpt-6-luna",
		"gpt-5.6-sol",
		"gpt-5.6-terra",
		"gpt-5.6-luna",
		"gpt-5.5",
		"gpt-5.3-codex-spark",
	}
	if !slices.Equal(modelIDs, wantModelIDs) {
		t.Errorf("BootstrapEntries() model IDs = %v, want %v", modelIDs, wantModelIDs)
	}

	tests := []struct {
		modelID string
		efforts []string
	}{
		{modelID: "gpt-6.1-sol", efforts: []string{"low", "medium", "high", "xhigh", "max", "ultra"}},
		{modelID: "gpt-6-astra", efforts: []string{"low", "medium", "high", "xhigh", "max", "ultra"}},
		{modelID: "gpt-6-sol", efforts: []string{"low", "medium", "high", "xhigh", "max", "ultra"}},
		{modelID: "gpt-6-luna", efforts: []string{"low", "medium", "high", "xhigh", "max"}},
		{modelID: "gpt-5.6-sol", efforts: []string{"low", "medium", "high", "xhigh", "max", "ultra"}},
		{modelID: "gpt-5.6-terra", efforts: []string{"low", "medium", "high", "xhigh", "max", "ultra"}},
		{modelID: "gpt-5.6-luna", efforts: []string{"low", "medium", "high", "xhigh", "max"}},
	}

	for _, tc := range tests {
		entry, ok := entries[tc.modelID]
		if !ok {
			t.Errorf("BootstrapEntries() missing %q", tc.modelID)
			continue
		}
		gotEfforts := make([]string, 0, len(entry.SupportedReasoningEfforts))
		for _, effort := range entry.SupportedReasoningEfforts {
			gotEfforts = append(gotEfforts, effort.ReasoningEffort)
		}
		if !slices.Equal(gotEfforts, tc.efforts) {
			t.Errorf("BootstrapEntries() %q efforts = %v, want %v", tc.modelID, gotEfforts, tc.efforts)
		}
	}

	if !slices.Equal(defaults, []string{"gpt-6.1-sol"}) {
		t.Errorf("BootstrapEntries() defaults = %v, want [gpt-6.1-sol]", defaults)
	}
	if got := entries["gpt-5.3-codex-spark"].DefaultReasoningEffort; got != "high" {
		t.Errorf("BootstrapEntries() gpt-5.3-codex-spark default reasoning effort = %q, want high", got)
	}
	for _, id := range []string{"gpt-6.1-sol", "gpt-6-astra", "gpt-5.6-sol"} {
		if entry := entries[id]; entry.DefaultReasoningEffort != "low" || entry.MaxContextWindow != 872000 {
			t.Errorf("%s bootstrap metadata = %#v", id, entry)
		}
	}
}

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

func TestSupportsRecordRequiresKnownRouteSupportOnceSupportMapExists(t *testing.T) {
	t.Parallel()

	catalog := NewCatalog(BootstrapEntries())
	catalog.ApplyRouteModels("acct:acct_plus", []Entry{
		{ID: "gpt-premium-only"},
	})

	plusRecord := accounts.Record{ID: "acct_plus", PlanType: "plus"}
	freeRecord := accounts.Record{ID: "acct_free", PlanType: "free"}

	if !catalog.SupportsRecord(plusRecord, "gpt-premium-only") {
		t.Fatal("SupportsRecord(plus) = false, want true")
	}
	if catalog.SupportsRecord(freeRecord, "gpt-premium-only") {
		t.Fatal("SupportsRecord(free) = true, want false when free route has no fetched support")
	}
}

func TestSupportsRecordAllowsBootstrapWhenNoRouteSupportKnown(t *testing.T) {
	t.Parallel()

	catalog := NewCatalog(BootstrapEntries())
	record := accounts.Record{ID: "acct_any", PlanType: "free"}

	if !catalog.SupportsRecord(record, "gpt-5.6-terra") {
		t.Fatal("SupportsRecord() = false, want bootstrap model allowed before any route support is known")
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

func TestResolveDefaultForRecordUsesRoutableModel(t *testing.T) {
	t.Parallel()

	catalog := NewCatalog(BootstrapEntries())
	catalog.ApplyRouteModels("acct:acct_plus", []Entry{
		{ID: "gpt-premium-default", IsDefault: true},
		{ID: "gpt-free-basic"},
	})
	catalog.ApplyRouteModels("acct:acct_free", []Entry{
		{ID: "gpt-free-basic"},
	})

	freeRecord := accounts.Record{ID: "acct_free", PlanType: "free"}
	if got := catalog.ResolveDefaultForRecord(freeRecord, ""); got != "gpt-free-basic" {
		t.Fatalf("ResolveDefaultForRecord(free) = %q, want gpt-free-basic", got)
	}
}
