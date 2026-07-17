package openai

import (
	"encoding/json"
	"fmt"
	"strings"

	"chatgpt-codex-proxy/internal/codex"
	"chatgpt-codex-proxy/internal/jsonutil"
	"chatgpt-codex-proxy/internal/models"
	"chatgpt-codex-proxy/internal/turn"
)

type ModelNotFoundError struct {
	Model string
}

func (e *ModelNotFoundError) Error() string {
	return fmt.Sprintf("unsupported model %q", e.Model)
}

type UnsupportedContentPartError struct {
	PartType string
}

func (e *UnsupportedContentPartError) Error() string {
	return fmt.Sprintf("unsupported_content_part: %s", e.PartType)
}

func ChatCompletions(req ChatCompletionsRequest, catalog *models.Catalog) (turn.NormalizedRequest, error) {
	model, modelExplicit, reasoning, serviceTier, err := normalizeModel(req.Model, req.ReasoningEffort, req.ServiceTier, catalog)
	if err != nil {
		return turn.NormalizedRequest{}, err
	}
	tools := req.Tools
	if len(tools) == 0 && len(req.Functions) > 0 {
		tools = legacyFunctionsAsTools(req.Functions)
	}
	toolNames := NewToolNames(toolNamesForChat(req, tools))
	toolChoice := json.RawMessage(nil)
	if len(req.Tools) > 0 {
		toolChoice = normalizeToolChoice(req.ToolChoice, toolNames)
	} else if choice := normalizeLegacyFunctionChoice(req.FunctionCall, toolNames); choice != nil {
		toolChoice = choice
	}
	out := newNormalizedRequest(
		model,
		modelExplicit,
		req.Stream,
		normalizeTools(tools, toolNames),
		toolChoice,
		reasoning,
		serviceTier,
		req.PreviousResponseID,
	)
	out.PromptCacheKey = strings.TrimSpace(req.PromptCacheKey)
	out.ParallelToolCalls = req.ParallelToolCalls
	out.ToolNameAliases = toolNames.Aliases()
	if req.ResponseFormat != nil {
		text, tupleSchema, err := normalizeChatResponseFormat(req.ResponseFormat)
		if err != nil {
			return turn.NormalizedRequest{}, err
		}
		out.Text = text
		out.TupleSchema = tupleSchema
	}

	var instructions []string
	customToolNames := chatCustomToolNames(req.Tools)
	toolCallTypes := make(map[string]string)
	for _, message := range req.Messages {
		if err := normalizeChatMessage(&out, &instructions, toolCallTypes, customToolNames, toolNames, message); err != nil {
			return turn.NormalizedRequest{}, err
		}
	}

	if len(out.Input) == 0 {
		out.Input = append(out.Input, codex.InputItem{
			Role: "user",
			Content: []codex.ContentPart{{
				Type: "input_text",
				Text: "",
			}},
		})
	}
	out.Instructions = strings.TrimSpace(strings.Join(instructions, "\n\n"))
	return out, nil
}

func chatCustomToolNames(tools []ToolDefinition) map[string]bool {
	if len(tools) == 0 {
		return nil
	}
	names := make(map[string]bool)
	for _, tool := range tools {
		if strings.TrimSpace(tool.Type) != "custom" {
			continue
		}
		if name := strings.TrimSpace(tool.Name); name != "" {
			names[name] = true
		}
	}
	return names
}

func customToolInputFromFunctionArguments(arguments string) string {
	trimmed := strings.TrimSpace(arguments)
	if trimmed == "" {
		return ""
	}
	var payload struct {
		Input string `json:"input"`
	}
	if err := json.Unmarshal([]byte(trimmed), &payload); err == nil && payload.Input != "" {
		return payload.Input
	}
	return arguments
}

func Responses(req ResponsesRequest, catalog *models.Catalog) (turn.NormalizedRequest, error) {
	model, modelExplicit, reasoning, serviceTier, err := normalizeModel(req.Model, "", req.ServiceTier, catalog)
	if err != nil {
		return turn.NormalizedRequest{}, err
	}
	reasoning = normalizeResponsesReasoning(req.Reasoning, reasoning)
	toolNames := NewToolNames(toolNamesForResponses(req.Tools, req.ToolChoice, req.Input))
	payload, err := normalizeResponsesPayload(req.Instructions, req.Text, req.Input, toolNames)
	if err != nil {
		return turn.NormalizedRequest{}, err
	}

	out := newNormalizedRequest(
		model,
		modelExplicit,
		req.Stream,
		normalizeTools(req.Tools, toolNames),
		normalizeToolChoice(req.ToolChoice, toolNames),
		reasoning,
		serviceTier,
		req.PreviousResponseID,
	)
	out.Instructions = payload.Instructions
	out.Input = payload.Input
	out.Text = payload.Text
	out.PromptCacheKey = strings.TrimSpace(req.PromptCacheKey)
	out.ParallelToolCalls = req.ParallelToolCalls
	out.TupleSchema = payload.TupleSchema
	out.ToolNameAliases = toolNames.Aliases()
	return out, nil
}

