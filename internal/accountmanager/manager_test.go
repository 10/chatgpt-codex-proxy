package accountmanager

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"chatgpt-codex-proxy/internal/accounts"
	"chatgpt-codex-proxy/internal/codexauth"
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

	manager := NewAccountManager(config.Config{}, accountsSvc, nil, nil)

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

func TestRefreshExpiresAccountAfterRejectedRefreshToken(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		status    int
		body      string
		wantError string
	}{
		"oauth invalid_grant":            {http.StatusBadRequest, `{"error":"invalid_grant","error_description":"refresh token is invalid"}`, "refresh token is invalid"},
		"reused refresh token":           {http.StatusBadRequest, `{"error":{"message":"Refresh token was already used.","code":"refresh_token_reused"}}`, "Refresh token was already used."},
		"unauthorized refresh":           {http.StatusUnauthorized, `{"error":{"message":"Unauthorized"}}`, "Unauthorized"},
		"rejected oauth client":          {http.StatusUnauthorized, `{"error":"invalid_client","error_description":"client authentication failed"}`, ""},
		"openai malformed refresh token": {http.StatusBadRequest, `{"error":{"message":"Invalid refresh token.","type":"invalid_request_error","param":null,"code":"invalid_refresh_token_ciphertext_header"}}`, "Invalid refresh token."},
		"openai invalid_refresh_token":   {http.StatusUnauthorized, `{"error":{"message":"Could not validate your refresh token. Please try signing in again.","type":"invalid_request_error","param":null,"code":"invalid_refresh_token"}}`, "Could not validate your refresh token. Please try signing in again."},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()

			manager, accountsSvc, _ := newRefreshTestManager(t, server.URL, "acct_rejected_refresh")
			if _, err := manager.Refresh(context.Background(), "acct_rejected_refresh"); err == nil {
				t.Fatal("Refresh() error = nil, want rejected refresh token")
			}

			record, ok, err := accountsSvc.Get("acct_rejected_refresh")
			if err != nil {
				t.Fatalf("accounts.Get() error = %v", err)
			}
			if !ok {
				t.Fatal("account missing after refresh failure")
			}
			if test.wantError == "" {
				if record.Status != accounts.StatusActive || record.CooldownUntil == nil {
					t.Fatalf("account = %q until %v, want active behind a cooldown", record.Status, record.CooldownUntil)
				}
				return
			}
			if record.Status != accounts.StatusExpired {
				t.Fatalf("account status = %q, want expired", record.Status)
			}
			if record.CooldownUntil != nil {
				t.Fatalf("cooldown until = %v, want none for terminal failure", record.CooldownUntil)
			}
			if record.LastError != "oauth token exchange failed: "+test.wantError {
				t.Fatalf("last error = %q", record.LastError)
			}
		})
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
	return NewAccountManager(cfg, accountsSvc, codexauth.NewOAuthService(cfg), nil), accountsSvc, now
}
