package anthropic

import (
	"strings"

	"chatgpt-codex-proxy/internal/codex"
	"chatgpt-codex-proxy/internal/jsonutil"
	"chatgpt-codex-proxy/internal/translate"
)

type StreamEvent struct {
	Type string
	Data map[string]any
}

type streamBlock struct {
	Index  int
	Type   string
	CallID string
}

type StreamEncoder struct {
	started      bool
	finished     bool
	nextIndex    int
	open         *streamBlock
	textEmitted  bool
	thinkEmitted bool
	thinkDeltas  []string
	toolEmitted  map[string]bool
	toolSent     map[string]int
	toolDone     map[string]bool
	toolStates   map[string]*translate.ToolCallState
	toolDeltas   map[string][]string
	toolOrder    []string
	inputTokens  int64
}

func NewStreamEncoder(inputTokens ...int64) *StreamEncoder {
	encoder := &StreamEncoder{
		toolEmitted: make(map[string]bool),
		toolSent:    make(map[string]int),
		toolDone:    make(map[string]bool),
		toolStates:  make(map[string]*translate.ToolCallState),
		toolDeltas:  make(map[string][]string),
	}
	if len(inputTokens) > 0 && inputTokens[0] > 0 {
		encoder.inputTokens = inputTokens[0]
	}
	return encoder
}

func (e *StreamEncoder) Events(event *codex.StreamEvent, accumulator *translate.Accumulator) []StreamEvent {
	if e == nil || e.finished || event == nil || accumulator == nil {
		return nil
	}
	result := e.ensureStarted(accumulator)

	switch event.Type {
	case "response.reasoning_summary_text.delta":
		if !shouldExposeThinking(accumulator) {
			break
		}
		delta := jsonutil.StringValue(event.Raw["delta"])
		if delta != "" {
			e.thinkDeltas = append(e.thinkDeltas, delta)
		}
	case "response.reasoning_summary_text.done":
		if !shouldExposeThinking(accumulator) {
			break
		}
		if signature := signatureFromEvent(event); signature != "" {
			result = append(result, e.completeThinking(accumulator.ReasoningSummary(), signature)...)
		}
	case "response.output_text.delta":
		delta := jsonutil.StringValue(event.Raw["delta"])
		if delta != "" {
			result = append(result, e.openText()...)
			result = append(result, e.delta("text_delta", "text", delta))
			e.textEmitted = true
		}
	case "response.output_text.done":
		if !e.textEmitted {
			if text := jsonutil.StringValue(event.Raw["text"]); text != "" {
				result = append(result, e.openText()...)
				result = append(result, e.delta("text_delta", "text", text))
				e.textEmitted = true
			}
		}
		if e.open != nil && e.open.Type == "text" {
			result = append(result, e.closeBlock()...)
		}
	case "response.function_call_arguments.delta", "response.custom_tool_call_input.delta":
		if state := accumulator.ToolCallStateForEvent(event); state != nil {
			e.registerTool(state)
			if delta := jsonutil.StringValue(event.Raw["delta"]); delta != "" {
				e.toolDeltas[state.CallID] = append(e.toolDeltas[state.CallID], delta)
			}
		}
	case "response.function_call_arguments.done", "response.custom_tool_call_input.done":
		if state := accumulator.ToolCallStateForEvent(event); state != nil {
			result = append(result, e.completeTool(state)...)
		}
	case "response.output_item.done":
		item := jsonutil.FirstMap(jsonutil.MapValue(event.Raw, "item"), jsonutil.MapValue(event.Raw, "output_item"))
		if jsonutil.StringValue(item["type"]) == "reasoning" {
			if !shouldExposeThinking(accumulator) {
				break
			}
			if signature := jsonutil.StringValue(item["encrypted_content"]); signature != "" {
				result = append(result, e.completeThinking(reasoningText(item), signature)...)
			} else {
				e.thinkDeltas = nil
			}
			break
		}
		if state := accumulator.ToolCallStateForEvent(event); state != nil {
			result = append(result, e.completeTool(state)...)
		}
	case "response.completed":
		result = append(result, e.hydrateTerminal(accumulator)...)
		result = append(result, e.closeBlock()...)
		stopReason, stopSequence := stopFromAccumulator(accumulator)
		result = append(result,
			StreamEvent{Type: "message_delta", Data: map[string]any{
				"type": "message_delta",
				"delta": map[string]any{
					"stop_reason":   stopReason,
					"stop_sequence": nullableString(stopSequence),
				},
				"usage": map[string]any{"output_tokens": usageFromAccumulator(accumulator).OutputTokens},
			}},
			StreamEvent{Type: "message_stop", Data: map[string]any{"type": "message_stop"}},
		)
		e.finished = true
	}
	return result
}

