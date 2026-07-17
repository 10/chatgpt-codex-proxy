package models

import (
	"slices"
	"testing"

	"chatgpt-codex-proxy/internal/accounts"
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
		"gpt-5.6-sol",
		"gpt-5.6-terra",
		"gpt-5.6-luna",
		"gpt-5.5",
		"gpt-5.4",
		"gpt-5.4-mini",
		"gpt-5.3-codex-spark",
	}
	if !slices.Equal(modelIDs, wantModelIDs) {
		t.Errorf("BootstrapEntries() model IDs = %v, want %v", modelIDs, wantModelIDs)
	}

	tests := []struct {
		modelID string
		efforts []string
	}{
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

	if !slices.Equal(defaults, []string{"gpt-5.6-sol"}) {
		t.Errorf("BootstrapEntries() defaults = %v, want [gpt-5.6-sol]", defaults)
	}
	if got := entries["gpt-5.3-codex-spark"].DefaultReasoningEffort; got != "high" {
		t.Errorf("BootstrapEntries() gpt-5.3-codex-spark default reasoning effort = %q, want high", got)
	}
}

func TestSupportsRecordRequiresKnownRouteSupportOnceSupportMapExists(t *testing.T) {
	t.Parallel()

	catalog := NewCatalog(BootstrapEntries())
	catalog.ApplyRouteModels("plan:plus", []Entry{
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

	if !catalog.SupportsRecord(record, "gpt-5.4") {
		t.Fatal("SupportsRecord() = false, want bootstrap model allowed before any route support is known")
	}
}

func TestRegisterRoutePreservesBootstrapVisibilityUntilRouteRefreshes(t *testing.T) {
	t.Parallel()

	catalog := NewCatalog(BootstrapEntries())
	catalog.RegisterRoute("plan:free")
	catalog.ApplyRouteModels("plan:plus", []Entry{
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
	if !seen["gpt-5.4"] {
		t.Fatal("bootstrap model missing while a known route remains unrefreshed")
	}

	freeRecord := accounts.Record{ID: "acct_free", PlanType: "free"}
	if !catalog.SupportsRecord(freeRecord, "gpt-5.4") {
		t.Fatal("SupportsRecord(free, bootstrap) = false, want bootstrap fallback for unrefreshed route")
	}
	if catalog.SupportsRecord(freeRecord, "gpt-premium-only") {
		t.Fatal("SupportsRecord(free, premium) = true, want false")
	}
}

func TestResolveDefaultForRecordUsesRoutableModel(t *testing.T) {
	t.Parallel()

	catalog := NewCatalog(BootstrapEntries())
	catalog.ApplyRouteModels("plan:plus", []Entry{
		{ID: "gpt-premium-default", IsDefault: true},
		{ID: "gpt-free-basic"},
	})
	catalog.ApplyRouteModels("plan:free", []Entry{
		{ID: "gpt-free-basic"},
	})

	freeRecord := accounts.Record{ID: "acct_free", PlanType: "free"}
	if got := catalog.ResolveDefaultForRecord(freeRecord, ""); got != "gpt-free-basic" {
		t.Fatalf("ResolveDefaultForRecord(free) = %q, want gpt-free-basic", got)
	}
}
