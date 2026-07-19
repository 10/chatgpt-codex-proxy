package openai

import (
	"encoding/json"
	"strings"

	"chatgpt-codex-proxy/internal/jsonutil"
)

func ReconvertJSONText(text string, schema map[string]any) (string, error) {
	if schema == nil || strings.TrimSpace(text) == "" {
		return text, nil
	}

	var decoded any
	if err := json.Unmarshal([]byte(text), &decoded); err != nil {
		return text, err
	}

	reconverted := reconvertTupleValues(decoded, schema, schema)
	payload, err := json.Marshal(reconverted)
	if err != nil {
		return text, err
	}
	return string(payload), nil
}

func PatchChatCompletionObjectForTuple(object map[string]any, schema map[string]any) error {
	if schema == nil || len(object) == 0 {
		return nil
	}

	choices := jsonutil.SliceOfMaps(object["choices"])
	if len(choices) == 0 {
		return nil
	}

	message := jsonutil.MapValue(choices[0], "message")
	return patchTupleTextField(message, "content", schema)
}

func PatchResponsesObjectForTuple(object map[string]any, schema map[string]any) error {
	if schema == nil || len(object) == 0 {
		return nil
	}
	if err := patchTupleTextField(object, "output_text", schema); err != nil {
		return err
	}
	return patchTupleOutputMessages(jsonutil.SliceOfMaps(object["output"]), schema)
}

func patchTupleOutputMessages(items []map[string]any, schema map[string]any) error {
	for _, item := range items {
		if jsonutil.StringValue(item["type"]) != "message" {
			continue
		}
		for _, content := range jsonutil.SliceOfMaps(item["content"]) {
			if jsonutil.StringValue(content["type"]) != "output_text" {
				continue
			}
			if err := patchTupleTextField(content, "text", schema); err != nil {
				return err
			}
		}
	}
	return nil
}

func patchTupleTextField(target map[string]any, key string, schema map[string]any) error {
	text := jsonutil.StringValue(target[key])
	if strings.TrimSpace(text) == "" {
		return nil
	}
	reconverted, err := ReconvertJSONText(text, schema)
	if err != nil {
		return err
	}
	target[key] = reconverted
	return nil
}
