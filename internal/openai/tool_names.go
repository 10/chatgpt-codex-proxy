package openai

import (
	"encoding/json"
	"strings"

	"chatgpt-codex-proxy/internal/turn"
)

type ToolNames = turn.ToolNames

var NewToolNames = turn.NewToolNames

func toolNamesForChat(req ChatCompletionsRequest, tools []ToolDefinition) []string {
	names := toolDefinitionNames(tools)
	for _, message := range req.Messages {
		for _, call := range message.ToolCalls {
			if strings.TrimSpace(call.Type) == "custom" && call.Custom != nil {
				names = append(names, call.Custom.Name)
				continue
			}
			names = append(names, call.Function.Name)
		}
		if message.FunctionCall != nil {
			names = append(names, message.FunctionCall.Name)
		}
	}
	if name := toolChoiceName(req.ToolChoice); name != "" {
		names = append(names, name)
	}
	if req.FunctionCall != nil {
		names = append(names, req.FunctionCall.Name)
	}
	return names
}

func toolNamesForResponses(tools []ToolDefinition, toolChoice json.RawMessage, input ResponsesInput) []string {
	names := toolDefinitionNames(tools)
	if name := toolChoiceName(toolChoice); name != "" {
		names = append(names, name)
	}
	for _, item := range input.Items {
		switch item.Type {
		case "function_call", "custom_tool_call":
			names = append(names, item.Name)
		}
	}
	return names
}

func toolDefinitionNames(tools []ToolDefinition) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		if tool.Type == "function" && tool.Function != nil {
			names = append(names, tool.Function.Name)
			continue
		}
		if tool.Name != "" {
			names = append(names, tool.Name)
		}
	}
	return names
}

func toolChoiceName(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var choice struct {
		Type     string `json:"type"`
		Name     string `json:"name,omitempty"`
		Function *struct {
			Name string `json:"name"`
		} `json:"function,omitempty"`
	}
	if err := json.Unmarshal(raw, &choice); err != nil || choice.Type != "function" && choice.Type != "custom" {
		return ""
	}
	if name := strings.TrimSpace(choice.Name); name != "" {
		return name
	}
	if choice.Function != nil {
		return strings.TrimSpace(choice.Function.Name)
	}
	return ""
}

var RestoreToolName = turn.RestoreToolName
var UpstreamToolName = turn.UpstreamToolName
var MergeToolNameAliases = turn.MergeToolNameAliases
