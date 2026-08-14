package mcp

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

const (
	maxMCPDefinitionNameRunes = 64
	maxMCPPluginIDRunes       = 128
	maxMCPServerNameRunes     = 128
	maxMCPRemoteToolNameRunes = 256
	maxMCPDescriptionRunes    = 1024
	maxMCPSchemaBytes         = 32 << 10
	maxMCPSchemaDepth         = 16
	maxMCPSchemaProperties    = 256
	maxMCPEnumValues          = 128
	maxMCPUnionBranches       = 16
	maxMCPToolPromptTokens    = 2048
	maxMCPToolsPerServer      = 256
	maxMCPToolsPerManager     = 512
	maxMCPWarningsPerServer   = 32
)

func normalizeListedToolDefinition(def tool.Definition, rawSchema ...any) (tool.Definition, string, error) {
	def = tool.CloneDefinition(def)
	def.Name = strings.TrimSpace(def.Name)
	if err := validateMCPIdentity("projected tool name", def.Name, maxMCPDefinitionNameRunes); err != nil {
		return tool.Definition{}, "", err
	}
	for _, item := range []struct {
		key   string
		label string
		limit int
	}{
		{tool.MetadataPluginID, "plugin id", maxMCPPluginIDRunes},
		{tool.MetadataMCPServer, "server name", maxMCPServerNameRunes},
		{tool.MetadataMCPTool, "remote tool name", maxMCPRemoteToolNameRunes},
	} {
		value, exists := def.Metadata[item.key]
		if !exists {
			continue
		}
		text, ok := value.(string)
		if !ok {
			return tool.Definition{}, "", fmt.Errorf("%s metadata must be a string", item.label)
		}
		text = strings.TrimSpace(text)
		if err := validateMCPIdentity(item.label, text, item.limit); err != nil {
			return tool.Definition{}, "", err
		}
		def.Metadata[item.key] = text
	}
	def.Description = strings.TrimSpace(def.Description)
	if !strings.HasPrefix(def.Description, tool.ExternalCapabilityDescriptionPrefix) {
		def.Description = strings.TrimSpace(tool.ExternalCapabilityDescriptionPrefix + " " + def.Description)
	}
	if def.Metadata == nil {
		def.Metadata = map[string]any{}
	}
	def.Metadata[tool.MetadataExternalCapability] = true
	def.Metadata[tool.MetadataDescriptionAuthority] = tool.MetadataAuthorityNonAuthorizing
	var warning string
	if utf8.RuneCountInString(def.Description) > maxMCPDescriptionRunes {
		def.Description = truncateRunes(def.Description, maxMCPDescriptionRunes)
		warning = fmt.Sprintf("description truncated to %d characters", maxMCPDescriptionRunes)
	}

	var schemaValue any = def.InputSchema
	if len(rawSchema) > 0 {
		schemaValue = rawSchema[0]
	}
	schema, err := normalizeMCPInputSchema(schemaValue)
	if err != nil {
		return tool.Definition{}, warning, err
	}
	def.InputSchema = schema
	if cost := tool.EstimateDefinitionPromptTokens(def); cost > maxMCPToolPromptTokens {
		return tool.Definition{}, warning, fmt.Errorf("projected definition costs %d tokens; maximum is %d", cost, maxMCPToolPromptTokens)
	}
	discovered := tool.NewToolSearchResult([]tool.Definition{def})
	if cost := tool.EstimateToolSearchResultPromptTokens(discovered); cost > maxMCPToolPromptTokens {
		return tool.Definition{}, warning, fmt.Errorf("projected discovery result costs %d tokens; maximum is %d", cost, maxMCPToolPromptTokens)
	}
	return def, warning, nil
}

func validateMCPIdentity(label, value string, limit int) error {
	count := utf8.RuneCountInString(strings.TrimSpace(value))
	if count == 0 {
		return fmt.Errorf("%s is required", label)
	}
	if count > limit {
		return fmt.Errorf("%s is %d characters; maximum is %d", label, count, limit)
	}
	return nil
}

