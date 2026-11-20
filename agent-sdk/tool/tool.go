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

// Result contains the ordered content produced by one tool invocation.
type Result struct {
	Content []ContentBlock `json:"content,omitempty"`
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
	out.Content = cloneContent(in.Content)
	return out, err
}
