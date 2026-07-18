package anthropic

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"reflect"
	"slices"
	"strings"

	"chatgpt-codex-proxy/internal/jsonutil"
	"chatgpt-codex-proxy/internal/turn"
	"github.com/santhosh-tekuri/jsonschema/v6"
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
	Type      string           `json:"type"`
	Text      string           `json:"text,omitempty"`
	Thinking  string           `json:"thinking,omitempty"`
	Signature string           `json:"signature,omitempty"`
	ID        string           `json:"id,omitempty"`
	Name      string           `json:"name,omitempty"`
	Input     json.RawMessage  `json:"input,omitempty"`
	ToolUseID string           `json:"tool_use_id,omitempty"`
	Content   []map[string]any `json:"content,omitempty"`
}

func (b ResponseBlock) MarshalJSON() ([]byte, error) {
	switch b.Type {
	case "thinking":
		return json.Marshal(struct {
			Type      string `json:"type"`
			Thinking  string `json:"thinking"`
			Signature string `json:"signature"`
		}{Type: b.Type, Thinking: b.Thinking, Signature: b.Signature})
	case "server_tool_use":
		return json.Marshal(struct {
			Type  string          `json:"type"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		}{Type: b.Type, ID: b.ID, Name: b.Name, Input: b.Input})
	case "web_search_tool_result":
		return json.Marshal(struct {
			Type      string           `json:"type"`
			ToolUseID string           `json:"tool_use_id"`
			Content   []map[string]any `json:"content"`
		}{Type: b.Type, ToolUseID: b.ToolUseID, Content: b.Content})
	}
	type alias ResponseBlock
	return json.Marshal(alias(b))
}

type Usage struct {
	InputTokens          int64 `json:"input_tokens"`
	OutputTokens         int64 `json:"output_tokens"`
	CacheReadInputTokens int64 `json:"cache_read_input_tokens,omitempty"`
}

func BuildMessage(accumulator *turn.Accumulator) MessageResponse {
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

func responseBlocks(accumulator *turn.Accumulator) []ResponseBlock {
	response := accumulator.ResponsesObject()
	output := jsonutil.SliceOfMaps(response["output"])
	blocks := make([]ResponseBlock, 0, len(output))
	exposeThinking := shouldExposeThinking(accumulator)
	webSearchSeen := make(map[string]bool)
	for index, item := range output {
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
					blocks = append(blocks, ResponseBlock{Type: "text", Text: normalizedOutputText(accumulator, jsonutil.StringValue(content["text"]))})
				case "refusal":
					blocks = append(blocks, ResponseBlock{Type: "text", Text: cmp.Or(jsonutil.StringValue(content["refusal"]), jsonutil.StringValue(content["text"]))})
				}
			}
		case "web_search_call":
			searchBlocks := webSearchResponseBlocks(item, fmt.Sprintf("web_search_%d", index))
			if len(searchBlocks) == 0 || webSearchSeen[searchBlocks[0].ID] {
				continue
			}
			webSearchSeen[searchBlocks[0].ID] = true
			blocks = append(blocks, searchBlocks...)
		case "function_call", "custom_tool_call":
			if !isExecutableToolCall(item, response, accumulator) {
				continue
			}
			arguments := cmp.Or(jsonutil.StringValue(item["arguments"]), jsonutil.StringValue(item["input"]))
			blocks = append(blocks, ResponseBlock{
				Type:  "tool_use",
				ID:    shortenCallID(cmp.Or(jsonutil.StringValue(item["call_id"]), jsonutil.StringValue(item["id"]))),
				Name:  jsonutil.StringValue(item["name"]),
				Input: decodeToolArguments(arguments),
			})
		}
	}

	if !slices.ContainsFunc(blocks, func(block ResponseBlock) bool { return block.Type == "text" }) {
		if text := accumulator.Text(); text != "" {
			blocks = append(blocks, ResponseBlock{Type: "text", Text: normalizedOutputText(accumulator, text)})
		}
	}
	return blocks
}

func webSearchResponseBlocks(item map[string]any, fallbackID string) []ResponseBlock {
	if status := strings.TrimSpace(jsonutil.StringValue(item["status"])); status != "" && status != "completed" {
		return nil
	}
	toolUseID := shortenCallID(jsonutil.FirstNonEmpty(
		jsonutil.StringValue(item["id"]),
		jsonutil.StringValue(item["output_item_id"]),
		jsonutil.StringValue(item["call_id"]),
		fallbackID,
	))
	if toolUseID == "" {
		return nil
	}

	input := map[string]any{}
	if query := webSearchQuery(item); query != "" {
		input["query"] = query
	}
	encodedInput, _ := json.Marshal(input)
	return []ResponseBlock{
		{Type: "server_tool_use", ID: toolUseID, Name: "web_search", Input: encodedInput},
		{Type: "web_search_tool_result", ToolUseID: toolUseID, Content: webSearchResultContent(item)},
	}
}

func webSearchQuery(item map[string]any) string {
	action := jsonutil.MapValue(item, "action")
	input := jsonutil.MapValue(item, "input")
	return jsonutil.FirstNonEmpty(
		jsonutil.StringValue(action["query"]),
		jsonutil.StringValue(item["query"]),
		jsonutil.StringValue(input["query"]),
	)
}

func webSearchResultContent(item map[string]any) []map[string]any {
	results := jsonutil.SliceOfMaps(item["results"])
	if len(results) == 0 {
		results = jsonutil.SliceOfMaps(jsonutil.MapValue(item, "action")["results"])
	}
	content := make([]map[string]any, 0, len(results))
	for _, result := range results {
		url := strings.TrimSpace(jsonutil.StringValue(result["url"]))
		if url == "" {
			continue
		}
		content = append(content, map[string]any{
			"type":     "web_search_result",
			"title":    jsonutil.FirstNonEmpty(jsonutil.StringValue(result["title"]), url),
			"url":      url,
			"page_age": nullableString(strings.TrimSpace(jsonutil.StringValue(result["page_age"]))),
		})
	}
	return content
}

func normalizedOutputText(accumulator *turn.Accumulator, text string) string {
	if accumulator == nil || len(accumulator.Normalized.ResponseSchema) == 0 || strings.TrimSpace(text) == "" {
		return text
	}
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return text
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return text
	}
	strictSchema := accumulator.Normalized.ResponseSchema
	if accumulator.Normalized.Text != nil {
		strictSchema = accumulator.Normalized.Text.Format.Schema
	}
	matcher := newSchemaMatcher()
	cleaned, changed := removeOptionalNullProperties(value, accumulator.Normalized.ResponseSchema, strictSchema, matcher)
	if !changed || !matcher.matches(accumulator.Normalized.ResponseSchema, cleaned) {
		return text
	}
	encoded, err := json.Marshal(cleaned)
	if err != nil {
		return text
	}
	return string(encoded)
}

func removeOptionalNullProperties(value any, schema, strictSchema map[string]any, matcher *schemaMatcher) (any, bool) {
	if matcher.matches(schema, value) {
		return value, false
	}
	strictSchema = unwrapSyntheticNullableSchema(schema, strictSchema, value, matcher)

	for _, key := range []string{"anyOf", "oneOf"} {
		children, _ := schema[key].([]any)
		strictChildren, _ := strictSchema[key].([]any)
		for index, raw := range children {
			child, ok := raw.(map[string]any)
			if !ok || index >= len(strictChildren) {
				continue
			}
			strictChild, ok := strictChildren[index].(map[string]any)
			if !ok || !matcher.matches(strictChild, value) {
				continue
			}
			if cleaned, changed := removeOptionalNullProperties(value, child, strictChild, matcher); changed && matcher.matches(child, cleaned) {
				return cleaned, true
			}
		}
	}

	if object, ok := value.(map[string]any); ok {
		properties, _ := schema["properties"].(map[string]any)
		strictProperties, _ := strictSchema["properties"].(map[string]any)
		required := schemaRequiredNames(schema["required"])
		cleaned := maps.Clone(object)
		changed := false
		for name, raw := range properties {
			child, ok := raw.(map[string]any)
			strictChild, strictOK := strictProperties[name].(map[string]any)
			if !ok || !strictOK {
				continue
			}
			propertyValue, exists := cleaned[name]
			if !exists {
				continue
			}
			if propertyValue == nil && !required[name] && !matcher.matches(child, nil) && matcher.matches(strictChild, nil) {
				delete(cleaned, name)
				changed = true
				continue
			}
			if childValue, childChanged := removeOptionalNullProperties(propertyValue, child, strictChild, matcher); childChanged {
				cleaned[name] = childValue
				changed = true
			}
		}
		if changed && matcher.matches(schema, cleaned) {
			return cleaned, true
		}
	}

	if array, ok := value.([]any); ok {
		items, itemsOK := schema["items"].(map[string]any)
		strictItems, strictOK := strictSchema["items"].(map[string]any)
		if itemsOK && strictOK {
			cleaned := slices.Clone(array)
			changed := false
			for index, item := range cleaned {
				if childValue, childChanged := removeOptionalNullProperties(item, items, strictItems, matcher); childChanged {
					cleaned[index] = childValue
					changed = true
				}
			}
			if changed && matcher.matches(schema, cleaned) {
				return cleaned, true
			}
		}
	}

	return value, false
}

func unwrapSyntheticNullableSchema(schema, strictSchema map[string]any, value any, matcher *schemaMatcher) map[string]any {
	if matcher.matches(schema, nil) || !matcher.matches(strictSchema, nil) {
		return strictSchema
	}
	children, _ := strictSchema["anyOf"].([]any)
	if len(children) != 2 {
		return strictSchema
	}
	nullable, nullableOK := children[1].(map[string]any)
	candidate, candidateOK := children[0].(map[string]any)
	if !nullableOK || len(nullable) != 1 || nullable["type"] != "null" || !candidateOK || !matcher.matches(candidate, value) {
		return strictSchema
	}
	return candidate
}

type schemaMatcher struct {
	compiled map[uintptr]*jsonschema.Schema
}

func newSchemaMatcher() *schemaMatcher {
	return &schemaMatcher{compiled: make(map[uintptr]*jsonschema.Schema)}
}

func (m *schemaMatcher) matches(schema map[string]any, value any) bool {
	key := reflect.ValueOf(schema).Pointer()
	compiled, cached := m.compiled[key]
	if !cached {
		compiled = compileSchema(schema)
		m.compiled[key] = compiled
	}
	return compiled != nil && compiled.Validate(value) == nil
}

func compileSchema(schema map[string]any) *jsonschema.Schema {
	encoded, err := json.Marshal(schema)
	if err != nil {
		return nil
	}
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(encoded))
	if err != nil {
		return nil
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.AssertFormat()
	if err := compiler.AddResource("schema.json", document); err != nil {
		return nil
	}
	compiled, err := compiler.Compile("schema.json")
	if err != nil {
		return nil
	}
	return compiled
}

func shouldExposeThinking(accumulator *turn.Accumulator) bool {
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

func isExecutableToolCall(item, response map[string]any, accumulator *turn.Accumulator) bool {
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

func isExecutableToolState(call *turn.ToolCallState, response map[string]any) bool {
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

func usageFromAccumulator(accumulator *turn.Accumulator) Usage {
	if accumulator == nil || accumulator.Usage == nil {
		return Usage{}
	}
	input := accumulator.Usage.InputTokens
	cached := int64(0)
	if accumulator.Usage.CachedTokens != nil {
		cached = min(*accumulator.Usage.CachedTokens, input)
		input -= cached
	}
	return Usage{
		InputTokens:          input,
		OutputTokens:         accumulator.Usage.OutputTokens,
		CacheReadInputTokens: cached,
	}
}

func stopFromAccumulator(accumulator *turn.Accumulator) (string, string) {
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
