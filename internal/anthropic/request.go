package anthropic

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"chatgpt-codex-proxy/internal/models"
	"chatgpt-codex-proxy/internal/turn"
)

const claudeCodeAttributionSystemPrefix = "x-anthropic-billing-header:"

func Normalize(request MessagesRequest, catalog *models.Catalog) (turn.NormalizedRequest, error) {
	model := strings.TrimSpace(request.Model)
	if model == "" {
		return turn.NormalizedRequest{}, errors.New("model is required")
	}
	if catalog != nil && !catalog.Has(model) {
		return turn.NormalizedRequest{}, &models.NotFoundError{Model: model}
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
		Request: turn.Request{
			Model:             model,
			Input:             turn.PrependDeveloperInstructions(input, instructions),
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

func normalizeMessages(messages []Message, toolNames *turn.ToolNames) ([]turn.InputItem, error) {
	knownCalls := make(map[string]struct{})
	knownSearches := make(map[string]struct{})
	result := make([]turn.InputItem, 0, len(messages))
	for _, message := range messages {
		role := strings.TrimSpace(message.Role)
		if role == "system" {
			if reminder := messageSystemReminderText(message.Content); reminder != "" {
				result = append(result, turn.InputItem{
					Role: "user",
					Content: []turn.ContentPart{{
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
		var content []turn.ContentPart
		flushContent := func() {
			if len(content) == 0 {
				return
			}
			result = append(result, turn.InputItem{Role: role, Content: content})
			content = nil
		}

		for _, block := range message.Content {
			switch block.Type {
			case "text":
				partType := "input_text"
				if role == "assistant" {
					partType = "output_text"
				}
				content = append(content, turn.ContentPart{Type: partType, Text: block.Text})
			case "image":
				if role != "user" {
					return nil, errors.New("image blocks are only supported in user messages")
				}
				imageURL, err := normalizeImageSource(block.Source)
				if err != nil {
					return nil, err
				}
				content = append(content, turn.ContentPart{Type: "input_image", ImageURL: imageURL})
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
				result = append(result, turn.InputItem{Type: "function_call", CallID: shortenCallID(callID), Name: toolNames.Shorten(block.Name), Arguments: arguments})
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
						output = append([]turn.ContentPart{{Type: "input_text", Text: "Tool error"}}, output...)
					}
				}
				result = append(result, turn.InputItem{Type: "function_call_output", CallID: shortenCallID(callID), OutputText: text, OutputContent: output})
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
				result = append(result, turn.InputItem{Type: "web_search_call", ID: shortenCallID(callID), Status: "completed"})
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
				item := turn.InputItem{Type: "reasoning", EncryptedContent: encrypted}
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

func normalizeToolResult(content Content) (string, []turn.ContentPart, error) {
	parts := make([]turn.ContentPart, 0, len(content))
	allText := true
	texts := make([]string, 0, len(content))
	for _, block := range content {
		switch block.Type {
		case "text":
			parts = append(parts, turn.ContentPart{Type: "input_text", Text: block.Text})
			texts = append(texts, block.Text)
		case "image":
			allText = false
			imageURL, err := normalizeImageSource(block.Source)
			if err != nil {
				return "", nil, err
			}
			parts = append(parts, turn.ContentPart{Type: "input_image", ImageURL: imageURL})
		default:
			return "", nil, fmt.Errorf("unsupported tool result block type %q", block.Type)
		}
	}
	if allText {
		return strings.Join(texts, "\n"), nil, nil
	}
	return "", parts, nil
}

func normalizeThinking(request MessagesRequest, catalog *models.Catalog) (*turn.Reasoning, []string, error) {
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
	effortFromBudget := false
	if effort == "" && thinkingEnabled {
		effort = thinkingEffortForBudget(request.Thinking.BudgetTokens)
		effortFromBudget = effort != ""
	}
	if effortFromBudget && catalog != nil {
		if entry, ok := catalog.Get(strings.TrimSpace(request.Model)); ok {
			effort = clampThinkingEffort(effort, entry.SupportedReasoningEfforts)
		}
	}
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
	reasoning := &turn.Reasoning{Effort: effort}
	if !thinkingEnabled {
		return reasoning, nil, nil
	}
	reasoning.Summary = "auto"
	return reasoning, []string{"reasoning.encrypted_content"}, nil
}

func thinkingEffortForBudget(budget int) string {
	switch {
	case budget <= 0:
		return ""
	case budget <= 1024:
		return "low"
	case budget <= 8192:
		return "medium"
	case budget <= 24576:
		return "high"
	default:
		return "xhigh"
	}
}

func clampThinkingEffort(effort string, supported []models.ReasoningEffort) string {
	order := []string{"minimal", "low", "medium", "high", "xhigh", "max", "ultra"}
	targetIndex := slices.Index(order, effort)
	if targetIndex < 0 {
		return effort
	}

	bestEffort := effort
	bestIndex := -1
	bestDistance := len(order) + 1
	for _, candidate := range supported {
		candidateEffort := strings.TrimSpace(candidate.ReasoningEffort)
		if candidateEffort == effort {
			return effort
		}
		candidateIndex := slices.Index(order, candidateEffort)
		if candidateIndex < 0 {
			continue
		}
		distance := candidateIndex - targetIndex
		if distance < 0 {
			distance = -distance
		}
		if distance < bestDistance || distance == bestDistance && candidateIndex < bestIndex {
			bestEffort = candidateEffort
			bestIndex = candidateIndex
			bestDistance = distance
		}
	}
	return bestEffort
}

func normalizeServiceTier(raw string) (string, error) {
	switch strings.TrimSpace(raw) {
	case "":
		return "", nil
	case "auto", "standard_only":
		return "default", nil
	case "fast", "priority":
		return "priority", nil
	case "ultrafast":
		return "ultrafast", nil
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
