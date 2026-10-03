//go:build live

package integration_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestLiveTextEndpoints(t *testing.T) {
	cfg := loadLiveConfig(t)
	for _, endpoint := range []string{"/completions", "/chat/completions", "/responses"} {
		for _, streaming := range []bool{false, true} {
			name := strings.TrimPrefix(endpoint, "/") + "/json"
			if streaming {
				name = strings.TrimPrefix(endpoint, "/") + "/stream"
			}
			t.Run(name, func(t *testing.T) {
				const expected = "ENDPOINT_OK"
				body := map[string]any{"model": cfg.Model, "stream": streaming}
				switch endpoint {
				case "/completions":
					body["prompt"] = "Reply with exactly " + expected
				case "/chat/completions":
					body["messages"] = []map[string]string{{"role": "user", "content": "Reply with exactly " + expected}}
				case "/responses":
					body["input"] = "Reply with exactly " + expected
				}
				data := postJSON(t, cfg, endpoint, body)
				var events []map[string]any
				if streaming {
					events = liveSSEObjects(t, data)
				} else {
					var event map[string]any
					if err := json.Unmarshal(data, &event); err != nil {
						t.Fatal(err)
					}
					events = append(events, event)
				}
				var text strings.Builder
				terminal := false
				for _, event := range events {
					if event["error"] != nil || event["type"] == "error" {
						t.Fatalf("response error: %v", event)
					}
					if endpoint == "/responses" {
						if streaming {
							if event["type"] == "response.output_text.delta" {
								text.WriteString(liveString(event["delta"]))
							}
							if event["type"] == "response.completed" {
								terminal = true
							}
						} else {
							text.WriteString(liveResponseText(event))
							terminal = event["status"] == "completed"
						}
						continue
					}
					choices, _ := event["choices"].([]any)
					for _, raw := range choices {
						choice, _ := raw.(map[string]any)
						if endpoint == "/completions" {
							text.WriteString(liveString(choice["text"]))
						} else {
							field := "message"
							if streaming {
								field = "delta"
							}
							message, _ := choice[field].(map[string]any)
							text.WriteString(liveString(message["content"]))
						}
						terminal = terminal || choice["finish_reason"] == "stop"
					}
				}
				if strings.TrimSpace(text.String()) != expected || !terminal {
					t.Fatalf("text=%q terminal=%v", text.String(), terminal)
				}
				if streaming && endpoint != "/responses" && !strings.Contains(string(data), "data: [DONE]") {
					t.Fatal("missing stream terminator")
				}
			})
		}
	}
	t.Run("responses structured output and continuation", func(t *testing.T) {
		body := postJSON(t, cfg, "/responses", map[string]any{
			"model": cfg.Model, "input": "The project code is MAPLE_37. Return it as JSON.",
			"text": map[string]any{"format": map[string]any{"type": "json_schema", "name": "project", "strict": true, "schema": map[string]any{
				"type": "object", "properties": map[string]any{"code": map[string]string{"type": "string"}}, "required": []string{"code"}, "additionalProperties": false,
			}}},
		})
		var first map[string]any
		if err := json.Unmarshal(body, &first); err != nil {
			t.Fatal(err)
		}
		var structured map[string]string
		if err := json.Unmarshal([]byte(liveResponseText(first)), &structured); err != nil || structured["code"] != "MAPLE_37" {
			t.Fatalf("structured output invalid: %v, %v", structured, err)
		}
		next := postJSON(t, cfg, "/responses", map[string]any{"previous_response_id": first["id"], "input": "What is the project code? Reply with the code only."})
		var continued map[string]any
		if err := json.Unmarshal(next, &continued); err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(liveResponseText(continued)) != "MAPLE_37" {
			t.Fatalf("continuation lost context: %q", liveResponseText(continued))
		}
	})
}

