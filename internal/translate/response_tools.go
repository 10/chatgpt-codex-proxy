package translate

import (
	"strings"

	"chatgpt-codex-proxy/internal/codex"
	"chatgpt-codex-proxy/internal/jsonutil"
)

func (a *Accumulator) applyToolArgumentEvent(event *codex.StreamEvent) {
	responseItemID := jsonutil.StringValue(event.Raw["item_id"])
	callID := jsonutil.FirstNonEmpty(jsonutil.StringValue(event.Raw["call_id"]), responseItemID)
	if callID == "" && responseItemID == "" {
		return
	}

	state := a.ensureToolCallState(responseItemID, callID, outputIndexFromMap(event.Raw))
	if state == nil {
		return
	}
	if name := jsonutil.StringValue(event.Raw["name"]); name != "" {
		state.Name = name
	}

	switch event.Type {
	case "response.function_call_arguments.delta":
		state.ToolType = "function"
		state.Arguments += jsonutil.StringValue(event.Raw["delta"])
		state.SawArgumentDelta = true
	case "response.function_call_arguments.done":
		state.ToolType = "function"
		if args := jsonutil.StringValue(event.Raw["arguments"]); args != "" {
			state.Arguments = args
		}
		state.Status = "completed"
	case "response.custom_tool_call_input.delta":
		state.ToolType = "custom"
		state.Input += jsonutil.StringValue(event.Raw["delta"])
		state.SawArgumentDelta = true
	case "response.custom_tool_call_input.done":
		state.ToolType = "custom"
		if input := jsonutil.StringValue(event.Raw["input"]); input != "" {
			state.Input = input
		}
		state.Status = "completed"
	}
}

func (a *Accumulator) ensureToolCallState(itemID, callID string, explicitIndex int) *ToolCallState {
	itemID = strings.TrimSpace(itemID)
	callID = strings.TrimSpace(callID)
	if itemID == "" {
		itemID = callID
	}
	if callID == "" {
		callID = itemID
	}
	if itemID == "" || callID == "" {
		return nil
	}

	if existing := firstToolCallState(a.toolCallByID[callID], a.toolCallByID[itemID]); existing != nil {
		if explicitIndex >= 0 {
			existing.OutputIndex = a.resolveOutputIndex(explicitIndex)
		}
		hadPlaceholderItemID := existing.ItemID == "" || existing.ItemID == existing.CallID
		hadPlaceholderCallID := existing.CallID == "" || existing.CallID == existing.ItemID
		if hadPlaceholderItemID {
			existing.ItemID = jsonutil.FirstNonEmpty(itemID, existing.ItemID)
		}
		if hadPlaceholderCallID {
			existing.CallID = jsonutil.FirstNonEmpty(callID, existing.CallID)
		}
		existing.ItemID = jsonutil.FirstNonEmpty(existing.ItemID, itemID, existing.CallID)
		existing.CallID = jsonutil.FirstNonEmpty(existing.CallID, callID, existing.ItemID)
		a.registerToolCallAliases(existing, existing.CallID, existing.ItemID, callID, itemID)
		return existing
	}

	state := &ToolCallState{
		ItemID:      itemID,
		CallID:      callID,
		OutputIndex: a.resolveOutputIndex(explicitIndex),
		Status:      "in_progress",
	}
	a.ToolCalls = append(a.ToolCalls, state)
	a.registerToolCallAliases(state, callID, itemID)
	return state
}

func (a *Accumulator) ToolCallStateForEvent(event *codex.StreamEvent) *ToolCallState {
	return a.toolCallStateForEvent(event)
}

func (a *Accumulator) registerToolCallAliases(call *ToolCallState, ids ...string) {
	for _, id := range ids {
		if strings.TrimSpace(id) == "" {
			continue
		}
		a.toolCallByID[id] = call
	}
}