func Compact(req ResponsesCompactRequest, catalog *models.Catalog) (turn.NormalizedCompactRequest, error) {
	model, modelExplicit, reasoning, _, err := normalizeModel(req.Model, "", "", catalog)
	if err != nil {
		return turn.NormalizedCompactRequest{}, err
	}
	reasoning = normalizeResponsesReasoning(req.Reasoning, reasoning)
	toolNames := NewToolNames(toolNamesForResponses(nil, nil, req.Input))
	payload, err := normalizeResponsesPayload(req.Instructions, req.Text, req.Input, toolNames)
	if err != nil {
		return turn.NormalizedCompactRequest{}, err
	}

	out := turn.NormalizedCompactRequest{
		ModelExplicit:      modelExplicit,
		PreviousResponseID: strings.TrimSpace(req.PreviousResponseID),
		CompactRequest: codex.CompactRequest{
			Model:        model,
			Instructions: payload.Instructions,
			Input:        payload.Input,
			Text:         payload.Text,
			Reasoning:    reasoning,
		},
		TupleSchema:     payload.TupleSchema,
		ToolNameAliases: toolNames.Aliases(),
	}
	return out, nil
}

type normalizedResponsesPayload struct {
	Instructions string
	Input        []codex.InputItem
	Text         *codex.TextConfig
	TupleSchema  map[string]any
}

func normalizeResponsesReasoning(explicit *Reasoning, fallback *codex.Reasoning) *codex.Reasoning {
	if explicit == nil {
		return fallback
	}
	reasoning := &codex.Reasoning{
		Effort:  explicit.Effort,
		Summary: explicit.Summary,
	}
	if reasoning.Effort != "" && reasoning.Summary == "" {
		reasoning.Summary = "auto"
	}
	return reasoning
}

func normalizeResponsesPayload(instructionsText string, textConfig *ResponsesText, input ResponsesInput, toolNames *ToolNames) (normalizedResponsesPayload, error) {
	var out normalizedResponsesPayload
	var instructions []string
	if text := strings.TrimSpace(instructionsText); text != "" {
		instructions = append(instructions, text)
	}

	out.Text, out.TupleSchema = normalizeResponsesText(textConfig)

	if input.String != "" {
		out.Input = append(out.Input, codex.InputItem{
			Role: "user",
			Content: []codex.ContentPart{{
				Type: "input_text",
				Text: input.String,
			}},
		})
	}

	for _, item := range input.Items {
		if err := appendResponsesInputItem(&out.Input, &instructions, toolNames, item); err != nil {
			return normalizedResponsesPayload{}, err
		}
	}

	out.Instructions = strings.TrimSpace(strings.Join(instructions, "\n\n"))
	return out, nil
}

func newNormalizedRequest(model string, modelExplicit bool, stream bool, tools []codex.Tool, toolChoice json.RawMessage, reasoning *codex.Reasoning, serviceTier, previousResponseID string) turn.NormalizedRequest {
	return turn.NormalizedRequest{
		ModelExplicit: modelExplicit,
		Request: codex.Request{
			Model:              model,
			Stream:             stream,
			Tools:              tools,
			ToolChoice:         toolChoice,
			Reasoning:          reasoning,
			ServiceTier:        serviceTier,
			PreviousResponseID: strings.TrimSpace(previousResponseID),
			Include:            reasoningInclude(reasoning),
		},
	}
}

