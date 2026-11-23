package tool

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/google/jsonschema-go/jsonschema"
)

// ValidateMCPArguments validates against the exact accepted schema without
// fetching references, applying defaults, or changing the invocation arguments.
func ValidateMCPArguments(def Definition, raw json.RawMessage) error {
	if len(raw) == 0 {
		raw = []byte(`{}`)
	}
	resolved, err := resolveJSONSchema(def.InputSchema)
	if err != nil {
		return fmt.Errorf("invalid MCP schema: %w", err)
	}
	var args map[string]any
	if !json.Valid(raw) {
		return NewError(ErrorCodeInvalidInput, "MCP arguments must be valid JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err = decoder.Decode(&args); err != nil || args == nil {
		return NewError(ErrorCodeInvalidInput, "MCP arguments must be a JSON object")
	}
	instance, err := validationNumbers(args)
	if err != nil {
		return NewError(ErrorCodeInvalidInput, "MCP argument number exceeds the schema validator's numeric range")
	}
	if err = resolved.Validate(instance); err != nil {
		return NewError(ErrorCodeInvalidInput, "MCP arguments do not match the discovered input schema: "+err.Error())
	}
	return nil
}

// ValidateOutput checks a structured JSON value against the declared output
// schema without changing the value or its original number tokens.
func ValidateOutput(schema map[string]any, raw json.RawMessage) error {
	resolved, err := resolveJSONSchema(schema)
	if err != nil {
		return fmt.Errorf("invalid output schema: %w", err)
	}
	if !json.Valid(raw) {
		return fmt.Errorf("output must be valid JSON")
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	value, err = validationNumbers(value)
	if err != nil {
		return err
	}
	return resolved.Validate(value)
}

func resolveJSONSchema(declaration map[string]any) (*jsonschema.Resolved, error) {
	raw, err := json.Marshal(declaration)
	if err != nil {
		return nil, err
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(raw, &schema); err != nil {
		return nil, err
	}
	return schema.Resolve(nil)
}

// The pinned validator classifies json.Number as a string. Convert only this
// detached validation tree; execution and presentation keep the original tokens.
func validationNumbers(value any) (any, error) {
	switch v := value.(type) {
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return n, nil
		}
		if n, err := strconv.ParseUint(string(v), 10, 64); err == nil {
			return n, nil
		}
		return v.Float64()
	case map[string]any:
		for k, item := range v {
			converted, err := validationNumbers(item)
			if err != nil {
				return nil, err
			}
			v[k] = converted
		}
	case []any:
		for i, item := range v {
			converted, err := validationNumbers(item)
			if err != nil {
				return nil, err
			}
			v[i] = converted
		}
	}
	return value, nil
}
