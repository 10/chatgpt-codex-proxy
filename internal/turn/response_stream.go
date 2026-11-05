package turn

import (
	"strings"

	"chatgpt-codex-proxy/internal/jsonutil"
)

type ResponseStreamEvent struct {
	Type    string
	Payload map[string]any
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
		if state.doneEmitted {
			continue
		}
		if a.IsIncomplete() && !strings.EqualFold(strings.TrimSpace(state.Status), "completed") {
			continue
		}
		events = append(events, a.ensureResponseToolCallCompleted(state)...)
	}
	return events
}

func (a *Accumulator) ensureResponseOutputItemAdded(state *ToolCallState) []ResponseStreamEvent {
	if state == nil || state.addedEmitted {
		return nil
	}
	state.addedEmitted = true
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
	if state == nil || state.doneEmitted {
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
	state.doneEmitted = true
	events = append(events, ResponseStreamEvent{
		Type: "response.output_item.done",
		Payload: map[string]any{
			"output_index": state.OutputIndex,
			"item":         state.responseOutputItem("completed"),
		},
	})
	return events
}
