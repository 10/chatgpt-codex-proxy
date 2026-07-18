package codexauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"chatgpt-codex-proxy/internal/accounts"
	"chatgpt-codex-proxy/internal/config"
	"chatgpt-codex-proxy/internal/httpbody"
	"chatgpt-codex-proxy/internal/jsonutil"
	"chatgpt-codex-proxy/internal/jwtutil"
)

const (
	desktopUserAgent   = "Codex Desktop/26.707.31428 (win32; x64)"
	deviceUserCodePath = "/api/accounts/deviceauth/usercode"
	deviceTokenPath    = "/api/accounts/deviceauth/token"
	oauthTokenPath     = "/oauth/token"
	deviceAuthPath     = "/codex/device"
	deviceRedirectPath = "/deviceauth/callback"
)

type DeviceCodeResponse struct {
	UserCode     string `json:"user_code"`
	DeviceAuthID string `json:"device_auth_id"`
	Interval     int    `json:"interval"`
}

type oauthTokenResponse struct {
	AccessToken  string                 `json:"access_token"`
	RefreshToken string                 `json:"refresh_token"`
	IDToken      string                 `json:"id_token"`
	ExpiresIn    jsonutil.FlexibleInt64 `json:"expires_in"`
}

type oauthTokenError struct {
	Code        string
	Description string
	Body        string
}

func (e *oauthTokenError) Error() string {
	message := strings.TrimSpace(e.Description)
	if message == "" {
		message = strings.TrimSpace(e.Body)
	}
	return fmt.Sprintf("oauth token exchange failed: %s", message)
}

func IsTerminalCredentialFailure(err error) bool {
	var oauthErr *oauthTokenError
	return errors.As(err, &oauthErr) && strings.EqualFold(strings.TrimSpace(oauthErr.Code), "invalid_grant")
}

type oauthClaims struct {
	ChatGPTAccountID string          `json:"chatgpt_account_id"`
	OpenAIAuth       oauthAuthClaims `json:"https://api.openai.com/auth"`
}

type oauthAuthClaims struct {
	ChatGPTAccountID string `json:"chatgpt_account_id"`
}

func (d *DeviceCodeResponse) UnmarshalJSON(data []byte) error {
	type rawDeviceCodeResponse struct {
		UserCode     string                 `json:"user_code"`
		DeviceAuthID string                 `json:"device_auth_id"`
		Interval     jsonutil.FlexibleInt64 `json:"interval"`
	}

	var raw rawDeviceCodeResponse
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	d.UserCode = raw.UserCode
	d.DeviceAuthID = raw.DeviceAuthID
	if value, ok := raw.Interval.Int64(); ok {
		d.Interval = int(value)
	}
	return nil
}

type DevicePollResponse struct {
	AuthorizationCode string `json:"authorization_code"`
	CodeVerifier      string `json:"code_verifier"`
}

type OAuthService struct {
	cfg    config.Config
	client *http.Client
}

func NewOAuthService(cfg config.Config) *OAuthService {
	return &OAuthService{
		cfg: cfg,
		client: &http.Client{
			Timeout: cfg.RequestTimeout,
		},
	}
}

func (s *OAuthService) DeviceAuthURL() string {
	return strings.TrimRight(s.cfg.AuthIssuer, "/") + deviceAuthPath
}

func (s *OAuthService) RequestDeviceCode(ctx context.Context) (DeviceCodeResponse, error) {
	endpoint := strings.TrimRight(s.cfg.AuthIssuer, "/") + deviceUserCodePath
	body := map[string]string{"client_id": s.cfg.OAuthClientID}
	result, _, err := doDeviceAuthJSON[DeviceCodeResponse](ctx, s.client, endpoint, body, s.defaultHeaders(), false)
	return result, err
}

func (s *OAuthService) PollDeviceCode(ctx context.Context, deviceAuthID, userCode string) (*DevicePollResponse, error) {
	endpoint := strings.TrimRight(s.cfg.AuthIssuer, "/") + deviceTokenPath
	body := map[string]string{
		"device_auth_id": deviceAuthID,
		"user_code":      userCode,
	}
	result, pending, err := doDeviceAuthJSON[DevicePollResponse](ctx, s.client, endpoint, body, s.defaultHeaders(), true)
	if err != nil {
		return nil, err
	}
	if pending {
		return nil, nil
	}
	return &result, nil
}