func (a *Accumulator) toolCallStateForEvent(event *codex.StreamEvent) *ToolCallState {
	if event == nil {
		return nil
	}

	switch event.Type {
	case "response.function_call_arguments.delta", "response.function_call_arguments.done", "response.custom_tool_call_input.delta", "response.custom_tool_call_input.done":
		itemID := jsonutil.StringValue(event.Raw["item_id"])
		callID := jsonutil.FirstNonEmpty(jsonutil.StringValue(event.Raw["call_id"]), itemID)
		return firstToolCallState(a.toolCallByID[callID], a.toolCallByID[itemID])
	case "response.output_item.added", "response.output_item.done":
		item := firstMap(jsonutil.MapValue(event.Raw, "item"), jsonutil.MapValue(event.Raw, "output_item"))
		itemType := jsonutil.StringValue(item["type"])
		if itemType != "function_call" && itemType != "custom_tool_call" {
			return nil
		}
		itemID := jsonutil.FirstNonEmpty(jsonutil.StringValue(item["id"]), jsonutil.StringValue(event.Raw["item_id"]))
		callID := jsonutil.FirstNonEmpty(jsonutil.StringValue(item["call_id"]), itemID)
		return firstToolCallState(a.toolCallByID[callID], a.toolCallByID[itemID])
	default:
		return nil
	}
}

func (a *Accumulator) ensureResponseOutputItemAdded(state *ToolCallState) []ResponseStreamEvent {
	if state == nil || state.AddedEmitted {
		return nil
	}
	state.AddedEmitted = true
	state.Status = jsonutil.FirstNonEmpty(state.Status, "in_progress")
	return []ResponseStreamEvent{{
		Type: "response.output_item.added",
		Payload: map[string]any{
			"output_index": state.OutputIndex,
			"item":         state.responseOutputItem("in_progress"),
		},
	}}
}

func (a *Accumulator) ensureResponseToolCallCompleted(state *ToolCallState) []ResponseStreamEvent {
	if state == nil || state.DoneEmitted {
		return nil
	}

	events := a.ensureResponseOutputItemAdded(state)
	if state.ToolType == "custom" {
		events = append(events, ResponseStreamEvent{
			Type: "response.custom_tool_call_input.done",
			Payload: map[string]any{
				"item_id":      state.ItemID,
				"output_index": state.OutputIndex,
				"input":        state.Input,
			},
		})
	} else {
		events = append(events, ResponseStreamEvent{
			Type: "response.function_call_arguments.done",
			Payload: map[string]any{
				"item_id":      state.ItemID,
				"call_id":      state.CallID,
				"output_index": state.OutputIndex,
				"name":         state.Name,
				"arguments":    state.Arguments,
			},
		})
	}
	state.Status = "completed"
	state.DoneEmitted = true
	events = append(events, ResponseStreamEvent{
		Type: "response.output_item.done",
		Payload: map[string]any{
			"output_index": state.OutputIndex,
			"item":         state.responseOutputItem("completed"),
		},
	})
	return events
}

func (t *ToolCallState) chatCompletionToolCall() map[string]any {
	if t == nil {
		return nil
	}
	if t.ToolType == "custom" {
		return map[string]any{
			"id":   t.CallID,
			"type": "custom",
			"custom": map[string]any{
				"name":  t.Name,
				"input": t.Input,
			},
		}
	}
	return map[string]any{
		"id":   t.CallID,
		"type": "function",
		"function": map[string]any{
			"name":      t.Name,
			"arguments": t.Arguments,
		},
	}
}

func (t *ToolCallState) responseOutputItem(status string) map[string]any {
	if t == nil {
		return nil
	}
	itemStatus := jsonutil.FirstNonEmpty(status, t.Status, "completed")
	if t.ToolType == "custom" {
		return map[string]any{
			"type":    "custom_tool_call",
			"id":      t.ItemID,
			"call_id": t.CallID,
			"name":    t.Name,
			"input":   t.Input,
			"status":  itemStatus,
		}
	}
	return map[string]any{
		"type":      "function_call",
		"id":        t.ItemID,
		"call_id":   t.CallID,
		"name":      t.Name,
		"arguments": t.Arguments,
		"status":    itemStatus,
	}
}

func firstToolCallState(values ...*ToolCallState) *ToolCallState {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}
