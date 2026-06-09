package tool

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/model"
)

// Definition is the stable tool declaration exposed to runtimes and model
// providers.
type Definition struct {
	Name        string         `json:"name,omitempty"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

const (
	MetadataToolKind  = "caelis.tool.kind"
	MetadataPluginID  = "caelis.plugin.id"
	MetadataMCPServer = "caelis.mcp.server"

	MetadataToolKindMCP = "mcp"
)

// Call is one provider-neutral tool invocation.
type Call struct {
	ID       string          `json:"id,omitempty"`
	Name     string          `json:"name,omitempty"`
	Input    json.RawMessage `json:"input,omitempty"`
	Metadata map[string]any  `json:"metadata,omitempty"`
	Observer Observer        `json:"-"`
}

// Result is one provider-neutral tool execution result.
type Result struct {
	ID       string         `json:"id,omitempty"`
	Name     string         `json:"name,omitempty"`
	Content  []model.Part   `json:"content,omitempty"`
	Meta     map[string]any `json:"meta,omitempty"`
	IsError  bool           `json:"is_error,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// Tool is the minimal tool execution contract.
type Tool interface {
	Definition() Definition
	Call(context.Context, Call) (Result, error)
}

// Observer receives transient tool updates emitted before the model-visible
// final result is available. Observed results are UI-only and must not be
// appended to model-visible tool history.
type Observer interface {
	ObserveToolResult(Result)
}

// Registry is the minimal tool lookup boundary used by future runtimes.
type Registry interface {
	List(context.Context) ([]Tool, error)
	Lookup(context.Context, string) (Tool, bool, error)
}

// NamedTool is one lightweight adapter for tools that expose one static
// definition and one call function.
type NamedTool struct {
	Def    Definition
	Invoke func(context.Context, Call) (Result, error)
}

func (t NamedTool) Definition() Definition {
	return CloneDefinition(t.Def)
}

func (t NamedTool) Call(ctx context.Context, call Call) (Result, error) {
	if t.Invoke == nil {
		return Result{}, nil
	}
	return CloneResult(t.Invoke(ctx, CloneCall(call)))
}

// Definitions returns cloned definitions for one tool slice.
func Definitions(tools []Tool) []Definition {
	if len(tools) == 0 {
		return nil
	}
	out := make([]Definition, 0, len(tools))
	for _, item := range tools {
		if item == nil {
			continue
		}
		out = append(out, CloneDefinition(item.Definition()))
	}
	return out
}

// ModelSpecs converts tool definitions into provider-neutral model specs.
func ModelSpecs(tools []Tool) []model.ToolSpec {
	definitions := Definitions(tools)
	if len(definitions) == 0 {
		return nil
	}
	out := make([]model.ToolSpec, 0, len(definitions))
	for _, def := range definitions {
		spec := model.NewFunctionToolSpec(
			strings.TrimSpace(def.Name),
			strings.TrimSpace(def.Description),
			maps.Clone(def.InputSchema),
		)
		if spec.Function != nil {
			spec.Function.Strict = inferStrictFunctionSchema(def.InputSchema)
		}
		out = append(out, spec)
	}
	return out
}

func inferStrictFunctionSchema(schema map[string]any) bool {
	if len(schema) == 0 {
		return false
	}
	return strictCompatibleSchema(schema)
}

func strictCompatibleSchema(schema map[string]any) bool {
	if len(schema) == 0 {
		return false
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

// CloneDefinition returns one deep copy of one definition.
func CloneDefinition(in Definition) Definition {
	out := in
	out.Name = strings.TrimSpace(in.Name)
	out.Description = strings.TrimSpace(in.Description)
	out.InputSchema = maps.Clone(in.InputSchema)
	out.Metadata = maps.Clone(in.Metadata)
	return out
}

// CloneCall returns one copy of one tool call.
func CloneCall(in Call) Call {
	out := in
	out.Name = strings.TrimSpace(in.Name)
	out.Input = append(json.RawMessage(nil), in.Input...)
	out.Metadata = maps.Clone(in.Metadata)
	return out
}

// CloneResult returns one copy of one tool result.
func CloneResult(in Result, err error) (Result, error) {
	out := in
	out.Name = strings.TrimSpace(in.Name)
	out.Content = slices.Clone(in.Content)
	out.Meta = maps.Clone(in.Meta)
	out.Metadata = maps.Clone(in.Metadata)
	return out, err
}
