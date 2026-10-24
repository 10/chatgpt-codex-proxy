package anthropic

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"chatgpt-codex-proxy/internal/codex"
	"chatgpt-codex-proxy/internal/turn"
)

func TestSessionIDUsesHeaderThenClaudeCodeMetadata(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		header   string
		metadata string
		want     string
	}{
		{
			name:     "header wins",
			header:   "header-session",
			metadata: `{"session_id":"metadata-session"}`,
			want:     "header-session",
		},
		{
			name:     "direct metadata",
			metadata: `{"session_id":"metadata-session"}`,
			want:     "metadata-session",
		},
		{
			name:     "claude code user suffix",
			metadata: `{"user_id":"user_abc_account_def_session_8c250f9a-54c6-4a89-8e95-387251a569ba"}`,
			want:     "8c250f9a-54c6-4a89-8e95-387251a569ba",
		},
		{
			name:     "embedded metadata",
			metadata: `{"user_id":"{\"session_id\":\"embedded-session\"}"}`,
			want:     "embedded-session",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := SessionID(test.header, json.RawMessage(test.metadata)); got != test.want {
				t.Fatalf("SessionID() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestReplayManagerKeepsClaudeCodeAgentAffinityAndDeletionIsolated(t *testing.T) {
	t.Parallel()

	manager := NewReplayManager(time.Minute)
	accumulator := turn.NewAccumulator(turn.NormalizedRequest{Request: codex.Request{Model: "gpt-5.4"}})
	accumulator.Apply(&codex.StreamEvent{Type: "response.completed", Raw: map[string]any{
		"response": map[string]any{
			"id": "resp_tool", "model": "gpt-5.4", "status": "completed",
			"output": []any{map[string]any{
				"type": "function_call", "call_id": "call_1", "name": "lookup", "arguments": `{}`,
			}},
		},
	}})
	agentA := ExecutionScope("shared-session", "agent-a", nil)
	agentB := ExecutionScope("shared-session", "agent-b", nil)
	if !manager.Remember(agentA, "gpt-5.4", "acct-a", accumulator) {
		t.Fatal("Remember(agent-a) = false")
	}
	if !manager.Remember(agentB, "gpt-5.4", "acct-b", accumulator) {
		t.Fatal("Remember(agent-b) = false")
	}

	maxTokens := 10
	request := MessagesRequest{
		Model:     "gpt-5.4",
		MaxTokens: &maxTokens,
		Messages: []Message{{Role: "user", Content: Content{{
			Type: "tool_result", ToolUseID: "call_1", Content: Content{{Type: "text", Text: "result"}},
		}}}},
	}
	if _, match := manager.Apply(agentA, request); !match.Applied || match.AccountID != "acct-a" {
		t.Fatalf("agent A match = %#v", match)
	}
	if _, match := manager.Apply(agentB, request); !match.Applied || match.AccountID != "acct-b" {
		t.Fatalf("agent B match = %#v", match)
	}

	manager.Delete(agentA, "gpt-5.4")
	if _, match := manager.Apply(agentA, request); match.Applied || match.AccountID != "" {
		t.Fatalf("deleted agent A match = %#v", match)
	}
	if _, match := manager.Apply(agentB, request); !match.Applied || match.AccountID != "acct-b" {
		t.Fatalf("agent B match after deleting agent A = %#v", match)
	}
}

func TestReplayManagerPreservesTextPositionBetweenStateBlocks(t *testing.T) {
	t.Parallel()

	manager := NewReplayManager(time.Minute)
	accumulator := turn.NewAccumulator(turn.NormalizedRequest{Request: codex.Request{
		Model:   "gpt-5.4",
		Include: []string{"reasoning.encrypted_content"},
	}})
	accumulator.Apply(&codex.StreamEvent{Type: "response.completed", Raw: map[string]any{
		"response": map[string]any{
			"id": "resp_tool", "model": "gpt-5.4", "status": "completed",
			"output": []any{
				map[string]any{"type": "reasoning", "encrypted_content": testCodexReasoningSignature()},
				map[string]any{
					"type": "message", "role": "assistant", "status": "completed",
					"content": []any{map[string]any{"type": "output_text", "text": "Checking now."}},
				},
				map[string]any{"type": "function_call", "call_id": "call_1", "name": "lookup", "arguments": `{}`},
			},
		},
	}})
	manager.Remember("session-a", "gpt-5.4", "acct-a", accumulator)

	maxTokens := 10
	request := MessagesRequest{
		Model:     "gpt-5.4",
		MaxTokens: &maxTokens,
		Messages: []Message{
			{Role: "assistant", Content: Content{{Type: "text", Text: "Checking now."}}},
			{Role: "user", Content: Content{{
				Type: "tool_result", ToolUseID: "call_1", Content: Content{{Type: "text", Text: "done"}},
			}}},
		},
	}
	replayed, match := manager.Apply("session-a", request)
	if !match.Applied {
		t.Fatal("Apply() did not restore missing reasoning")
	}
	blocks := replayed.Messages[0].Content
	want := []string{"thinking", "text", "tool_use"}
	if len(blocks) != len(want) {
		t.Fatalf("replayed blocks = %#v", blocks)
	}
	for index, block := range blocks {
		if block.Type != want[index] {
			t.Fatalf("block %d type = %q, want %q: %#v", index, block.Type, want[index], blocks)
		}
	}
}

func TestReplayManagerExpiresRecords(t *testing.T) {
	t.Parallel()

	now := time.Now()
	manager := NewReplayManager(time.Minute)
	manager.now = func() time.Time { return now }
	accumulator := turn.NewAccumulator(turn.NormalizedRequest{Request: codex.Request{Model: "gpt-5.4"}})
	accumulator.Apply(&codex.StreamEvent{Type: "response.completed", Raw: map[string]any{
		"response": map[string]any{
			"id": "resp_tool", "model": "gpt-5.4", "status": "completed",
			"output": []any{map[string]any{"type": "function_call", "call_id": "call_1", "name": "lookup", "arguments": `{}`}},
		},
	}})
	manager.Remember("session-a", "gpt-5.4", "acct-a", accumulator)
	now = now.Add(2 * time.Minute)

	maxTokens := 10
	request := MessagesRequest{
		Model:     "gpt-5.4",
		MaxTokens: &maxTokens,
		Messages: []Message{{Role: "user", Content: Content{{
			Type: "tool_result", ToolUseID: "call_1", Content: Content{{Type: "text", Text: "result"}},
		}}}},
	}
	_, match := manager.Apply("session-a", request)
	if match.Applied {
		t.Fatal("expired replay state was applied")
	}
}

func TestReplayManagerBoundsEntriesAndBytes(t *testing.T) {
	t.Parallel()

	now := time.Now()
	manager := NewReplayManager(time.Minute)
	manager.maxEntries = 2
	manager.now = func() time.Time { return now }
	accumulator := turn.NewAccumulator(turn.NormalizedRequest{Request: codex.Request{Model: "gpt-5.4"}})
	accumulator.Apply(&codex.StreamEvent{Type: "response.completed", Raw: map[string]any{
		"response": map[string]any{
			"id": "resp_tool", "model": "gpt-5.4", "status": "completed",
			"output": []any{map[string]any{"type": "function_call", "call_id": "call_1", "name": "lookup", "arguments": `{}`}},
		},
	}})

	for index := range 3 {
		if !manager.Remember(fmt.Sprintf("session-%d", index), "gpt-5.4", "acct-a", accumulator) {
			t.Fatalf("Remember(session-%d) = false", index)
		}
		now = now.Add(time.Second)
	}
	if len(manager.records) > manager.maxEntries || manager.bytes > manager.maxBytes {
		t.Fatalf("cache bounds exceeded: entries=%d bytes=%d", len(manager.records), manager.bytes)
	}

	maxTokens := 10
	request := MessagesRequest{
		Model:     "gpt-5.4",
		MaxTokens: &maxTokens,
		Messages: []Message{{Role: "user", Content: Content{{
			Type: "tool_result", ToolUseID: "call_1", Content: Content{{Type: "text", Text: "result"}},
		}}}},
	}
	if _, match := manager.Apply("session-0", request); match.Applied {
		t.Fatal("oldest replay record was not evicted")
	}
	if _, match := manager.Apply("session-2", request); !match.Applied {
		t.Fatal("newest replay record was evicted")
	}

	byteLimited := NewReplayManager(time.Minute)
	byteLimited.maxBytes = 1
	if byteLimited.Remember("session-a", "gpt-5.4", "acct-a", accumulator) {
		t.Fatal("oversized replay record was retained")
	}
	if len(byteLimited.records) != 0 || byteLimited.bytes != 0 {
		t.Fatalf("oversized record changed cache: entries=%d bytes=%d", len(byteLimited.records), byteLimited.bytes)
	}
}
