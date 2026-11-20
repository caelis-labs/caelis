package tool

import (
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/internal/jsonvalue"
	"github.com/caelis-labs/caelis/agent-sdk/model"
)

// ModelSpecs projects the supplied tools into provider-neutral specs in input
// order, skipping nil and unnamed tools. Callers choose which tools to expose.
func ModelSpecs(tools []Tool) []model.ToolSpec {
	defs := make([]Definition, 0, len(tools))
	for _, one := range tools {
		if one == nil {
			continue
		}
		if def := one.Definition(); def.Name != "" {
			defs = append(defs, def)
		}
	}
	return modelSpecsFromDefinitions(defs)
}

func modelSpecsFromDefinitions(definitions []Definition) []model.ToolSpec {
	if len(definitions) == 0 {
		return nil
	}
	out := make([]model.ToolSpec, 0, len(definitions))
	for _, def := range definitions {
		spec := model.NewFunctionToolSpec(
			strings.TrimSpace(def.Name),
			strings.TrimSpace(def.Description),
			jsonvalue.CloneMap(def.InputSchema),
		)
		if spec.Function != nil {
			spec.Function.Strict = strictCompatibleSchema(def.InputSchema)
		}
		out = append(out, spec)
	}
	return out
}

func strictCompatibleSchema(schema map[string]any) bool {
	if len(schema) == 0 {
		return false
	}
	// Strict function-tool dialects only accept a JSON Schema subset. Keep
	// conditional contracts in the canonical schema, but do not advertise them
	// as provider-strict until a serializer proves it can preserve the complete
	// accepted set.
	for _, keyword := range []string{"allOf", "oneOf", "not", "if", "then", "else", "dependentRequired"} {
		if _, ok := schema[keyword]; ok {
			return false
		}
	}
	if _, ok := schema["anyOf"]; ok {
		return strictCompatibleSchemaAnyOf(schema["anyOf"])
	}
	switch schemaPrimaryType(schema["type"]) {
	case "object":
		if additionalProperties, ok := schema["additionalProperties"].(bool); !ok || additionalProperties {
			return false
		}
		properties, _ := schema["properties"].(map[string]any)
		for _, value := range properties {
			nested, _ := value.(map[string]any)
			if len(nested) == 0 || !strictCompatibleSchema(nested) {
				return false
			}
		}
		return true
	case "array":
		items, ok := schema["items"]
		if !ok || items == nil {
			return true
		}
		nested, _ := items.(map[string]any)
		return len(nested) > 0 && strictCompatibleSchema(nested)
	case "string", "integer", "number", "boolean", "null":
		return true
	default:
		return false
	}
}

func strictCompatibleSchemaAnyOf(value any) bool {
	variants, ok := value.([]any)
	if !ok || len(variants) == 0 {
		return false
	}
	for _, variant := range variants {
		nested, _ := variant.(map[string]any)
		if len(nested) == 0 || !strictCompatibleSchema(nested) {
			return false
		}
	}
	return true
}

func schemaPrimaryType(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.ToLower(strings.TrimSpace(typed))
	case []string:
		return primaryTypeFromStrings(typed)
	case []any:
		values := make([]string, 0, len(typed))
		for _, item := range typed {
			text, _ := item.(string)
			values = append(values, text)
		}
		return primaryTypeFromStrings(values)
	default:
		return ""
	}
}

func primaryTypeFromStrings(values []string) string {
	primary := ""
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" || value == "null" {
			continue
		}
		if primary != "" && primary != value {
			return ""
		}
		primary = value
	}
	return primary
}
