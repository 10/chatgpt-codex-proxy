package anthropic

import (
	"bytes"
	"encoding/json"
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
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens,omitempty"`
}

func BuildMessage(accumulator *translate.Accumulator) MessageResponse {
	response := MessageResponse{
		ID:      MessageID(accumulator.ResponseID),
		Type:    "message",
		Role:    "assistant",
		Model:   firstNonEmpty(accumulator.Model, accumulator.Normalized.Model),
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
	if strings.HasPrefix(responseID, "resp_") {
		return "msg_" + strings.TrimPrefix(responseID, "resp_")
	}
	if responseID == "" {
		return "msg_proxy"
	}
	if strings.HasPrefix(responseID, "msg_") {
		return responseID
	}
	return "msg_" + responseID
}

func responseBlocks(accumulator *translate.Accumulator) []ResponseBlock {
	if accumulator == nil {
		return []ResponseBlock{}
	}
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
					blocks = append(blocks, ResponseBlock{Type: "text", Text: firstNonEmpty(jsonutil.StringValue(content["refusal"]), jsonutil.StringValue(content["text"]))})
				}
			}
		case "function_call", "custom_tool_call":
			if !isExecutableToolCall(item, response, accumulator) {
				continue
			}
			arguments := firstNonEmpty(jsonutil.StringValue(item["arguments"]), jsonutil.StringValue(item["input"]))
			blocks = append(blocks, ResponseBlock{
				Type:  "tool_use",
				ID:    firstNonEmpty(jsonutil.StringValue(item["call_id"]), jsonutil.StringValue(item["id"])),
				Name:  jsonutil.StringValue(item["name"]),
				Input: decodeToolArguments(arguments),
			})
		}
	}

	if !hasBlockType(blocks, "text") {
		if text := accumulator.Text(); text != "" {
			blocks = append(blocks, ResponseBlock{Type: "text", Text: text})
		}
	}
	return blocks
}

func shouldExposeThinking(accumulator *translate.Accumulator) bool {
	if accumulator == nil {
		return false
	}
	if accumulator.Normalized.Reasoning != nil && strings.TrimSpace(accumulator.Normalized.Reasoning.Summary) != "" {
		return true
	}
	for _, include := range accumulator.Normalized.Include {
		if include == "reasoning.encrypted_content" {
			return true
		}
	}
	return false
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
		return json.RawMessage(bytes.Clone(raw))
	}
	fallback, _ := json.Marshal(struct {
		Input string `json:"input"`
	}{Input: arguments})
	return json.RawMessage(fallback)
}

func isExecutableToolCall(item, response map[string]any, accumulator *translate.Accumulator) bool {
	callID := firstNonEmpty(jsonutil.StringValue(item["call_id"]), jsonutil.StringValue(item["id"]))
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

func hasBlockType(blocks []ResponseBlock, blockType string) bool {
	for _, block := range blocks {
		if block.Type == blockType {
			return true
		}
	}
	return false
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
	if accumulator == nil {
		return "end_turn", ""
	}
	response := jsonutil.MapValue(accumulator.RawFinal, "response")
	if stopSequence := jsonutil.StringValue(response["stop_sequence"]); stopSequence != "" {
		return "stop_sequence", stopSequence
	}
	incomplete := jsonutil.MapValue(response, "incomplete_details")
	reason := firstNonEmpty(jsonutil.StringValue(incomplete["reason"]), jsonutil.StringValue(response["stop_reason"]))
	switch reason {
	case "max_output_tokens", "max_tokens", "context_length_exceeded":
		return "max_tokens", ""
	case "content_filter", "refusal":
		return "refusal", ""
	}
	if strings.EqualFold(firstNonEmpty(accumulator.Status, jsonutil.StringValue(response["status"])), "incomplete") {
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
