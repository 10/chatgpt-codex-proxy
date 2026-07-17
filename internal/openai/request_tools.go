package openai

import (
	"encoding/json"
	"strconv"
	"strings"

	"chatgpt-codex-proxy/internal/codex"
)

func legacyFunctionsAsTools(functions []LegacyFunctionDefinition) []ToolDefinition {
	if len(functions) == 0 {
		return nil
	}
	tools := make([]ToolDefinition, 0, len(functions))
	for _, function := range functions {
		tools = append(tools, ToolDefinition{
			Type: "function",
			Function: &FunctionTool{
				Name:        function.Name,
				Description: function.Description,
				Parameters:  function.Parameters,
			},
		})
	}
	return tools
}

func normalizeTools(tools []ToolDefinition, toolNames *ToolNames) []codex.Tool {
	if len(tools) == 0 {
		return nil
	}

	result := make([]codex.Tool, 0, len(tools))
	for _, tool := range tools {
		switch tool.Type {
		case "function":
			function := tool.Function
			if function == nil && strings.TrimSpace(tool.Name) != "" {
				function = &FunctionTool{
					Name:        tool.Name,
					Description: tool.Description,
					Parameters:  tool.Parameters,
					Strict:      tool.Strict,
				}
			}
			if function == nil {
				continue
			}
			result = append(result, codex.Tool{
				Type:        "function",
				Name:        toolNames.Shorten(function.Name),
				Description: function.Description,
				Parameters:  NormalizeSchema(function.Parameters),
				Strict:      function.Strict,
			})
		case "web_search", "web_search_preview":
			result = append(result, codex.Tool{
				Type:              "web_search",
				SearchContextSize: tool.SearchContextSize,
				UserLocation:      tool.UserLocation,
			})
		default:
			if strings.TrimSpace(tool.Name) != "" {
				tool.Name = toolNames.Shorten(tool.Name)
			}
			result = append(result, tool)
		}
	}
	return result
}

func normalizeToolChoice(raw json.RawMessage, toolNames *ToolNames) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	if strings.TrimSpace(string(raw)) == "null" {
		return nil
	}
	var mode string
	if err := json.Unmarshal(raw, &mode); err == nil {
		return json.RawMessage(strconv.Quote(mode))
	}
	var choice struct {
		Type     string `json:"type"`
		Name     string `json:"name,omitempty"`
		Function *struct {
			Name string `json:"name"`
		} `json:"function,omitempty"`
	}
	if err := json.Unmarshal(raw, &choice); err != nil {
		return append(json.RawMessage(nil), raw...)
	}
	switch strings.TrimSpace(choice.Type) {
	case "function":
		name := strings.TrimSpace(choice.Name)
		if name == "" && choice.Function != nil {
			name = strings.TrimSpace(choice.Function.Name)
		}
		if name != "" {
			name = toolNames.Shorten(name)
			return json.RawMessage(`{"type":"function","name":` + strconv.Quote(name) + `}`)
		}
	case "custom":
		if name := strings.TrimSpace(choice.Name); name != "" {
			return json.RawMessage(`{"type":"custom","name":` + strconv.Quote(toolNames.Shorten(name)) + `}`)
		}
	case "web_search", "web_search_preview":
		return json.RawMessage(`{"type":"web_search"}`)
	}
	return append(json.RawMessage(nil), raw...)
}

func normalizeLegacyFunctionChoice(choice *LegacyFunctionCallChoice, toolNames *ToolNames) json.RawMessage {
	if choice == nil || (choice.Mode == "" && choice.Name == "") {
		return nil
	}
	switch strings.TrimSpace(choice.Mode) {
	case "none", "auto":
		return json.RawMessage(strconv.Quote(choice.Mode))
	}
	if name := strings.TrimSpace(choice.Name); name != "" {
		name = toolNames.Shorten(name)
		return json.RawMessage(`{"type":"function","name":` + strconv.Quote(name) + `}`)
	}
	return nil
}