func normalizeChatMessage(out *turn.NormalizedRequest, instructions *[]string, toolCallTypes map[string]string, customToolNames map[string]bool, toolNames *ToolNames, message ChatMessage) error {
	switch message.Role {
	case "system", "developer":
		return appendInstructionText(instructions, message.Content)
	case "user", "assistant":
		if len(message.ToolCalls) > 0 {
			if len(message.Content) > 0 {
				if err := appendRoleContentInput(&out.Input, message.Role, "", message.Content); err != nil {
					return err
				}
			}
			for _, call := range message.ToolCalls {
				callType := normalizeChatToolCallType(call, customToolNames)
				toolCallTypes[call.ID] = callType
				out.Input = append(out.Input, chatToolCallInputItem(call, callType, toolNames))
			}
			return nil
		}
		if message.FunctionCall != nil {
			out.Input = append(out.Input, codex.InputItem{
				Type:      "function_call",
				Name:      toolNames.Shorten(message.FunctionCall.Name),
				Arguments: message.FunctionCall.Arguments,
			})
			return nil
		}
		return appendRoleContentInput(&out.Input, message.Role, "", message.Content)
	case "tool":
		text, content, err := normalizeToolOutput(message.Content)
		if err != nil {
			return err
		}
		itemType := "function_call_output"
		if toolCallTypes[message.ToolCallID] == "custom" {
			itemType = "custom_tool_call_output"
		}
		out.Input = append(out.Input, codex.InputItem{
			Type:          itemType,
			CallID:        message.ToolCallID,
			OutputText:    text,
			OutputContent: content,
		})
		return nil
	default:
		return fmt.Errorf("unsupported role %q", message.Role)
	}
}

func normalizeChatToolCallType(call ToolCall, customToolNames map[string]bool) string {
	callType := strings.TrimSpace(call.Type)
	if callType == "" {
		callType = "function"
	}
	if callType == "function" && customToolNames[strings.TrimSpace(call.Function.Name)] {
		return "custom"
	}
	return callType
}

func chatToolCallInputItem(call ToolCall, callType string, toolNames *ToolNames) codex.InputItem {
	if callType != "custom" {
		return codex.InputItem{
			Type:      "function_call",
			CallID:    call.ID,
			Name:      toolNames.Shorten(call.Function.Name),
			Arguments: call.Function.Arguments,
		}
	}

	name := ""
	input := ""
	if call.Custom != nil {
		name = call.Custom.Name
		input = call.Custom.Input
	}
	if name == "" {
		name = call.Function.Name
	}
	if input == "" {
		input = customToolInputFromFunctionArguments(call.Function.Arguments)
	}
	return codex.InputItem{
		Type:   "custom_tool_call",
		CallID: call.ID,
		Name:   toolNames.Shorten(name),
		Input:  input,
	}
}

func appendResponsesInputItem(out *[]codex.InputItem, instructions *[]string, toolNames *ToolNames, item ResponsesInputItem) error {
	if item.Type == "" && (item.Role == "system" || item.Role == "developer") {
		return appendInstructionText(instructions, item.Content)
	}

	switch item.Type {
	case "web_search_call":
		*out = append(*out, codex.InputItem{
			Type:   "web_search_call",
			ID:     strings.TrimSpace(item.ID),
			Status: strings.TrimSpace(item.Status),
		})
	case "function_call":
		*out = append(*out, codex.InputItem{
			Type:      "function_call",
			CallID:    item.CallID,
			Name:      toolNames.Shorten(item.Name),
			Arguments: item.Arguments,
		})
	case "custom_tool_call":
		*out = append(*out, codex.InputItem{
			Type:   "custom_tool_call",
			CallID: item.CallID,
			Name:   toolNames.Shorten(item.Name),
			Input:  item.Input,
		})
	case "function_call_output", "custom_tool_call_output":
		parts, err := normalizeContentPartsChecked(item.OutputContent)
		if err != nil {
			return err
		}
		*out = append(*out, codex.InputItem{
			Type:          item.Type,
			CallID:        item.CallID,
			OutputText:    item.OutputText,
			OutputContent: parts,
		})
	case "reasoning":
		parts, err := normalizeContentPartsChecked(item.Content)
		if err != nil {
			return err
		}
		*out = append(*out, codex.InputItem{
			Type:             "reasoning",
			ID:               strings.TrimSpace(item.ID),
			Status:           strings.TrimSpace(item.Status),
			Content:          parts,
			Summary:          append([]ReasoningPart(nil), item.Summary...),
			EncryptedContent: strings.TrimSpace(item.EncryptedContent),
		})
	case "compaction":
		*out = append(*out, codex.InputItem{
			Type:             "compaction",
			ID:               strings.TrimSpace(item.ID),
			EncryptedContent: strings.TrimSpace(item.EncryptedContent),
		})
	default:
		role := item.Role
		if role == "" {
			role = "user"
		}
		return appendRoleContentInput(out, role, item.Phase, item.Content)
	}
	return nil
}

