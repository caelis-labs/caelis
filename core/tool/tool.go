// Package tool defines the stable tool contract used by the core runtime.
package tool

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strings"

	"github.com/OnslaughtSnail/caelis/core/model"
)

// Definition is the stable tool declaration exposed to runtimes and model
// providers.
type Definition struct {
	Name        string         `json:"name,omitempty"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema,omitempty"`
	Meta        map[string]any `json:"meta,omitempty"`
}

// Call is one provider-neutral tool invocation.
type Call struct {
	ID       string          `json:"id,omitempty"`
	Name     string          `json:"name,omitempty"`
	Input    json.RawMessage `json:"input,omitempty"`
	Meta     map[string]any  `json:"meta,omitempty"`
	Observer Observer        `json:"-"`
}

// Result is one provider-neutral tool execution result. Content is the
// model-visible result; Meta is non-critical display or adapter metadata.
type Result struct {
	ID      string         `json:"id,omitempty"`
	Name    string         `json:"name,omitempty"`
	Content []model.Part   `json:"content,omitempty"`
	IsError bool           `json:"is_error,omitempty"`
	Meta    map[string]any `json:"meta,omitempty"`
}

// Observer receives transient tool updates emitted before a final model-visible
// result is available.
type Observer interface {
	ObserveToolResult(Result)
}

// Tool is the minimal execution contract for model-callable tools.
type Tool interface {
	Definition() Definition
	Call(context.Context, Call) (Result, error)
}

// Registry resolves available tools for an invocation.
type Registry interface {
	List(context.Context) ([]Tool, error)
	Lookup(context.Context, string) (Tool, bool, error)
}

// NamedTool is a lightweight adapter for static tools.
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

func ModelSpecs(tools []Tool) []model.ToolSpec {
	definitions := Definitions(tools)
	if len(definitions) == 0 {
		return nil
	}
	out := make([]model.ToolSpec, 0, len(definitions))
	for _, def := range definitions {
		out = append(out, model.NewFunctionToolSpec(def.Name, def.Description, def.InputSchema))
	}
	return out
}

func CloneDefinition(in Definition) Definition {
	out := in
	out.Name = strings.TrimSpace(in.Name)
	out.Description = strings.TrimSpace(in.Description)
	out.InputSchema = maps.Clone(in.InputSchema)
	out.Meta = maps.Clone(in.Meta)
	return out
}

func CloneCall(in Call) Call {
	out := in
	out.ID = strings.TrimSpace(in.ID)
	out.Name = strings.TrimSpace(in.Name)
	out.Input = slices.Clone(in.Input)
	out.Meta = maps.Clone(in.Meta)
	return out
}

func CloneResult(in Result, err error) (Result, error) {
	out := in
	out.ID = strings.TrimSpace(in.ID)
	out.Name = strings.TrimSpace(in.Name)
	out.Content = model.CloneParts(in.Content)
	out.Meta = maps.Clone(in.Meta)
	return out, err
}
