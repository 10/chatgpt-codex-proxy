package models

var bootstrapReasoningEfforts = []ReasoningEffort{
	{ReasoningEffort: "low", Description: "Fastest responses"},
	{ReasoningEffort: "medium", Description: "Balanced"},
	{ReasoningEffort: "high", Description: "Greater reasoning depth"},
	{ReasoningEffort: "xhigh", Description: "Extra high reasoning depth"},
}

var bootstrapEntries = []Entry{
	bootstrapEntry("gpt-5.6-sol", true, "medium",
		ReasoningEffort{ReasoningEffort: "max", Description: "Maximum reasoning depth for the hardest problems"},
		ReasoningEffort{ReasoningEffort: "ultra", Description: "Maximum reasoning with automatic task delegation"},
	),
	bootstrapEntry("gpt-5.6-terra", false, "medium",
		ReasoningEffort{ReasoningEffort: "max", Description: "Maximum reasoning depth for the hardest problems"},
		ReasoningEffort{ReasoningEffort: "ultra", Description: "Maximum reasoning with automatic task delegation"},
	),
	bootstrapEntry("gpt-5.6-luna", false, "medium",
		ReasoningEffort{ReasoningEffort: "max", Description: "Maximum reasoning depth for the hardest problems"},
	),
	bootstrapEntry("gpt-5.5", false, "medium"),
	bootstrapEntry("gpt-5.4", false, "medium"),
	bootstrapEntry("gpt-5.4-mini", false, "medium"),
	bootstrapEntry("gpt-5.3-codex-spark", false, "high"),
}

func bootstrapEntry(id string, isDefault bool, defaultReasoningEffort string, additionalEfforts ...ReasoningEffort) Entry {
	efforts := append([]ReasoningEffort(nil), bootstrapReasoningEfforts...)
	efforts = append(efforts, additionalEfforts...)
	return Entry{
		ID:                        id,
		DisplayName:               id,
		Description:               "Bootstrap fallback model catalog entry",
		IsDefault:                 isDefault,
		DefaultReasoningEffort:    defaultReasoningEffort,
		SupportedReasoningEfforts: efforts,
		Source:                    SourceBootstrap,
	}
}

func BootstrapEntries() []Entry {
	return cloneEntries(bootstrapEntries)
}
