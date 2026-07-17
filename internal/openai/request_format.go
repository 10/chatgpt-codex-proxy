package openai

import (
	"fmt"

	"chatgpt-codex-proxy/internal/codex"
)

func normalizeResponsesText(text *ResponsesText) (*codex.TextConfig, map[string]any) {
	if text == nil || text.Format == nil {
		return nil, nil
	}

	var tupleSchema map[string]any
	format := codex.TextFormat{
		Type: text.Format.Type,
		Name: text.Format.Name,
	}
	if text.Format.Type == "json_schema" {
		prepared, original := PrepareSchema(text.Format.Schema)
		format.Schema = prepared
		format.Strict = text.Format.Strict
		tupleSchema = original
	} else {
		format.Schema = text.Format.Schema
		format.Strict = text.Format.Strict
	}
	return &codex.TextConfig{Format: format}, tupleSchema
}

func normalizeChatResponseFormat(format *ResponseFormat) (*codex.TextConfig, map[string]any, error) {
	if format == nil {
		return nil, nil, nil
	}
	switch format.Type {
	case "", "text":
		return nil, nil, nil
	case "json_object":
		return &codex.TextConfig{
			Format: codex.TextFormat{Type: "json_object"},
		}, nil, nil
	case "json_schema":
		if format.JSONSchema == nil {
			return nil, nil, fmt.Errorf("response_format.json_schema is required")
		}
		prepared, tupleSchema := PrepareSchema(format.JSONSchema.Schema)
		return &codex.TextConfig{
			Format: codex.TextFormat{
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

func reasoningInclude(reasoning *codex.Reasoning) []string {
	if reasoning == nil {
		return nil
	}
	return []string{"reasoning.encrypted_content"}
}
