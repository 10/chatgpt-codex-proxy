package anthropic

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"reflect"
	"slices"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"chatgpt-codex-proxy/internal/jsonutil"
	"chatgpt-codex-proxy/internal/schema"
	"chatgpt-codex-proxy/internal/turn"
)

func normalizeOutputFormat(config *OutputConfig) (*turn.TextConfig, map[string]any, error) {
	if config == nil || config.Format == nil {
		return nil, nil, nil
	}
	format := config.Format
	if strings.TrimSpace(format.Type) != "json_schema" {
		return nil, nil, fmt.Errorf("unsupported output format %q", format.Type)
	}
	if len(format.Schema) == 0 {
		return nil, nil, errors.New("schema is required")
	}
	responseSchema := schema.Normalize(format.Schema)
	strictSchema := jsonutil.CloneMap(responseSchema)
	if err := makeOpenAIStrictSchema(strictSchema); err != nil {
		return nil, nil, err
	}
	if compileSchema(responseSchema) == nil {
		return nil, nil, errors.New("output schema is invalid")
	}
	return &turn.TextConfig{Format: turn.TextFormat{
		Type:   "json_schema",
		Name:   cmp.Or(strings.TrimSpace(format.Name), "anthropic_output"),
		Schema: strictSchema,
		Strict: true,
	}}, responseSchema, nil
}

func makeOpenAIStrictSchema(node map[string]any) error {
	if node["type"] != "object" {
		return errors.New("output schema root must have type object")
	}
	if _, ok := node["anyOf"]; ok {
		return errors.New(`output schema keyword "anyOf" is not supported at the root`)
	}
	return makeOpenAIStrictSchemaNode(node, newSchemaMatcher())
}

func makeOpenAIStrictSchemaNode(node map[string]any, matcher *schemaMatcher) error {
	for _, keyword := range slices.Sorted(maps.Keys(node)) {
		if !supportedOutputSchemaKeyword(keyword) {
			return fmt.Errorf("output schema keyword %q is not supported", keyword)
		}
	}
	if err := validateOutputSchemaKeywordValues(node); err != nil {
		return err
	}
	if err := visitSchemaChildren(node, func(child map[string]any) error {
		return makeOpenAIStrictSchemaNode(child, matcher)
	}); err != nil {
		return err
	}
	properties, hasProperties := node["properties"].(map[string]any)
	hasObjectType := schemaHasType(node["type"], "object")
	if !hasProperties && !hasObjectType {
		return nil
	}
	if !hasObjectType {
		return errors.New("output schema nodes with properties must include type object")
	}
	if !hasProperties {
		properties = map[string]any{}
		node["properties"] = properties
	}
	if additionalProperties, ok := node["additionalProperties"]; ok && additionalProperties != false {
		return errors.New("output schema object nodes must set additionalProperties to false")
	}
	node["additionalProperties"] = false
	required := schemaRequiredNames(node["required"])
	for name := range required {
		if _, ok := properties[name]; !ok {
			return fmt.Errorf("output schema required property %q must be declared in properties", name)
		}
	}
	for name, raw := range properties {
		child, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("output schema property %q must use an object schema", name)
		}
		if !required[name] {
			properties[name] = makeSchemaNullable(child, matcher)
		}
		required[name] = true
	}
	requiredNames := slices.Sorted(maps.Keys(required))
	if requiredNames == nil {
		requiredNames = []string{}
	}
	node["required"] = requiredNames
	return nil
}

func supportedOutputSchemaKeyword(keyword string) bool {
	switch keyword {
	case
		"type",
		"properties",
		"required",
		"additionalProperties",
		"items",
		"enum",
		"const",
		"anyOf",
		"pattern",
		"format",
		"multipleOf",
		"maximum",
		"exclusiveMaximum",
		"minimum",
		"exclusiveMinimum",
		"minItems",
		"maxItems",
		"title",
		"description":
		return true
	default:
		return false
	}
}

