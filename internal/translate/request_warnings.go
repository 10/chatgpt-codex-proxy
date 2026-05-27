package translate

import "chatgpt-codex-proxy/internal/openai"

func collectChatCompatibilityWarnings(req openai.ChatCompletionsRequest) []CompatibilityWarning {
	var warnings []CompatibilityWarning
	if req.N != nil {
		warnings = append(warnings, unsupportedFieldWarning(EndpointChat, "n"))
	}
	if req.Temperature != nil {
		warnings = append(warnings, unsupportedFieldWarning(EndpointChat, "temperature"))
	}
	if req.TopP != nil {
		warnings = append(warnings, unsupportedFieldWarning(EndpointChat, "top_p"))
	}
	if req.MaxTokens != nil {
		warnings = append(warnings, unsupportedFieldWarning(EndpointChat, "max_tokens"))
	}
	if req.PresencePenalty != nil {
		warnings = append(warnings, unsupportedFieldWarning(EndpointChat, "presence_penalty"))
	}
	if req.FrequencyPenalty != nil {
		warnings = append(warnings, unsupportedFieldWarning(EndpointChat, "frequency_penalty"))
	}
	if len(req.Stop) > 0 {
		warnings = append(warnings, unsupportedFieldWarning(EndpointChat, "stop"))
	}
	if req.User != nil {
		warnings = append(warnings, unsupportedFieldWarning(EndpointChat, "user"))
	}
	if req.ParallelToolCalls != nil {
		warnings = append(warnings, unsupportedFieldWarning(EndpointChat, "parallel_tool_calls"))
	}
	if len(req.StreamOptions) > 0 {
		warnings = append(warnings, unsupportedFieldWarning(EndpointChat, "stream_options"))
	}
	return warnings
}

func collectResponsesCompatibilityWarnings(req openai.ResponsesRequest) []CompatibilityWarning {
	var warnings []CompatibilityWarning
	if req.Temperature != nil {
		warnings = append(warnings, unsupportedFieldWarning(EndpointResponses, "temperature"))
	}
	if req.TopP != nil {
		warnings = append(warnings, unsupportedFieldWarning(EndpointResponses, "top_p"))
	}
	if req.MaxOutputTokens != nil {
		warnings = append(warnings, unsupportedFieldWarning(EndpointResponses, "max_output_tokens"))
	}
	if req.ParallelToolCalls != nil {
		warnings = append(warnings, unsupportedFieldWarning(EndpointResponses, "parallel_tool_calls"))
	}
	if req.Store != nil {
		warnings = append(warnings, unsupportedFieldWarning(EndpointResponses, "store"))
	}
	if req.Background != nil {
		warnings = append(warnings, unsupportedFieldWarning(EndpointResponses, "background"))
	}
	if req.User != nil {
		warnings = append(warnings, unsupportedFieldWarning(EndpointResponses, "user"))
	}
	if len(req.Metadata) > 0 {
		warnings = append(warnings, unsupportedFieldWarning(EndpointResponses, "metadata"))
	}
	if len(req.StreamOptions) > 0 {
		warnings = append(warnings, unsupportedFieldWarning(EndpointResponses, "stream_options"))
	}
	return warnings
}

func unsupportedFieldWarning(endpoint Endpoint, field string) CompatibilityWarning {
	return CompatibilityWarning{
		Field:    field,
		Endpoint: endpoint,
		Behavior: "ignored_with_warning",
		Detail:   "field is accepted for compatibility but not applied in this proxy",
	}
}
