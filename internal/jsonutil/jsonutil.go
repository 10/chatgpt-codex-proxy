// Package jsonutil provides helpers for decoded JSON trees shared across packages.
package jsonutil

import "strings"

// StringValue returns value as a string, or "" if it is not a string.
func StringValue(value any) string {
	str, _ := value.(string)
	return str
}

// FirstNonEmpty returns the first string that is not all whitespace.
func FirstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// MapValue returns the nested map for key, or nil if the value is not a map.
func MapValue(raw map[string]any, key string) map[string]any {
	value, _ := raw[key].(map[string]any)
	return value
}

// FirstMap returns the first non-empty map.
func FirstMap(values ...map[string]any) map[string]any {
	for _, value := range values {
		if len(value) > 0 {
			return value
		}
	}
	return nil
}

// CloneMap recursively clones decoded JSON map trees.
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

// CloneValue recursively clones decoded JSON map and slice values.
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

// SliceOfMaps coerces a JSON-decoded value into []map[string]any, copying the
// outer slice.
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
