package codex

import (
	"encoding/json"
	"fmt"
	"strings"

	"chatgpt-codex-proxy/internal/jsonutil"
	"chatgpt-codex-proxy/internal/models"
)

type backendReasoningEffort struct {
	ReasoningEffort    string `json:"reasoning_effort,omitempty"`
	ReasoningEffortAlt string `json:"reasoningEffort,omitempty"`
	Effort             string `json:"effort,omitempty"`
	Description        string `json:"description,omitempty"`
}

type backendModelEntry struct {
	Slug                      string                   `json:"slug,omitempty"`
	ID                        string                   `json:"id,omitempty"`
	Name                      string                   `json:"name,omitempty"`
	DisplayName               string                   `json:"display_name,omitempty"`
	Description               string                   `json:"description,omitempty"`
	ContextWindow             int                      `json:"context_window,omitempty"`
	MaxContextWindow          int                      `json:"max_context_window,omitempty"`
	IsDefault                 bool                     `json:"is_default,omitempty"`
	DefaultReasoningEffort    string                   `json:"default_reasoning_effort,omitempty"`
	DefaultReasoningLevel     string                   `json:"default_reasoning_level,omitempty"`
	SupportedReasoningEfforts []backendReasoningEffort `json:"supported_reasoning_efforts,omitempty"`
	SupportedReasoningLevels  []backendReasoningEffort `json:"supported_reasoning_levels,omitempty"`
}

func parseCodexModelsResponse(payload string) ([]models.Entry, error) {
	var decoded struct {
		Models json.RawMessage `json:"models"`
	}
	decoder := json.NewDecoder(strings.NewReader(payload))
	if err := decoder.Decode(&decoded); err != nil {
		return nil, err
	}
	if len(decoded.Models) == 0 {
		return nil, fmt.Errorf("missing models field")
	}
	var entries []backendModelEntry
	if err := json.Unmarshal(decoded.Models, &entries); err != nil {
		return nil, fmt.Errorf("models field is not a model array: %w", err)
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("models array is empty")
	}
	modelsByID := make(map[string]models.Entry)
	order := make([]string, 0, len(entries))

	for idx, raw := range entries {
		entry := normalizeBackendEntry(raw)
		if entry.ID == "" {
			return nil, fmt.Errorf("models[%d] is missing slug, id, and name", idx)
		}
		if _, exists := modelsByID[entry.ID]; !exists {
			order = append(order, entry.ID)
		}
		modelsByID[entry.ID] = entry
	}

	normalized := make([]models.Entry, 0, len(modelsByID))
	for _, id := range order {
		normalized = append(normalized, modelsByID[id])
	}
	return normalized, nil
}

func normalizeBackendEntry(raw backendModelEntry) models.Entry {
	id := strings.TrimSpace(jsonutil.FirstNonEmpty(raw.Slug, raw.ID, raw.Name))

	efforts := normalizeEfforts(raw)
	defaultEffort := strings.TrimSpace(jsonutil.FirstNonEmpty(raw.DefaultReasoningEffort, raw.DefaultReasoningLevel))
	if defaultEffort == "" {
		defaultEffort = efforts[0].ReasoningEffort
	}

	return models.Entry{
		ID:                        id,
		DisplayName:               jsonutil.FirstNonEmpty(strings.TrimSpace(raw.DisplayName), strings.TrimSpace(raw.Name), id),
		Description:               strings.TrimSpace(raw.Description),
		ContextWindow:             raw.ContextWindow,
		MaxContextWindow:          raw.MaxContextWindow,
		IsDefault:                 raw.IsDefault,
		DefaultReasoningEffort:    defaultEffort,
		SupportedReasoningEfforts: efforts,
	}
}

func normalizeEfforts(raw backendModelEntry) []models.ReasoningEffort {
	out := make([]models.ReasoningEffort, 0)
	for _, effort := range raw.SupportedReasoningEfforts {
		name := jsonutil.FirstNonEmpty(effort.ReasoningEffort, effort.ReasoningEffortAlt, effort.Effort)
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		out = append(out, models.ReasoningEffort{
			ReasoningEffort: name,
			Description:     strings.TrimSpace(effort.Description),
		})
	}
	for _, effort := range raw.SupportedReasoningLevels {
		name := strings.TrimSpace(effort.Effort)
		if name == "" {
			continue
		}
		out = append(out, models.ReasoningEffort{
			ReasoningEffort: name,
			Description:     strings.TrimSpace(effort.Description),
		})
	}
	if len(out) == 0 {
		out = []models.ReasoningEffort{
			{ReasoningEffort: "medium", Description: "Default"},
		}
	}
	return dedupeEfforts(out)
}

func dedupeEfforts(efforts []models.ReasoningEffort) []models.ReasoningEffort {
	seen := make(map[string]struct{}, len(efforts))
	out := make([]models.ReasoningEffort, 0, len(efforts))
	for _, effort := range efforts {
		key := strings.TrimSpace(effort.ReasoningEffort)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, effort)
	}
	return out
}
