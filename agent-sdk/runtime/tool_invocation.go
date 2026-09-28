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

// invocationTool binds identity before policy admission. Approval, the execution
// journal and any asynchronous continuation consume this same identity; assigning
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
	// Never accept identity supplied by a caller, provider input or metadata.
	call.Execution = tool.InvocationContext{
		SessionID: t.sessionID, TurnID: t.turnID,
		ItemID: fmt.Sprintf("tool-step-%d:%s", t.sequence.Add(1), strings.TrimSpace(call.ID)),
	}
	return t.base.Call(ctx, call)
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
