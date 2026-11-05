package codexauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"chatgpt-codex-proxy/internal/accounts"
	"chatgpt-codex-proxy/internal/config"
)

func TestDeviceCodeResponseUnmarshalJSONAcceptsPaddedAndBlankInterval(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
		want int
	}{
		{
			name: "padded string",
			body: `{"user_code":"ABC","device_auth_id":"dev_123","interval":" 45 "}`,
			want: 45,
		},
		{
			name: "blank string",
			body: `{"user_code":"ABC","device_auth_id":"dev_123","interval":""}`,
			want: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var decoded DeviceCodeResponse
			if err := json.Unmarshal([]byte(tc.body), &decoded); err != nil {
				t.Fatalf("json.Unmarshal() error = %v", err)
			}
			if decoded.Interval != tc.want {
				t.Fatalf("interval = %d, want %d", decoded.Interval, tc.want)
			}
		})
	}
}

func TestRefreshPreservesCredentialsAndReadsAccountClaims(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, body, wantAccountID string
	}{
		{name: "top-level ID token claim", body: `{"access_token":"next-access","id_token":"` + makeJWT(`{"chatgpt_account_id":"acct_123"}`) + `","expires_in":3600}`, wantAccountID: "acct_123"},
		{name: "nested access token claim", body: `{"access_token":"` + makeJWT(`{"https://api.openai.com/auth":{"chatgpt_account_id":"acct_456"}}`) + `","expires_in":3600}`, wantAccountID: "acct_456"},
		{name: "missing claims retain account", body: `{"access_token":"next-access","expires_in":3600}`, wantAccountID: "existing-account"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := r.ParseForm(); err != nil {
					t.Error(err)
				}
				if r.Method != http.MethodPost || r.URL.Path != "/oauth/token" || r.Form.Get("refresh_token") != "existing-refresh" || r.Form.Get("grant_type") != "refresh_token" {
					t.Errorf("incorrect refresh request: %s %s %#v", r.Method, r.URL.Path, r.Form)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			service := NewOAuthService(config.Config{AuthIssuer: server.URL})
			existing := accounts.OAuthToken{AccessToken: "old-access", RefreshToken: "existing-refresh", ExpiresAt: time.Now().UTC().Add(-time.Minute)}
			refreshed, accountID, err := service.Refresh(context.Background(), existing, "existing-account")
			if err != nil {
				t.Fatal(err)
			}
			if accountID != tc.wantAccountID || refreshed.RefreshToken != existing.RefreshToken || refreshed.AccessToken == existing.AccessToken || !refreshed.ExpiresAt.After(time.Now()) {
				t.Fatalf("refreshed token = %#v, accountID = %q, want account %q and retained refresh token", refreshed, accountID, tc.wantAccountID)
			}
		})
	}
}

func TestPollDeviceCodeDistinguishesPendingAuthorizationAndFailure(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                 string
		status               int
		body                 string
		wantReady, wantError bool
	}{
		{name: "forbidden pending", status: http.StatusForbidden},
		{name: "not found pending", status: http.StatusNotFound},
		{name: "pending error code", status: http.StatusBadRequest, body: `{"error":"authorization_pending"}`},
		{name: "pending error text", status: http.StatusBadRequest, body: `{"error":"device not found"}`},
		{name: "upstream failure", status: http.StatusServiceUnavailable, body: `{"error":"temporarily_unavailable"}`, wantError: true},
		{name: "authorized", status: http.StatusOK, body: `{"authorization_code":"code","code_verifier":"verifier"}`, wantReady: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			service := NewOAuthService(config.Config{AuthIssuer: server.URL})
			result, err := service.PollDeviceCode(context.Background(), "device", "ABC")
			if (err != nil) != tc.wantError || (result != nil) != tc.wantReady {
				t.Fatalf("PollDeviceCode() = %#v, %v", result, err)
			}
			if result != nil && (result.AuthorizationCode != "code" || result.CodeVerifier != "verifier") {
				t.Fatalf("lost authorization result: %#v", result)
			}
		})
	}
}

func makeJWT(payload string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	body := base64.RawURLEncoding.EncodeToString([]byte(payload))
	return strings.Join([]string{header, body, "signature"}, ".")
}