func validateOutputSchemaKeywordValues(node map[string]any) error {
	if schemaHasType(node["type"], "array") {
		if _, ok := node["items"]; !ok {
			return errors.New("output schema array nodes must define items")
		}
	}
	if schemaHasType(node["type"], "object") {
		if _, ok := node["anyOf"]; ok {
			return errors.New(`output schema keyword "anyOf" cannot be combined with type object`)
		}
	}
	if rawFormat, ok := node["format"]; ok {
		format, ok := rawFormat.(string)
		if !ok || !supportedOutputSchemaFormat(format) {
			return fmt.Errorf("output schema format %q is not supported", format)
		}
	}
	for _, keyword := range []string{"pattern", "format"} {
		if _, ok := node[keyword]; ok && !schemaHasType(node["type"], "string") {
			return fmt.Errorf("output schema keyword %q requires type string", keyword)
		}
	}
	for _, keyword := range []string{"multipleOf", "maximum", "exclusiveMaximum", "minimum", "exclusiveMinimum"} {
		if _, ok := node[keyword]; ok && !schemaHasType(node["type"], "number") && !schemaHasType(node["type"], "integer") {
			return fmt.Errorf("output schema keyword %q requires a numeric type", keyword)
		}
	}
	for _, keyword := range []string{"items", "minItems", "maxItems"} {
		if _, ok := node[keyword]; ok && !schemaHasType(node["type"], "array") {
			return fmt.Errorf("output schema keyword %q requires type array", keyword)
		}
	}
	for _, keyword := range []string{"properties", "required", "additionalProperties"} {
		if _, ok := node[keyword]; ok && !schemaHasType(node["type"], "object") {
			return fmt.Errorf("output schema keyword %q requires type object", keyword)
		}
	}
	return nil
}

func supportedOutputSchemaFormat(format string) bool {
	switch format {
	case "date-time", "time", "date", "duration", "email", "hostname", "ipv4", "ipv6", "uuid":
		return true
	default:
		return false
	}
}

func visitSchemaChildren(node map[string]any, visit func(map[string]any) error) error {
	properties, _ := node["properties"].(map[string]any)
	for _, raw := range properties {
		if child, ok := raw.(map[string]any); ok {
			if err := visit(child); err != nil {
				return err
			}
		}
	}
	if raw, exists := node["items"]; exists {
		child, ok := raw.(map[string]any)
		if !ok {
			return errors.New(`output schema "items" must use an object schema`)
		}
		if err := visit(child); err != nil {
			return err
		}
	}
	if rawChildren, exists := node["anyOf"]; exists {
		children, ok := rawChildren.([]any)
		if !ok {
			return errors.New(`output schema "anyOf" must be an array`)
		}
		if len(children) == 0 {
			return errors.New(`output schema "anyOf" must contain at least one schema`)
		}
		for _, raw := range children {
			child, ok := raw.(map[string]any)
			if !ok {
				return errors.New(`output schema "anyOf" entries must use object schemas`)
			}
			if err := visit(child); err != nil {
				return err
			}
		}
	}
	return nil
}

func schemaRequiredNames(raw any) map[string]bool {
	required := make(map[string]bool)
	switch values := raw.(type) {
	case []any:
		for _, value := range values {
			if name, ok := value.(string); ok {
				required[name] = true
			}
		}
	case []string:
		for _, name := range values {
			required[name] = true
		}
	}
	return required
}

func makeSchemaNullable(schema map[string]any, matcher *schemaMatcher) map[string]any {
	if matcher.matches(schema, nil) {
		return schema
	}
	return map[string]any{"anyOf": []any{schema, map[string]any{"type": "null"}}}
}

func schemaHasType(raw any, want string) bool {
	switch schemaType := raw.(type) {
	case string:
		return schemaType == want
	case []any:
		return slices.Contains(schemaType, any(want))
	case []string:
		return slices.Contains(schemaType, want)
	}
	return false
}

func normalizedOutputText(accumulator *turn.Accumulator, text string) string {
	if accumulator == nil || len(accumulator.Normalized.ResponseSchema) == 0 || strings.TrimSpace(text) == "" {
		return text
	}
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return text
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return text
	}
	strictSchema := accumulator.Normalized.ResponseSchema
	if accumulator.Normalized.Text != nil {
		strictSchema = accumulator.Normalized.Text.Format.Schema
	}
	matcher := newSchemaMatcher()
	cleaned, changed := removeOptionalNullProperties(value, accumulator.Normalized.ResponseSchema, strictSchema, matcher)
	if !changed || !matcher.matches(accumulator.Normalized.ResponseSchema, cleaned) {
		return text
	}
	encoded, err := json.Marshal(cleaned)
	if err != nil {
		return text
	}
	return string(encoded)
}

