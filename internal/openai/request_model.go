package openai

import (
	"slices"
	"strings"

	"chatgpt-codex-proxy/internal/models"
	"chatgpt-codex-proxy/internal/turn"
)

func normalizeModel(rawModel, reasoningEffort, serviceTier string, catalog *models.Catalog) (string, bool, *turn.Reasoning, string, error) {
	model := strings.TrimSpace(rawModel)
	modelExplicit := model != ""
	if modelExplicit {
		if catalog != nil {
			if !catalog.Has(model) {
				return "", false, nil, "", &models.NotFoundError{Model: model}
			}
		} else if !slices.ContainsFunc(models.BootstrapEntries(), func(entry models.Entry) bool { return entry.ID == model }) {
			return "", false, nil, "", &models.NotFoundError{Model: model}
		}
	}

	effort := strings.TrimSpace(reasoningEffort)
	var reasoning *turn.Reasoning
	if effort != "" {
		reasoning = &turn.Reasoning{Effort: effort, Summary: "auto"}
	}
	serviceTier = strings.TrimSpace(serviceTier)
	if strings.EqualFold(serviceTier, "auto") {
		serviceTier = "default"
	} else if strings.EqualFold(serviceTier, "fast") {
		serviceTier = "priority"
	}
	return model, modelExplicit, reasoning, serviceTier, nil
}
