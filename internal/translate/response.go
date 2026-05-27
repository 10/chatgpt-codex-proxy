package translate

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"chatgpt-codex-proxy/internal/codex"
	"chatgpt-codex-proxy/internal/jsonutil"
)

type ToolCallState struct {
	ItemID           string
	CallID           string
	ToolType         string
	Name             string
	Input            string
	Arguments        string
	OutputIndex      int
	Status           string
	AddedEmitted     bool
	DoneEmitted      bool
	SawArgumentDelta bool
}

func (t *ToolCallState) Completed() bool {
	return t != nil && strings.EqualFold(strings.TrimSpace(t.Status), "completed")
}

type ResponseStreamEvent struct {
	Type    string
	Payload map[string]any
}

type outputItemState struct {
	Key         string
	OutputIndex int
	Item        map[string]any
}

type Accumulator struct {
	Normalized              NormalizedRequest
	ResponseID              string
	Model                   string
	TextBuilder             strings.Builder
	ReasoningSummaryBuilder strings.Builder
	Usage                   *codex.Usage
	ToolCalls               []*ToolCallState
	toolCallByID            map[string]*ToolCallState
	OutputItems             []*outputItemState
	outputItemByKey         map[string]*outputItemState
	Status                  string
	RawFinal                map[string]any
	nextOutputIndex         int
}

func NewAccumulator(normalized NormalizedRequest) *Accumulator {
	return &Accumulator{
		Normalized:      normalized,
		toolCallByID:    make(map[string]*ToolCallState),
		outputItemByKey: make(map[string]*outputItemState),
	}
}

func (a *Accumulator) Apply(event *codex.StreamEvent) {
	if event == nil {
		return
	}
	response := jsonutil.MapValue(event.Raw, "response")
	a.applyResponseMetadata(response)
	a.applyRootMetadata(event.Raw)
	a.applyTextEvent(event, response)
	a.applyFallbackText(event)
	a.applyToolEvent(event)
	a.applyOutputEvent(event)
	a.applyUsageMetadata(event.Raw)
	a.applyCompletedEvent(event, response)
}

func (a *Accumulator) applyResponseMetadata(response map[string]any) {
	if response == nil {
		return
	}
	if id := jsonutil.StringValue(response["id"]); id != "" {
		a.ResponseID = id
	}
	if model := jsonutil.StringValue(response["model"]); model != "" {
		a.Model = model
	}
	if status := jsonutil.StringValue(response["status"]); status != "" {
		a.Status = status
	}
	if usage := usageFromRaw(response["usage"]); usage != nil {
		a.Usage = usage
	}
	if output := jsonutil.SliceOfMaps(response["output"]); len(output) > 0 {
		a.replaceOutputItems(output)
	}
}

func (a *Accumulator) applyRootMetadata(raw map[string]any) {
	if id := jsonutil.StringValue(raw["response_id"]); id != "" && a.ResponseID == "" {
		a.ResponseID = id
	}
	if model := jsonutil.StringValue(raw["model"]); model != "" && a.Model == "" {
		a.Model = model
	}
}

func (a *Accumulator) applyTextEvent(event *codex.StreamEvent, response map[string]any) {
	switch event.Type {
	case "response.output_text.delta":
		if delta := jsonutil.StringValue(event.Raw["delta"]); delta != "" {
			a.TextBuilder.WriteString(delta)
		}
	case "response.reasoning_summary_text.delta":
		if delta := jsonutil.StringValue(event.Raw["delta"]); delta != "" {
			a.ReasoningSummaryBuilder.WriteString(delta)
		}
	case "response.output_text.done":
		if a.TextBuilder.Len() == 0 {
			a.TextBuilder.WriteString(jsonutil.StringValue(event.Raw["text"]))
		}
	case "response.content_part.done":
		if a.TextBuilder.Len() == 0 {
			part := jsonutil.MapValue(event.Raw, "part")
			if text := jsonutil.StringValue(part["text"]); text != "" {
				a.TextBuilder.WriteString(text)
			}
		}
	case "response.completed":
		if a.TextBuilder.Len() == 0 && response != nil {
			if text := jsonutil.StringValue(response["output_text"]); text != "" {
				a.TextBuilder.WriteString(text)
			}
		}
	}
}

