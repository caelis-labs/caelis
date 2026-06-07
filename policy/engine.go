package policy

import "context"

// Engine is the top-level policy evaluation contract.
type Engine interface {
	// Evaluate assesses a policy request and returns a decision.
	Evaluate(context.Context, Request) (Decision, error)
}

// Profile is a named policy profile (e.g. "workspace-write", "read-only").
type Profile interface {
	// Name returns the profile name.
	Name() string

	// Evaluate assesses a policy request and returns a decision.
	Evaluate(context.Context, Request) (Decision, error)
}
