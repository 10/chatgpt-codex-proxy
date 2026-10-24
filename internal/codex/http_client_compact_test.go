package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	httpcloak "github.com/sardanioss/httpcloak/client"

	"chatgpt-codex-proxy/internal/accounts"
	"chatgpt-codex-proxy/internal/config"
)

func TestCompactResponseStreamsCompactionTrigger(t *testing.T) {
	t.Parallel()
	input := make([]InputItem, 2, 3)
	input[0] = InputItem{Role: "user", Content: []ContentPart{{Type: "input_text", Text: "Remember the launch code."}}}
	input[1] = InputItem{Role: "assistant", Content: []ContentPart{{Type: "output_text", Text: "MARIGOLD_742"}}}
	input[:cap(input)][2] = InputItem{Type: "must_not_overwrite"}
	request := CompactRequest{
		Model: "gpt-6-astra", Instructions: "Preserve the launch code.", Input: input,
		Reasoning: &Reasoning{Effort: "low"}, Text: &TextConfig{Verbosity: "low"},
	}
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/codex/responses" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost || r.Header.Get("Accept") != "text/event-stream" {
			t.Errorf("request = %s, accept=%q", r.Method, r.Header.Get("Accept"))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if body["model"] != request.Model || body["instructions"] != request.Instructions || body["stream"] != true || body["store"] != false {
			t.Errorf("incorrect compaction payload: %#v", body)
		}
		if !reflect.DeepEqual(body["reasoning"], map[string]any{"effort": "low"}) || !reflect.DeepEqual(body["text"], map[string]any{"verbosity": "low"}) {
			t.Errorf("lost request settings: %#v", body)
		}
		items, _ := body["input"].([]any)
		if len(items) != 3 || !reflect.DeepEqual(items[2], map[string]any{"type": "compaction_trigger"}) {
			t.Errorf("input = %#v, want full history followed by compaction trigger", items)
		} else if items[1].(map[string]any)["content"] != "MARIGOLD_742" {
			t.Errorf("lost assistant history: %#v", items)
		}
		if _, exists := body["previous_response_id"]; exists {
			t.Error("unexpected previous_response_id")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("x-codex-primary-used-percent", "12")
		fmt.Fprint(w, `data: {"type":"response.created","response":{"id":"resp_compact","created_at":123}}

data: {"type":"response.output_item.added","output_index":0,"item":{"id":"cmp_1","type":"compaction"}}

data: {"type":"response.output_item.done","output_index":0,"item":{"id":"cmp_1","type":"compaction","encrypted_content":"encrypted_summary"}}

data: {"type":"response.completed","response":{"id":"resp_compact","status":"completed","output":[],"usage":{"input_tokens":9000,"output_tokens":100,"total_tokens":9100}}}

`)
	}))
	upstream.EnableHTTP2 = true
	upstream.StartTLS()
	defer upstream.Close()
	client := NewHTTPClient(config.Config{CodexBaseURL: upstream.URL, RequestTimeout: 5 * time.Second})
	defer client.Close()
	client.sessions["test"] = httpcloak.NewSession(chromiumPreset, httpcloak.WithTimeout(5*time.Second), httpcloak.WithDisableHTTP3(), httpcloak.WithoutRetry(), httpcloak.WithInsecureSkipVerify())
	result, quota, err := client.CompactResponse(context.Background(), accounts.Record{ID: "test"}, request)
	if err != nil {
		t.Fatal(err)
	}
	if result.ID != "resp_compact" || result.Object != "response.compaction" || result.CreatedAt != 123 {
		t.Fatalf("incorrect response metadata: %#v", result)
	}
	if len(result.Output) != 1 || result.Output[0]["encrypted_content"] != "encrypted_summary" {
		t.Fatalf("lost streamed compaction item: %#v", result.Output)
	}
	if result.Usage["input_tokens"] != json.Number("9000") || quota == nil || *quota.RateLimit.UsedPercent != 12 {
		t.Fatalf("lost usage/quota: %#v, %#v", result.Usage, quota)
	}
	if input[:cap(input)][2].Type != "must_not_overwrite" {
		t.Fatal("compaction modified caller's input backing array")
	}
}

func TestCompactResponseRejectsUnusableStreams(t *testing.T) {
	t.Parallel()
	const done = `data: {"type":"response.output_item.done","item":{"type":"compaction","id":"cmp_1","encrypted_content":"encrypted"}}` + "\n\n"
	const completed = `data: {"type":"response.completed","response":{"id":"resp_1","output":[]}}` + "\n\n"
	cases := []struct {
		name, body, want string
		status           int
	}{
		{name: "missing compaction", body: completed, want: "exactly one compaction"},
		{name: "duplicate compaction", body: done + done + completed, want: "exactly one compaction"},
		{name: "empty ciphertext", body: `data: {"type":"response.output_item.done","item":{"type":"compaction","encrypted_content":""}}` + "\n\n" + completed, want: "encrypted_content"},
		{name: "truncated stream", body: done, want: "response.completed"},
		{name: "incomplete response", body: done + `data: {"type":"response.incomplete","response":{"incomplete_details":{"reason":"max_output_tokens"}}}` + "\n\n", want: "incomplete"},
		{name: "stream rate limit", body: `data: {"type":"response.failed","response":{"error":{"code":"rate_limit_exceeded","message":"try later","resets_in_seconds":30}}}` + "\n\n", want: "try later", status: 429},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, tc.body)
			}))
			upstream.EnableHTTP2 = true
			upstream.StartTLS()
			defer upstream.Close()
			client := NewHTTPClient(config.Config{CodexBaseURL: upstream.URL, RequestTimeout: 5 * time.Second})
			defer client.Close()
			client.sessions["test"] = httpcloak.NewSession(chromiumPreset, httpcloak.WithTimeout(5*time.Second), httpcloak.WithDisableHTTP3(), httpcloak.WithoutRetry(), httpcloak.WithInsecureSkipVerify())
			_, _, err := client.CompactResponse(context.Background(), accounts.Record{ID: "test"}, CompactRequest{Model: "gpt-6-astra"})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v, want %q", err, tc.want)
			}
			if tc.status != 0 {
				var upstreamErr *UpstreamError
				if !errors.As(err, &upstreamErr) || upstreamErr.StatusCode != tc.status || upstreamErr.RetryAfter != 30 {
					t.Fatalf("lost rate-limit details: %v", err)
				}
			}
		})
	}
}
