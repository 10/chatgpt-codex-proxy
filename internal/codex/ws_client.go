package codex

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/gorilla/websocket"

	"chatgpt-codex-proxy/internal/httpbody"
)

// WSStream wraps a websocket connection and exposes the event-stream interface used by the server package.
type WSStream struct {
	conn    *websocket.Conn
	headers http.Header
}

func ConnectWS(ctx context.Context, endpoint string, headers http.Header, body any) (*WSStream, error) {
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

func (s *WSStream) NextEvent() (*StreamEvent, error) {
	_, message, err := s.conn.ReadMessage()
	if err != nil {
		return nil, err
	}
	var raw map[string]any
	if err := json.Unmarshal(message, &raw); err != nil {
		return nil, err
	}
	eventType := ""
	if typ, ok := raw["type"].(string); ok {
		eventType = typ
	}
	if strings.TrimSpace(eventType) == "" {
		return nil, io.EOF
	}
	return &StreamEvent{Type: eventType, Raw: raw}, nil
}
