package models

var bootstrapReasoningEfforts = []ReasoningEffort{
	{ReasoningEffort: "low", Description: "Fastest responses"},
	{ReasoningEffort: "medium", Description: "Balanced"},
	{ReasoningEffort: "high", Description: "Greater reasoning depth"},
	{ReasoningEffort: "xhigh", Description: "Extra high reasoning depth"},
}

var bootstrapEntries = []Entry{
	bootstrapEntry("gpt-5.5", true),
	bootstrapEntry("gpt-5.4", false),
	bootstrapEntry("gpt-5.4-mini", false),
	bootstrapEntry("gpt-5.3-codex", false),
	bootstrapEntry("gpt-5.2-codex", false),
	bootstrapEntry("gpt-5.2", false),
}

func bootstrapEntry(id string, isDefault bool) Entry {
	return Entry{
		ID:                        id,
		DisplayName:               id,
		Description:               "Bootstrap fallback model catalog entry",
		IsDefault:                 isDefault,
		DefaultReasoningEffort:    "medium",
		SupportedReasoningEfforts: bootstrapReasoningEfforts,
		Source:                    SourceBootstrap,
	}
}

func BootstrapEntries() []Entry {
	return cloneEntries(bootstrapEntries)
}
