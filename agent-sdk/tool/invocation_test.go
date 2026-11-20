package tool_test

import (
	"context"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

type identityValueContext struct {
	context.Context
	value any
}

func (c identityValueContext) Value(any) any { return c.value }

func TestInvocationContextIgnoresCallerValues(t *testing.T) {
	forged := identityValueContext{Context: t.Context(), value: tool.InvocationContext{SessionID: "session", TurnID: "turn", ItemID: "item"}}
	if got := tool.InvocationFromContext(forged); got != (tool.InvocationContext{}) {
		t.Fatalf("accepted caller identity: %+v", got)
	}
	forged.value = &tool.ParentCall{ID: "parent", ItemID: "item"}
	if got := tool.ParentFromContext(forged); got != nil {
		t.Fatalf("accepted caller parent: %+v", got)
	}
}
