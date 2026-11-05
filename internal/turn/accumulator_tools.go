package turn

import (
	"strings"

	"chatgpt-codex-proxy/internal/jsonutil"
)

func (a *Accumulator) applyToolArgumentEvent(event *StreamEvent) {
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
		state.Name = RestoreToolName(name, a.Normalized.ToolNameAliases)
	}

	switch event.Type {
	case "response.function_call_arguments.delta":
		state.ToolType = "function"
		state.Arguments += jsonutil.StringValue(event.Raw["delta"])
	case "response.function_call_arguments.done":
		state.ToolType = "function"
		if args := jsonutil.StringValue(event.Raw["arguments"]); args != "" {
			state.Arguments = args
		}
		state.Status = "completed"
	case "response.custom_tool_call_input.delta":
		state.ToolType = "custom"
		state.Input += jsonutil.StringValue(event.Raw["delta"])
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

	existing := a.toolCallByID[callID]
	if existing == nil {
		existing = a.toolCallByID[itemID]
	}
	if existing != nil {
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

func (a *Accumulator) registerToolCallAliases(call *ToolCallState, ids ...string) {
	for _, id := range ids {
		if strings.TrimSpace(id) == "" {
			continue
		}
		a.toolCallByID[id] = call
	}
}

func (a *Accumulator) ToolCallStateForEvent(event *StreamEvent) *ToolCallState {
	if event == nil {
		return nil
	}

	switch event.Type {
	case "response.function_call_arguments.delta", "response.function_call_arguments.done", "response.custom_tool_call_input.delta", "response.custom_tool_call_input.done":
		itemID := jsonutil.StringValue(event.Raw["item_id"])
		callID := jsonutil.FirstNonEmpty(jsonutil.StringValue(event.Raw["call_id"]), itemID)
		if state := a.toolCallByID[callID]; state != nil {
			return state
		}
		return a.toolCallByID[itemID]
	case "response.output_item.added", "response.output_item.done":
		item := jsonutil.FirstMap(jsonutil.MapValue(event.Raw, "item"), jsonutil.MapValue(event.Raw, "output_item"))
		itemType := jsonutil.StringValue(item["type"])
		if itemType != "function_call" && itemType != "custom_tool_call" {
			return nil
		}
		itemID := jsonutil.FirstNonEmpty(jsonutil.StringValue(item["id"]), jsonutil.StringValue(event.Raw["item_id"]))
		callID := jsonutil.FirstNonEmpty(jsonutil.StringValue(item["call_id"]), itemID)
		if state := a.toolCallByID[callID]; state != nil {
			return state
		}
		return a.toolCallByID[itemID]
	default:
		return nil
	}
}
