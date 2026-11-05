package turn

import (
	"cmp"
	"maps"
	"slices"
	"strings"
	"time"

	"chatgpt-codex-proxy/internal/jsonutil"
)

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

	entries := make([]outputEntry, 0, len(a.ToolCalls)+len(a.outputItems))
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
	for order, state := range a.outputItems {
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

func (t *ToolCallState) chatCompletionToolCall() map[string]any {
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