func (a *Accumulator) applyFallbackText(event *codex.StreamEvent) {
	if a.TextBuilder.Len() != 0 || !strings.Contains(event.Type, "text") {
		return
	}
	if delta := jsonutil.FirstNonEmpty(
		jsonutil.StringValue(event.Raw["output_text"]),
		jsonutil.StringValue(jsonutil.MapValue(event.Raw, "item")["text"]),
	); delta != "" {
		a.TextBuilder.WriteString(delta)
	}
}

func (a *Accumulator) applyToolEvent(event *codex.StreamEvent) {
	if strings.HasPrefix(event.Type, "response.function_call_arguments.") || strings.HasPrefix(event.Type, "response.custom_tool_call_input.") {
		a.applyToolArgumentEvent(event)
	}
}

func (a *Accumulator) applyOutputEvent(event *codex.StreamEvent) {
	if item := jsonutil.FirstMap(jsonutil.MapValue(event.Raw, "item"), jsonutil.MapValue(event.Raw, "output_item")); item != nil {
		a.captureOutputItem(item, outputIndexFromMap(event.Raw))
	}
	if output := jsonutil.SliceOfMaps(event.Raw["output"]); len(output) > 0 {
		a.replaceOutputItems(output)
	}
}

func (a *Accumulator) applyUsageMetadata(raw map[string]any) {
	if usage := usageFromRaw(raw["usage"]); usage != nil {
		a.Usage = usage
	}
}

func (a *Accumulator) applyCompletedEvent(event *codex.StreamEvent, response map[string]any) {
	if event.Type != "response.completed" {
		return
	}
	a.RawFinal = event.Raw
	if response == nil {
		return
	}
	if usage := usageFromRaw(response["usage"]); usage != nil {
		a.Usage = usage
	}
	if output := jsonutil.SliceOfMaps(response["output"]); len(output) > 0 {
		a.replaceOutputItems(output)
	}
}

func (a *Accumulator) Text() string {
	if text := strings.TrimSpace(a.TextBuilder.String()); text != "" {
		return text
	}
	for _, item := range a.sortedOutputItems() {
		if itemType := jsonutil.StringValue(item["type"]); itemType == "message" {
			for _, content := range jsonutil.SliceOfMaps(item["content"]) {
				if text := jsonutil.StringValue(content["text"]); text != "" {
					return text
				}
			}
		}
	}
	return ""
}

func (a *Accumulator) IsCompleted() bool {
	return a != nil && a.RawFinal != nil
}

func (a *Accumulator) ReasoningSummary() string {
	if a == nil {
		return ""
	}
	return strings.TrimSpace(a.ReasoningSummaryBuilder.String())
}

func (a *Accumulator) ResponsesStreamEventsForEvent(event *codex.StreamEvent) ([]ResponseStreamEvent, bool) {
	if event == nil {
		return nil, false
	}

	switch event.Type {
	case "response.function_call_arguments.delta", "response.custom_tool_call_input.delta":
		state := a.toolCallStateForEvent(event)
		if state == nil {
			return nil, false
		}
		events := a.ensureResponseOutputItemAdded(state)
		delta := jsonutil.StringValue(event.Raw["delta"])
		if delta != "" {
			eventType := "response.function_call_arguments.delta"
			if event.Type == "response.custom_tool_call_input.delta" {
				eventType = "response.custom_tool_call_input.delta"
			}
			events = append(events, ResponseStreamEvent{
				Type: eventType,
				Payload: map[string]any{
					"item_id":      state.ItemID,
					"output_index": state.OutputIndex,
					"delta":        delta,
				},
			})
		}
		return events, true
	case "response.function_call_arguments.done", "response.custom_tool_call_input.done":
		state := a.toolCallStateForEvent(event)
		if state == nil {
			return nil, false
		}
		return a.ensureResponseToolCallCompleted(state), true
	case "response.output_item.added":
		state := a.toolCallStateForEvent(event)
		if state == nil {
			return nil, false
		}
		return a.ensureResponseOutputItemAdded(state), true
	case "response.output_item.done":
		state := a.toolCallStateForEvent(event)
		if state == nil {
			return nil, false
		}
		return a.ensureResponseToolCallCompleted(state), true
	default:
		return nil, false
	}
}

