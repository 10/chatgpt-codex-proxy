package translate

import (
	"encoding/json"
	"maps"
	"strconv"
	"strings"

	"chatgpt-codex-proxy/internal/openai"
)

const maxToolNameLength = 64

type ToolNames struct {
	originalToShort map[string]string
	shortToOriginal map[string]string
	used            map[string]struct{}
}

func NewToolNames(names []string) *ToolNames {
	mapping := &ToolNames{
		originalToShort: make(map[string]string),
		shortToOriginal: make(map[string]string),
		used:            make(map[string]struct{}),
	}
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" || len(name) > maxToolNameLength {
			continue
		}
		mapping.originalToShort[name] = name
		mapping.used[name] = struct{}{}
	}
	for _, name := range names {
		mapping.Shorten(name)
	}
	return mapping
}

func (m *ToolNames) Shorten(name string) string {
	name = strings.TrimSpace(name)
	if name == "" || m == nil {
		return name
	}
	if shortened, ok := m.originalToShort[name]; ok {
		return shortened
	}

	candidate := toolNameCandidate(name)
	if _, exists := m.used[candidate]; exists {
		base := candidate
		for suffixNumber := 1; ; suffixNumber++ {
			suffix := "_" + strconv.Itoa(suffixNumber)
			candidate = base[:min(len(base), maxToolNameLength-len(suffix))] + suffix
			if _, exists := m.used[candidate]; !exists {
				break
			}
		}
	}

	m.originalToShort[name] = candidate
	m.used[candidate] = struct{}{}
	if candidate != name {
		m.shortToOriginal[candidate] = name
	}
	return candidate
}

func (m *ToolNames) Aliases() map[string]string {
	if m == nil || len(m.shortToOriginal) == 0 {
		return nil
	}
	return m.shortToOriginal
}

func toolNameCandidate(name string) string {
	if len(name) <= maxToolNameLength {
		return name
	}
	if strings.HasPrefix(name, "mcp__") {
		if index := strings.LastIndex(name, "__"); index > 0 {
			candidate := "mcp__" + name[index+2:]
			if len(candidate) > maxToolNameLength {
				candidate = candidate[:maxToolNameLength]
			}
			return candidate
		}
	}
	return name[:maxToolNameLength]
}

func toolNamesForChat(req openai.ChatCompletionsRequest, tools []openai.ToolDefinition) []string {
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

func toolNamesForResponses(tools []openai.ToolDefinition, toolChoice json.RawMessage, input openai.ResponsesInput) []string {
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

func toolDefinitionNames(tools []openai.ToolDefinition) []string {
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

func RestoreToolName(name string, aliases map[string]string) string {
	if original := aliases[strings.TrimSpace(name)]; original != "" {
		return original
	}
	return name
}

func UpstreamToolName(name string, aliases map[string]string) string {
	trimmed := strings.TrimSpace(name)
	for shortened, original := range aliases {
		if original == trimmed {
			return shortened
		}
	}
	return name
}

func MergeToolNameAliases(current, previous map[string]string) map[string]string {
	if len(previous) == 0 {
		return current
	}
	merged := maps.Clone(previous)
	maps.Copy(merged, current)
	return merged
}
