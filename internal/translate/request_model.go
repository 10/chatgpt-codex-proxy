package translate

import (
	"strings"

	"chatgpt-codex-proxy/internal/codex"
	"chatgpt-codex-proxy/internal/models"
)

func normalizeModel(rawModel, reasoningEffort, serviceTier string, catalogs ...*models.Catalog) (string, bool, *codex.Reasoning, string, error) {
	catalog := firstCatalog(catalogs...)
	model := strings.TrimSpace(rawModel)
	modelExplicit := model != ""
	if modelExplicit {
		if catalog != nil {
			if !catalog.Has(model) {
				return "", false, nil, "", &ModelNotFoundError{Model: model}
			}
		} else if !bootstrapModelSupported(model) {
			return "", false, nil, "", &ModelNotFoundError{Model: model}
		}
	}

	effort := strings.TrimSpace(reasoningEffort)
	var reasoning *codex.Reasoning
	if effort != "" {
		reasoning = &codex.Reasoning{Effort: effort, Summary: "auto"}
	}
	return model, modelExplicit, reasoning, normalizeServiceTier(serviceTier), nil
}

func normalizeServiceTier(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "fast":
		return "priority"
	default:
		return strings.TrimSpace(value)
	}
}

func firstCatalog(catalogs ...*models.Catalog) *models.Catalog {
	for _, catalog := range catalogs {
		if catalog != nil {
			return catalog
		}
	}
	return nil
}

func bootstrapModelSupported(model string) bool {
	model = strings.TrimSpace(model)
	if model == "" {
		return false
	}
	for _, entry := range models.BootstrapEntries() {
		if entry.ID == model {
			return true
		}
	}
	return false
}