func normalizeMCPInputSchema(schema any) (map[string]any, error) {
	if schema == nil {
		return map[string]any{"type": "object"}, nil
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		return nil, fmt.Errorf("input schema is not JSON-serializable: %w", err)
	}
	if len(raw) > maxMCPSchemaBytes {
		return nil, fmt.Errorf("input schema is %d bytes; maximum is %d", len(raw), maxMCPSchemaBytes)
	}
	var cloned map[string]any
	if err := json.Unmarshal(raw, &cloned); err != nil || cloned == nil {
		return nil, fmt.Errorf("input schema must be a JSON object")
	}
	if rawType, exists := cloned["type"]; !exists {
		cloned["type"] = "object"
	} else {
		schemaType, ok := rawType.(string)
		if !ok {
			return nil, fmt.Errorf("input schema top-level type must be the string %q", "object")
		}
		if schemaType != "object" {
			return nil, fmt.Errorf("input schema top-level type %q is not object", schemaType)
		}
	}
	if normalizedRaw, err := json.Marshal(cloned); err != nil || len(normalizedRaw) > maxMCPSchemaBytes {
		return nil, fmt.Errorf("normalized input schema exceeds %d bytes", maxMCPSchemaBytes)
	}
	propertyCount := 0
	if err := validateMCPSchemaNode(cloned, 1, &propertyCount, "input schema"); err != nil {
		return nil, err
	}
	return cloned, nil
}

