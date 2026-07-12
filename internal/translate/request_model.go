package translate

import (
	"slices"
	"strings"

	"chatgpt-codex-proxy/internal/codex"
	"chatgpt-codex-proxy/internal/models"
)

func normalizeModel(rawModel, reasoningEffort, serviceTier string, catalog *models.Catalog) (string, bool, *codex.Reasoning, string, error) {
	model := strings.TrimSpace(rawModel)
	modelExplicit := model != ""
	if modelExplicit {
		if catalog != nil {
			if !catalog.Has(model) {
				return "", false, nil, "", &ModelNotFoundError{Model: model}
			}
		} else if !slices.ContainsFunc(models.BootstrapEntries(), func(entry models.Entry) bool { return entry.ID == model }) {
			return "", false, nil, "", &ModelNotFoundError{Model: model}
		}
	}

	effort := strings.TrimSpace(reasoningEffort)
	var reasoning *codex.Reasoning
	if effort != "" {
		reasoning = &codex.Reasoning{Effort: effort, Summary: "auto"}
	}
	serviceTier = strings.TrimSpace(serviceTier)
	if strings.EqualFold(serviceTier, "auto") {
		serviceTier = "default"
	} else if strings.EqualFold(serviceTier, "fast") {
		serviceTier = "priority"
	}
	return model, modelExplicit, reasoning, serviceTier, nil
}

func firstCatalog(catalogs ...*models.Catalog) *models.Catalog {
	for _, catalog := range catalogs {
		if catalog != nil {
			return catalog
		}
	}
	return nil
}
