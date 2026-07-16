package generation

import (
	"maps"
	"strconv"
	"strings"
)

const maxToolNameLength = 64

// ToolNames owns the reversible mapping required when a client tool name is
// longer than the Codex backend accepts.
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
			prefixLength := maxToolNameLength - len(suffix)
			candidate = base
			if len(candidate) > prefixLength {
				candidate = candidate[:prefixLength]
			}
			candidate += suffix
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
	return maps.Clone(m.shortToOriginal)
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
