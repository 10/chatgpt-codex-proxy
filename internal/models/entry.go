package models

type ReasoningEffort struct {
	ReasoningEffort string `json:"reasoning_effort"`
	Description     string `json:"description,omitempty"`
}

type Entry struct {
	ID                        string            `json:"id"`
	DisplayName               string            `json:"display_name"`
	Description               string            `json:"description,omitempty"`
	ContextWindow             int               `json:"context_window,omitempty"`
	MaxContextWindow          int               `json:"max_context_window,omitempty"`
	IsDefault                 bool              `json:"is_default,omitempty"`
	DefaultReasoningEffort    string            `json:"default_reasoning_effort,omitempty"`
	SupportedReasoningEfforts []ReasoningEffort `json:"supported_reasoning_efforts,omitempty"`
	Priority                  int               `json:"priority,omitempty"`
	Hidden                    bool              `json:"hidden,omitempty"`
}
