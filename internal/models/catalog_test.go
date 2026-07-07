package models

import (
	"testing"
	"time"

	"chatgpt-codex-proxy/internal/accounts"
)

func TestSupportsRecordRequiresKnownRouteSupportOnceSupportMapExists(t *testing.T) {
	t.Parallel()

	catalog := NewCatalog(BootstrapEntries())
	catalog.ApplyRouteModels("plan:plus", []Entry{
		{ID: "gpt-premium-only", Source: SourceUpstream},
	}, time.Now().UTC())

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
		{ID: "gpt-premium-only", Source: SourceUpstream, IsDefault: true},
	}, time.Now().UTC())

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
		{ID: "gpt-premium-default", Source: SourceUpstream, IsDefault: true},
		{ID: "gpt-free-basic", Source: SourceUpstream},
	}, time.Now().UTC())
	catalog.ApplyRouteModels("plan:free", []Entry{
		{ID: "gpt-free-basic", Source: SourceUpstream},
	}, time.Now().UTC())

	freeRecord := accounts.Record{ID: "acct_free", PlanType: "free"}
	if got := catalog.ResolveDefaultForRecord(freeRecord, ""); got != "gpt-free-basic" {
		t.Fatalf("ResolveDefaultForRecord(free) = %q, want gpt-free-basic", got)
	}
}
