package translate

import (
	"maps"

	"chatgpt-codex-proxy/internal/jsonutil"
)

var (
	schemaMapChildKeys    = []string{"properties", "patternProperties", "$defs", "definitions"}
	schemaSliceChildKeys  = []string{"oneOf", "anyOf", "allOf"}
	schemaDirectChildKeys = []string{"if", "then", "else", "not"}
)

func PrepareSchema(schema map[string]any) (prepared map[string]any, tupleSchema map[string]any) {
	cloned := jsonutil.CloneMap(schema)
	if !hasTupleSchemas(cloned) {
		return injectAdditionalProperties(cloned), nil
	}

	original := jsonutil.CloneMap(schema)
	convertTupleSchemas(cloned)
	return injectAdditionalProperties(cloned), original
}

func NormalizeSchema(schema map[string]any) map[string]any {
	if len(schema) == 0 {
		return schema
	}
	cloned := jsonutil.CloneMap(schema)
	defs := schemaDefinitions(cloned)
	resolved := resolveLocalRefs(cloned, defs, nil)
	delete(resolved, "$defs")
	delete(resolved, "definitions")
	return injectAdditionalProperties(resolved)
}

func injectAdditionalProperties(node map[string]any) map[string]any {
	if node["type"] == "object" {
		if _, ok := node["properties"]; !ok {
			node["properties"] = map[string]any{}
		}
		if _, ok := node["additionalProperties"]; !ok {
			node["additionalProperties"] = false
		}
	}

	forEachSchemaChild(node, func(child map[string]any) {
		injectAdditionalProperties(child)
	})

	return node
}

func schemaDefinitions(schema map[string]any) map[string]map[string]any {
	defs := make(map[string]map[string]any)
	for _, key := range []string{"$defs", "definitions"} {
		rawDefs, ok := schema[key].(map[string]any)
		if !ok {
			continue
		}
		for name, raw := range rawDefs {
			definition, ok := raw.(map[string]any)
			if ok {
				defs["#/"+key+"/"+name] = definition
			}
		}
	}
	return defs
}

func resolveLocalRefs(node map[string]any, defs map[string]map[string]any, resolving map[string]bool) map[string]any {
	ref, _ := node["$ref"].(string)
	if ref != "" {
		if resolving[ref] {
			return node
		}
		definition, ok := defs[ref]
		if !ok {
			return node
		}
		nextResolving := maps.Clone(resolving)
		if nextResolving == nil {
			nextResolving = map[string]bool{}
		}
		nextResolving[ref] = true
		resolved := jsonutil.CloneMap(definition)
		if resolved == nil {
			return node
		}
		resolved = resolveLocalRefs(resolved, defs, nextResolving)
		resolving = nextResolving
		for key, value := range node {
			if key != "$ref" {
				resolved[key] = value
			}
		}
		node = resolved
	}

	updateSchemaChildren(node, func(child map[string]any) map[string]any {
		return resolveLocalRefs(child, defs, resolving)
	})

	return node
}

func forEachSchemaChild(node map[string]any, visit func(map[string]any)) {
	updateSchemaChildren(node, func(child map[string]any) map[string]any {
		visit(child)
		return child
	})
}

func updateSchemaChildren(node map[string]any, update func(map[string]any) map[string]any) {
	for _, key := range schemaMapChildKeys {
		children, ok := node[key].(map[string]any)
		if !ok {
			continue
		}
		for name, raw := range children {
			if child, ok := raw.(map[string]any); ok {
				children[name] = update(child)
			}
		}
	}

	if items, ok := node["items"].(map[string]any); ok {
		node["items"] = update(items)
	}

	if prefixItems, ok := node["prefixItems"].([]any); ok {
		for index, raw := range prefixItems {
			if child, ok := raw.(map[string]any); ok {
				prefixItems[index] = update(child)
			}
		}
	}

	for _, key := range schemaSliceChildKeys {
		entries, ok := node[key].([]any)
		if !ok {
			continue
		}
		for index, raw := range entries {
			if child, ok := raw.(map[string]any); ok {
				entries[index] = update(child)
			}
		}
	}

	for _, key := range schemaDirectChildKeys {
		if child, ok := node[key].(map[string]any); ok {
			node[key] = update(child)
		}
	}
}

func anySchemaChild(node map[string]any, match func(map[string]any) bool) bool {
	found := false
	forEachSchemaChild(node, func(child map[string]any) {
		if found {
			return
		}
		found = match(child)
	})
	return found
}