func appendInstructionText(instructions *[]string, content MessageContent) error {
	text, err := flattenContent(content)
	if err != nil {
		return err
	}
	if strings.TrimSpace(text) != "" {
		*instructions = append(*instructions, text)
	}
	return nil
}

func appendRoleContentInput(out *[]codex.InputItem, role, phase string, content MessageContent) error {
	parts, err := normalizeContentPartsChecked(content)
	if err != nil {
		return err
	}
	*out = append(*out, codex.InputItem{
		Role:    role,
		Phase:   strings.TrimSpace(phase),
		Content: parts,
	})
	return nil
}

func normalizeToolOutput(content MessageContent) (string, []codex.ContentPart, error) {
	parts, err := normalizeContentPartsChecked(content)
	if err != nil {
		return "", nil, err
	}
	allText := true
	for _, part := range parts {
		if part.Type != "input_text" {
			allText = false
			break
		}
	}
	if !allText {
		return "", parts, nil
	}
	text := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part.Text) != "" {
			text = append(text, part.Text)
		}
	}
	return strings.Join(text, "\n"), nil, nil
}

func normalizeContentPartsChecked(parts MessageContent) ([]codex.ContentPart, error) {
	if len(parts) == 0 {
		return nil, nil
	}
	out := make([]codex.ContentPart, 0, len(parts))
	for _, part := range parts {
		contentType, kind, ok := classifyContentPartType(part.Type)
		if !ok {
			return nil, &UnsupportedContentPartError{PartType: part.Type}
		}
		switch kind {
		case contentPartText:
			out = append(out, codex.ContentPart{
				Type: contentType,
				Text: part.Text,
			})
		case contentPartImage:
			if part.ImageURL == nil || strings.TrimSpace(part.ImageURL.URL) == "" && strings.TrimSpace(part.ImageURL.FileID) == "" {
				return nil, fmt.Errorf("image_url.url or image_url.file_id is required")
			}
			out = append(out, codex.ContentPart{
				Type:     "input_image",
				ImageURL: strings.TrimSpace(part.ImageURL.URL),
				FileID:   strings.TrimSpace(part.ImageURL.FileID),
				Detail:   jsonutil.FirstNonEmpty(strings.TrimSpace(part.Detail), strings.TrimSpace(part.ImageURL.Detail)),
			})
		case contentPartFile:
			fileURL := strings.TrimSpace(part.FileURL)
			fileData := strings.TrimSpace(part.FileData)
			fileID := strings.TrimSpace(part.FileID)
			filename := strings.TrimSpace(part.Filename)
			if part.File != nil {
				fileURL = jsonutil.FirstNonEmpty(fileURL, strings.TrimSpace(part.File.FileURL))
				fileData = jsonutil.FirstNonEmpty(fileData, strings.TrimSpace(part.File.FileData))
				fileID = jsonutil.FirstNonEmpty(fileID, strings.TrimSpace(part.File.FileID))
				filename = jsonutil.FirstNonEmpty(filename, strings.TrimSpace(part.File.Filename))
			}
			if fileData == "" && fileURL == "" && fileID == "" {
				return nil, fmt.Errorf("input_file requires file_data, file_url, or file_id")
			}
			out = append(out, codex.ContentPart{
				Type:     "input_file",
				Detail:   strings.TrimSpace(part.Detail),
				FileURL:  fileURL,
				FileData: fileData,
				FileID:   fileID,
				Filename: filename,
			})
		}
	}
	return out, nil
}

func flattenContent(content MessageContent) (string, error) {
	var parts []string
	for _, part := range content {
		_, kind, ok := classifyContentPartType(part.Type)
		if !ok || kind != contentPartText {
			return "", &UnsupportedContentPartError{PartType: part.Type}
		}
		if strings.TrimSpace(part.Text) != "" {
			parts = append(parts, part.Text)
		}
	}
	return strings.Join(parts, "\n"), nil
}

type contentPartKind uint8

const (
	contentPartText contentPartKind = iota
	contentPartImage
	contentPartFile
)

func classifyContentPartType(partType string) (string, contentPartKind, bool) {
	switch partType {
	case "", "text", "input_text":
		return "input_text", contentPartText, true
	case "output_text":
		return "output_text", contentPartText, true
	case "reasoning_text":
		return "reasoning_text", contentPartText, true
	case "image_url", "input_image":
		return "input_image", contentPartImage, true
	case "file", "input_file":
		return "input_file", contentPartFile, true
	default:
		return "", 0, false
	}
}
