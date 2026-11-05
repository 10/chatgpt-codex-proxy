package jsonutil

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

type FlexibleInt64 struct {
	value int64
	set   bool
}

func (f *FlexibleInt64) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		return nil
	}

	var number json.Number
	numberErr := json.Unmarshal(data, &number)
	display := number.String()
	if numberErr != nil {
		var text string
		if err := json.Unmarshal(data, &text); err != nil {
			return fmt.Errorf("unsupported numeric value %q", string(data))
		}
		display = text
		if text = strings.TrimSpace(text); text == "" {
			return nil
		}
		number = json.Number(text)
	}

	value, err := number.Int64()
	if err != nil {
		floatValue, floatErr := number.Float64()
		if floatErr != nil {
			return fmt.Errorf("parse numeric value %q: %w", display, err)
		}
		value = int64(floatValue)
	}
	f.value = value
	f.set = true
	return nil
}

func (f FlexibleInt64) Int64() (int64, bool) {
	return f.value, f.set
}

func StringValue(value any) string {
	str, _ := value.(string)
	return str
}

func FirstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func MapValue(raw map[string]any, key string) map[string]any {
	value, _ := raw[key].(map[string]any)
	return value
}

func FirstMap(values ...map[string]any) map[string]any {
	for _, value := range values {
		if len(value) > 0 {
			return value
		}
	}
	return nil
}

func CloneMap(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	dst := make(map[string]any, len(src))
	for key, value := range src {
		dst[key] = CloneValue(value)
	}
	return dst
}

func CloneValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return CloneMap(typed)
	case []map[string]any:
		out := make([]map[string]any, len(typed))
		for i, item := range typed {
			out[i] = CloneMap(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = CloneValue(item)
		}
		return out
	default:
		return value
	}
}

func SliceOfMaps(value any) []map[string]any {
	switch items := value.(type) {
	case []map[string]any:
		return append([]map[string]any(nil), items...)
	case []any:
		out := make([]map[string]any, 0, len(items))
		for _, item := range items {
			mapped, ok := item.(map[string]any)
			if ok {
				out = append(out, mapped)
			}
		}
		return out
	default:
		return nil
	}
}

func IntValue(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	case json.Number:
		parsed, err := typed.Int64()
		if err == nil {
			return int(parsed), true
		}
		floatValue, floatErr := typed.Float64()
		if floatErr == nil {
			return int(floatValue), true
		}
		return 0, false
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		return parsed, err == nil
	default:
		return 0, false
	}
}
