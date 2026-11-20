package runtime

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/toolbinding"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

// invocationTool binds identity before policy admission. Approval, the tool facts and any asynchronous continuation consume this same identity; assigning
// it neither authorizes execution nor records that a tool has started.
type invocationTool struct {
	base      tool.Tool
	sessionID string
	turnID    string
	sequence  *atomic.Uint64
}

func (t invocationTool) Definition() tool.Definition {
	return tool.CloneDefinition(t.base.Definition())
}

func (t invocationTool) RuntimeTaskResultSource(toolbinding.Token) bool {
	return toolbinding.IsTaskResultSource(t.base)
}

func (t invocationTool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	return t.RuntimeCall(toolbinding.Token{}, ctx, call, toolbinding.Dispatch{})
}

func (t invocationTool) RuntimeCall(_ toolbinding.Token, ctx context.Context, call tool.Call, dispatch toolbinding.Dispatch) (tool.Result, error) {
	if !toolbinding.IsPrepared(ctx, call) {
		ctx = toolbinding.WithOccurrence(ctx, toolbinding.Allocate(t, call), nil, nil)
	}
	return toolbinding.Call(ctx, t.base, call, dispatch)
}

func (t invocationTool) RuntimePrepareInvocation(_ toolbinding.Token, call tool.Call) tool.InvocationContext {
	return tool.InvocationContext{SessionID: t.sessionID, TurnID: t.turnID, ItemID: fmt.Sprintf("tool-%d:%s", t.sequence.Add(1), strings.TrimSpace(call.ID))}
}

func wrapToolsForInvocation(ref session.SessionRef, turnID string, sequence *atomic.Uint64, tools []tool.Tool) []tool.Tool {
	if sequence == nil {
		sequence = new(atomic.Uint64)
	}
	out := make([]tool.Tool, 0, len(tools))
	for _, item := range tools {
		if item != nil {
			out = append(out, invocationTool{base: item, sessionID: strings.TrimSpace(ref.SessionID), turnID: strings.TrimSpace(turnID), sequence: sequence})
		}
	}
	return out
}

// toolTaskOperationID binds a genuine Task submission to this admitted
// occurrence. Direct legacy adapters leave identity to their existing owner.
func toolTaskOperationID(ctx context.Context, call tool.Call) string {
	execution := tool.InvocationFromContext(ctx)
	if execution.ItemID == "" {
		return ""
	}
	return "tool/" + execution.TurnID + "/" + execution.ItemID + "/" + call.Name
}
