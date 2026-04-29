package tool

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strings"

	sdkmodel "github.com/OnslaughtSnail/caelis/sdk/model"
)

// Definition is the stable tool declaration exposed to runtimes and model
// providers.
type Definition struct {
	Name        string         `json:"name,omitempty"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

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
	ID       string          `json:"id,omitempty"`
	Name     string          `json:"name,omitempty"`
	Content  []sdkmodel.Part `json:"content,omitempty"`
	Meta     map[string]any  `json:"meta,omitempty"`
	IsError  bool            `json:"is_error,omitempty"`
	Metadata map[string]any  `json:"metadata,omitempty"`
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
func ModelSpecs(tools []Tool) []sdkmodel.ToolSpec {
	definitions := Definitions(tools)
	if len(definitions) == 0 {
		return nil
	}
	out := make([]sdkmodel.ToolSpec, 0, len(definitions))
	for _, def := range definitions {
		out = append(out, sdkmodel.NewFunctionToolSpec(
			strings.TrimSpace(def.Name),
			strings.TrimSpace(def.Description),
			maps.Clone(def.InputSchema),
		))
	}
	return out
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
