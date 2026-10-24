package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"chatgpt-codex-proxy/internal/accounts"
	"chatgpt-codex-proxy/internal/codex"
)

func TestTextStreamsPreserveTerminalOutput(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	for _, endpoint := range []string{"completions", "chat/completions"} {
		for _, tc := range []struct {
			name              string
			delta, incomplete bool
		}{
			{name: "terminal text only"},
			{name: "incomplete with deltas", delta: true, incomplete: true},
		} {
			t.Run(endpoint+"/"+tc.name, func(t *testing.T) {
				app := newFailoverTestApp(t)
				app.httpStream = func(context.Context, accounts.Record, codex.Request, string) (eventStream, error) {
					var events []*codex.StreamEvent
					if tc.delta {
						events = append(events, &codex.StreamEvent{Type: "response.output_text.delta", Raw: map[string]any{"delta": "answer"}})
					}
					status := "completed"
					if tc.incomplete {
						status = "incomplete"
					}
					response := map[string]any{"id": "resp_fixture", "status": status, "model": "gpt-6-astra", "output_text": "answer"}
					if tc.incomplete {
						response["incomplete_details"] = map[string]any{"reason": "max_output_tokens"}
					}
					events = append(events, &codex.StreamEvent{Type: "response." + status, Raw: map[string]any{"response": response}})
					return &fakeEventStream{events: events}, nil
				}
				body := `{"model":"gpt-6-astra","stream":true,"prompt":"test"}`
				if endpoint == "chat/completions" {
					body = `{"model":"gpt-6-astra","stream":true,"messages":[{"role":"user","content":"test"}]}`
				}
				recorder := httptest.NewRecorder()
				ctx, _ := gin.CreateTestContext(recorder)
				ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/"+endpoint, strings.NewReader(body))
				if endpoint == "completions" {
					app.handleCompletions(ctx)
				} else {
					app.handleChatCompletions(ctx)
				}
				var text strings.Builder
				finish := ""
				done := false
				for _, event := range parseSSEEvents(t, recorder.Body.String()) {
					if event.Raw == "[DONE]" {
						done = true
						continue
					}
					if event.Data["error"] != nil {
						t.Fatalf("unexpected stream error: %v", event.Data["error"])
					}
					choices := sliceOfMapsFromAny(event.Data["choices"])
					for _, choice := range choices {
						if value, ok := choice["finish_reason"].(string); ok {
							finish = value
						}
						if endpoint == "completions" {
							value, _ := choice["text"].(string)
							text.WriteString(value)
						} else {
							delta := nestedMapFromAny(choice["delta"])
							value, _ := delta["content"].(string)
							text.WriteString(value)
						}
					}
				}
				expectedFinish := "stop"
				if tc.incomplete {
					expectedFinish = "length"
				}
				if text.String() != "answer" || finish != expectedFinish || !done {
					t.Fatalf("text=%q finish=%q done=%v, want answer/%s/true", text.String(), finish, done, expectedFinish)
				}
			})
		}
	}
}