func (a *Accumulator) PendingResponseToolCallCompletionEvents() []ResponseStreamEvent {
	events := make([]ResponseStreamEvent, 0)
	for _, state := range a.ToolCalls {
		if state.DoneEmitted {
			continue
		}
		events = append(events, a.ensureResponseToolCallCompleted(state)...)
	}
	return events
}

func (a *Accumulator) captureOutputItem(item map[string]any, explicitIndex int) {
	if len(item) == 0 {
		return
	}

	itemType := jsonutil.StringValue(item["type"])
	if itemType == "function_call" || itemType == "custom_tool_call" {
		callID := jsonutil.FirstNonEmpty(jsonutil.StringValue(item["call_id"]), jsonutil.StringValue(item["id"]))
		itemID := jsonutil.FirstNonEmpty(jsonutil.StringValue(item["id"]), callID)
		if callID == "" && itemID == "" {
			return
		}
		state := a.ensureToolCallState(itemID, callID, explicitIndex)
		if state == nil {
			return
		}
		if name := jsonutil.StringValue(item["name"]); name != "" {
			state.Name = name
		}
		switch itemType {
		case "custom_tool_call":
			state.ToolType = "custom"
			if input := jsonutil.StringValue(item["input"]); input != "" {
				state.Input = input
			}
		default:
			state.ToolType = "function"
			if arguments := jsonutil.StringValue(item["arguments"]); arguments != "" {
				state.Arguments = arguments
			}
		}
		if status := jsonutil.StringValue(item["status"]); status != "" {
			state.Status = status
		}
		return
	}

	index := a.resolveOutputIndex(explicitIndex)
	key := outputItemKey(item, index)
	cloned := jsonutil.CloneMap(item)
	if existing, ok := a.outputItemByKey[key]; ok {
		existing.Item = cloned
		existing.OutputIndex = index
		return
	}
	state := &outputItemState{
		Key:         key,
		OutputIndex: index,
		Item:        cloned,
	}
	a.OutputItems = append(a.OutputItems, state)
	a.outputItemByKey[key] = state
}

func (a *Accumulator) replaceOutputItems(items []map[string]any) {
	a.OutputItems = nil
	a.outputItemByKey = make(map[string]*outputItemState)
	for idx, item := range items {
		a.captureOutputItem(item, idx)
	}
}

func (a *Accumulator) ChatCompletionObject() map[string]any {
	message := map[string]any{
		"role":    "assistant",
		"content": a.Text(),
	}
	if a.Normalized.Reasoning != nil {
		if summary := a.ReasoningSummary(); summary != "" {
			message["reasoning_content"] = summary
		}
	}
	if toolCalls := a.chatCompletionToolCalls(); len(toolCalls) > 0 {
		message["tool_calls"] = toolCalls
	}
	return map[string]any{
		"id":      jsonutil.FirstNonEmpty(a.ResponseID, "chatcmpl_proxy"),
		"object":  "chat.completion",
		"model":   jsonutil.FirstNonEmpty(a.Model, a.Normalized.Model),
		"choices": []map[string]any{{"index": 0, "message": message, "finish_reason": finishReason(a)}},
		"usage":   a.ChatUsageObject(),
	}
}

func (a *Accumulator) ResponsesObject() map[string]any {
	text := a.Text()
	output := a.responsesOutput(text)
	return map[string]any{
		"id":          jsonutil.FirstNonEmpty(a.ResponseID, "resp_proxy"),
		"object":      "response",
		"model":       jsonutil.FirstNonEmpty(a.Model, a.Normalized.Model),
		"status":      jsonutil.FirstNonEmpty(a.Status, "completed"),
		"output":      output,
		"output_text": text,
		"usage":       a.ResponsesUsageObject(),
	}
}

