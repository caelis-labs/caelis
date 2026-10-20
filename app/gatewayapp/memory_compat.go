package gatewayapp

import (
	"context"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/memorybinding"
)

// MemoryBindingSelectionContext is retained for source compatibility only.
// Deprecated: Core Memory is retired; Host construction never calls selectors.
type MemoryBindingSelectionContext struct {
	SessionRef         session.SessionRef
	Workspace          session.WorkspaceRef
	FallbackBindingRef memorybinding.BindingRef
}

// MemoryBindingSelector is an ignored embedding option.
// Deprecated: Core Memory is retired; selectors are never called.
type MemoryBindingSelector func(context.Context, MemoryBindingSelectionContext) (memorybinding.BindingRef, error)

// MemoryLabelSelectionContext is retained for source compatibility only.
// Deprecated: Core Memory is retired; no labels are selected or persisted.
type MemoryLabelSelectionContext struct {
	SessionRef session.SessionRef
	Workspace  session.WorkspaceRef
	BindingRef memorybinding.BindingRef
}

// MemoryLabelSelector is an ignored embedding option.
// Deprecated: Core Memory is retired; selectors are never called.
type MemoryLabelSelector func(context.Context, MemoryLabelSelectionContext) ([]string, error)
