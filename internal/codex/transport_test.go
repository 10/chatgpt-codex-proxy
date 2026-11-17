package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	httpcloak "github.com/sardanioss/httpcloak/client"

	"chatgpt-codex-proxy/internal/accounts"
	"chatgpt-codex-proxy/internal/config"
	"chatgpt-codex-proxy/internal/turn"
)

func TestResponseTransportsPreserveRequestAndEventSemantics(t *testing.T) {
	for _, transport := range []string{"http", "websocket"} {
		t.Run(transport, func(t *testing.T) {
			t.Parallel()
			const eventJSON = `{"type":"error","sequence_number":9007199254740993,"error":{"code":"rate_limit_exceeded","message":"busy","retry_after":"12.9"}}`
			upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/codex/responses" {
					t.Errorf("upstream path = %s", r.URL.Path)
				}
				for name, want := range map[string]string{
					"Authorization": "Bearer access-token", "ChatGPT-Account-Id": "upstream-account", "Cookie": "session=cookie", "X-Codex-Turn-State": "turn-state",
				} {
					if got := r.Header.Get(name); got != want {
						t.Errorf("%s = %q, want %q", name, got, want)
					}
				}
				if got := r.Header.Get("Session-Id"); len(got) != 36 || got[14] != '4' || !strings.ContainsRune("89ab", rune(got[19])) {
					t.Errorf("Session-Id = %q, want a version 4 UUID for the conversation", got)
				}
				for _, name := range []string{"User-Agent", "X-Client-Request-Id", "OpenAI-Beta"} {
					if r.Header.Get(name) == "" {
						t.Errorf("missing %s", name)
					}
				}
				var body map[string]any
				if transport == "websocket" {
					conn, err := (&websocket.Upgrader{}).Upgrade(w, r, http.Header{"X-Codex-Turn-State": []string{"next-turn-state"}})
					if err != nil {
						t.Error(err)
						return
					}
					defer conn.Close()
					if err := conn.ReadJSON(&body); err != nil {
						t.Error(err)
						return
					}
					if body["type"] != "response.create" || body["generate"] != false || body["previous_response_id"] != "resp_previous" {
						t.Errorf("incorrect websocket payload: %#v", body)
					}
					for _, payload := range []string{eventJSON, eventJSON + eventJSON} {
						if err := conn.WriteMessage(websocket.TextMessage, []byte(payload)); err != nil {
							t.Error(err)
						}
					}
				} else {
					if r.Method != http.MethodPost || r.Header.Get("Accept") != "text/event-stream" {
						t.Errorf("stream request = %s, accept=%q", r.Method, r.Header.Get("Accept"))
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
						return
					}
					if body["stream"] != true || body["store"] != false || body["previous_response_id"] != nil {
						t.Errorf("incorrect HTTP stream payload: %#v", body)
					}
					w.Header().Set("Content-Type", "text/event-stream")
					w.Header().Set("X-Codex-Turn-State", "next-turn-state")
					fmt.Fprintf(w, "data: %s\n\ndata: %s%s\n\n", eventJSON, eventJSON, eventJSON)
				}
				if body["model"] != "test-model" || body["instructions"] != "test instructions" {
					t.Errorf("request content lost: %#v", body)
				}
			}))
			if transport == "http" {
				upstream.EnableHTTP2 = true
				upstream.StartTLS()
			} else {
				upstream.Start()
			}
			defer upstream.Close()
			client := NewHTTPClient(config.Config{CodexBaseURL: upstream.URL, RequestTimeout: 3 * time.Second})
			defer client.Close()
			if transport == "http" {
				client.sessions["local-account"] = httpcloak.NewSession(chromiumPreset, httpcloak.WithTimeout(3*time.Second), httpcloak.WithDisableHTTP3(), httpcloak.WithoutRetry(), httpcloak.WithInsecureSkipVerify())
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			generate := false
			request := turn.NormalizedRequest{Request: turn.Request{
				Model: "test-model", Instructions: "test instructions", Input: []turn.InputItem{{Role: "user", Content: []turn.ContentPart{{Type: "input_text", Text: "hello"}}}},
				Store: true, PreviousResponseID: "resp_previous",
			}, Generate: &generate}
			account := accounts.Record{ID: "local-account", AccountID: "upstream-account", Token: accounts.OAuthToken{AccessToken: "access-token"}, Cookies: map[string]string{"session": "cookie"}}
			var stream interface {
				NextEvent() (*turn.StreamEvent, error)
				Headers() http.Header
				Close() error
			}
			var err error
			if transport == "websocket" {
				stream, err = client.ConnectResponsesWS(ctx, account, request, "turn-state")
			} else {
				stream, err = client.StreamResponse(ctx, account, request.Request, "turn-state")
			}
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			if stream.Headers().Get("X-Codex-Turn-State") != "next-turn-state" {
				t.Fatal("lost upstream continuation headers")
			}
			event, err := stream.NextEvent()
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(event.Raw)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(encoded), `"sequence_number":9007199254740993`) {
				t.Fatalf("event integer lost precision: %s", encoded)
			}
			var upstreamErr *UpstreamError
			if !errors.As(StreamEventError(event), &upstreamErr) || upstreamErr.StatusCode != http.StatusTooManyRequests || upstreamErr.RetryAfter != 12 {
				t.Fatalf("lost stream failure metadata: %#v", upstreamErr)
			}
			if event, err := stream.NextEvent(); err == nil {
				t.Fatalf("accepted multiple JSON values as one event: %#v", event)
			}
		})
	}
}

