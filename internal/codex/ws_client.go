package codex

import (
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/gorilla/websocket"

	"chatgpt-codex-proxy/internal/accounts"
	"chatgpt-codex-proxy/internal/httpbody"
	"chatgpt-codex-proxy/internal/turn"
)

type WSStream struct {
	conn    *websocket.Conn
	headers http.Header
}

func (c *HTTPClient) ConnectResponsesWS(ctx context.Context, record accounts.Record, req turn.NormalizedRequest, turnState string) (*WSStream, error) {
	headers := buildHeaders(record.Token.AccessToken, headerOptions{
		AccountID:   record.AccountID,
		Cookies:     record.Cookies,
		TurnState:   turnState,
		RequestID:   newRequestID(),
		IncludeBeta: true,
	})
	endpoint := strings.TrimRight(c.cfg.CodexBaseURL, "/") + "/codex/responses"
	endpoint = strings.Replace(endpoint, "https://", "wss://", 1)
	endpoint = strings.Replace(endpoint, "http://", "ws://", 1)
	return connectWS(ctx, endpoint, headers, WSCreatePayload(req))
}

func connectWS(ctx context.Context, endpoint string, headers http.Header, body any) (*WSStream, error) {
	dialer := websocket.Dialer{}
	conn, resp, err := dialer.DialContext(ctx, endpoint, headers)
	if err != nil {
		if resp != nil {
			payload := httpbody.ReadLimitedErrorBody(resp.Body)
			resp.Body.Close()
			return nil, NewUpstreamError("websocket dial", resp.StatusCode, payload, resp.Header)
		}
		return nil, err
	}
	if err := conn.WriteJSON(body); err != nil {
		conn.Close()
		return nil, err
	}
	return &WSStream{
		conn:    conn,
		headers: resp.Header.Clone(),
	}, nil
}

func (s *WSStream) Close() error {
	return s.conn.Close()
}

func (s *WSStream) Headers() http.Header {
	return s.headers.Clone()
}

func (s *WSStream) SendJSON(body any) error {
	return s.conn.WriteJSON(body)
}

func (s *WSStream) NextEvent() (*turn.StreamEvent, error) {
	_, message, err := s.conn.ReadMessage()
	if err != nil {
		return nil, err
	}
	event, err := decodeStreamEvent(message)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(event.Type) == "" {
		return nil, io.EOF
	}
	return event, nil
}
