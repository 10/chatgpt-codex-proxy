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

func TestDeviceCodeResponseUnmarshalJSONAcceptsStringAndNumber(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
		want int
	}{
		{
			name: "number",
			body: `{"user_code":"ABC","device_auth_id":"dev_123","interval":15}`,
			want: 15,
		},
		{
			name: "string",
			body: `{"user_code":"ABC","device_auth_id":"dev_123","interval":"30"}`,
			want: 30,
		},
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

func TestOAuthTokenResponseUnmarshalJSONAcceptsBlankAndPaddedExpiresIn(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
	}{
		{
			name: "padded string",
			body: `{"access_token":"access","expires_in":" 7200 "}`,
		},
		{
			name: "blank string",
			body: `{"access_token":"access","expires_in":""}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var decoded oauthTokenResponse
			if err := json.Unmarshal([]byte(tc.body), &decoded); err != nil {
				t.Fatalf("json.Unmarshal() error = %v", err)
			}
		})
	}
}

func TestBuildOAuthTokenUsesTypedExpiresIn(t *testing.T) {
	t.Parallel()

	before := time.Now().UTC()
	token := buildOAuthToken(oauthTokenResponse{
		AccessToken:  "access",
		RefreshToken: "refresh",
		ExpiresIn:    flexibleInt64{value: 7200, set: true},
	})

	if token.AccessToken != "access" || token.RefreshToken != "refresh" {
		t.Fatalf("token = %#v", token)
	}
	delta := token.ExpiresAt.Sub(before)
	if delta < 7190*time.Second || delta > 7210*time.Second {
		t.Fatalf("expires_at delta = %s, want about 2h", delta)
	}
}

func TestRefreshPreservesExistingRefreshTokenWhenResponseOmitsOne(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"next-access","expires_in":3600}`))
	}))
	defer server.Close()

	service := NewOAuthService(config.Config{AuthIssuer: server.URL})
	existing := accounts.OAuthToken{
		AccessToken:  "old-access",
		RefreshToken: "existing-refresh",
		ExpiresAt:    time.Now().UTC().Add(-time.Minute),
	}

	refreshed, _, err := service.Refresh(context.Background(), existing, "acct_123")
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if refreshed.RefreshToken != existing.RefreshToken {
		t.Fatalf("refresh token = %q, want existing token %q", refreshed.RefreshToken, existing.RefreshToken)
	}
}

func TestExtractAccountIDReadsJWTClaims(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  oauthTokenResponse
		want string
	}{
		{
			name: "top-level claim",
			raw: oauthTokenResponse{
				IDToken: makeJWT(`{"chatgpt_account_id":"acct_123"}`),
			},
			want: "acct_123",
		},
		{
			name: "nested claim",
			raw: oauthTokenResponse{
				AccessToken: makeJWT(`{"https://api.openai.com/auth":{"chatgpt_account_id":"acct_456"}}`),
			},
			want: "acct_456",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := extractAccountID(tc.raw); got != tc.want {
				t.Fatalf("extractAccountID() = %q, want %q", got, tc.want)
			}
		})
	}
}

func makeJWT(payload string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	body := base64.RawURLEncoding.EncodeToString([]byte(payload))
	return strings.Join([]string{header, body, "signature"}, ".")
}
