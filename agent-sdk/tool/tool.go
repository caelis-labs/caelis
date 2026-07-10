package tool

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/internal/jsonvalue"
	"github.com/caelis-labs/caelis/agent-sdk/model"
)

// Definition is the stable tool declaration exposed to runtimes and model
// providers.
type Definition struct {
	Name        string         `json:"name,omitempty"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
	EffectClass EffectClass    `json:"effect_class,omitempty"`
}

type EffectClass string

const (
	EffectReadOnly      EffectClass = "read_only"
	EffectIdempotent    EffectClass = "idempotent"
	EffectNonIdempotent EffectClass = "non_idempotent"
)

type RecoveryStatus string

const (
	RecoveryUnknown   RecoveryStatus = "unknown"
	RecoverySucceeded RecoveryStatus = "succeeded"
	RecoveryFailed    RecoveryStatus = "failed"
)

type RecoveryRequest struct {
	ExecutionIdentity string `json:"execution_identity"`
	Call              Call   `json:"call"`
}

type RecoveryResult struct {
	Status RecoveryStatus `json:"status"`
	Result Result         `json:"result,omitempty"`
	Reason string         `json:"reason,omitempty"`
}

// Recoverer optionally reconciles an execution whose side-effect outcome is
// unknown. Runtime never replays the original Call automatically.
type Recoverer interface {
	Recover(context.Context, RecoveryRequest) (RecoveryResult, error)
}

const (
	MetadataToolKind            = "caelis.tool.kind"
	MetadataPluginID            = "caelis.plugin.id"
	MetadataMCPServer           = "caelis.mcp.server"
	MetadataMCPTool             = "caelis.mcp.tool"
	MetadataDiscoveredToolNames = "caelis.tool.discovered_names"
	MetadataExecutionJournal    = "caelis.execution_journal"

	MetadataToolKindMCP        = "mcp"
	MetadataToolKindToolSearch = "tool_search"

	ToolSearchToolName = "tool_search"
)

// Call is one provider-neutral tool invocation.
type Call struct {
	ID           string          `json:"id,omitempty"`
	Name         string          `json:"name,omitempty"`
	Input        json.RawMessage `json:"input,omitempty"`
	Metadata     map[string]any  `json:"metadata,omitempty"`
	RuntimeModel model.LLM       `json:"-"`
	Observer     Observer        `json:"-"`
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

// RuntimeModel returns the turn-local model associated with a call, when the
// caller is an agent runtime that can provide one. It is intentionally outside
// Metadata so it cannot leak into serialized tool calls.
func RuntimeModel(call Call) (model.LLM, bool) {
	if call.RuntimeModel == nil {
		return nil, false
	}
	return call.RuntimeModel, true
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

// ModelSpecs converts the default model-visible tool set into provider-neutral
// specs. Deferred tools remain hidden until ToolVisibility reveals them.
func ModelSpecs(tools []Tool) []model.ToolSpec {
	return NewToolVisibility(tools).ModelSpecs()
}

// AllModelSpecs converts every tool definition into provider-neutral model
// specs without deferred-tool filtering.
func AllModelSpecs(tools []Tool) []model.ToolSpec {
	definitions := Definitions(tools)
	return modelSpecsFromDefinitions(definitions)
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
			spec.Function.Strict = inferStrictFunctionSchema(def.InputSchema)
		}
		out = append(out, spec)
	}
	return out
}

// CanonicalName normalizes a tool name for case-insensitive internal lookup.
func CanonicalName(name string) string {
	return strings.ToUpper(strings.TrimSpace(name))
}

func IsMCPDefinition(def Definition) bool {
	return definitionKind(def) == MetadataToolKindMCP
}

func IsToolSearchDefinition(def Definition) bool {
	return definitionKind(def) == MetadataToolKindToolSearch &&
		strings.EqualFold(strings.TrimSpace(def.Name), ToolSearchToolName)
}

func definitionKind(def Definition) string {
	kind, _ := def.Metadata[MetadataToolKind].(string)
	return strings.ToLower(strings.TrimSpace(kind))
}

// EffectClassOf resolves an explicit effect declaration and conservatively
// falls back to standard annotation hints. Unknown tools are non-idempotent.
func EffectClassOf(def Definition) EffectClass {
	switch def.EffectClass {
	case EffectReadOnly, EffectIdempotent, EffectNonIdempotent:
		return def.EffectClass
	}
	annotations, _ := def.Metadata["annotations"].(map[string]any)
	if value, _ := annotations["readOnlyHint"].(bool); value {
		return EffectReadOnly
	}
	if value, _ := annotations["idempotentHint"].(bool); value {
		return EffectIdempotent
	}
	return EffectNonIdempotent
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

// CloneDefinition returns one deep copy of one definition.
func CloneDefinition(in Definition) Definition {
	out := in
	out.Name = strings.TrimSpace(in.Name)
	out.Description = strings.TrimSpace(in.Description)
	out.InputSchema = jsonvalue.CloneMap(in.InputSchema)
	out.Metadata = jsonvalue.CloneMap(in.Metadata)
	return out
}

// CloneCall returns one copy of one tool call.
func CloneCall(in Call) Call {
	out := in
	out.Name = strings.TrimSpace(in.Name)
	out.Input = append(json.RawMessage(nil), in.Input...)
	out.Metadata = jsonvalue.CloneMap(in.Metadata)
	return out
}

// CloneResult returns one copy of one tool result.
func CloneResult(in Result, err error) (Result, error) {
	out := in
	out.Name = strings.TrimSpace(in.Name)
	out.Content = slices.Clone(in.Content)
	out.Meta = jsonvalue.CloneMap(in.Meta)
	out.Metadata = jsonvalue.CloneMap(in.Metadata)
	return out, err
}
