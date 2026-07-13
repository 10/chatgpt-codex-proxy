package codex

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"chatgpt-codex-proxy/internal/accounts"
	"chatgpt-codex-proxy/internal/config"
)

type memoryStore struct {
	state accounts.State
}

func (m *memoryStore) Load() (accounts.State, error) {
	return m.state, nil
}

func (m *memoryStore) Save(state accounts.State) error {
	m.state = state
	return nil
}

func TestGetUsageCachedBypassesEnsureReady(t *testing.T) {
	t.Parallel()

	resetAt := time.Now().UTC().Add(30 * time.Minute)
	accountsSvc, err := accounts.NewService(&memoryStore{state: accounts.State{
		Records: []*accounts.Record{{
			ID:        "acct_disabled",
			AccountID: "upstream_disabled",
			Status:    accounts.StatusDisabled,
			CachedQuota: &accounts.QuotaSnapshot{
				PlanType:  "plus",
				Source:    "usage_endpoint",
				FetchedAt: time.Now().UTC(),
				RateLimit: accounts.RateLimitWindow{
					Allowed:      true,
					LimitReached: false,
					ResetAt:      &resetAt,
				},
			},
			Token: accounts.OAuthToken{
				AccessToken: "access-token",
				ExpiresAt:   time.Now().UTC().Add(-time.Hour),
			},
			CreatedAt: time.Now().UTC(),
			UpdatedAt: time.Now().UTC(),
		}},
	}}, accounts.RotationLeastUsed)
	if err != nil {
		t.Fatalf("accounts.NewService() error = %v", err)
	}

	manager := NewAccountManager(config.Config{}, accountsSvc, nil, nil, nil)

	record, quota, err := manager.GetUsage(context.Background(), "acct_disabled", true)
	if err != nil {
		t.Fatalf("GetUsage(cached=true) error = %v", err)
	}
	if record.ID != "acct_disabled" {
		t.Fatalf("record.ID = %q, want acct_disabled", record.ID)
	}
	if record.Status != accounts.StatusDisabled {
		t.Fatalf("record.Status = %q, want disabled", record.Status)
	}
	if quota == nil {
		t.Fatal("quota = nil, want cached quota")
	}
	if quota.PlanType != "plus" {
		t.Fatalf("quota.PlanType = %q, want plus", quota.PlanType)
	}
}

func TestRefreshKeepsAccountActiveAfterTransientFailure(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"temporarily_unavailable"}`, http.StatusServiceUnavailable)
	}))
	defer server.Close()

	manager, accountsSvc, now := newRefreshTestManager(t, server.URL, "acct_transient_refresh")
	if _, err := manager.Refresh(context.Background(), "acct_transient_refresh"); err == nil {
		t.Fatal("Refresh() error = nil, want transient failure")
	}

	record, ok, err := accountsSvc.Get("acct_transient_refresh")
	if err != nil {
		t.Fatalf("accounts.Get() error = %v", err)
	}
	if !ok {
		t.Fatal("account missing after refresh failure")
	}
	if record.Status != accounts.StatusActive {
		t.Fatalf("account status = %q, want active", record.Status)
	}
	if record.CooldownUntil == nil || !record.CooldownUntil.After(now) {
		t.Fatalf("cooldown until = %v, want future cooldown", record.CooldownUntil)
	}
}

func TestRefreshExpiresAccountAfterInvalidGrant(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"refresh token is invalid"}`))
	}))
	defer server.Close()

	manager, accountsSvc, _ := newRefreshTestManager(t, server.URL, "acct_invalid_grant")
	if _, err := manager.Refresh(context.Background(), "acct_invalid_grant"); err == nil {
		t.Fatal("Refresh() error = nil, want invalid_grant failure")
	}

	record, ok, err := accountsSvc.Get("acct_invalid_grant")
	if err != nil {
		t.Fatalf("accounts.Get() error = %v", err)
	}
	if !ok {
		t.Fatal("account missing after refresh failure")
	}
	if record.Status != accounts.StatusExpired {
		t.Fatalf("account status = %q, want expired", record.Status)
	}
	if record.CooldownUntil != nil {
		t.Fatalf("cooldown until = %v, want none for terminal failure", record.CooldownUntil)
	}
}

func TestRefreshKeepsAccountActiveWhenRequestIsCanceled(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("refresh request reached server after its context was canceled")
	}))
	defer server.Close()

	manager, accountsSvc, now := newRefreshTestManager(t, server.URL, "acct_canceled_refresh")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := manager.Refresh(ctx, "acct_canceled_refresh"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Refresh() error = %v, want context.Canceled", err)
	}

	record, ok, err := accountsSvc.Get("acct_canceled_refresh")
	if err != nil {
		t.Fatalf("accounts.Get() error = %v", err)
	}
	if !ok {
		t.Fatal("account missing after canceled refresh")
	}
	if record.Status != accounts.StatusActive {
		t.Fatalf("account status = %q, want active", record.Status)
	}
	if record.CooldownUntil == nil || !record.CooldownUntil.After(now) {
		t.Fatalf("cooldown until = %v, want future cooldown", record.CooldownUntil)
	}
}

func newRefreshTestManager(t *testing.T, authIssuer, accountID string) (*AccountManager, *accounts.Service, time.Time) {
	t.Helper()

	now := time.Now().UTC()
	accountsSvc, err := accounts.NewService(&memoryStore{state: accounts.State{
		Records: []*accounts.Record{{
			ID:        accountID,
			AccountID: "upstream_" + accountID,
			Status:    accounts.StatusActive,
			Token: accounts.OAuthToken{
				AccessToken:  "expired-access",
				RefreshToken: "refresh-token",
				ExpiresAt:    now.Add(-time.Minute),
			},
			CreatedAt: now,
			UpdatedAt: now,
		}},
	}}, accounts.RotationLeastUsed)
	if err != nil {
		t.Fatalf("accounts.NewService() error = %v", err)
	}

	cfg := config.Config{AuthIssuer: authIssuer}
	return NewAccountManager(cfg, accountsSvc, NewOAuthService(cfg), nil, nil), accountsSvc, now
}
