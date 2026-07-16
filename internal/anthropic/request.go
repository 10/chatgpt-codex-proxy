package anthropic

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"chatgpt-codex-proxy/internal/codex"
	"chatgpt-codex-proxy/internal/generation"
	"chatgpt-codex-proxy/internal/models"
	"chatgpt-codex-proxy/internal/translate"
)

const defaultInstructions = "You are a helpful assistant."

func DecodeMessages(data []byte) (MessagesRequest, error) {
	var request MessagesRequest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return MessagesRequest{}, invalid("", err.Error())
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return MessagesRequest{}, invalid("", "request body must contain one JSON object")
		}
		return MessagesRequest{}, invalid("", err.Error())
	}
	return request, nil
}

func Normalize(request MessagesRequest, catalog *models.Catalog) (translate.NormalizedRequest, error) {
	model := strings.TrimSpace(request.Model)
	if model == "" {
		return translate.NormalizedRequest{}, invalid("model", "model is required")
	}
	if catalog != nil && !catalog.Has(model) {
		return translate.NormalizedRequest{}, &translate.ModelNotFoundError{Model: model}
	}
	if request.MaxTokens == nil {
		return translate.NormalizedRequest{}, invalid("max_tokens", "max_tokens is required")
	}
	if *request.MaxTokens < 0 {
		return translate.NormalizedRequest{}, invalid("max_tokens", "max_tokens must be greater than or equal to 0")
	}
	if len(request.Messages) == 0 {
		return translate.NormalizedRequest{}, invalid("messages", "messages must contain at least one message")
	}

	toolNames := generation.NewToolNames(requestToolNames(request))
	tools, err := normalizeTools(request.Tools, toolNames)
	if err != nil {
		return translate.NormalizedRequest{}, err
	}
	toolChoice, parallelToolCalls, err := normalizeToolChoice(request.ToolChoice, toolNames)
	if err != nil {
		return translate.NormalizedRequest{}, err
	}
	instructions, err := normalizeSystem(request.System)
	if err != nil {
		return translate.NormalizedRequest{}, err
	}
	input, err := normalizeMessages(request.Messages, toolNames)
	if err != nil {
		return translate.NormalizedRequest{}, err
	}
	reasoning, include, err := normalizeThinking(request, catalog)
	if err != nil {
		return translate.NormalizedRequest{}, err
	}
	text, err := normalizeOutputFormat(request.OutputConfig)
	if err != nil {
		return translate.NormalizedRequest{}, err
	}
	serviceTier, err := normalizeServiceTier(request.ServiceTier)
	if err != nil {
		return translate.NormalizedRequest{}, err
	}

	normalized := translate.NormalizedRequest{
		ModelExplicit:       true,
		DisableContinuation: true,
		ToolNameAliases:     toolNames.Aliases(),
		Request: codex.Request{
			Model:             model,
			Instructions:      firstNonEmpty(strings.TrimSpace(instructions), defaultInstructions),
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

func NormalizeTokenCount(request MessagesRequest, catalog *models.Catalog) (translate.NormalizedRequest, error) {
	if request.MaxTokens == nil {
		zero := 0
		request.MaxTokens = &zero
	}
	normalized, err := Normalize(request, catalog)
	if err != nil {
		return translate.NormalizedRequest{}, err
	}
	normalized.Generate = nil
	normalized.Stream = false
	return normalized, nil
}

func normalizeSystem(content Content) (string, error) {
	parts := make([]string, 0, len(content))
	for index, block := range content {
		if block.Type != "text" {
			return "", invalid(fmt.Sprintf("system.%d.type", index), "system only supports text blocks")
		}
		if text := strings.TrimSpace(block.Text); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n"), nil
}

func normalizeMessages(messages []Message, toolNames *generation.ToolNames) ([]codex.InputItem, error) {
	knownCalls := make(map[string]struct{})
	result := make([]codex.InputItem, 0, len(messages))
	for messageIndex, message := range messages {
		role := strings.TrimSpace(message.Role)
		if role != "user" && role != "assistant" {
			return nil, invalid(fmt.Sprintf("messages.%d.role", messageIndex), "role must be user or assistant")
		}
		var content []codex.ContentPart
		flushContent := func() {
			if len(content) == 0 {
				return
			}
			result = append(result, codex.InputItem{Role: role, Content: content})
			content = nil
		}

		for blockIndex, block := range message.Content {
			field := fmt.Sprintf("messages.%d.content.%d", messageIndex, blockIndex)
			switch block.Type {
			case "text":
				partType := "input_text"
				if role == "assistant" {
					partType = "output_text"
				}
				content = append(content, codex.ContentPart{Type: partType, Text: block.Text})
			case "image":
				if role != "user" {
					return nil, invalid(field, "image blocks are only supported in user messages")
				}
				imageURL, err := normalizeImageSource(block.Source)
				if err != nil {
					return nil, invalid(field+".source", err.Error())
				}
				content = append(content, codex.ContentPart{Type: "input_image", ImageURL: imageURL})
			case "tool_use":
				if role != "assistant" {
					return nil, invalid(field, "tool_use blocks require the assistant role")
				}
				flushContent()
				callID := strings.TrimSpace(block.ID)
				if callID == "" || strings.TrimSpace(block.Name) == "" {
					return nil, invalid(field, "tool_use requires id and name")
				}
				arguments, err := normalizeToolInput(block.Input)
				if err != nil {
					return nil, invalid(field+".input", err.Error())
				}
				knownCalls[callID] = struct{}{}
				result = append(result, codex.InputItem{Type: "function_call", CallID: callID, Name: toolNames.Shorten(block.Name), Arguments: arguments})
			case "tool_result":
				if role != "user" {
					return nil, invalid(field, "tool_result blocks require the user role")
				}
				flushContent()
				callID := strings.TrimSpace(block.ToolUseID)
				if callID == "" {
					return nil, invalid(field+".tool_use_id", "tool_use_id is required")
				}
				if _, ok := knownCalls[callID]; !ok {
					return nil, invalid(field+".tool_use_id", "tool_result references an unknown tool_use id")
				}
				text, output, err := normalizeToolResult(block.Content)
				if err != nil {
					return nil, invalid(field+".content", err.Error())
				}
				if block.IsError {
					if text != "" {
						text = "Tool error: " + text
					} else {
						output = append([]codex.ContentPart{{Type: "input_text", Text: "Tool error"}}, output...)
					}
				}
				result = append(result, codex.InputItem{Type: "function_call_output", CallID: callID, OutputText: text, OutputContent: output})
			case "thinking", "redacted_thinking":
				if role != "assistant" {
					return nil, invalid(field, block.Type+" blocks require the assistant role")
				}
				flushContent()
				encrypted := strings.TrimSpace(firstNonEmpty(block.Signature, block.Data))
				item := codex.InputItem{Type: "reasoning", EncryptedContent: encrypted}
				if text := strings.TrimSpace(block.Thinking); text != "" {
					item.Summary = []generation.ReasoningPart{{Type: "summary_text", Text: text}}
				}
				result = append(result, item)
			default:
				return nil, invalid(field+".type", fmt.Sprintf("unsupported content block type %q", block.Type))
			}
		}
		flushContent()
	}
	return result, nil
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

func normalizeTools(tools []Tool, names *generation.ToolNames) ([]codex.Tool, error) {
	result := make([]codex.Tool, 0, len(tools))
	for index, tool := range tools {
		toolType := strings.TrimSpace(tool.Type)
		if strings.HasPrefix(toolType, "web_search") {
			return nil, invalid(fmt.Sprintf("tools.%d.type", index), "hosted web search is not supported by the Anthropic adapter")
		}
		if toolType != "" && toolType != "custom" {
			return nil, invalid(fmt.Sprintf("tools.%d.type", index), fmt.Sprintf("unsupported tool type %q", toolType))
		}
		if strings.TrimSpace(tool.Name) == "" {
			return nil, invalid(fmt.Sprintf("tools.%d.name", index), "tool name is required")
		}
		if len(tool.InputSchema) == 0 {
			return nil, invalid(fmt.Sprintf("tools.%d.input_schema", index), "input_schema is required")
		}
		result = append(result, codex.Tool{
			Type:        "function",
			Name:        names.Shorten(tool.Name),
			Description: tool.Description,
			Parameters:  translate.NormalizeSchema(tool.InputSchema),
		})
	}
	return result, nil
}

func normalizeToolChoice(choice *ToolChoice, names *generation.ToolNames) (json.RawMessage, *bool, error) {
	if choice == nil {
		return nil, nil, nil
	}
	var encoded json.RawMessage
	switch strings.TrimSpace(choice.Type) {
	case "", "auto":
		encoded = json.RawMessage(`"auto"`)
	case "any":
		encoded = json.RawMessage(`"required"`)
	case "none":
		encoded = json.RawMessage(`"none"`)
	case "tool":
		if strings.TrimSpace(choice.Name) == "" {
			return nil, nil, invalid("tool_choice.name", "name is required for tool choice type tool")
		}
		selection := map[string]any{"type": "function", "name": names.Shorten(choice.Name)}
		value, _ := json.Marshal(selection)
		encoded = value
	default:
		return nil, nil, invalid("tool_choice.type", fmt.Sprintf("unsupported tool choice type %q", choice.Type))
	}
	var parallel *bool
	if choice.DisableParallelToolUse != nil {
		value := !*choice.DisableParallelToolUse
		parallel = &value
	}
	return encoded, parallel, nil
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
		return nil, nil, invalid("thinking.type", fmt.Sprintf("unsupported thinking type %q", typeName))
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
			return nil, nil, invalid("output_config.effort", fmt.Sprintf("unsupported reasoning effort %q for model %q", effort, request.Model))
		}
	}
	reasoning := &codex.Reasoning{Effort: effort}
	if !thinkingEnabled {
		return reasoning, nil, nil
	}
	reasoning.Summary = "auto"
	return reasoning, []string{"reasoning.encrypted_content"}, nil
}

func normalizeOutputFormat(config *OutputConfig) (*codex.TextConfig, error) {
	if config == nil || config.Format == nil {
		return nil, nil
	}
	format := config.Format
	if strings.TrimSpace(format.Type) != "json_schema" {
		return nil, invalid("output_config.format.type", fmt.Sprintf("unsupported output format %q", format.Type))
	}
	if len(format.Schema) == 0 {
		return nil, invalid("output_config.format.schema", "schema is required")
	}
	return &codex.TextConfig{Format: codex.TextFormat{
		Type:   "json_schema",
		Name:   firstNonEmpty(strings.TrimSpace(format.Name), "anthropic_output"),
		Schema: translate.NormalizeSchema(format.Schema),
		Strict: true,
	}}, nil
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
		return "", invalid("service_tier", fmt.Sprintf("unsupported service tier %q", raw))
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

func invalid(field, message string) error {
	return &RequestError{Field: field, Message: message}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