func TestStreamResponseKeepsOneSessionIDPerConversation(t *testing.T) {
	t.Parallel()
	sessionIDs := make(chan string, 4)
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sessionIDs <- r.Header.Get("Session-Id")
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp\"}}\n\n")
	}))
	upstream.EnableHTTP2 = true
	upstream.StartTLS()
	defer upstream.Close()
	client := NewHTTPClient(config.Config{CodexBaseURL: upstream.URL, RequestTimeout: 3 * time.Second})
	defer client.Close()
	client.sessions["account"] = httpcloak.NewSession(chromiumPreset, httpcloak.WithTimeout(3*time.Second), httpcloak.WithDisableHTTP3(), httpcloak.WithoutRetry(), httpcloak.WithInsecureSkipVerify())

	message := func(role, text string) turn.InputItem {
		return turn.InputItem{Role: role, Content: []turn.ContentPart{{Type: "input_text", Text: text}}}
	}
	firstTurn := []turn.InputItem{message("developer", "Be brief."), message("user", "Plan a trip")}
	secondTurn := append(append([]turn.InputItem(nil), firstTurn...), message("assistant", "Where to?"), message("user", "Lisbon"))
	otherChat := []turn.InputItem{message("developer", "Be brief."), message("user", "Fix my bike")}
	for _, request := range []struct {
		accountID string
		input     []turn.InputItem
	}{{"upstream-a", firstTurn}, {"upstream-a", secondTurn}, {"upstream-a", otherChat}, {"upstream-b", firstTurn}} {
		stream, err := client.StreamResponse(context.Background(), accounts.Record{ID: "account", AccountID: request.accountID}, turn.Request{Model: "gpt-6.1-sol", Input: request.input}, "")
		if err != nil {
			t.Fatal(err)
		}
		stream.Close()
	}

	first, second, other, otherAccount := <-sessionIDs, <-sessionIDs, <-sessionIDs, <-sessionIDs
	if first == "" || first != second || first == other || first == otherAccount {
		t.Fatalf("session ids = %q, %q, %q, %q; want one per conversation and account", first, second, other, otherAccount)
	}
}

func TestResponsesWebSocketWarmupSendsSessionID(t *testing.T) {
	t.Parallel()
	sessionIDs := make(chan string, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sessionIDs <- r.Header.Get("Session-Id")
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		conn.Close()
	}))
	defer upstream.Close()
	client := NewHTTPClient(config.Config{CodexBaseURL: upstream.URL, RequestTimeout: 3 * time.Second})
	defer client.Close()

	generate := false
	stream, err := client.ConnectResponsesWS(context.Background(), accounts.Record{ID: "account"}, turn.NormalizedRequest{
		Request: turn.Request{Model: "gpt-6.1-sol", PromptCacheKey: "codex-cli-session"}, Generate: &generate,
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	stream.Close()
	if got := <-sessionIDs; len(got) != 36 {
		t.Fatalf("warm-up Session-Id = %q, want one from prompt_cache_key", got)
	}
}