func (e *StreamEncoder) ensureStarted(accumulator *translate.Accumulator) []StreamEvent {
	if e.started {
		return nil
	}
	e.started = true
	usage := usageFromAccumulator(accumulator)
	if usage.InputTokens == 0 {
		usage.InputTokens = e.inputTokens
	}
	message := MessageResponse{
		ID:         MessageID(accumulator.ResponseID),
		Type:       "message",
		Role:       "assistant",
		Model:      firstNonEmpty(accumulator.Model, accumulator.Normalized.Model),
		Content:    []ResponseBlock{},
		StopReason: nil,
		Usage: Usage{
			InputTokens:              usage.InputTokens,
			OutputTokens:             0,
			CacheCreationInputTokens: usage.CacheCreationInputTokens,
			CacheReadInputTokens:     usage.CacheReadInputTokens,
		},
	}
	return []StreamEvent{{Type: "message_start", Data: map[string]any{"type": "message_start", "message": message}}}
}

func (e *StreamEncoder) openText() []StreamEvent {
	if e.open != nil && e.open.Type == "text" {
		return nil
	}
	events := e.closeBlock()
	block := &streamBlock{Index: e.nextIndex, Type: "text"}
	e.nextIndex++
	e.open = block
	return append(events, StreamEvent{Type: "content_block_start", Data: map[string]any{
		"type": "content_block_start", "index": block.Index,
		"content_block": map[string]any{"type": "text", "text": ""},
	}})
}

func (e *StreamEncoder) openThinking() []StreamEvent {
	if e.open != nil && e.open.Type == "thinking" {
		return nil
	}
	events := e.closeBlock()
	block := &streamBlock{Index: e.nextIndex, Type: "thinking"}
	e.nextIndex++
	e.open = block
	return append(events, StreamEvent{Type: "content_block_start", Data: map[string]any{
		"type": "content_block_start", "index": block.Index,
		"content_block": map[string]any{"type": "thinking", "thinking": "", "signature": ""},
	}})
}

func (e *StreamEncoder) completeThinking(thinking, signature string) []StreamEvent {
	if e.thinkEmitted || strings.TrimSpace(signature) == "" {
		return nil
	}
	events := e.openThinking()
	if len(e.thinkDeltas) > 0 {
		for _, delta := range e.thinkDeltas {
			events = append(events, e.delta("thinking_delta", "thinking", delta))
		}
	} else if thinking != "" {
		events = append(events, e.delta("thinking_delta", "thinking", thinking))
	}
	events = append(events, e.delta("signature_delta", "signature", signature))
	events = append(events, e.closeBlock()...)
	e.thinkDeltas = nil
	e.thinkEmitted = true
	return events
}

func (e *StreamEncoder) openTool(state *translate.ToolCallState) []StreamEvent {
	if state == nil || strings.TrimSpace(state.CallID) == "" {
		return nil
	}
	if e.open != nil && e.open.Type == "tool_use" && e.open.CallID == state.CallID {
		return nil
	}
	if e.open != nil {
		return nil
	}
	if e.toolEmitted[state.CallID] {
		return nil
	}
	events := e.closeBlock()
	block := &streamBlock{Index: e.nextIndex, Type: "tool_use", CallID: state.CallID}
	e.nextIndex++
	e.open = block
	e.toolEmitted[state.CallID] = true
	return append(events, StreamEvent{Type: "content_block_start", Data: map[string]any{
		"type": "content_block_start", "index": block.Index,
		"content_block": map[string]any{"type": "tool_use", "id": state.CallID, "name": state.Name, "input": map[string]any{}},
	}})
}

func (e *StreamEncoder) closeBlock() []StreamEvent {
	if e.open == nil {
		return nil
	}
	index := e.open.Index
	e.open = nil
	return []StreamEvent{{Type: "content_block_stop", Data: map[string]any{"type": "content_block_stop", "index": index}}}
}

