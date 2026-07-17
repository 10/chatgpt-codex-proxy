package anthropic

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode"

	"chatgpt-codex-proxy/internal/codex"
	"chatgpt-codex-proxy/internal/jsonutil"
	"chatgpt-codex-proxy/internal/models"
	"chatgpt-codex-proxy/internal/openai"
	"chatgpt-codex-proxy/internal/turn"
)

const defaultInstructions = "You are a helpful assistant."
const claudeCodeAttributionSystemPrefix = "x-anthropic-billing-header:"

func Normalize(request MessagesRequest, catalog *models.Catalog) (turn.NormalizedRequest, error) {
	model := strings.TrimSpace(request.Model)
	if model == "" {
		return turn.NormalizedRequest{}, errors.New("model is required")
	}
	if catalog != nil && !catalog.Has(model) {
		return turn.NormalizedRequest{}, &openai.ModelNotFoundError{Model: model}
	}
	if request.MaxTokens == nil {
		return turn.NormalizedRequest{}, errors.New("max_tokens is required")
	}
	if *request.MaxTokens < 0 {
		return turn.NormalizedRequest{}, errors.New("max_tokens must be greater than or equal to 0")
	}
	if len(request.Messages) == 0 {
		return turn.NormalizedRequest{}, errors.New("messages must contain at least one message")
	}

	toolNames := turn.NewToolNames(requestToolNames(request))
	tools, err := normalizeTools(request.Tools, toolNames)
	if err != nil {
		return turn.NormalizedRequest{}, err
	}
	toolChoice, parallelToolCalls, err := normalizeToolChoice(request.ToolChoice, toolNames, request.Tools)
	if err != nil {
		return turn.NormalizedRequest{}, err
	}
	instructions, err := normalizeSystem(request.System)
	if err != nil {
		return turn.NormalizedRequest{}, err
	}
	input, err := normalizeMessages(request.Messages, toolNames)
	if err != nil {
		return turn.NormalizedRequest{}, err
	}
	reasoning, include, err := normalizeThinking(request, catalog)
	if err != nil {
		return turn.NormalizedRequest{}, err
	}
	text, responseSchema, err := normalizeOutputFormat(request.OutputConfig)
	if err != nil {
		return turn.NormalizedRequest{}, err
	}
	serviceTier, err := normalizeServiceTier(request.ServiceTier)
	if err != nil {
		return turn.NormalizedRequest{}, err
	}

	normalized := turn.NormalizedRequest{
		ModelExplicit:   true,
		ResponseSchema:  responseSchema,
		ToolNameAliases: toolNames.Aliases(),
		Request: codex.Request{
			Model:             model,
			Instructions:      cmp.Or(strings.TrimSpace(instructions), defaultInstructions),
			Input:             input,
			Stream:            request.Stream,
			Tools:             tools,
			ToolChoice:        toolChoice,
			Text:              text,
			Reasoning:         reasoning,
			ServiceTier:       serviceTier,
			Include:           include,
			ParallelToolCalls: parallelToolCalls,
		},
	}
	if *request.MaxTokens == 0 {
		generate := false
		normalized.Generate = &generate
	}
	return normalized, nil
}