func (a *Accumulator) responsesOutput(text string) []map[string]any {
	type outputEntry struct {
		OutputIndex int
		Order       int
		Item        map[string]any
	}

	entries := make([]outputEntry, 0, len(a.ToolCalls)+len(a.OutputItems))
	for order, state := range a.ToolCalls {
		entries = append(entries, outputEntry{
			OutputIndex: state.OutputIndex,
			Order:       order,
			Item:        state.responseOutputItem("completed"),
		})
	}

	baseOrder := len(entries)
	for order, state := range a.OutputItems {
		cloned := jsonutil.CloneMap(state.Item)
		if jsonutil.StringValue(cloned["type"]) == "message" {
			content := jsonutil.SliceOfMaps(cloned["content"])
			if len(content) == 0 && strings.TrimSpace(text) != "" {
				cloned["content"] = responseTextContent(text)
			}
			if jsonutil.StringValue(cloned["status"]) == "" {
				cloned["status"] = "completed"
			}
		}
		entries = append(entries, outputEntry{
			OutputIndex: state.OutputIndex,
			Order:       baseOrder + order,
			Item:        cloned,
		})
	}

	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].OutputIndex == entries[j].OutputIndex {
			return entries[i].Order < entries[j].Order
		}
		return entries[i].OutputIndex < entries[j].OutputIndex
	})

	output := make([]map[string]any, 0, len(entries)+1)
	for _, entry := range entries {
		output = append(output, entry.Item)
	}

	if len(output) == 0 {
		output = append(output, map[string]any{
			"type":    "message",
			"role":    "assistant",
			"status":  "completed",
			"content": responseTextContent(text),
		})
	}
	return output
}

func (a *Accumulator) chatCompletionToolCalls() []map[string]any {
	if len(a.ToolCalls) == 0 {
		return nil
	}

	out := make([]map[string]any, 0, len(a.ToolCalls))
	for _, state := range a.ToolCalls {
		out = append(out, state.chatCompletionToolCall())
	}
	return out
}

func responseTextContent(text string) []map[string]any {
	if strings.TrimSpace(text) == "" {
		return []map[string]any{}
	}
	return []map[string]any{{
		"type": "output_text",
		"text": text,
	}}
}

func ChatChunk(responseID, model string, delta map[string]any, finishReason string) map[string]any {
	choice := map[string]any{
		"index": 0,
		"delta": delta,
	}
	if finishReason != "" {
		choice["finish_reason"] = finishReason
	}
	return map[string]any{
		"id":      jsonutil.FirstNonEmpty(responseID, "chatcmpl_proxy"),
		"object":  "chat.completion.chunk",
		"model":   model,
		"choices": []map[string]any{choice},
	}
}

func ChatChunkWithUsage(responseID, model string, delta map[string]any, finishReason string, usage map[string]any) map[string]any {
	chunk := ChatChunk(responseID, model, delta, finishReason)
	if usage != nil {
		chunk["usage"] = usage
	}
	return chunk
}

func ResponseEventJSON(eventType string, responseID string, payload map[string]any) []byte {
	eventPayload := make(map[string]any, len(payload)+2)
	for key, value := range payload {
		eventPayload[key] = value
	}
	if responseID != "" {
		eventPayload["response_id"] = responseID
	}
	eventPayload["type"] = eventType
	data, _ := json.Marshal(eventPayload)
	return data
}

func finishReason(a *Accumulator) string {
	if len(a.ToolCalls) > 0 {
		return "tool_calls"
	}
	return "stop"
}

func (a *Accumulator) resolveOutputIndex(preferred int) int {
	if preferred >= 0 {
		if preferred >= a.nextOutputIndex {
			a.nextOutputIndex = preferred + 1
		}
		return preferred
	}
	index := a.nextOutputIndex
	a.nextOutputIndex++
	return index
}

func (a *Accumulator) sortedOutputItems() []map[string]any {
	if len(a.OutputItems) == 0 {
		return nil
	}

	states := append([]*outputItemState(nil), a.OutputItems...)
	sort.SliceStable(states, func(i, j int) bool {
		return states[i].OutputIndex < states[j].OutputIndex
	})

	items := make([]map[string]any, 0, len(states))
	for _, state := range states {
		items = append(items, state.Item)
	}
	return items
}

func outputIndexFromMap(raw map[string]any) int {
	if raw == nil {
		return -1
	}
	if value, ok := intValue(raw["output_index"]); ok {
		return value
	}
	return -1
}

func outputItemKey(item map[string]any, outputIndex int) string {
	if id := jsonutil.StringValue(item["id"]); id != "" {
		return "id:" + id
	}
	if outputIndex >= 0 {
		return fmt.Sprintf("index:%d", outputIndex)
	}
	return fmt.Sprintf("anon:%p", item)
}

func MustJSON(value any) []byte {
	data, err := json.Marshal(value)
	if err != nil {
		return []byte(fmt.Sprintf(`{"error":"%v"}`, err))
	}
	return data
}
