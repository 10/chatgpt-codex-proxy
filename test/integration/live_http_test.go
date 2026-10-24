//go:build live

package integration_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestLiveResponsesStructuredOutputAndContinuation(t *testing.T) {
	cfg := loadLiveConfig(t)
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
}

func TestLiveReadEndpoints(t *testing.T) {
	cfg := loadLiveConfig(t)
	root := strings.TrimSuffix(strings.TrimRight(cfg.BaseURL, "/"), "/v1")
	get := func(path string) map[string]any {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, root+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
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
	list := get("/v1/models")
	data, _ := list["data"].([]any)
	found := false
	for _, raw := range data {
		item, _ := raw.(map[string]any)
		found = found || item["id"] == cfg.Model
	}
	if !found {
		t.Fatalf("model list missing %s", cfg.Model)
	}
	accounts := get("/admin/accounts")
	entries, _ := accounts["accounts"].([]any)
	if len(entries) == 0 {
		t.Fatal("no accounts available")
	}
	account := entries[0].(map[string]any)
	path := "/admin/accounts/" + url.PathEscape(liveString(account["id"])) + "/usage"
	if get(path)["quota_source"] != "usage_endpoint" {
		t.Fatal("fresh usage lookup failed")
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
