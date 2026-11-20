package tool

import (
	"context"

	"github.com/caelis-labs/caelis/agent-sdk/internal/invocationctx"
)

// InvocationContext identifies one tool invocation within a canonical Session/Turn.
// Runtime assigns ItemID before policy admission and retains it in approval and
// execution records; it is unique within the Turn, unlike the reusable Call.ID.
// These values cannot be supplied through model input or metadata. Identity alone
// does not authorize execution.
type InvocationContext struct {
	SessionID string `json:"session_id"`
	TurnID    string `json:"turn_id"`
	ItemID    string `json:"item_id"`
}

// ParentCall relates a nested invocation to the admitted host call that owns it.
// It is Runtime data, never script arguments or an authorization capability.
type ParentCall struct {
	// Kind identifies the Runtime-owned relation, not execution authority.
	// Empty retains ordinary parent provenance; code_mode requires a live
	// Runtime admission binding which cannot be reconstructed from this record.
	Kind   string `json:"kind,omitempty"`
	ID     string `json:"id"`
	Name   string `json:"name"`
	ItemID string `json:"item_id"`
}

// InvocationFromContext returns Runtime-assigned identity for the current tool
// occurrence. Standalone tools receive a zero identity. Identity conveys no
// authorization and does not guarantee that the tool executed.
func InvocationFromContext(ctx context.Context) InvocationContext {
	return invocationctx.Read[InvocationContext](ctx, invocationctx.ExecutionKey{})
}

// ParentFromContext returns a copy of the current occurrence's parent relation,
// or nil for a root call. The relation does not convey execution authority.
func ParentFromContext(ctx context.Context) *ParentCall {
	parent := invocationctx.Read[*ParentCall](ctx, invocationctx.ParentKey{})
	if parent == nil {
		return nil
	}
	copy := *parent
	return &copy
}
