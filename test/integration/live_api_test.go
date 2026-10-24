//go:build live

package integration_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

type liveConfig struct {
	APIKey  string
	BaseURL string
	Model   string
}

func TestLiveChatCompletionsCustomToolStreamingRoundTrip(t *testing.T) {
	t.Parallel()

	cfg := loadLiveConfig(t)
	tool := map[string]any{
		"type":        "custom",
		"name":        "ApplyPatch",
		"description": "Apply a patch to the workspace",
		"format": map[string]any{
			"type":   "grammar",
			"syntax": "lark",
			"definition": strings.Join([]string{
				"start: begin_patch hunk end_patch",
				`begin_patch: "*** Begin Patch" LF`,
				`end_patch: "*** End Patch" LF?`,
				"",
				"hunk: add_hunk | update_hunk",
				`add_hunk: "*** Add File: " filename LF add_line+`,
				`update_hunk: "*** Update File: " filename LF change?`,
				"",
				"filename: /(.+)/",
				`add_line: "+" /(.*)/ LF -> line`,
				"",
				"change: (change_context | change_line)+ eof_line?",
				`change_context: ("@@" | "@@ " /(.+)/) LF`,
				`change_line: ("+" | "-" | " ") /(.*)/ LF`,
				`eof_line: "*** End of File" LF`,
				"",
				"%import common.LF",
			}, "\n"),
		},
	}
	userPrompt := "Use the ApplyPatch tool to create test.txt with the exact contents hello. Do not answer in natural language before calling the tool."

	streamBody := postJSON(t, cfg, "/chat/completions", map[string]any{
		"model":  cfg.Model,
		"stream": true,
		"messages": []map[string]any{
			{"role": "user", "content": userPrompt},
		},
		"tools": []map[string]any{tool},
		"tool_choice": map[string]any{
			"type": "custom",
			"name": "ApplyPatch",
		},
	})

	callID, toolName, arguments := extractStreamedToolCall(t, streamBody)
	if callID == "" {
		t.Fatal("stream did not include a tool call id")
	}
	if toolName != "ApplyPatch" {
		t.Fatalf("streamed tool name = %q, want ApplyPatch", toolName)
	}
	if !strings.Contains(arguments, "test.txt") {
		t.Fatalf("streamed tool arguments = %q, want patch content mentioning test.txt", arguments)
	}
	if !strings.Contains(arguments, "*** Begin Patch") {
		t.Fatalf("streamed tool arguments = %q, want ApplyPatch input", arguments)
	}

	followUpBody := postJSON(t, cfg, "/chat/completions", map[string]any{
		"model":  cfg.Model,
		"stream": false,
		"messages": []map[string]any{
			{"role": "user", "content": "Use the ApplyPatch tool to create test.txt with the exact contents hello. After the tool returns, respond with exactly DONE."},
			{
				"role":    "assistant",
				"content": "",
				"tool_calls": []map[string]any{
					{
						"id":   callID,
						"type": "function",
						"function": map[string]any{
							"name":      toolName,
							"arguments": arguments,
						},
					},
				},
			},
			{
				"role":         "tool",
				"tool_call_id": callID,
				"content":      "Patch applied successfully.",
			},
		},
		"tools":       []map[string]any{tool},
		"tool_choice": "none",
	})

	var response struct {
		Choices []struct {
			Message struct {
				Content   string            `json:"content"`
				ToolCalls []json.RawMessage `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(followUpBody, &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Choices) != 1 || len(response.Choices[0].Message.ToolCalls) != 0 || strings.TrimSpace(response.Choices[0].Message.Content) != "DONE" {
		t.Fatalf("unexpected follow-up completion: %s", followUpBody)
	}
}

func loadLiveConfig(t *testing.T) liveConfig {
	t.Helper()

	cfg := liveConfig{
		APIKey:  os.Getenv("OPENAI_API_KEY"),
		BaseURL: os.Getenv("OPENAI_BASE_URL"),
		Model:   os.Getenv("OPENAI_MODEL"),
	}
	if cfg.APIKey == "" {
		cfg.APIKey = "change-me-to-a-long-random-string"
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "http://localhost:8080/v1"
	}
	if cfg.Model == "" {
		cfg.Model = "gpt-6.1-sol"
	}

	return cfg
}

func postJSON(t *testing.T, cfg liveConfig, path string, payload any, headers ...map[string]string) []byte {
	t.Helper()

	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(cfg.BaseURL, "/")+path, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if len(headers) == 0 {
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}
	for _, header := range headers {
		for name, value := range header {
			req.Header.Set(name, value)
		}
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 90 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request %s failed: %v", path, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	if resp.StatusCode >= http.StatusBadRequest {
		t.Fatalf("request %s returned %d: %s", path, resp.StatusCode, string(respBody))
	}

	return respBody
}

func extractStreamedToolCall(t *testing.T, body []byte) (string, string, string) {
	t.Helper()

	var (
		callID    string
		toolName  string
		arguments strings.Builder
		sawFinish bool
	)

	for _, payload := range liveSSEObjects(t, body) {
		choices, _ := payload["choices"].([]any)
		if len(choices) == 0 {
			continue
		}
		choice, _ := choices[0].(map[string]any)
		if finish, _ := choice["finish_reason"].(string); finish == "tool_calls" {
			sawFinish = true
		}

		delta, _ := choice["delta"].(map[string]any)
		if delta == nil {
			continue
		}
		rawToolCalls, _ := delta["tool_calls"].([]any)
		if len(rawToolCalls) == 0 {
			continue
		}

		first, _ := rawToolCalls[0].(map[string]any)
		if id, _ := first["id"].(string); id != "" {
			callID = id
		}
		function, _ := first["function"].(map[string]any)
		if function == nil {
			continue
		}
		if name, _ := function["name"].(string); name != "" {
			toolName = name
		}
		if deltaArgs, _ := function["arguments"].(string); deltaArgs != "" {
			arguments.WriteString(deltaArgs)
		}
	}

	if !sawFinish {
		t.Fatalf("stream body did not end with finish_reason=tool_calls: %s", string(body))
	}

	return callID, toolName, arguments.String()
}
