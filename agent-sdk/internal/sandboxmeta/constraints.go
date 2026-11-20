// Package sandboxmeta owns the SDK's tool-call encoding of sandbox constraints.
package sandboxmeta

import (
	"encoding/json"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
)

// ConstraintsKey is the tool-call metadata key for policy-provided constraints.
const ConstraintsKey = "sandbox_constraints"

// Constraints decodes a typed or JSON-compatible constraint set. The boolean
// distinguishes an explicitly decoded zero value from missing or invalid metadata
// so the execution path can select its existing default.
func Constraints(metadata map[string]any) (sandbox.Constraints, bool) {
	raw := metadata[ConstraintsKey]
	if raw == nil {
		return sandbox.Constraints{}, false
	}
	if typed, ok := raw.(sandbox.Constraints); ok {
		return sandbox.NormalizeConstraints(typed), true
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return sandbox.Constraints{}, false
	}
	var constraints sandbox.Constraints
	if err := json.Unmarshal(encoded, &constraints); err != nil {
		return sandbox.Constraints{}, false
	}
	return sandbox.NormalizeConstraints(constraints), true
}
