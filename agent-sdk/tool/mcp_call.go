package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/google/jsonschema-go/jsonschema"
)

const MCPCallToolName = "mcp_call"

// NewMCPCallTool declares the fixed MCP invocation envelope. Runtime resolves
// it to an already discovered real Tool before entering its execution wrappers.
func NewMCPCallTool() Tool {
	return NamedTool{Def: Definition{Name: MCPCallToolName, Description: "Call an MCP tool previously returned by tool_search. Use its exact name and arguments matching its input_schema. Search again after a source or schema change. Undiscovered names cannot be called.", InputSchema: map[string]any{
		"type": "object", "additionalProperties": false, "required": []any{"tool_name", "arguments"}, "properties": map[string]any{
			"tool_name": map[string]any{"type": "string", "minLength": 1}, "arguments": map[string]any{"type": "object", "additionalProperties": true},
		}}, Metadata: map[string]any{MetadataToolKind: MCPCallToolName}}, Invoke: func(_ context.Context, _ Call) (Result, error) {
		return Result{}, NewError(ErrorCodeInvalidInput, "mcp_call requires Runtime discovery and routing")
	}}
}

// WithMCPCallTool adds one fixed invocation declaration beside tool_search.
// The original tool slice and individual tools remain unchanged.
func WithMCPCallTool(tools []Tool) []Tool {
	search, call := false, false
	for _, one := range tools {
		if one != nil {
			search = search || IsToolSearchDefinition(one.Definition())
			call = call || IsMCPCallDefinition(one.Definition())
		}
	}
	if !search || call {
		return tools
	}
	return append(append([]Tool(nil), tools...), NewMCPCallTool())
}

// IsMCPCallDefinition recognizes the Runtime's fixed MCP envelope declaration.
func IsMCPCallDefinition(def Definition) bool {
	return def.Name == MCPCallToolName && definitionKind(def) == MCPCallToolName
}

// ParseMCPCall converts the model envelope into a real call, preserving its ID.
// It performs no lookup and grants no authority to the returned name.
func ParseMCPCall(call model.ToolCall) (model.ToolCall, error) {
	var args struct {
		Name      string          `json:"tool_name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	dec := json.NewDecoder(strings.NewReader(call.Args))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&args); err != nil {
		return model.ToolCall{}, NewError(ErrorCodeInvalidInput, "invalid mcp_call envelope")
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return model.ToolCall{}, NewError(ErrorCodeInvalidInput, "mcp_call requires exactly one JSON object")
	}
	if args.Name == "" || args.Name != strings.TrimSpace(args.Name) || len(args.Arguments) == 0 || bytes.TrimSpace(args.Arguments)[0] != '{' {
		return model.ToolCall{}, NewError(ErrorCodeInvalidInput, "mcp_call requires an exact tool_name and object arguments")
	}
	return model.ToolCall{ID: call.ID, Name: args.Name, Args: string(args.Arguments)}, nil
}

// ValidateMCPArguments validates against the exact accepted schema without
// fetching references, applying defaults, or changing the invocation arguments.
func ValidateMCPArguments(def Definition, raw json.RawMessage) error {
	if len(raw) == 0 {
		raw = []byte(`{}`)
	}
	schemaRaw, err := json.Marshal(def.InputSchema)
	if err != nil {
		return fmt.Errorf("invalid MCP schema: %w", err)
	}
	var schema jsonschema.Schema
	if err = json.Unmarshal(schemaRaw, &schema); err != nil {
		return fmt.Errorf("invalid MCP schema: %w", err)
	}
	resolved, err := schema.Resolve(nil)
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
