package anthropic

import (
	"cmp"
	"fmt"
	"strings"

	"chatgpt-codex-proxy/internal/codex"
	"chatgpt-codex-proxy/internal/jsonutil"
	"chatgpt-codex-proxy/internal/translate"
)

type StreamEvent = map[string]any

type streamBlock struct {
	Index  int
	Type   string
	CallID string
}

type StreamEncoder struct {
	started       bool
	nextIndex     int
	open          *streamBlock
	textEmitted   bool
	thinkEmitted  bool
	thinkDeltas   []string
	toolSent      map[string]int
	toolDone      map[string]bool
	toolStates    map[string]*translate.ToolCallState
	toolDeltas    map[string][]string
	toolOrder     []string
	webSearchDone map[string]bool
	inputTokens   int64
}

func NewStreamEncoder(inputTokens int64) *StreamEncoder {
	return &StreamEncoder{
		toolSent:      make(map[string]int),
		toolDone:      make(map[string]bool),
		toolStates:    make(map[string]*translate.ToolCallState),
		toolDeltas:    make(map[string][]string),
		webSearchDone: make(map[string]bool),
		inputTokens:   max(0, inputTokens),
	}
}

func (e *StreamEncoder) Events(event *codex.StreamEvent, accumulator *translate.Accumulator) []StreamEvent {
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
		if signature := cmp.Or(
			jsonutil.StringValue(event.Raw["signature"]),
			jsonutil.StringValue(event.Raw["encrypted_content"]),
			jsonutil.StringValue(jsonutil.MapValue(event.Raw, "item")["encrypted_content"]),
		); signature != "" {
			result = append(result, e.completeThinking(accumulator.ReasoningSummary(), signature)...)
		}
	case "response.output_text.delta":
		delta := jsonutil.StringValue(event.Raw["delta"])
		if delta != "" && len(accumulator.Normalized.ResponseSchema) == 0 {
			result = append(result, e.openText()...)
			result = append(result, e.delta("text_delta", "text", delta))
			e.textEmitted = true
		}
	case "response.output_text.done":
		if len(accumulator.Normalized.ResponseSchema) > 0 && !e.textEmitted {
			text := cmp.Or(jsonutil.StringValue(event.Raw["text"]), accumulator.Text())
			if text != "" {
				result = append(result, e.openText()...)
				result = append(result, e.delta("text_delta", "text", normalizedOutputText(accumulator, text)))
				e.textEmitted = true
			}
		}
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
		if jsonutil.StringValue(item["type"]) == "web_search_call" {
			fallbackID := webSearchStreamFallbackID(event.Raw)
			result = append(result, e.completeWebSearch(item, fallbackID)...)
			break
		}
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
	case "response.completed", "response.incomplete":
		result = append(result, e.hydrateTerminal(accumulator)...)
		result = append(result, e.closeBlock()...)
		stopReason, stopSequence := stopFromAccumulator(accumulator)
		result = append(result,
			StreamEvent{
				"type": "message_delta",
				"delta": map[string]any{
					"stop_reason":   stopReason,
					"stop_sequence": nullableString(stopSequence),
				},
				"usage": map[string]any{"output_tokens": usageFromAccumulator(accumulator).OutputTokens},
			},
			StreamEvent{"type": "message_stop"},
		)
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
	usage.OutputTokens = 0
	message := MessageResponse{
		ID:      MessageID(accumulator.ResponseID),
		Type:    "message",
		Role:    "assistant",
		Model:   cmp.Or(accumulator.Model, accumulator.Normalized.Model),
		Content: []ResponseBlock{},
		Usage:   usage,
	}
	return []StreamEvent{{"type": "message_start", "message": message}}
}

func (e *StreamEncoder) openText() []StreamEvent {
	if e.open != nil && e.open.Type == "text" {
		return nil
	}
	events := e.closeBlock()
	block := &streamBlock{Index: e.nextIndex, Type: "text"}
	e.nextIndex++
	e.open = block
	return append(events, StreamEvent{
		"type": "content_block_start", "index": block.Index,
		"content_block": map[string]any{"type": "text", "text": ""},
	})
}

