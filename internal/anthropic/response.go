package anthropic

import (
	"bytes"
	"cmp"
	"encoding/json"
	"slices"
	"strings"

	"chatgpt-codex-proxy/internal/jsonutil"
	"chatgpt-codex-proxy/internal/translate"
)

type MessageResponse struct {
	ID           string          `json:"id"`
	Type         string          `json:"type"`
	Role         string          `json:"role"`
	Model        string          `json:"model"`
	Content      []ResponseBlock `json:"content"`
	StopReason   *string         `json:"stop_reason"`
	StopSequence *string         `json:"stop_sequence"`
	Usage        Usage           `json:"usage"`
}

type ResponseBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
	Signature string          `json:"signature,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
}

func (b ResponseBlock) MarshalJSON() ([]byte, error) {
	if b.Type == "thinking" {
		return json.Marshal(struct {
			Type      string `json:"type"`
			Thinking  string `json:"thinking"`
			Signature string `json:"signature"`
		}{Type: b.Type, Thinking: b.Thinking, Signature: b.Signature})
	}
	type alias ResponseBlock
	return json.Marshal(alias(b))
}

type Usage struct {
	InputTokens          int64 `json:"input_tokens"`
	OutputTokens         int64 `json:"output_tokens"`
	CacheReadInputTokens int64 `json:"cache_read_input_tokens,omitempty"`
}

func BuildMessage(accumulator *translate.Accumulator) MessageResponse {
	response := MessageResponse{
		ID:      MessageID(accumulator.ResponseID),
		Type:    "message",
		Role:    "assistant",
		Model:   cmp.Or(accumulator.Model, accumulator.Normalized.Model),
		Content: responseBlocks(accumulator),
		Usage:   usageFromAccumulator(accumulator),
	}
	stopReason, stopSequence := stopFromAccumulator(accumulator)
	response.StopReason = &stopReason
	if stopSequence != "" {
		response.StopSequence = &stopSequence
	}
	return response
}

func MessageID(responseID string) string {
	responseID = strings.TrimSpace(responseID)
	if responseID == "" {
		return "msg_proxy"
	}
	if strings.HasPrefix(responseID, "msg_") {
		return responseID
	}
	return "msg_" + strings.TrimPrefix(responseID, "resp_")
}

func responseBlocks(accumulator *translate.Accumulator) []ResponseBlock {
	response := accumulator.ResponsesObject()
	output := jsonutil.SliceOfMaps(response["output"])
	blocks := make([]ResponseBlock, 0, len(output))
	exposeThinking := shouldExposeThinking(accumulator)
	for _, item := range output {
		switch jsonutil.StringValue(item["type"]) {
		case "reasoning":
			if !exposeThinking {
				continue
			}
			thinking := reasoningText(item)
			signature := jsonutil.StringValue(item["encrypted_content"])
			if signature != "" {
				blocks = append(blocks, ResponseBlock{Type: "thinking", Thinking: thinking, Signature: signature})
			}
		case "message":
			for _, content := range jsonutil.SliceOfMaps(item["content"]) {
				switch jsonutil.StringValue(content["type"]) {
				case "output_text", "text":
					blocks = append(blocks, ResponseBlock{Type: "text", Text: jsonutil.StringValue(content["text"])})
				case "refusal":
					blocks = append(blocks, ResponseBlock{Type: "text", Text: cmp.Or(jsonutil.StringValue(content["refusal"]), jsonutil.StringValue(content["text"]))})
				}
			}
		case "function_call", "custom_tool_call":
			if !isExecutableToolCall(item, response, accumulator) {
				continue
			}
			arguments := cmp.Or(jsonutil.StringValue(item["arguments"]), jsonutil.StringValue(item["input"]))
			blocks = append(blocks, ResponseBlock{
				Type:  "tool_use",
				ID:    cmp.Or(jsonutil.StringValue(item["call_id"]), jsonutil.StringValue(item["id"])),
				Name:  jsonutil.StringValue(item["name"]),
				Input: decodeToolArguments(arguments),
			})
		}
	}

	if !slices.ContainsFunc(blocks, func(block ResponseBlock) bool { return block.Type == "text" }) {
		if text := accumulator.Text(); text != "" {
			blocks = append(blocks, ResponseBlock{Type: "text", Text: text})
		}
	}
	return blocks
}

func shouldExposeThinking(accumulator *translate.Accumulator) bool {
	if accumulator.Normalized.Reasoning != nil && strings.TrimSpace(accumulator.Normalized.Reasoning.Summary) != "" {
		return true
	}
	return slices.Contains(accumulator.Normalized.Include, "reasoning.encrypted_content")
}

func reasoningText(item map[string]any) string {
	parts := make([]string, 0)
	for _, summary := range jsonutil.SliceOfMaps(item["summary"]) {
		if text := strings.TrimSpace(jsonutil.StringValue(summary["text"])); text != "" {
			parts = append(parts, text)
		}
	}
	if len(parts) == 0 {
		for _, content := range jsonutil.SliceOfMaps(item["content"]) {
			if text := strings.TrimSpace(jsonutil.StringValue(content["text"])); text != "" {
				parts = append(parts, text)
			}
		}
	}
	return strings.Join(parts, "\n")
}

func decodeToolArguments(arguments string) json.RawMessage {
	raw := bytes.TrimSpace([]byte(arguments))
	if len(raw) == 0 {
		return json.RawMessage(`{}`)
	}
	if json.Valid(raw) && raw[0] == '{' {
		return json.RawMessage(raw)
	}
	fallback, _ := json.Marshal(map[string]string{"input": arguments})
	return json.RawMessage(fallback)
}

func isExecutableToolCall(item, response map[string]any, accumulator *translate.Accumulator) bool {
	callID := cmp.Or(jsonutil.StringValue(item["call_id"]), jsonutil.StringValue(item["id"]))
	for _, call := range accumulator.ToolCalls {
		if call.CallID != callID && call.ItemID != callID {
			continue
		}
		return isExecutableToolState(call, response)
	}
	itemStatus := strings.ToLower(strings.TrimSpace(jsonutil.StringValue(item["status"])))
	if itemStatus != "" {
		return itemStatus == "completed"
	}
	responseStatus := strings.ToLower(strings.TrimSpace(jsonutil.StringValue(response["status"])))
	return responseStatus != "incomplete" && responseStatus != "failed" && responseStatus != "cancelled"
}

func isExecutableToolState(call *translate.ToolCallState, response map[string]any) bool {
	if call == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(call.Status)) {
	case "completed":
		return true
	case "incomplete", "failed", "cancelled":
		return false
	}
	responseStatus := strings.ToLower(strings.TrimSpace(jsonutil.StringValue(response["status"])))
	return responseStatus != "incomplete" && responseStatus != "failed" && responseStatus != "cancelled"
}

func usageFromAccumulator(accumulator *translate.Accumulator) Usage {
	if accumulator == nil || accumulator.Usage == nil {
		return Usage{}
	}
	input := accumulator.Usage.InputTokens
	cached := int64(0)
	if accumulator.Usage.CachedTokens != nil {
		cached = *accumulator.Usage.CachedTokens
		if cached > input {
			cached = input
		}
		input -= cached
	}
	return Usage{
		InputTokens:          input,
		OutputTokens:         accumulator.Usage.OutputTokens,
		CacheReadInputTokens: cached,
	}
}

func stopFromAccumulator(accumulator *translate.Accumulator) (string, string) {
	response := jsonutil.MapValue(accumulator.RawFinal, "response")
	if stopSequence := jsonutil.StringValue(response["stop_sequence"]); stopSequence != "" {
		return "stop_sequence", stopSequence
	}
	incomplete := jsonutil.MapValue(response, "incomplete_details")
	reason := cmp.Or(jsonutil.StringValue(incomplete["reason"]), jsonutil.StringValue(response["stop_reason"]))
	switch reason {
	case "max_output_tokens", "max_tokens", "context_length_exceeded":
		return "max_tokens", ""
	case "content_filter", "refusal":
		return "refusal", ""
	}
	if strings.EqualFold(cmp.Or(accumulator.Status, jsonutil.StringValue(response["status"])), "incomplete") {
		return "max_tokens", ""
	}
	if responseContainsRefusal(response) {
		return "refusal", ""
	}
	if len(accumulator.ToolCalls) > 0 {
		return "tool_use", ""
	}
	return "end_turn", ""
}

func responseContainsRefusal(response map[string]any) bool {
	for _, item := range jsonutil.SliceOfMaps(response["output"]) {
		for _, content := range jsonutil.SliceOfMaps(item["content"]) {
			if jsonutil.StringValue(content["type"]) == "refusal" {
				return true
			}
		}
	}
	return false
}
