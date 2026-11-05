package openai

import (
	"fmt"

	"chatgpt-codex-proxy/internal/schema"
	"chatgpt-codex-proxy/internal/turn"
)

func normalizeResponsesText(text *ResponsesText) (*turn.TextConfig, map[string]any) {
	if text == nil {
		return nil, nil
	}

	config := &turn.TextConfig{Verbosity: text.Verbosity}
	if text.Format == nil {
		if text.Verbosity == "" {
			return nil, nil
		}
		return config, nil
	}

	var tupleSchema map[string]any
	format := turn.TextFormat{
		Type:   text.Format.Type,
		Name:   text.Format.Name,
		Schema: text.Format.Schema,
		Strict: text.Format.Strict,
	}
	if text.Format.Type == "json_schema" {
		format.Schema, tupleSchema = schema.Prepare(text.Format.Schema)
	}
	config.Format = format
	return config, tupleSchema
}

func normalizeChatResponseFormat(format *ResponseFormat) (*turn.TextConfig, map[string]any, error) {
	if format == nil {
		return nil, nil, nil
	}
	switch format.Type {
	case "", "text":
		return nil, nil, nil
	case "json_object":
		return &turn.TextConfig{
			Format: turn.TextFormat{Type: "json_object"},
		}, nil, nil
	case "json_schema":
		if format.JSONSchema == nil {
			return nil, nil, fmt.Errorf("response_format.json_schema is required")
		}
		prepared, tupleSchema := schema.Prepare(format.JSONSchema.Schema)
		return &turn.TextConfig{
			Format: turn.TextFormat{
				Type:   "json_schema",
				Name:   format.JSONSchema.Name,
				Schema: prepared,
				Strict: format.JSONSchema.Strict,
			},
		}, tupleSchema, nil
	default:
		return nil, nil, fmt.Errorf("unsupported response_format %q", format.Type)
	}
}

func reasoningInclude(reasoning *turn.Reasoning) []string {
	if reasoning == nil {
		return nil
	}
	return []string{"reasoning.encrypted_content"}
}
