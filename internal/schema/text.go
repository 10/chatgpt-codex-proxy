package schema

import (
	"encoding/json"
	"strings"
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