func removeOptionalNullProperties(value any, schema, strictSchema map[string]any, matcher *schemaMatcher) (any, bool) {
	if matcher.matches(schema, value) {
		return value, false
	}
	strictSchema = unwrapSyntheticNullableSchema(schema, strictSchema, value, matcher)

	for _, key := range []string{"anyOf", "oneOf"} {
		children, _ := schema[key].([]any)
		strictChildren, _ := strictSchema[key].([]any)
		for index, raw := range children {
			child, ok := raw.(map[string]any)
			if !ok || index >= len(strictChildren) {
				continue
			}
			strictChild, ok := strictChildren[index].(map[string]any)
			if !ok || !matcher.matches(strictChild, value) {
				continue
			}
			if cleaned, changed := removeOptionalNullProperties(value, child, strictChild, matcher); changed && matcher.matches(child, cleaned) {
				return cleaned, true
			}
		}
	}

	if object, ok := value.(map[string]any); ok {
		properties, _ := schema["properties"].(map[string]any)
		strictProperties, _ := strictSchema["properties"].(map[string]any)
		required := schemaRequiredNames(schema["required"])
		cleaned := maps.Clone(object)
		changed := false
		for name, raw := range properties {
			child, ok := raw.(map[string]any)
			strictChild, strictOK := strictProperties[name].(map[string]any)
			if !ok || !strictOK {
				continue
			}
			propertyValue, exists := cleaned[name]
			if !exists {
				continue
			}
			if propertyValue == nil && !required[name] && !matcher.matches(child, nil) && matcher.matches(strictChild, nil) {
				delete(cleaned, name)
				changed = true
				continue
			}
			if childValue, childChanged := removeOptionalNullProperties(propertyValue, child, strictChild, matcher); childChanged {
				cleaned[name] = childValue
				changed = true
			}
		}
		if changed && matcher.matches(schema, cleaned) {
			return cleaned, true
		}
	}

	if array, ok := value.([]any); ok {
		items, itemsOK := schema["items"].(map[string]any)
		strictItems, strictOK := strictSchema["items"].(map[string]any)
		if itemsOK && strictOK {
			cleaned := slices.Clone(array)
			changed := false
			for index, item := range cleaned {
				if childValue, childChanged := removeOptionalNullProperties(item, items, strictItems, matcher); childChanged {
					cleaned[index] = childValue
					changed = true
				}
			}
			if changed && matcher.matches(schema, cleaned) {
				return cleaned, true
			}
		}
	}

	return value, false
}

func unwrapSyntheticNullableSchema(schema, strictSchema map[string]any, value any, matcher *schemaMatcher) map[string]any {
	if matcher.matches(schema, nil) || !matcher.matches(strictSchema, nil) {
		return strictSchema
	}
	children, _ := strictSchema["anyOf"].([]any)
	if len(children) != 2 {
		return strictSchema
	}
	nullable, nullableOK := children[1].(map[string]any)
	candidate, candidateOK := children[0].(map[string]any)
	if !nullableOK || len(nullable) != 1 || nullable["type"] != "null" || !candidateOK || !matcher.matches(candidate, value) {
		return strictSchema
	}
	return candidate
}

type schemaMatcher struct {
	compiled map[uintptr]*jsonschema.Schema
}

func newSchemaMatcher() *schemaMatcher {
	return &schemaMatcher{compiled: make(map[uintptr]*jsonschema.Schema)}
}

func (m *schemaMatcher) matches(schema map[string]any, value any) bool {
	key := reflect.ValueOf(schema).Pointer()
	compiled, cached := m.compiled[key]
	if !cached {
		compiled = compileSchema(schema)
		m.compiled[key] = compiled
	}
	return compiled != nil && compiled.Validate(value) == nil
}

func compileSchema(schema map[string]any) *jsonschema.Schema {
	encoded, err := json.Marshal(schema)
	if err != nil {
		return nil
	}
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(encoded))
	if err != nil {
		return nil
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.AssertFormat()
	if err := compiler.AddResource("schema.json", document); err != nil {
		return nil
	}
	compiled, err := compiler.Compile("schema.json")
	if err != nil {
		return nil
	}
	return compiled
}
