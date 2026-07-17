package server

import (
	"io"
	"strings"

	"chatgpt-codex-proxy/internal/codex"
	"chatgpt-codex-proxy/internal/jsonutil"
	"chatgpt-codex-proxy/internal/turn"
)

type chatToolCallStreamer struct {
	indexByCallID map[string]int
	initialized   map[string]bool
	argumentsSent map[string]int
	nextIndex     int
	createdAt     int64
}

func (s *chatToolCallStreamer) writeChunk(w io.Writer, accumulator *turn.Accumulator, normalized turn.NormalizedRequest, event *codex.StreamEvent) bool {
	if event == nil {
		return false
	}
	state := accumulator.ToolCallStateForEvent(event)
	if state == nil || strings.TrimSpace(state.CallID) == "" {
		return false
	}
	callID := state.CallID

	idx, exists := s.indexByCallID[callID]
	if !exists {
		idx = s.nextIndex
		s.indexByCallID[callID] = idx
		s.nextIndex++
	}

	emitted := false
	if !s.initialized[callID] && strings.TrimSpace(state.Name) != "" {
		// Chat Completions clients like Cursor reliably understand function-call
		// deltas but may reject streamed custom-tool deltas. We expose every tool
		// call as function-shaped here and map custom tools back upstream on replay.
		chunkToolCall := map[string]any{
			"index": idx,
			"id":    callID,
		}
		chunkToolCall["type"] = "function"
		chunkToolCall["function"] = map[string]any{
			"name":      state.Name,
			"arguments": "",
		}
		writeSSE(w, "", turn.MustJSON(turn.ChatChunk(accumulator.ResponseID, jsonutil.FirstNonEmpty(accumulator.Model, normalized.Model), map[string]any{
			"tool_calls": []map[string]any{chunkToolCall},
		}, "", s.createdAt)))
		s.initialized[callID] = true
		emitted = true
	}

	if !s.initialized[callID] {
		return emitted
	}

	value := state.Arguments
	if state.ToolType == "custom" {
		value = state.Input
	}
	sent := s.argumentsSent[callID]
	if sent >= len(value) {
		return emitted
	}

	writeSSE(w, "", turn.MustJSON(turn.ChatChunk(accumulator.ResponseID, jsonutil.FirstNonEmpty(accumulator.Model, normalized.Model), map[string]any{
		"tool_calls": []map[string]any{{
			"index": idx,
			"function": map[string]any{
				"arguments": value[sent:],
			},
		}},
	}, "", s.createdAt)))
	s.argumentsSent[callID] = len(value)
	return true
}
