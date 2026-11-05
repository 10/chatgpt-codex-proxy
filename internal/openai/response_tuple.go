package openai

import (
	"strings"

	"chatgpt-codex-proxy/internal/jsonutil"
	"chatgpt-codex-proxy/internal/schema"
)

func PatchChatCompletionObjectForTuple(object map[string]any, originalSchema map[string]any) error {
	if originalSchema == nil || len(object) == 0 {
		return nil
	}

	choices := jsonutil.SliceOfMaps(object["choices"])
	if len(choices) == 0 {
		return nil
	}

	message := jsonutil.MapValue(choices[0], "message")
	return patchTupleTextField(message, "content", originalSchema)
}

func PatchResponsesObjectForTuple(object map[string]any, originalSchema map[string]any) error {
	if originalSchema == nil || len(object) == 0 {
		return nil
	}
	if err := patchTupleTextField(object, "output_text", originalSchema); err != nil {
		return err
	}
	return patchTupleOutputMessages(jsonutil.SliceOfMaps(object["output"]), originalSchema)
}

func patchTupleOutputMessages(items []map[string]any, originalSchema map[string]any) error {
	for _, item := range items {
		if jsonutil.StringValue(item["type"]) != "message" {
			continue
		}
		for _, content := range jsonutil.SliceOfMaps(item["content"]) {
			if jsonutil.StringValue(content["type"]) != "output_text" {
				continue
			}
			if err := patchTupleTextField(content, "text", originalSchema); err != nil {
				return err
			}
		}
	}
	return nil
}

func patchTupleTextField(target map[string]any, key string, originalSchema map[string]any) error {
	text := jsonutil.StringValue(target[key])
	if strings.TrimSpace(text) == "" {
		return nil
	}
	reconverted, err := schema.ReconvertJSONText(text, originalSchema)
	if err != nil {
		return err
	}
	target[key] = reconverted
	return nil
}
