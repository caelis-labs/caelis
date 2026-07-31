package controladapter

import (
	"context"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// Adapter and its constructors remain test-only while the existing behavior
// suite is migrated to focused server assemblers. They are absent from the
// production package API.
type Adapter = assembler

func NewAdapter(ctx context.Context, stack *RuntimeStack, preferredSessionID, bindingKey, modelText string) (*Adapter, error) {
	return newAssembler(ctx, stack, preferredSessionID, bindingKey, modelText)
}

func NewAdapterForSession(ctx context.Context, stack *RuntimeStack, active session.Session, bindingKey, modelText string) (*Adapter, error) {
	return newAssemblerForSession(ctx, stack, active, bindingKey, modelText)
}

func newAdapterForStack(stack *RuntimeStack, bindingKey, modelText string) *Adapter {
	return newAssemblerForStack(stack, bindingKey, modelText)
}