func normalizeSystem(content Content) (string, error) {
	parts := make([]string, 0, len(content))
	for _, block := range content {
		if block.Type != "text" {
			return "", errors.New("system only supports text blocks")
		}
		if text := strings.TrimSpace(block.Text); text != "" && !isClaudeCodeAttributionSystemText(block.Text) {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n"), nil
}

func normalizeMessages(messages []Message, toolNames *turn.ToolNames) ([]codex.InputItem, error) {
	knownCalls := make(map[string]struct{})
	knownSearches := make(map[string]struct{})
	result := make([]codex.InputItem, 0, len(messages))
	for _, message := range messages {
		role := strings.TrimSpace(message.Role)
		if role == "system" {
			if reminder := messageSystemReminderText(message.Content); reminder != "" {
				result = append(result, codex.InputItem{
					Role: "user",
					Content: []codex.ContentPart{{
						Type: "input_text",
						Text: reminder,
					}},
				})
			}
			continue
		}
		if role != "user" && role != "assistant" {
			return nil, errors.New("role must be user or assistant")
		}
		var content []codex.ContentPart
		flushContent := func() {
			if len(content) == 0 {
				return
			}
			result = append(result, codex.InputItem{Role: role, Content: content})
			content = nil
		}

		for _, block := range message.Content {
			switch block.Type {
			case "text":
				partType := "input_text"
				if role == "assistant" {
					partType = "output_text"
				}
				content = append(content, codex.ContentPart{Type: partType, Text: block.Text})
			case "image":
				if role != "user" {
					return nil, errors.New("image blocks are only supported in user messages")
				}
				imageURL, err := normalizeImageSource(block.Source)
				if err != nil {
					return nil, err
				}
				content = append(content, codex.ContentPart{Type: "input_image", ImageURL: imageURL})
			case "tool_use":
				if role != "assistant" {
					return nil, errors.New("tool_use blocks require the assistant role")
				}
				flushContent()
				callID := strings.TrimSpace(block.ID)
				if callID == "" || strings.TrimSpace(block.Name) == "" {
					return nil, errors.New("tool_use requires id and name")
				}
				arguments, err := normalizeToolInput(block.Input)
				if err != nil {
					return nil, err
				}
				knownCalls[callID] = struct{}{}
				result = append(result, codex.InputItem{Type: "function_call", CallID: shortenCallID(callID), Name: toolNames.Shorten(block.Name), Arguments: arguments})
			case "tool_result":
				if role != "user" {
					return nil, errors.New("tool_result blocks require the user role")
				}
				flushContent()
				callID := strings.TrimSpace(block.ToolUseID)
				if callID == "" {
					return nil, errors.New("tool_use_id is required")
				}
				if _, ok := knownCalls[callID]; !ok {
					return nil, errors.New("tool_result references an unknown tool_use id")
				}
				text, output, err := normalizeToolResult(block.Content)
				if err != nil {
					return nil, err
				}
				if block.IsError {
					if text != "" {
						text = "Tool error: " + text
					} else {
						output = append([]codex.ContentPart{{Type: "input_text", Text: "Tool error"}}, output...)
					}
				}
				result = append(result, codex.InputItem{Type: "function_call_output", CallID: shortenCallID(callID), OutputText: text, OutputContent: output})
			case "server_tool_use":
				if role != "assistant" {
					return nil, errors.New("server_tool_use blocks require the assistant role")
				}
				flushContent()
				callID := strings.TrimSpace(block.ID)
				if callID == "" || strings.TrimSpace(block.Name) != "web_search" {
					return nil, errors.New("server_tool_use requires an id and the web_search name")
				}
				if _, err := normalizeToolInput(block.Input); err != nil {
					return nil, err
				}
				knownSearches[callID] = struct{}{}
				result = append(result, codex.InputItem{Type: "web_search_call", ID: shortenCallID(callID), Status: "completed"})
			case "web_search_tool_result":
				if role != "assistant" {
					return nil, errors.New("web_search_tool_result blocks require the assistant role")
				}
				callID := strings.TrimSpace(block.ToolUseID)
				if callID == "" {
					return nil, errors.New("tool_use_id is required")
				}
				if _, ok := knownSearches[callID]; !ok {
					return nil, errors.New("web_search_tool_result references an unknown server_tool_use id")
				}
			case "thinking", "redacted_thinking":
				if role != "assistant" {
					return nil, errors.New(block.Type + " blocks require the assistant role")
				}
				encrypted := strings.TrimSpace(cmp.Or(block.Signature, block.Data))
				if !IsValidCodexReasoningSignature(encrypted) {
					continue
				}
				flushContent()
				item := codex.InputItem{Type: "reasoning", EncryptedContent: encrypted}
				if text := strings.TrimSpace(block.Thinking); text != "" {
					item.Summary = []turn.ReasoningPart{{Type: "summary_text", Text: text}}
				}
				result = append(result, item)
			default:
				return nil, fmt.Errorf("unsupported content block type %q", block.Type)
			}
		}
		flushContent()
	}
	return result, nil
}

func messageSystemReminderText(content Content) string {
	parts := make([]string, 0, len(content))
	for _, block := range content {
		if block.Type != "text" || block.Text == "" || isClaudeCodeAttributionSystemText(block.Text) {
			continue
		}
		parts = append(parts, block.Text)
	}
	if len(parts) == 0 {
		return ""
	}
	text := strings.Join(parts, "\n")
	if strings.TrimSpace(text) == "" {
		return ""
	}
	return "<system-reminder>\n" + text + "\n</system-reminder>"
}

func isClaudeCodeAttributionSystemText(text string) bool {
	text = strings.TrimLeftFunc(text, unicode.IsSpace)
	return strings.HasPrefix(text, claudeCodeAttributionSystemPrefix)
}

func shortenCallID(id string) string {
	const limit = 64
	if len(id) <= limit {
		return id
	}

	sum := sha256.Sum256([]byte(id))
	suffix := "_" + hex.EncodeToString(sum[:8])
	return id[:limit-len(suffix)] + suffix
}

func normalizeImageSource(source *ImageSource) (string, error) {
	if source == nil {
		return "", fmt.Errorf("source is required")
	}
	switch strings.TrimSpace(source.Type) {
	case "base64":
		mediaType := strings.TrimSpace(source.MediaType)
		if !strings.HasPrefix(mediaType, "image/") || strings.TrimSpace(source.Data) == "" {
			return "", fmt.Errorf("base64 image source requires media_type and data")
		}
		return "data:" + mediaType + ";base64," + strings.TrimSpace(source.Data), nil
	case "url":
		if strings.TrimSpace(source.URL) == "" {
			return "", fmt.Errorf("URL image source requires url")
		}
		return strings.TrimSpace(source.URL), nil
	default:
		return "", fmt.Errorf("unsupported image source type %q", source.Type)
	}
}

func normalizeToolInput(input json.RawMessage) (string, error) {
	trimmed := bytes.TrimSpace(input)
	if len(trimmed) == 0 {
		return "{}", nil
	}
	if !json.Valid(trimmed) || trimmed[0] != '{' {
		return "", fmt.Errorf("tool input must be a JSON object")
	}
	return string(trimmed), nil
}

func normalizeToolResult(content Content) (string, []codex.ContentPart, error) {
	parts := make([]codex.ContentPart, 0, len(content))
	allText := true
	texts := make([]string, 0, len(content))
	for _, block := range content {
		switch block.Type {
		case "text":
			parts = append(parts, codex.ContentPart{Type: "input_text", Text: block.Text})
			texts = append(texts, block.Text)
		case "image":
			allText = false
			imageURL, err := normalizeImageSource(block.Source)
			if err != nil {
				return "", nil, err
			}
			parts = append(parts, codex.ContentPart{Type: "input_image", ImageURL: imageURL})
		default:
			return "", nil, fmt.Errorf("unsupported tool result block type %q", block.Type)
		}
	}
	if allText {
		return strings.Join(texts, "\n"), nil, nil
	}
	return "", parts, nil
}

func normalizeThinking(request MessagesRequest, catalog *models.Catalog) (*codex.Reasoning, []string, error) {
	typeName := ""
	if request.Thinking != nil {
		typeName = strings.TrimSpace(request.Thinking.Type)
	}
	effort := ""
	if request.OutputConfig != nil {
		effort = strings.TrimSpace(request.OutputConfig.Effort)
	}
	if typeName != "" && typeName != "enabled" && typeName != "adaptive" && typeName != "disabled" {
		return nil, nil, fmt.Errorf("unsupported thinking type %q", typeName)
	}
	if typeName == "" && effort == "" {
		return nil, nil, nil
	}
	thinkingEnabled := typeName == "enabled" || typeName == "adaptive"
	if effort == "" && thinkingEnabled && catalog != nil {
		if entry, ok := catalog.Get(strings.TrimSpace(request.Model)); ok {
			effort = entry.DefaultReasoningEffort
		}
	}
	if effort == "" && !thinkingEnabled {
		return nil, nil, nil
	}
	if effort != "" && catalog != nil {
		if entry, ok := catalog.Get(strings.TrimSpace(request.Model)); ok && !slices.ContainsFunc(entry.SupportedReasoningEfforts, func(candidate models.ReasoningEffort) bool {
			return candidate.ReasoningEffort == effort
		}) {
			return nil, nil, fmt.Errorf("unsupported reasoning effort %q for model %q", effort, request.Model)
		}
	}
	reasoning := &codex.Reasoning{Effort: effort}
	if !thinkingEnabled {
		return reasoning, nil, nil
	}
	reasoning.Summary = "auto"
	return reasoning, []string{"reasoning.encrypted_content"}, nil
}

func normalizeOutputFormat(config *OutputConfig) (*codex.TextConfig, map[string]any, error) {
	if config == nil || config.Format == nil {
		return nil, nil, nil
	}
	format := config.Format
	if strings.TrimSpace(format.Type) != "json_schema" {
		return nil, nil, fmt.Errorf("unsupported output format %q", format.Type)
	}
	if len(format.Schema) == 0 {
		return nil, nil, errors.New("schema is required")
	}
	responseSchema := openai.NormalizeSchema(format.Schema)
	strictSchema := jsonutil.CloneMap(responseSchema)
	if err := makeOpenAIStrictSchema(strictSchema); err != nil {
		return nil, nil, err
	}
	if compileSchema(responseSchema) == nil {
		return nil, nil, errors.New("output schema is invalid")
	}
	return &codex.TextConfig{Format: codex.TextFormat{
		Type:   "json_schema",
		Name:   cmp.Or(strings.TrimSpace(format.Name), "anthropic_output"),
		Schema: strictSchema,
		Strict: true,
	}}, responseSchema, nil
}

func makeOpenAIStrictSchema(node map[string]any) error {
	if node["type"] != "object" {
		return errors.New("output schema root must have type object")
	}
	if _, ok := node["anyOf"]; ok {
		return errors.New(`output schema keyword "anyOf" is not supported at the root`)
	}
	return makeOpenAIStrictSchemaNode(node, newSchemaMatcher())
}

func makeOpenAIStrictSchemaNode(node map[string]any, matcher *schemaMatcher) error {
	for _, keyword := range slices.Sorted(maps.Keys(node)) {
		if !supportedOutputSchemaKeyword(keyword) {
			return fmt.Errorf("output schema keyword %q is not supported", keyword)
		}
	}
	if err := validateOutputSchemaKeywordValues(node); err != nil {
		return err
	}
	if err := visitSchemaChildren(node, func(child map[string]any) error {
		return makeOpenAIStrictSchemaNode(child, matcher)
	}); err != nil {
		return err
	}
	properties, hasProperties := node["properties"].(map[string]any)
	hasObjectType := schemaHasType(node["type"], "object")
	if !hasProperties && !hasObjectType {
		return nil
	}
	if !hasObjectType {
		return errors.New("output schema nodes with properties must include type object")
	}
	if !hasProperties {
		properties = map[string]any{}
		node["properties"] = properties
	}
	if additionalProperties, ok := node["additionalProperties"]; ok && additionalProperties != false {
		return errors.New("output schema object nodes must set additionalProperties to false")
	}
	node["additionalProperties"] = false
	required := schemaRequiredNames(node["required"])
	for name := range required {
		if _, ok := properties[name]; !ok {
			return fmt.Errorf("output schema required property %q must be declared in properties", name)
		}
	}
	for name, raw := range properties {
		child, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("output schema property %q must use an object schema", name)
		}
		if !required[name] {
			properties[name] = makeSchemaNullable(child, matcher)
		}
		required[name] = true
	}
	requiredNames := slices.Sorted(maps.Keys(required))
	if requiredNames == nil {
		requiredNames = []string{}
	}
	node["required"] = requiredNames
	return nil
}

func supportedOutputSchemaKeyword(keyword string) bool {
	switch keyword {
	case
		"type",
		"properties",
		"required",
		"additionalProperties",
		"items",
		"enum",
		"const",
		"anyOf",
		"pattern",
		"format",
		"multipleOf",
		"maximum",
		"exclusiveMaximum",
		"minimum",
		"exclusiveMinimum",
		"minItems",
		"maxItems",
		"title",
		"description":
		return true
	default:
		return false
	}
}

func validateOutputSchemaKeywordValues(node map[string]any) error {
	if schemaHasType(node["type"], "array") {
		if _, ok := node["items"]; !ok {
			return errors.New("output schema array nodes must define items")
		}
	}
	if schemaHasType(node["type"], "object") {
		if _, ok := node["anyOf"]; ok {
			return errors.New(`output schema keyword "anyOf" cannot be combined with type object`)
		}
	}
	if rawFormat, ok := node["format"]; ok {
		format, ok := rawFormat.(string)
		if !ok || !supportedOutputSchemaFormat(format) {
			return fmt.Errorf("output schema format %q is not supported", format)
		}
	}
	for _, keyword := range []string{"pattern", "format"} {
		if _, ok := node[keyword]; ok && !schemaHasType(node["type"], "string") {
			return fmt.Errorf("output schema keyword %q requires type string", keyword)
		}
	}
	for _, keyword := range []string{"multipleOf", "maximum", "exclusiveMaximum", "minimum", "exclusiveMinimum"} {
		if _, ok := node[keyword]; ok && !schemaHasType(node["type"], "number") && !schemaHasType(node["type"], "integer") {
			return fmt.Errorf("output schema keyword %q requires a numeric type", keyword)
		}
	}
	for _, keyword := range []string{"items", "minItems", "maxItems"} {
		if _, ok := node[keyword]; ok && !schemaHasType(node["type"], "array") {
			return fmt.Errorf("output schema keyword %q requires type array", keyword)
		}
	}
	for _, keyword := range []string{"properties", "required", "additionalProperties"} {
		if _, ok := node[keyword]; ok && !schemaHasType(node["type"], "object") {
			return fmt.Errorf("output schema keyword %q requires type object", keyword)
		}
	}
	return nil
}

func supportedOutputSchemaFormat(format string) bool {
	switch format {
	case "date-time", "time", "date", "duration", "email", "hostname", "ipv4", "ipv6", "uuid":
		return true
	default:
		return false
	}
}

func visitSchemaChildren(node map[string]any, visit func(map[string]any) error) error {
	properties, _ := node["properties"].(map[string]any)
	for _, raw := range properties {
		if child, ok := raw.(map[string]any); ok {
			if err := visit(child); err != nil {
				return err
			}
		}
	}
	if raw, exists := node["items"]; exists {
		child, ok := raw.(map[string]any)
		if !ok {
			return errors.New(`output schema "items" must use an object schema`)
		}
		if err := visit(child); err != nil {
			return err
		}
	}
	if rawChildren, exists := node["anyOf"]; exists {
		children, ok := rawChildren.([]any)
		if !ok {
			return errors.New(`output schema "anyOf" must be an array`)
		}
		if len(children) == 0 {
			return errors.New(`output schema "anyOf" must contain at least one schema`)
		}
		for _, raw := range children {
			child, ok := raw.(map[string]any)
			if !ok {
				return errors.New(`output schema "anyOf" entries must use object schemas`)
			}
			if err := visit(child); err != nil {
				return err
			}
		}
	}
	return nil
}

func schemaRequiredNames(raw any) map[string]bool {
	required := make(map[string]bool)
	switch values := raw.(type) {
	case []any:
		for _, value := range values {
			if name, ok := value.(string); ok {
				required[name] = true
			}
		}
	case []string:
		for _, name := range values {
			required[name] = true
		}
	}
	return required
}

func makeSchemaNullable(schema map[string]any, matcher *schemaMatcher) map[string]any {
	if matcher.matches(schema, nil) {
		return schema
	}
	return map[string]any{"anyOf": []any{schema, map[string]any{"type": "null"}}}
}

func schemaHasType(raw any, want string) bool {
	switch schemaType := raw.(type) {
	case string:
		return schemaType == want
	case []any:
		return slices.Contains(schemaType, any(want))
	case []string:
		return slices.Contains(schemaType, want)
	}
	return false
}

func normalizeServiceTier(raw string) (string, error) {
	switch strings.TrimSpace(raw) {
	case "":
		return "", nil
	case "auto", "standard_only":
		return "default", nil
	case "fast", "priority":
		return "priority", nil
	default:
		return "", fmt.Errorf("unsupported service tier %q", raw)
	}
}

func requestToolNames(request MessagesRequest) []string {
	names := make([]string, 0, len(request.Tools))
	for _, tool := range request.Tools {
		if strings.TrimSpace(tool.Name) != "" {
			names = append(names, tool.Name)
		}
	}
	if request.ToolChoice != nil && strings.TrimSpace(request.ToolChoice.Name) != "" {
		names = append(names, request.ToolChoice.Name)
	}
	for _, message := range request.Messages {
		for _, block := range message.Content {
			if block.Type == "tool_use" && strings.TrimSpace(block.Name) != "" {
				names = append(names, block.Name)
			}
		}
	}
	return names
}
