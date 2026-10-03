package models

var bootstrapReasoningEfforts = []ReasoningEffort{
	{ReasoningEffort: "low", Description: "Fastest responses"},
	{ReasoningEffort: "medium", Description: "Balanced"},
	{ReasoningEffort: "high", Description: "Greater reasoning depth"},
	{ReasoningEffort: "xhigh", Description: "Extra high reasoning depth"},
}

// Codex limits differ from the public API. Source: openai/codex rust-v0.160.0,
// codex-rs/models-manager/models.json. Fetched account metadata takes precedence.
var bootstrapEntries = []Entry{
	bootstrapEntry("gpt-6.1-sol", true, "low", 872000,
		ReasoningEffort{ReasoningEffort: "max", Description: "Maximum reasoning depth for the hardest problems"},
		ReasoningEffort{ReasoningEffort: "ultra", Description: "Maximum reasoning with automatic task delegation"},
	),
	bootstrapEntry("gpt-6-astra", false, "low", 872000,
		ReasoningEffort{ReasoningEffort: "max", Description: "Maximum reasoning depth for the hardest problems"},
		ReasoningEffort{ReasoningEffort: "ultra", Description: "Maximum reasoning with automatic task delegation"},
	),
	bootstrapEntry("gpt-6-sol", false, "medium", 872000,
		ReasoningEffort{ReasoningEffort: "max", Description: "Maximum reasoning depth for the hardest problems"},
		ReasoningEffort{ReasoningEffort: "ultra", Description: "Maximum reasoning with automatic task delegation"},
	),
	bootstrapEntry("gpt-6-luna", false, "medium", 872000,
		ReasoningEffort{ReasoningEffort: "max", Description: "Maximum reasoning depth for the hardest problems"},
	),
	bootstrapEntry("gpt-5.6-sol", false, "low", 872000,
		ReasoningEffort{ReasoningEffort: "max", Description: "Maximum reasoning depth for the hardest problems"},
		ReasoningEffort{ReasoningEffort: "ultra", Description: "Maximum reasoning with automatic task delegation"},
	),
	bootstrapEntry("gpt-5.6-terra", false, "medium", 872000,
		ReasoningEffort{ReasoningEffort: "max", Description: "Maximum reasoning depth for the hardest problems"},
		ReasoningEffort{ReasoningEffort: "ultra", Description: "Maximum reasoning with automatic task delegation"},
	),
	bootstrapEntry("gpt-5.6-luna", false, "medium", 872000,
		ReasoningEffort{ReasoningEffort: "max", Description: "Maximum reasoning depth for the hardest problems"},
	),
	bootstrapEntry("gpt-5.5", false, "medium", 272000),
	bootstrapEntry("gpt-5.3-codex-spark", false, "high", 128000),
}

func bootstrapEntry(id string, isDefault bool, defaultReasoningEffort string, maxContextWindow int, additionalEfforts ...ReasoningEffort) Entry {
	efforts := append([]ReasoningEffort(nil), bootstrapReasoningEfforts...)
	efforts = append(efforts, additionalEfforts...)
	return Entry{
		ID:                        id,
		DisplayName:               id,
		Description:               "Bootstrap fallback model catalog entry",
		ContextWindow:             min(272000, maxContextWindow),
		MaxContextWindow:          maxContextWindow,
		IsDefault:                 isDefault,
		DefaultReasoningEffort:    defaultReasoningEffort,
		SupportedReasoningEfforts: efforts,
	}
}

func BootstrapEntries() []Entry {
	return cloneEntries(bootstrapEntries)
}
