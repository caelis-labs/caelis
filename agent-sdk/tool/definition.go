package tool

import (
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/internal/jsonvalue"
)

// Definition is the stable tool declaration exposed to runtimes and model
// providers.
type Definition struct {
	Name        string         `json:"name,omitempty"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema,omitempty"`
	// OutputSchema describes a tool's structured business result, independently
	// of ordered text and media content. It is not a provider input declaration.
	OutputSchema map[string]any `json:"output_schema,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
	// MayCompleteTurn declares that a successful Call can return TurnComplete.
	// Chat executes batches containing such a tool in model order.
	MayCompleteTurn bool `json:"-"`
}

// CloneDefinition returns one deep copy of one definition.
func CloneDefinition(in Definition) Definition {
	out := in
	out.Name = strings.TrimSpace(in.Name)
	out.Description = strings.TrimSpace(in.Description)
	out.InputSchema = jsonvalue.CloneMap(in.InputSchema)
	out.OutputSchema = jsonvalue.CloneMap(in.OutputSchema)
	out.Metadata = jsonvalue.CloneMap(in.Metadata)
	return out
}
