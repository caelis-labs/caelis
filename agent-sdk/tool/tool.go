package tool

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/internal/jsonvalue"
)

// Tool is the minimal tool execution contract. Call reports failure through
// its error return. A failed call may include a diagnostic result.
type Tool interface {
	Definition() Definition
	Call(context.Context, Call) (Result, error)
}

// Call is one provider-neutral tool invocation.
type Call struct {
	ID       string          `json:"id,omitempty"`
	Name     string          `json:"name,omitempty"`
	Input    json.RawMessage `json:"input,omitempty"`
	Metadata map[string]any  `json:"metadata,omitempty"`
}

// Result is one provider-neutral tool execution result.
type Result struct {
	ID      string         `json:"id,omitempty"`
	Name    string         `json:"name,omitempty"`
	Content []ContentBlock `json:"content,omitempty"`
	// TurnComplete is a trusted host tool disposition. A completed result stops
	// this Run before another serial tool effect or model request. Tools that
	// return it must declare MayCompleteTurn in their Definition.
	TurnComplete bool           `json:"turn_complete,omitempty"`
	Meta         map[string]any `json:"meta,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
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
	out.Content = cloneContent(in.Content)
	out.Meta = jsonvalue.CloneMap(in.Meta)
	out.Metadata = jsonvalue.CloneMap(in.Metadata)
	return out, err
}
