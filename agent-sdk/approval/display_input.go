package approval

import (
	"regexp"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

const sensitiveInputDisplay = "[sensitive field supplied]"

// RedactInputForDisplay removes values explicitly marked writeOnly by the
// owning tool's JSON Schema. Unmarked input is preserved, not claimed safe.
// This copy is presentation evidence; the original Call remains authoritative.
func RedactInputForDisplay(input map[string]any, schema map[string]any) (map[string]any, bool) {
	copy := session.CloneState(input)
	budget := 4096
	value, changed := redactDisplayValue(copy, schema, schema, 0, &budget)
	if out, ok := value.(map[string]any); ok {
		return out, changed
	}
	if changed {
		return map[string]any{"sensitive_input": sensitiveInputDisplay}, true
	}
	return copy, false
}

// InputFieldSensitive reports whether the schema marks this exact field or its
// container writeOnly. It never infers sensitivity from a property name.
func InputFieldSensitive(schema map[string]any, name string) bool {
	_, redacted := RedactInputForDisplay(map[string]any{name: "present"}, schema)
	return redacted
}

func redactDisplayValue(value any, schema, root map[string]any, depth int, budget *int) (any, bool) {
	if !spendDisplayWork(budget) {
		return sensitiveInputDisplay, true
	}
	if schema == nil {
		return value, false
	}
	if text, ok := value.(string); ok && text == sensitiveInputDisplay {
		return value, true
	}
	if marked, _ := schema["writeOnly"].(bool); marked {
		return sensitiveInputDisplay, true
	}
	// Cyclic or excessively deep annotated schemas cannot safely classify a
	// display value. Omit it rather than descending without a bound.
	if depth > 64 {
		return sensitiveInputDisplay, true
	}
	changed := false
	if ref, _ := schema["$ref"].(string); strings.HasPrefix(ref, "#/") {
		var node any = root
		for _, part := range strings.Split(ref[2:], "/") {
			if !spendDisplayWork(budget) {
				return sensitiveInputDisplay, true
			}
			object, _ := node.(map[string]any)
			part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
			node = object[part]
		}
		if target, ok := node.(map[string]any); ok {
			var redacted bool
			value, redacted = redactDisplayValue(value, target, root, depth+1, budget)
			changed = changed || redacted
			if text, ok := value.(string); ok && text == sensitiveInputDisplay {
				return value, true
			}
		}
	}
	for _, key := range []string{"allOf", "anyOf", "oneOf"} {
		branches, _ := schema[key].([]any)
		for _, branch := range branches {
			if !spendDisplayWork(budget) {
				return sensitiveInputDisplay, true
			}
			if child, ok := branch.(map[string]any); ok {
				var redacted bool
				value, redacted = redactDisplayValue(value, child, root, depth+1, budget)
				changed = changed || redacted
				if text, ok := value.(string); ok && text == sensitiveInputDisplay {
					return value, true
				}
			}
		}
	}
	// Both conditional outcomes are considered for display redaction. This is
	// deliberately conservative; it does not evaluate or authorize the tool.
	for _, key := range []string{"then", "else"} {
		if child, ok := schema[key].(map[string]any); ok {
			var redacted bool
			value, redacted = redactDisplayValue(value, child, root, depth+1, budget)
			changed = changed || redacted
			if text, ok := value.(string); ok && text == sensitiveInputDisplay {
				return value, true
			}
		}
	}
	switch typed := value.(type) {
	case map[string]any:
		properties, _ := schema["properties"].(map[string]any)
		patterns, _ := schema["patternProperties"].(map[string]any)
		additional, _ := schema["additionalProperties"].(map[string]any)
		unevaluated, _ := schema["unevaluatedProperties"].(map[string]any)
		dependent, _ := schema["dependentSchemas"].(map[string]any)
		for name, raw := range dependent {
			if !spendDisplayWork(budget) {
				return sensitiveInputDisplay, true
			}
			if _, present := typed[name]; present {
				if child, ok := raw.(map[string]any); ok {
					var redacted bool
					value, redacted = redactDisplayValue(value, child, root, depth+1, budget)
					changed = changed || redacted
					if text, ok := value.(string); ok && text == sensitiveInputDisplay {
						return value, true
					}
				}
			}
		}
		for key, field := range typed {
			child, _ := properties[key].(map[string]any)
			field, redacted := redactDisplayValue(field, child, root, depth+1, budget)
			changed = changed || redacted
			if *budget <= 0 {
				return sensitiveInputDisplay, true
			}
			matched := child != nil
			for pattern, raw := range patterns {
				// Unmatched patterns still perform work. Charge before compiling
				// and matching so wide inputs and schemas share the same bound.
				if !spendDisplayWork(budget) {
					return sensitiveInputDisplay, true
				}
				matcher, err := regexp.Compile(pattern)
				if err != nil {
					// An unsupported annotated pattern cannot safely classify its
					// object. Do not copy that object's possibly marked values.
					return sensitiveInputDisplay, true
				}
				if matcher.MatchString(key) {
					matched = true
					child, _ := raw.(map[string]any)
					field, redacted = redactDisplayValue(field, child, root, depth+1, budget)
					changed = changed || redacted
				}
			}
			if !matched {
				for _, child := range []map[string]any{additional, unevaluated} {
					field, redacted = redactDisplayValue(field, child, root, depth+1, budget)
					changed = changed || redacted
				}
			}
			typed[key] = field
		}
	case []any:
		items, _ := schema["items"].(map[string]any)
		prefix, _ := schema["prefixItems"].([]any)
		for i, field := range typed {
			child := items
			if i < len(prefix) {
				child, _ = prefix[i].(map[string]any)
			}
			redactedValue, redacted := redactDisplayValue(field, child, root, depth+1, budget)
			typed[i] = redactedValue
			changed = changed || redacted
			if *budget <= 0 {
				return sensitiveInputDisplay, true
			}
		}
	}
	return value, changed
}

func spendDisplayWork(budget *int) bool {
	if *budget <= 0 {
		return false
	}
	*budget--
	return true
}
