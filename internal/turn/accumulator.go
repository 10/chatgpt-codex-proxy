package turn

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

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
	addedEmitted bool
	doneEmitted  bool
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
	textBuilder             strings.Builder
	reasoningSummaryBuilder strings.Builder
	Usage                   *Usage
	ToolCalls               []*ToolCallState
	toolCallByID            map[string]*ToolCallState
	outputItems             []*outputItemState
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
	usage := usageFromRaw(event.Raw["usage"])
	if responseUsage := usageFromRaw(response["usage"]); responseUsage != nil && (usage == nil || event.IsTerminalResponse()) {
		usage = responseUsage
	}
	if usage != nil {
		a.Usage = usage
	}
	if event.IsTerminalResponse() {
		a.terminalEventType = event.Type
		a.RawFinal = event.Raw
		if output := jsonutil.SliceOfMaps(response["output"]); len(output) > 0 {
			a.replaceOutputItems(output)
		}
	}
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
			a.textBuilder.WriteString(delta)
		}
	case "response.reasoning_summary_text.delta":
		if delta := jsonutil.StringValue(event.Raw["delta"]); delta != "" {
			a.reasoningSummaryBuilder.WriteString(delta)
		}
	case "response.output_text.done":
		if a.textBuilder.Len() == 0 {
			a.textBuilder.WriteString(jsonutil.StringValue(event.Raw["text"]))
		}
	case "response.content_part.done":
		if a.textBuilder.Len() == 0 {
			part := jsonutil.MapValue(event.Raw, "part")
			if text := jsonutil.StringValue(part["text"]); text != "" {
				a.textBuilder.WriteString(text)
			}
		}
	case "response.completed", "response.incomplete":
		if a.textBuilder.Len() == 0 && response != nil {
			if text := jsonutil.StringValue(response["output_text"]); text != "" {
				a.textBuilder.WriteString(text)
			}
		}
	}
}

func (a *Accumulator) applyFallbackText(event *StreamEvent) {
	if a.textBuilder.Len() != 0 || !strings.Contains(event.Type, "text") {
		return
	}
	if delta := jsonutil.FirstNonEmpty(
		jsonutil.StringValue(event.Raw["output_text"]),
		jsonutil.StringValue(jsonutil.MapValue(event.Raw, "item")["text"]),
	); delta != "" {
		a.textBuilder.WriteString(delta)
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

func (a *Accumulator) Text() string {
	if text := strings.TrimSpace(a.textBuilder.String()); text != "" {
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
	return strings.TrimSpace(a.reasoningSummaryBuilder.String())
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
	a.outputItems = append(a.outputItems, state)
	a.outputItemByKey[key] = state
}

func (a *Accumulator) replaceOutputItems(items []map[string]any) {
	a.outputItems = nil
	a.outputItemByKey = make(map[string]*outputItemState)
	for idx, item := range items {
		a.captureOutputItem(item, idx)
	}
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
	if len(a.outputItems) == 0 {
		return nil
	}

	states := append([]*outputItemState(nil), a.outputItems...)
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
