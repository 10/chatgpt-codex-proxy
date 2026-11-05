package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	httpcloak "github.com/sardanioss/httpcloak/client"

	"chatgpt-codex-proxy/internal/accounts"
	"chatgpt-codex-proxy/internal/clientidentity"
	"chatgpt-codex-proxy/internal/config"
	"chatgpt-codex-proxy/internal/httpbody"
	"chatgpt-codex-proxy/internal/models"
	"chatgpt-codex-proxy/internal/turn"
)

type HTTPClient struct {
	cfg      config.Config
	mu       sync.Mutex
	sessions map[string]*httpcloak.Client
}

var requestSequence uint64

func NewHTTPClient(cfg config.Config) *HTTPClient {
	return &HTTPClient{
		cfg:      cfg,
		sessions: make(map[string]*httpcloak.Client),
	}
}

func (c *HTTPClient) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, session := range c.sessions {
		session.Close()
	}
	c.sessions = make(map[string]*httpcloak.Client)
}

func (c *HTTPClient) GetUsage(ctx context.Context, record accounts.Record) (UsageResponse, *accounts.QuotaSnapshot, error) {
	session := c.sessionFor(record.ID)
	headers := buildHeaders(record.Token.AccessToken, headerOptions{
		AccountID:      record.AccountID,
		Cookies:        record.Cookies,
		RequestID:      newRequestID(),
		Accept:         "application/json",
		AcceptEncoding: "gzip, deflate",
	})

	resp, err := session.Get(ctx, joinURL(c.cfg.CodexBaseURL, "/codex/usage"), headers)
	if err != nil {
		return UsageResponse{}, nil, err
	}
	defer resp.Close()

	payload, err := resp.Text()
	if err != nil {
		return UsageResponse{}, nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return UsageResponse{}, nil, NewUpstreamError("codex usage", resp.StatusCode, payload, canonicalHeader(resp.Headers))
	}

	var decoded UsageResponse
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		return UsageResponse{}, nil, err
	}
	return decoded, QuotaFromUsageResponse(decoded), nil
}

func (c *HTTPClient) GetCodexModels(ctx context.Context, record accounts.Record) ([]models.Entry, error) {
	session := c.sessionFor(record.ID)
	headers := buildHeaders(record.Token.AccessToken, headerOptions{
		AccountID:      record.AccountID,
		Cookies:        record.Cookies,
		RequestID:      newRequestID(),
		Accept:         "application/json",
		AcceptEncoding: "gzip, deflate",
	})

	endpoint := c.codexModelsURL()
	resp, err := session.Get(ctx, endpoint, headers)
	if err != nil {
		return nil, err
	}
	payload, err := resp.Text()
	resp.Close()
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, NewUpstreamError("codex models", resp.StatusCode, payload, canonicalHeader(resp.Headers))
	}

	entries, err := parseCodexModelsResponse(payload)
	if err != nil {
		return nil, fmt.Errorf("codex models returned an unusable body: %w", err)
	}
	return entries, nil
}

func (c *HTTPClient) StreamResponse(ctx context.Context, record accounts.Record, req turn.Request, turnState string) (*StreamReader, error) {
	session := c.sessionFor(record.ID)
	headers := buildHeaders(record.Token.AccessToken, headerOptions{
		AccountID:   record.AccountID,
		Cookies:     record.Cookies,
		ContentType: "application/json",
		TurnState:   turnState,
		RequestID:   newRequestID(),
		IncludeBeta: true,
		Accept:      "text/event-stream",
	})

	bodyReq := StreamRequestPayload(req)
	payload, err := json.Marshal(bodyReq)
	if err != nil {
		return nil, err
	}

	streamResp, err := session.DoStream(ctx, &httpcloak.Request{
		Method:  http.MethodPost,
		URL:     joinURL(c.cfg.CodexBaseURL, "/codex/responses"),
		Headers: headers,
		Body:    bytes.NewReader(payload),
	})
	if err != nil {
		return nil, err
	}
	if streamResp.StatusCode < 200 || streamResp.StatusCode >= 300 {
		data := httpbody.ReadLimitedErrorBody(streamResp)
		headers := canonicalHeader(streamResp.Headers)
		streamResp.Close()
		return nil, NewUpstreamError("codex response", streamResp.StatusCode, data, headers)
	}

	return &StreamReader{
		reader:  bufio.NewReader(streamResp),
		Closer:  streamResp,
		headers: canonicalHeader(streamResp.Headers),
	}, nil
}

func (c *HTTPClient) sessionFor(accountID string) *httpcloak.Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, ok := c.sessions[accountID]; ok {
		return existing
	}
	session := httpcloak.NewSession(
		chromiumPreset,
		httpcloak.WithTimeout(c.cfg.RequestTimeout),
		httpcloak.WithDisableHTTP3(),
		httpcloak.WithoutRetry(),
	)
	c.sessions[accountID] = session
	return session
}

func canonicalHeader(headers map[string][]string) http.Header {
	out := make(http.Header, len(headers))
	for key, values := range headers {
		canonical := http.CanonicalHeaderKey(key)
		out[canonical] = append([]string(nil), values...)
	}
	return out
}

func joinURL(base, path string) string {
	return strings.TrimRight(base, "/") + path
}

func newRequestID() string {
	return fmt.Sprintf("req_%d_%08x", time.Now().UTC().UnixNano(), atomic.AddUint64(&requestSequence, 1))
}

func StreamRequestPayload(req turn.Request) turn.Request {
	bodyReq := req
	bodyReq.Stream = true
	bodyReq.Store = false
	bodyReq.PreviousResponseID = ""
	return bodyReq
}

func (c *HTTPClient) codexModelsURL() string {
	return joinURL(c.cfg.CodexBaseURL, "/codex/models?client_version="+clientidentity.DesktopVersion)
}