func (s *OAuthService) ExchangeAuthorizationCode(ctx context.Context, authorizationCode, codeVerifier string) (accounts.OAuthToken, string, error) {
	endpoint := strings.TrimRight(s.cfg.AuthIssuer, "/") + oauthTokenPath
	values := url.Values{}
	values.Set("grant_type", "authorization_code")
	values.Set("code", authorizationCode)
	values.Set("redirect_uri", strings.TrimRight(s.cfg.AuthIssuer, "/")+deviceRedirectPath)
	values.Set("client_id", s.cfg.OAuthClientID)
	values.Set("code_verifier", codeVerifier)

	resp, err := doForm(ctx, s.client, endpoint, values, s.defaultHeaders())
	if err != nil {
		return accounts.OAuthToken{}, "", err
	}
	accountID := extractAccountID(resp)
	return buildOAuthToken(resp), accountID, nil
}

func (s *OAuthService) Refresh(ctx context.Context, existing accounts.OAuthToken, accountID string) (accounts.OAuthToken, string, error) {
	endpoint := strings.TrimRight(s.cfg.AuthIssuer, "/") + oauthTokenPath
	values := url.Values{}
	values.Set("grant_type", "refresh_token")
	values.Set("refresh_token", existing.RefreshToken)
	values.Set("client_id", s.cfg.OAuthClientID)

	resp, err := doForm(ctx, s.client, endpoint, values, s.defaultHeaders())
	if err != nil {
		return accounts.OAuthToken{}, "", err
	}
	nextAccountID := extractAccountID(resp)
	if nextAccountID == "" {
		nextAccountID = accountID
	}
	nextToken := buildOAuthToken(resp)
	if strings.TrimSpace(nextToken.RefreshToken) == "" {
		nextToken.RefreshToken = existing.RefreshToken
	}
	return nextToken, nextAccountID, nil
}

func buildOAuthToken(raw oauthTokenResponse) accounts.OAuthToken {
	expiresIn := int64(3600)
	if value, ok := raw.ExpiresIn.Int64(); ok {
		expiresIn = value
	}
	return accounts.OAuthToken{
		AccessToken:  raw.AccessToken,
		RefreshToken: raw.RefreshToken,
		ExpiresAt:    time.Now().UTC().Add(time.Duration(expiresIn) * time.Second),
	}
}

func extractAccountID(raw oauthTokenResponse) string {
	for _, token := range []string{raw.IDToken, raw.AccessToken} {
		if token == "" {
			continue
		}
		claims, _ := jwtutil.DecodePayload[oauthClaims](token)
		if accountID := claims.ChatGPTAccountID; accountID != "" {
			return accountID
		}
		if accountID := claims.OpenAIAuth.ChatGPTAccountID; accountID != "" {
			return accountID
		}
	}
	return ""
}

func (s *OAuthService) defaultHeaders() http.Header {
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	headers.Set("User-Agent", desktopUserAgent)
	return headers
}

func doDeviceAuthJSON[T any](ctx context.Context, client *http.Client, endpoint string, body any, headers http.Header, allowPending bool) (T, bool, error) {
	var zero T
	payload, err := json.Marshal(body)
	if err != nil {
		return zero, false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return zero, false, err
	}
	req.Header = headers.Clone()
	resp, err := client.Do(req)
	if err != nil {
		return zero, false, err
	}
	defer resp.Body.Close()
	if allowPending && (resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound) {
		if _, err := io.Copy(io.Discard, io.LimitReader(resp.Body, httpbody.Limit)); err != nil {
			return zero, false, fmt.Errorf("drain pending oauth response: %w", err)
		}
		return zero, true, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyText := strings.TrimSpace(httpbody.ReadLimitedErrorBody(resp.Body))
		return zero, false, fmt.Errorf("oauth request failed: %s", bodyText)
	}
	var decoded T
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return zero, false, err
	}
	return decoded, false, nil
}

func doForm(ctx context.Context, client *http.Client, endpoint string, values url.Values, headers http.Header) (oauthTokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return oauthTokenResponse{}, err
	}
	req.Header = headers.Clone()
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return oauthTokenResponse{}, err
	}
	defer resp.Body.Close()
	bodyText := httpbody.ReadLimitedErrorBody(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var payload struct {
			Error            string `json:"error"`
			ErrorDescription string `json:"error_description"`
		}
		_ = json.Unmarshal([]byte(bodyText), &payload)
		return oauthTokenResponse{}, &oauthTokenError{
			Code:        strings.TrimSpace(payload.Error),
			Description: strings.TrimSpace(payload.ErrorDescription),
			Body:        strings.TrimSpace(bodyText),
		}
	}
	var decoded oauthTokenResponse
	if err := json.Unmarshal([]byte(bodyText), &decoded); err != nil {
		return oauthTokenResponse{}, err
	}
	return decoded, nil
}
