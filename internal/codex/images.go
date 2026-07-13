package codex

import (
	"bytes"
	"context"
	"io"
	"net/http"

	httpcloak "github.com/sardanioss/httpcloak/client"

	"chatgpt-codex-proxy/internal/accounts"
)

type RawImageResponse struct {
	Body    io.ReadCloser
	Headers http.Header
}

func (r *RawImageResponse) Close() error {
	return r.Body.Close()
}

func (c *HTTPClient) OpenImage(ctx context.Context, record accounts.Record, path string, payload []byte, stream bool) (*RawImageResponse, error) {
	accept := "application/json"
	if stream {
		accept = "text/event-stream"
	}
	headers := BuildHeaders(record.Token.AccessToken, HeaderOptions{
		AccountID:   record.AccountID,
		Cookies:     record.Cookies,
		ContentType: "application/json",
		RequestID:   NewRequestID(),
		Accept:      accept,
	})

	resp, err := c.sessionFor(record.ID).DoStream(ctx, &httpcloak.Request{
		Method:  http.MethodPost,
		URL:     JoinURL(c.cfg.CodexBaseURL, path),
		Headers: headers,
		Body:    bytes.NewReader(payload),
	})
	if err != nil {
		return nil, err
	}
	responseHeaders := CanonicalHeader(resp.Headers)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body := readLimitedErrorBody(resp)
		resp.Close()
		return nil, NewUpstreamError("codex image response", resp.StatusCode, body, responseHeaders)
	}

	return &RawImageResponse{Body: resp, Headers: responseHeaders}, nil
}