func (e *StreamEncoder) delta(deltaType, field, value string) StreamEvent {
	return StreamEvent{Type: "content_block_delta", Data: map[string]any{
		"type":  "content_block_delta",
		"index": e.open.Index,
		"delta": map[string]any{"type": deltaType, field: value},
	}}
}

func (e *StreamEncoder) toolRemainder(state *translate.ToolCallState) []StreamEvent {
	if state == nil || e.open == nil {
		return nil
	}
	value := state.Arguments
	if state.ToolType == "custom" {
		value = state.Input
	}
	sent := e.toolSent[state.CallID]
	if sent >= len(value) {
		return nil
	}
	remainder := value[sent:]
	e.toolSent[state.CallID] = len(value)
	return []StreamEvent{e.delta("input_json_delta", "partial_json", remainder)}
}

func (e *StreamEncoder) hydrateTerminal(accumulator *translate.Accumulator) []StreamEvent {
	var events []StreamEvent
	toolStreamStarted := e.open != nil && e.open.Type == "tool_use" || len(e.toolEmitted) > 0 || len(e.toolDeltas) > 0
	response := jsonutil.MapValue(accumulator.RawFinal, "response")
	for _, state := range accumulator.ToolCalls {
		if !isExecutableToolState(state, response) {
			delete(e.toolStates, state.CallID)
			continue
		}
		e.registerTool(state)
		e.toolDone[state.CallID] = true
	}
	flushTerminalTools := func() {
		if e.open != nil && e.open.Type == "tool_use" {
			if state := e.toolStates[e.open.CallID]; state != nil && e.toolDone[e.open.CallID] {
				events = append(events, e.toolRemainder(state)...)
			}
			events = append(events, e.closeBlock()...)
		}
		events = append(events, e.flushTools()...)
	}
	if toolStreamStarted {
		flushTerminalTools()
	}

	blocks := responseBlocks(accumulator)
	if !e.thinkEmitted {
		for _, block := range blocks {
			if block.Type != "thinking" {
				continue
			}
			events = append(events, e.completeThinking(block.Thinking, block.Signature)...)
			break
		}
	}
	if !e.textEmitted {
		for _, block := range blocks {
			if block.Type != "text" || block.Text == "" {
				continue
			}
			events = append(events, e.openText()...)
			events = append(events, e.delta("text_delta", "text", block.Text))
			events = append(events, e.closeBlock()...)
			e.textEmitted = true
			break
		}
	}
	if !toolStreamStarted {
		flushTerminalTools()
	}
	return events
}

func (e *StreamEncoder) registerTool(state *translate.ToolCallState) {
	if state == nil || strings.TrimSpace(state.CallID) == "" {
		return
	}
	if _, exists := e.toolStates[state.CallID]; !exists {
		e.toolOrder = append(e.toolOrder, state.CallID)
	}
	e.toolStates[state.CallID] = state
}

func (e *StreamEncoder) completeTool(state *translate.ToolCallState) []StreamEvent {
	e.registerTool(state)
	if state == nil || strings.TrimSpace(state.CallID) == "" {
		return nil
	}
	e.toolDone[state.CallID] = true
	return e.flushTools()
}

func (e *StreamEncoder) flushTools() []StreamEvent {
	if e.open != nil {
		return nil
	}
	var events []StreamEvent
	for _, callID := range e.toolOrder {
		if e.toolEmitted[callID] {
			continue
		}
		state := e.toolStates[callID]
		if state == nil {
			continue
		}
		if !e.toolDone[callID] {
			return events
		}
		events = append(events, e.openTool(state)...)
		for _, delta := range e.toolDeltas[callID] {
			events = append(events, e.delta("input_json_delta", "partial_json", delta))
			e.toolSent[callID] += len(delta)
		}
		delete(e.toolDeltas, callID)
		events = append(events, e.toolRemainder(state)...)
		events = append(events, e.closeBlock()...)
	}
	return events
}

func signatureFromEvent(event *codex.StreamEvent) string {
	if event == nil {
		return ""
	}
	return firstNonEmpty(
		jsonutil.StringValue(event.Raw["signature"]),
		jsonutil.StringValue(event.Raw["encrypted_content"]),
		jsonutil.StringValue(jsonutil.MapValue(event.Raw, "item")["encrypted_content"]),
	)
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
