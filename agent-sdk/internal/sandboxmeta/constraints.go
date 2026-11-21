// Package sandboxmeta owns the SDK's tool-call encoding of sandbox constraints.
package sandboxmeta

import (
	"encoding/json"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
)

// ConstraintsKey is the tool-call metadata key for policy-provided constraints.
const ConstraintsKey = "sandbox_constraints"

// DecodeConstraints decodes a typed or JSON-compatible constraint set. It
// reports whether a value was decoded, including an explicit zero constraint.
// Callers choose whether an invalid value rejects the call or selects defaults.
func DecodeConstraints(metadata map[string]any) (sandbox.Constraints, bool, error) {
	raw := metadata[ConstraintsKey]
	if raw == nil {
		return sandbox.Constraints{}, false, nil
	}
	if typed, ok := raw.(sandbox.Constraints); ok {
		return sandbox.NormalizeConstraints(typed), true, nil
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return sandbox.Constraints{}, false, err
	}
	var constraints sandbox.Constraints
	if err := json.Unmarshal(encoded, &constraints); err != nil {
		return sandbox.Constraints{}, false, err
	}
	return sandbox.NormalizeConstraints(constraints), true, nil
}