func (e *StreamEncoder) openThinking() []StreamEvent {
	if e.open != nil && e.open.Type == "thinking" {
		return nil
	}
	events := e.closeBlock()
	block := &streamBlock{Index: e.nextIndex, Type: "thinking"}
	e.nextIndex++
	e.open = block
	return append(events, StreamEvent{
		"type": "content_block_start", "index": block.Index,
		"content_block": map[string]any{"type": "thinking", "thinking": "", "signature": ""},
	})
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
	block := &streamBlock{Index: e.nextIndex, Type: "tool_use", CallID: state.CallID}
	e.nextIndex++
	e.open = block
	e.toolSent[state.CallID] = 0
	return []StreamEvent{{
		"type": "content_block_start", "index": block.Index,
		"content_block": map[string]any{"type": "tool_use", "id": shortenCallID(state.CallID), "name": state.Name, "input": map[string]any{}},
	}}
}

func (e *StreamEncoder) closeBlock() []StreamEvent {
	if e.open == nil {
		return nil
	}
	index := e.open.Index
	e.open = nil
	return []StreamEvent{{"type": "content_block_stop", "index": index}}
}

func (e *StreamEncoder) delta(deltaType, field, value string) StreamEvent {
	return StreamEvent{
		"type":  "content_block_delta",
		"index": e.open.Index,
		"delta": map[string]any{"type": deltaType, field: value},
	}
}

func (e *StreamEncoder) toolRemainder(state *translate.ToolCallState) []StreamEvent {
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
	toolStreamStarted := e.open != nil && e.open.Type == "tool_use" || len(e.toolSent) > 0 || len(e.toolDeltas) > 0
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
	responsesObject := accumulator.ResponsesObject()
	for index, item := range jsonutil.SliceOfMaps(responsesObject["output"]) {
		if jsonutil.StringValue(item["type"]) == "web_search_call" {
			events = append(events, e.completeWebSearch(item, fmt.Sprintf("web_search_%d", index))...)
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

func webSearchStreamFallbackID(raw map[string]any) string {
	switch index := raw["output_index"].(type) {
	case int:
		return fmt.Sprintf("web_search_%d", index)
	case int64:
		return fmt.Sprintf("web_search_%d", index)
	case float64:
		if index >= 0 && index == float64(int(index)) {
			return fmt.Sprintf("web_search_%d", int(index))
		}
	}
	return jsonutil.FirstNonEmpty(jsonutil.StringValue(raw["item_id"]), jsonutil.StringValue(raw["output_item_id"]))
}

func (e *StreamEncoder) completeWebSearch(item map[string]any, fallbackID string) []StreamEvent {
	blocks := webSearchResponseBlocks(item, fallbackID)
	if len(blocks) != 2 {
		return nil
	}
	toolUse := blocks[0]
	if e.webSearchDone[toolUse.ID] {
		return nil
	}
	e.webSearchDone[toolUse.ID] = true

	events := e.closeBlock()
	useIndex := e.nextIndex
	e.nextIndex++
	events = append(events, StreamEvent{
		"type":  "content_block_start",
		"index": useIndex,
		"content_block": map[string]any{
			"type":  "server_tool_use",
			"id":    toolUse.ID,
			"name":  toolUse.Name,
			"input": map[string]any{},
		},
	})
	if string(toolUse.Input) != "{}" {
		events = append(events, StreamEvent{
			"type":  "content_block_delta",
			"index": useIndex,
			"delta": map[string]any{
				"type":         "input_json_delta",
				"partial_json": string(toolUse.Input),
			},
		})
	}
	events = append(events, StreamEvent{"type": "content_block_stop", "index": useIndex})

	searchResult := blocks[1]
	resultIndex := e.nextIndex
	e.nextIndex++
	events = append(events,
		StreamEvent{
			"type":  "content_block_start",
			"index": resultIndex,
			"content_block": map[string]any{
				"type":        "web_search_tool_result",
				"tool_use_id": searchResult.ToolUseID,
				"content":     searchResult.Content,
			},
		},
		StreamEvent{"type": "content_block_stop", "index": resultIndex},
	)
	return events
}

func (e *StreamEncoder) registerTool(state *translate.ToolCallState) {
	if _, exists := e.toolStates[state.CallID]; !exists {
		e.toolOrder = append(e.toolOrder, state.CallID)
	}
	e.toolStates[state.CallID] = state
}

func (e *StreamEncoder) completeTool(state *translate.ToolCallState) []StreamEvent {
	e.registerTool(state)
	e.toolDone[state.CallID] = true
	return e.flushTools()
}

func (e *StreamEncoder) flushTools() []StreamEvent {
	if e.open != nil {
		return nil
	}
	var events []StreamEvent
	for _, callID := range e.toolOrder {
		if _, emitted := e.toolSent[callID]; emitted {
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

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
