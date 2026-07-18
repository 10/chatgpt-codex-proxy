package turn

import (
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"chatgpt-codex-proxy/internal/jsonutil"
)

type ToolCallState struct {
	ItemID       string
	CallID       string
	ToolType     string
	Name         string
	Input        string
	Arguments    string
	OutputIndex  int
	Status       string
	AddedEmitted bool
	DoneEmitted  bool
}

type ResponseStreamEvent struct {
	Type    string
	Payload map[string]any
}

type outputItemState struct {
	OutputIndex int
	Item        map[string]any
}

type Accumulator struct {
	Normalized              NormalizedRequest
	ResponseID              string
	CreatedAt               int64
	Model                   string
	TextBuilder             strings.Builder
	ReasoningSummaryBuilder strings.Builder
	Usage                   *Usage
	ToolCalls               []*ToolCallState
	toolCallByID            map[string]*ToolCallState
	OutputItems             []*outputItemState
	outputItemByKey         map[string]*outputItemState
	Status                  string
	terminalEventType       string
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

func (a *Accumulator) Apply(event *StreamEvent) {
	if event == nil {
		return
	}
	response := jsonutil.MapValue(event.Raw, "response")
	a.applyResponseMetadata(response)
	a.applyRootMetadata(event.Raw)
	a.applyTextEvent(event, response)
	a.applyFallbackText(event)
	if strings.HasPrefix(event.Type, "response.function_call_arguments.") || strings.HasPrefix(event.Type, "response.custom_tool_call_input.") {
		a.applyToolArgumentEvent(event)
	}
	a.applyOutputEvent(event)
	if usage := usageFromRaw(event.Raw["usage"]); usage != nil {
		a.Usage = usage
	}
	a.applyTerminalEvent(event, response)
}

func (a *Accumulator) applyResponseMetadata(response map[string]any) {
	if response == nil {
		return
	}
	if id := jsonutil.StringValue(response["id"]); id != "" {
		a.ResponseID = id
	}
	if createdAt, ok := intValue(response["created_at"]); ok && createdAt > 0 {
		a.CreatedAt = int64(createdAt)
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

func (a *Accumulator) applyTextEvent(event *StreamEvent, response map[string]any) {
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
	case "response.completed", "response.incomplete":
		if a.TextBuilder.Len() == 0 && response != nil {
			if text := jsonutil.StringValue(response["output_text"]); text != "" {
				a.TextBuilder.WriteString(text)
			}
		}
	}
}

func (a *Accumulator) applyFallbackText(event *StreamEvent) {
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

func (a *Accumulator) applyOutputEvent(event *StreamEvent) {
	if item := jsonutil.FirstMap(jsonutil.MapValue(event.Raw, "item"), jsonutil.MapValue(event.Raw, "output_item")); item != nil {
		a.captureOutputItem(item, outputIndexFromMap(event.Raw))
	}
	if output := jsonutil.SliceOfMaps(event.Raw["output"]); len(output) > 0 {
		a.replaceOutputItems(output)
	}
}

func (a *Accumulator) applyTerminalEvent(event *StreamEvent, response map[string]any) {
	if !event.IsTerminalResponse() {
		return
	}
	a.terminalEventType = event.Type
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
	return a != nil && a.terminalEventType == "response.completed"
}

func (a *Accumulator) IsTerminal() bool {
	return a != nil && a.terminalEventType != ""
}

func (a *Accumulator) IsIncomplete() bool {
	return a != nil && (a.terminalEventType == "response.incomplete" || strings.EqualFold(strings.TrimSpace(a.Status), "incomplete"))
}

func (a *Accumulator) NativeFinishReason() string {
	if !a.IsIncomplete() {
		return ""
	}
	response := jsonutil.MapValue(a.RawFinal, "response")
	incomplete := jsonutil.MapValue(response, "incomplete_details")
	return jsonutil.FirstNonEmpty(jsonutil.StringValue(incomplete["reason"]), jsonutil.StringValue(response["stop_reason"]))
}

func (a *Accumulator) ChatFinishReason() string {
	if a.IsIncomplete() {
		switch a.NativeFinishReason() {
		case "max_tokens", "max_output_tokens", "context_length_exceeded":
			return "length"
		case "content_filter", "refusal":
			return "content_filter"
		default:
			return "stop"
		}
	}
	if len(a.ToolCalls) > 0 {
		return "tool_calls"
	}
	return "stop"
}

func (a *Accumulator) ReasoningSummary() string {
	if a == nil {
		return ""
	}
	return strings.TrimSpace(a.ReasoningSummaryBuilder.String())
}

func (a *Accumulator) ResponsesStreamEventsForEvent(event *StreamEvent) ([]ResponseStreamEvent, bool) {
	if event == nil {
		return nil, false
	}

	switch event.Type {
	case "response.function_call_arguments.delta", "response.custom_tool_call_input.delta":
		state := a.ToolCallStateForEvent(event)
		if state == nil {
			return nil, false
		}
		events := a.ensureResponseOutputItemAdded(state)
		delta := jsonutil.StringValue(event.Raw["delta"])
		if delta != "" {
			events = append(events, ResponseStreamEvent{
				Type: event.Type,
				Payload: map[string]any{
					"item_id":      state.ItemID,
					"output_index": state.OutputIndex,
					"delta":        delta,
				},
			})
		}
		return events, true
	case "response.function_call_arguments.done", "response.custom_tool_call_input.done", "response.output_item.done":
		state := a.ToolCallStateForEvent(event)
		if state == nil {
			return nil, false
		}
		return a.ensureResponseToolCallCompleted(state), true
	case "response.output_item.added":
		state := a.ToolCallStateForEvent(event)
		if state == nil {
			return nil, false
		}
		return a.ensureResponseOutputItemAdded(state), true
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
		if a.IsIncomplete() && !strings.EqualFold(strings.TrimSpace(state.Status), "completed") {
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
			state.Name = RestoreToolName(name, a.Normalized.ToolNameAliases)
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
	if images := a.ChatImages(); len(images) > 0 {
		message["images"] = images
	}
	choice := map[string]any{"index": 0, "message": message, "finish_reason": a.ChatFinishReason()}
	if nativeFinishReason := a.NativeFinishReason(); nativeFinishReason != "" {
		choice["native_finish_reason"] = nativeFinishReason
	}
	return map[string]any{
		"id":      jsonutil.FirstNonEmpty(a.ResponseID, "chatcmpl_proxy"),
		"object":  "chat.completion",
		"created": a.createdAt(),
		"model":   jsonutil.FirstNonEmpty(a.Model, a.Normalized.Model),
		"choices": []map[string]any{choice},
		"usage":   a.ChatUsageObject(),
	}
}

func (a *Accumulator) CompletionObject() map[string]any {
	choice := map[string]any{"index": 0, "text": a.Text(), "finish_reason": a.ChatFinishReason()}
	if nativeFinishReason := a.NativeFinishReason(); nativeFinishReason != "" {
		choice["native_finish_reason"] = nativeFinishReason
	}
	return map[string]any{
		"id":      jsonutil.FirstNonEmpty(a.ResponseID, "cmpl_proxy"),
		"object":  "text_completion",
		"created": a.createdAt(),
		"model":   jsonutil.FirstNonEmpty(a.Model, a.Normalized.Model),
		"choices": []map[string]any{choice},
		"usage":   a.ChatUsageObject(),
	}
}

func (a *Accumulator) ChatImages() []map[string]any {
	images := make([]map[string]any, 0)
	for _, item := range a.sortedOutputItems() {
		if jsonutil.StringValue(item["type"]) != "image_generation_call" {
			continue
		}
		if image := ChatImage(len(images), jsonutil.StringValue(item["output_format"]), jsonutil.StringValue(item["result"])); image != nil {
			images = append(images, image)
		}
	}
	return images
}

func ChatImage(index int, outputFormat, base64Data string) map[string]any {
	if strings.TrimSpace(base64Data) == "" {
		return nil
	}
	mimeType := "image/png"
	switch strings.ToLower(strings.TrimSpace(outputFormat)) {
	case "jpeg", "jpg":
		mimeType = "image/jpeg"
	case "webp":
		mimeType = "image/webp"
	case "gif":
		mimeType = "image/gif"
	}
	return map[string]any{
		"index": index,
		"type":  "image_url",
		"image_url": map[string]any{
			"url": "data:" + mimeType + ";base64," + base64Data,
		},
	}
}

func (a *Accumulator) ResponsesObject() map[string]any {
	text := a.Text()
	output := a.responsesOutput(text)
	response := jsonutil.CloneMap(jsonutil.MapValue(a.RawFinal, "response"))
	if response == nil {
		response = map[string]any{}
	}
	response["id"] = jsonutil.FirstNonEmpty(a.ResponseID, jsonutil.StringValue(response["id"]), "resp_proxy")
	response["object"] = jsonutil.FirstNonEmpty(jsonutil.StringValue(response["object"]), "response")
	response["model"] = jsonutil.FirstNonEmpty(a.Model, jsonutil.StringValue(response["model"]), a.Normalized.Model)
	response["status"] = jsonutil.FirstNonEmpty(a.Status, jsonutil.StringValue(response["status"]), "completed")
	response["output"] = output
	response["output_text"] = text

	if rebuiltUsage := a.ResponsesUsageObject(); rebuiltUsage != nil {
		usage := jsonutil.CloneMap(jsonutil.MapValue(response, "usage"))
		if usage == nil {
			usage = map[string]any{}
		}
		for key, value := range rebuiltUsage {
			if details, ok := value.(map[string]any); ok {
				merged := jsonutil.CloneMap(jsonutil.MapValue(usage, key))
				if merged == nil {
					merged = map[string]any{}
				}
				maps.Copy(merged, details)
				usage[key] = merged
				continue
			}
			usage[key] = value
		}
		response["usage"] = usage
	}
	return response
}

func (a *Accumulator) createdAt() int64 {
	if a != nil && a.CreatedAt > 0 {
		return a.CreatedAt
	}
	return time.Now().UTC().Unix()
}

func (a *Accumulator) responsesOutput(text string) []map[string]any {
	type outputEntry struct {
		OutputIndex int
		Order       int
		Item        map[string]any
	}

	entries := make([]outputEntry, 0, len(a.ToolCalls)+len(a.OutputItems))
	for order, state := range a.ToolCalls {
		status := "completed"
		if a.IsIncomplete() {
			status = state.Status
			if strings.TrimSpace(status) == "" {
				status = "incomplete"
			}
		}
		entries = append(entries, outputEntry{
			OutputIndex: state.OutputIndex,
			Order:       order,
			Item:        state.responseOutputItem(status),
		})
	}

	baseOrder := len(entries)
	terminalItemStatus := "completed"
	if a.IsIncomplete() {
		terminalItemStatus = "incomplete"
	}
	for order, state := range a.OutputItems {
		cloned := jsonutil.CloneMap(state.Item)
		if jsonutil.StringValue(cloned["type"]) == "message" {
			content := jsonutil.SliceOfMaps(cloned["content"])
			if len(content) == 0 && strings.TrimSpace(text) != "" {
				cloned["content"] = responseTextContent(text)
			}
			if jsonutil.StringValue(cloned["status"]) == "" {
				cloned["status"] = terminalItemStatus
			}
		}
		entries = append(entries, outputEntry{
			OutputIndex: state.OutputIndex,
			Order:       baseOrder + order,
			Item:        cloned,
		})
	}

	slices.SortStableFunc(entries, func(a, b outputEntry) int {
		return cmp.Or(cmp.Compare(a.OutputIndex, b.OutputIndex), cmp.Compare(a.Order, b.Order))
	})

	output := make([]map[string]any, 0, len(entries)+1)
	for _, entry := range entries {
		output = append(output, entry.Item)
	}

	if len(output) == 0 {
		if a.IsIncomplete() && strings.TrimSpace(text) == "" {
			return output
		}
		output = append(output, map[string]any{
			"type":    "message",
			"role":    "assistant",
			"status":  terminalItemStatus,
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

func ChatChunk(responseID, model string, delta map[string]any, finishReason string, createdAt int64) map[string]any {
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
		"created": createdAt,
		"model":   model,
		"choices": []map[string]any{choice},
	}
}

func ResponseEventJSON(eventType string, responseID string, payload map[string]any) []byte {
	eventPayload := maps.Clone(payload)
	if eventPayload == nil {
		eventPayload = map[string]any{}
	}
	if responseID != "" {
		eventPayload["response_id"] = responseID
	}
	eventPayload["type"] = eventType
	data, _ := json.Marshal(eventPayload)
	return data
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
	slices.SortStableFunc(states, func(a, b *outputItemState) int {
		return cmp.Compare(a.OutputIndex, b.OutputIndex)
	})

	items := make([]map[string]any, 0, len(states))
	for _, state := range states {
		items = append(items, state.Item)
	}
	return items
}

func outputIndexFromMap(raw map[string]any) int {
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
