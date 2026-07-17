package anthropic

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"

	"chatgpt-codex-proxy/internal/codex"
	"chatgpt-codex-proxy/internal/openai"
	"chatgpt-codex-proxy/internal/turn"
)

func normalizeTools(tools []Tool, names *turn.ToolNames) ([]codex.Tool, error) {
	result := make([]codex.Tool, 0, len(tools))
	for _, tool := range tools {
		toolType := strings.TrimSpace(tool.Type)
		if isHostedWebSearchToolType(toolType) {
			if tool.MaxUses != nil {
				return nil, errors.New("max_uses is not supported for hosted web search")
			}
			if len(tool.BlockedDomains) > 0 {
				return nil, errors.New("blocked_domains is not supported for hosted web search")
			}
			result = append(result, normalizeHostedWebSearchTool(tool))
			continue
		}
		if toolType != "" && toolType != "custom" {
			return nil, fmt.Errorf("unsupported tool type %q", toolType)
		}
		if strings.TrimSpace(tool.Name) == "" {
			return nil, errors.New("tool name is required")
		}
		if len(tool.InputSchema) == 0 {
			return nil, errors.New("input_schema is required")
		}
		result = append(result, codex.Tool{
			Type:        "function",
			Name:        names.Shorten(tool.Name),
			Description: tool.Description,
			Parameters:  openai.NormalizeSchema(tool.InputSchema),
		})
	}
	return result, nil
}

func normalizeHostedWebSearchTool(tool Tool) codex.Tool {
	normalized := codex.Tool{
		Type:         "web_search",
		UserLocation: maps.Clone(tool.UserLocation),
	}
	if len(tool.AllowedDomains) > 0 {
		filters, _ := json.Marshal(map[string]any{"allowed_domains": tool.AllowedDomains})
		normalized.ExtraFields = map[string]json.RawMessage{"filters": filters}
	}
	return normalized
}

func isHostedWebSearchToolType(toolType string) bool {
	return toolType == "web_search_20250305" || toolType == "web_search_20260209"
}

func normalizeToolChoice(choice *ToolChoice, names *turn.ToolNames, tools []Tool) (json.RawMessage, *bool, error) {
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
			return nil, nil, errors.New("name is required for tool choice type tool")
		}
		selection := map[string]any{"type": "function", "name": names.Shorten(choice.Name)}
		if isHostedWebSearchToolName(choice.Name, tools) {
			selection = map[string]any{"type": "web_search"}
		}
		value, _ := json.Marshal(selection)
		encoded = value
	default:
		return nil, nil, fmt.Errorf("unsupported tool choice type %q", choice.Type)
	}
	var parallel *bool
	if choice.DisableParallelToolUse != nil {
		value := !*choice.DisableParallelToolUse
		parallel = &value
	}
	return encoded, parallel, nil
}

func isHostedWebSearchToolName(name string, tools []Tool) bool {
	name = strings.TrimSpace(name)
	for _, tool := range tools {
		if !isHostedWebSearchToolType(strings.TrimSpace(tool.Type)) {
			continue
		}
		toolName := strings.TrimSpace(tool.Name)
		if toolName == "" {
			toolName = "web_search"
		}
		if toolName == name {
			return true
		}
	}
	return false
}