func TestLiveReadEndpoints(t *testing.T) {
	cfg := loadLiveConfig(t)
	root := strings.TrimSuffix(strings.TrimRight(cfg.BaseURL, "/"), "/v1")
	get := func(path string, headers map[string]string) map[string]any {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, root+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := (&http.Client{Timeout: 90 * time.Second}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: HTTP %d", path, resp.StatusCode)
		}
		var result map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	if get("/health/live", nil)["status"] != "ok" {
		t.Fatal("liveness failed")
	}
	health := get("/health", nil)
	if health["status"] != "ok" {
		t.Fatal("health failed")
	}
	list := get("/v1/models", nil)
	data, _ := list["data"].([]any)
	found := false
	for _, raw := range data {
		item, _ := raw.(map[string]any)
		found = found || item["id"] == cfg.Model
	}
	if !found {
		t.Fatalf("model list missing %s", cfg.Model)
	}
	if get("/v1/models/"+url.PathEscape(cfg.Model), nil)["id"] != cfg.Model {
		t.Fatal("model detail mismatch")
	}
	codex := get("/v1/models?client_version=26.930.21537", nil)
	if entries, _ := codex["models"].([]any); len(entries) == 0 {
		t.Fatal("Codex model catalog empty")
	}
	anthropicHeaders := map[string]string{"Anthropic-Version": "2023-06-01"}
	if entries, _ := get("/v1/models", anthropicHeaders)["data"].([]any); len(entries) == 0 {
		t.Fatal("Anthropic catalog empty")
	}
	if get("/v1/models/"+url.PathEscape(cfg.Model), anthropicHeaders)["type"] != "model" {
		t.Fatal("Anthropic model detail invalid")
	}
	accounts := get("/admin/accounts", nil)
	entries, _ := accounts["accounts"].([]any)
	if len(entries) == 0 {
		t.Fatal("no accounts available")
	}
	for _, raw := range entries {
		account, _ := raw.(map[string]any)
		if account["token"] != nil || account["cookies"] != nil {
			t.Fatal("account list exposed credentials")
		}
	}
	account := entries[0].(map[string]any)
	path := "/admin/accounts/" + url.PathEscape(liveString(account["id"])) + "/usage"
	if get(path+"?cached=true", nil)["account_id"] != account["id"] {
		t.Fatal("cached usage account mismatch")
	}
	if get(path, nil)["quota_source"] != "usage_endpoint" {
		t.Fatal("fresh usage lookup failed")
	}
	if get("/admin/rotation", nil)["strategy"] == nil {
		t.Fatal("rotation strategy missing")
	}
	var result map[string]any
	if err := json.Unmarshal(postJSON(t, cfg, "/chat/completions", map[string]any{"messages": []map[string]string{{"role": "user", "content": "Reply with OK."}}}), &result); err != nil {
		t.Fatal(err)
	}
	if result["model"] != health["default_model"] {
		t.Fatalf("omitted model selected %v, default is %v", result["model"], health["default_model"])
	}
}

func liveSSEObjects(t *testing.T, data []byte) []map[string]any {
	t.Helper()
	var events []map[string]any
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	return events
}

func liveResponseText(response map[string]any) string {
	var text strings.Builder
	output, _ := response["output"].([]any)
	for _, raw := range output {
		item, _ := raw.(map[string]any)
		content, _ := item["content"].([]any)
		for _, rawPart := range content {
			part, _ := rawPart.(map[string]any)
			text.WriteString(liveString(part["text"]))
		}
	}
	return text.String()
}

func liveString(value any) string { text, _ := value.(string); return text }

func TestLivePublicValidation(t *testing.T) {
	cfg := loadLiveConfig(t)
	for _, tc := range []struct {
		path, body string
		status     int
	}{
		{"/responses", `{"model":"missing-model","input":"hello"}`, 404},
		{"/responses", "{", 400},
		{"/chat/completions", "{}", 400},
		{"/completions", `{"prompt":["one","two"]}`, 400},
		{"/images/generations", `{"prompt":""}`, 400},
		{"/images/edits", `{"prompt":"edit this"}`, 400},
		{"/responses", `{"previous_response_id":"resp_missing","input":"hello"}`, 400},
	} {
		t.Run(tc.path+tc.body, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPost, cfg.BaseURL+tc.path, strings.NewReader(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
			req.Header.Set("Content-Type", "application/json")
			resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			data, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != tc.status {
				t.Fatalf("HTTP %d, want %d: %s", resp.StatusCode, tc.status, data)
			}
			var payload map[string]any
			if err := json.Unmarshal(data, &payload); err != nil || payload["error"] == nil {
				t.Fatalf("missing structured error: %s", data)
			}
		})
	}
}
