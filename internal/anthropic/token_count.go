package anthropic

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tiktoken-go/tokenizer"

	"chatgpt-codex-proxy/internal/translate"
)

const estimatedImageTokens int64 = 256

func CountInputTokens(normalized translate.NormalizedRequest) (int64, error) {
	codec, err := tokenizerForModel(normalized.Model)
	if err != nil {
		return 0, fmt.Errorf("initialize tokenizer: %w", err)
	}
	segments := make([]string, 0, len(normalized.Input)+len(normalized.Tools)+1)
	var imageTokens int64
	appendSegment := func(value string) {
		if value = strings.TrimSpace(value); value != "" {
			segments = append(segments, value)
		}
	}
	appendSegment(normalized.Instructions)
	for _, item := range normalized.Input {
		appendSegment(item.Role)
		appendSegment(item.Type)
		appendSegment(item.Name)
		appendSegment(item.Arguments)
		appendSegment(item.Input)
		appendSegment(item.OutputText)
		for _, part := range item.Content {
			appendSegment(part.Text)
			if strings.TrimSpace(part.ImageURL) != "" {
				imageTokens += estimatedImageTokens
			}
		}
		for _, part := range item.OutputContent {
			appendSegment(part.Text)
			if strings.TrimSpace(part.ImageURL) != "" {
				imageTokens += estimatedImageTokens
			}
		}
		for _, part := range item.Summary {
			appendSegment(part.Text)
		}
	}
	for _, tool := range normalized.Tools {
		appendSegment(tool.Type)
		appendSegment(tool.Name)
		appendSegment(tool.Description)
		if len(tool.Parameters) > 0 {
			encoded, marshalErr := json.Marshal(tool.Parameters)
			if marshalErr != nil {
				return 0, fmt.Errorf("encode tool parameters: %w", marshalErr)
			}
			appendSegment(string(encoded))
		}
	}
	if normalized.Text != nil {
		appendSegment(normalized.Text.Format.Name)
		if len(normalized.Text.Format.Schema) > 0 {
			encoded, marshalErr := json.Marshal(normalized.Text.Format.Schema)
			if marshalErr != nil {
				return 0, fmt.Errorf("encode output schema: %w", marshalErr)
			}
			appendSegment(string(encoded))
		}
	}
	if len(segments) == 0 {
		return imageTokens, nil
	}
	count, err := codec.Count(strings.Join(segments, "\n"))
	if err != nil {
		return 0, err
	}
	return int64(count) + imageTokens, nil
}

func tokenizerForModel(model string) (tokenizer.Codec, error) {
	model = strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.HasPrefix(model, "gpt-5"):
		return tokenizer.ForModel(tokenizer.GPT5)
	case strings.HasPrefix(model, "gpt-4.1"):
		return tokenizer.ForModel(tokenizer.GPT41)
	case strings.HasPrefix(model, "gpt-4o"):
		return tokenizer.ForModel(tokenizer.GPT4o)
	case strings.HasPrefix(model, "gpt-4"):
		return tokenizer.ForModel(tokenizer.GPT4)
	default:
		return tokenizer.Get(tokenizer.Cl100kBase)
	}
}