func validateMCPSchemaNode(schema map[string]any, depth int, propertyCount *int, path string) error {
	if depth > maxMCPSchemaDepth {
		return fmt.Errorf("input schema depth exceeds %d at %s", maxMCPSchemaDepth, path)
	}
	for keyword, value := range schema {
		switch keyword {
		case "type":
			if err := validateMCPSchemaType(value, path+".type"); err != nil {
				return err
			}
		case "properties":
			properties, ok := value.(map[string]any)
			if !ok {
				return fmt.Errorf("%s.properties must be an object", path)
			}
			*propertyCount += len(properties)
			if *propertyCount > maxMCPSchemaProperties {
				return fmt.Errorf("input schema properties exceed %d", maxMCPSchemaProperties)
			}
			for name, raw := range properties {
				child, ok := raw.(map[string]any)
				if !ok {
					return fmt.Errorf("%s.properties[%q] must be a schema object", path, name)
				}
				if err := validateMCPSchemaNode(child, depth+1, propertyCount, path+".properties["+name+"]"); err != nil {
					return err
				}
			}
		case "required":
			if err := validateMCPStringArray(value, path+".required", 0); err != nil {
				return err
			}
		case "items":
			child, ok := value.(map[string]any)
			if !ok {
				return fmt.Errorf("%s.items must be a schema object", path)
			}
			if err := validateMCPSchemaNode(child, depth+1, propertyCount, path+".items"); err != nil {
				return err
			}
		case "additionalProperties":
			switch typed := value.(type) {
			case bool:
			case map[string]any:
				if err := validateMCPSchemaNode(typed, depth+1, propertyCount, path+".additionalProperties"); err != nil {
					return err
				}
			default:
				return fmt.Errorf("%s.additionalProperties must be a boolean or schema object", path)
			}
		case "enum":
			values, ok := value.([]any)
			if !ok || len(values) == 0 {
				return fmt.Errorf("%s.enum must be a non-empty array", path)
			}
			if len(values) > maxMCPEnumValues {
				return fmt.Errorf("input schema enum values exceed %d", maxMCPEnumValues)
			}
		case "const", "default", "example":
			// The complete schema was JSON round-tripped before validation, so these
			// business values are already safe to preserve without reinterpretation.
		case "examples":
			if _, ok := value.([]any); !ok {
				return fmt.Errorf("%s.examples must be an array", path)
			}
		case "description", "title", "$comment", "$id", "$schema", "format", "pattern":
			if _, ok := value.(string); !ok {
				return fmt.Errorf("%s.%s must be a string", path, keyword)
			}
		case "readOnly", "writeOnly", "deprecated", "uniqueItems":
			if _, ok := value.(bool); !ok {
				return fmt.Errorf("%s.%s must be a boolean", path, keyword)
			}
		case "minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum":
			if !validMCPNumber(value, false) {
				return fmt.Errorf("%s.%s must be a finite number", path, keyword)
			}
		case "multipleOf":
			if !validMCPNumber(value, true) {
				return fmt.Errorf("%s.multipleOf must be a positive finite number", path)
			}
		case "minLength", "maxLength", "minItems", "maxItems", "minProperties", "maxProperties":
			if !validMCPNonNegativeInteger(value) {
				return fmt.Errorf("%s.%s must be a non-negative integer", path, keyword)
			}
		case "allOf", "anyOf", "oneOf":
			branches, ok := value.([]any)
			if !ok || len(branches) == 0 {
				return fmt.Errorf("%s.%s must be a non-empty array", path, keyword)
			}
			if len(branches) > maxMCPUnionBranches {
				return fmt.Errorf("input schema %s branches exceed %d", keyword, maxMCPUnionBranches)
			}
			for index, raw := range branches {
				child, ok := raw.(map[string]any)
				if !ok {
					return fmt.Errorf("%s.%s[%d] must be a schema object", path, keyword, index)
				}
				if err := validateMCPSchemaNode(child, depth+1, propertyCount, fmt.Sprintf("%s.%s[%d]", path, keyword, index)); err != nil {
					return err
				}
			}
		case "not", "if", "then", "else":
			child, ok := value.(map[string]any)
			if !ok {
				return fmt.Errorf("%s.%s must be a schema object", path, keyword)
			}
			if err := validateMCPSchemaNode(child, depth+1, propertyCount, path+"."+keyword); err != nil {
				return err
			}
		case "dependentRequired":
			dependencies, ok := value.(map[string]any)
			if !ok {
				return fmt.Errorf("%s.dependentRequired must be an object", path)
			}
			for name, raw := range dependencies {
				if err := validateMCPStringArray(raw, path+".dependentRequired["+name+"]", 0); err != nil {
					return err
				}
			}
		case "dependentSchemas":
			dependencies, ok := value.(map[string]any)
			if !ok {
				return fmt.Errorf("%s.dependentSchemas must be an object", path)
			}
			for name, raw := range dependencies {
				child, ok := raw.(map[string]any)
				if !ok {
					return fmt.Errorf("%s.dependentSchemas[%q] must be a schema object", path, name)
				}
				if err := validateMCPSchemaNode(child, depth+1, propertyCount, path+".dependentSchemas["+name+"]"); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("%s contains unsupported keyword %q", path, keyword)
		}
	}
	return nil
}

func validateMCPSchemaType(value any, path string) error {
	allowed := map[string]bool{"array": true, "boolean": true, "integer": true, "null": true, "number": true, "object": true, "string": true}
	values := []any{value}
	if array, ok := value.([]any); ok {
		if len(array) == 0 {
			return fmt.Errorf("%s must not be empty", path)
		}
		values = array
	}
	seen := map[string]bool{}
	for _, raw := range values {
		text, ok := raw.(string)
		text = strings.ToLower(strings.TrimSpace(text))
		if !ok || !allowed[text] || seen[text] {
			return fmt.Errorf("%s must contain unique maintained JSON Schema types", path)
		}
		seen[text] = true
	}
	return nil
}

func validateMCPStringArray(value any, path string, maxValues int) error {
	values, ok := value.([]any)
	if !ok {
		return fmt.Errorf("%s must be an array of strings", path)
	}
	if maxValues > 0 && len(values) > maxValues {
		return fmt.Errorf("%s exceeds %d values", path, maxValues)
	}
	seen := map[string]bool{}
	for _, raw := range values {
		text, ok := raw.(string)
		if !ok || strings.TrimSpace(text) == "" || seen[text] {
			return fmt.Errorf("%s must contain unique non-empty strings", path)
		}
		seen[text] = true
	}
	return nil
}

func validMCPNumber(value any, positive bool) bool {
	number, ok := value.(float64)
	return ok && !math.IsNaN(number) && !math.IsInf(number, 0) && (!positive || number > 0)
}

func validMCPNonNegativeInteger(value any) bool {
	number, ok := value.(float64)
	return ok && number >= 0 && number == math.Trunc(number)
}

func truncateRunes(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return strings.TrimSpace(string(runes[:limit]))
}
