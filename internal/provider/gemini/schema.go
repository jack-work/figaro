package gemini

import (
	"encoding/json"
	"strings"
)

var allowedSchemaKeys = map[string]bool{
	"type":        true,
	"format":      true,
	"description": true,
	"nullable":    true,
	"enum":        true,
	"items":       true,
	"maxItems":    true,
	"minItems":    true,
	"properties":  true,
	"required":    true,
	"minimum":     true,
	"maximum":     true,
	"anyOf":       true,
}

func NormalizeToolParameters(schema any) any {
	if schema == nil {
		return map[string]any{"type": "object", "properties": map[string]any{}}
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		return map[string]any{"type": "object", "properties": map[string]any{}}
	}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return map[string]any{"type": "object", "properties": map[string]any{}}
	}
	defs := extractSchemaDefs(root)
	cleaned := cleanSchemaNode(root, defs, 0)
	if len(cleaned) == 0 {
		return map[string]any{"type": "object", "properties": map[string]any{}}
	}
	if _, hasType := cleaned["type"]; !hasType {
		if _, hasAnyOf := cleaned["anyOf"]; !hasAnyOf {
			if _, hasProps := cleaned["properties"]; hasProps {
				cleaned["type"] = "object"
			}
		}
	}
	return cleaned
}

func extractSchemaDefs(root map[string]any) map[string]map[string]any {
	out := make(map[string]map[string]any)
	for _, key := range []string{"$defs", "definitions"} {
		if m, ok := root[key].(map[string]any); ok {
			for name, def := range m {
				if dm, ok := def.(map[string]any); ok {
					out[name] = dm
				}
			}
		}
	}
	return out
}

func cleanSchemaNode(node map[string]any, defs map[string]map[string]any, depth int) map[string]any {
	if node == nil || depth > 24 {
		return map[string]any{}
	}
	if ref, ok := node["$ref"].(string); ok {
		for _, prefix := range []string{"#/$defs/", "#/definitions/"} {
			if strings.HasPrefix(ref, prefix) {
				name := strings.TrimPrefix(ref, prefix)
				if target, exists := defs[name]; exists {
					resolved := cleanSchemaNode(target, defs, depth+1)
					if desc, ok := node["description"].(string); ok && desc != "" {
						resolved["description"] = desc
					}
					return resolved
				}
			}
		}
	}

	out := make(map[string]any)

	if constVal, hasConst := node["const"]; hasConst {
		out["enum"] = []any{constVal}
	}

	if rawType, hasType := node["type"]; hasType {
		switch tv := rawType.(type) {
		case string:
			if tv == "null" {
				out["type"] = "string"
				out["nullable"] = true
			} else {
				out["type"] = tv
			}
		case []any:
			var nonNull []string
			nullable := false
			for _, elem := range tv {
				s, _ := elem.(string)
				if s == "null" {
					nullable = true
					continue
				}
				if s != "" {
					nonNull = append(nonNull, s)
				}
			}
			if nullable {
				out["nullable"] = true
			}
			if len(nonNull) == 1 {
				out["type"] = nonNull[0]
			} else if len(nonNull) > 1 {
				variants := make([]any, 0, len(nonNull))
				for _, tname := range nonNull {
					variants = append(variants, map[string]any{"type": tname})
				}
				out["anyOf"] = variants
			}
		}
	}

	for _, unionKey := range []string{"anyOf", "oneOf"} {
		if arr, ok := node[unionKey].([]any); ok && len(arr) > 0 {
			var cleanedVariants []any
			nullable := false
			for _, item := range arr {
				im, ok := item.(map[string]any)
				if !ok {
					continue
				}
				if im["type"] == "null" {
					nullable = true
					continue
				}
				cv := cleanSchemaNode(im, defs, depth+1)
				if len(cv) > 0 {
					cleanedVariants = append(cleanedVariants, cv)
				}
			}
			if nullable {
				out["nullable"] = true
			}
			if len(cleanedVariants) == 1 {
				if single, ok := cleanedVariants[0].(map[string]any); ok {
					for k, v := range single {
						if _, exists := out[k]; !exists {
							out[k] = v
						}
					}
				}
			} else if len(cleanedVariants) > 1 {
				out["anyOf"] = cleanedVariants
			}
		}
	}

	if props, ok := node["properties"].(map[string]any); ok {
		cleanProps := make(map[string]any, len(props))
		for propName, propVal := range props {
			if pm, ok := propVal.(map[string]any); ok {
				cleanProps[propName] = cleanSchemaNode(pm, defs, depth+1)
			}
		}
		out["properties"] = cleanProps
		if _, hasType := out["type"]; !hasType {
			out["type"] = "object"
		}
	}

	if items, ok := node["items"].(map[string]any); ok {
		out["items"] = cleanSchemaNode(items, defs, depth+1)
		if _, hasType := out["type"]; !hasType {
			out["type"] = "array"
		}
	}

	if req, ok := node["required"].([]any); ok {
		var reqStrs []string
		propsMap, hasProps := out["properties"].(map[string]any)
		for _, r := range req {
			if s, ok := r.(string); ok && s != "" {
				if !hasProps || propsMap[s] != nil {
					reqStrs = append(reqStrs, s)
				}
			}
		}
		if len(reqStrs) > 0 {
			out["required"] = reqStrs
		}
	}

	if enumVals, ok := node["enum"].([]any); ok && len(enumVals) > 0 {
		var filtered []any
		nullable := false
		for _, ev := range enumVals {
			if ev == nil {
				nullable = true
				continue
			}
			filtered = append(filtered, ev)
		}
		if nullable {
			out["nullable"] = true
		}
		if len(filtered) > 0 {
			out["enum"] = filtered
		}
	}

	for _, key := range []string{"description", "format", "nullable", "minimum", "maximum", "minItems", "maxItems"} {
		if val, exists := node[key]; exists && allowedSchemaKeys[key] {
			out[key] = val
		}
	}

	if out["type"] == "object" {
		if _, hasProps := out["properties"]; !hasProps {
			out["properties"] = map[string]any{}
		}
	}

	return out
}
