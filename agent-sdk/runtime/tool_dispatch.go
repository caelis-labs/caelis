package runtime

import (
	"context"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/internal/hookrun"

	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/toolbinding"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

// dispatchTool owns the existing final dispatch checks. Accepted intent and
// complete outcome are Session tool facts, never a second execution lifecycle.
type dispatchTool struct {
	base       tool.Tool
	sessions   session.Service
	sessionRef session.SessionRef
	writes     *sessionWriteQueue
}

func (t dispatchTool) Definition() tool.Definition { return tool.CloneDefinition(t.base.Definition()) }
func (t dispatchTool) RuntimeTaskResultSource(toolbinding.Token) bool {
	return toolbinding.IsTaskResultSource(t.base)
}
func (t dispatchTool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	return t.RuntimeCall(toolbinding.Token{}, ctx, call, toolbinding.Dispatch{})
}

func (t dispatchTool) RuntimeCall(_ toolbinding.Token, ctx context.Context, call tool.Call, attachment toolbinding.Dispatch) (tool.Result, error) {
	admission, err := codeModeAdmissionFor(ctx, call)
	if err != nil {
		return tool.Result{}, err
	}
	def := t.base.Definition()
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	if admission != nil {
		releaseWrite, err := t.claimCodeModeBinding(ctx, call)
		if err != nil {
			return tool.Result{}, err
		}
		releaseAdmission, err := admission.validate(ctx)
		if err != nil {
			releaseWrite()
			return tool.Result{}, err
		}
		err = consumePolicyDispatch(ctx)
		releaseAdmission()
		releaseWrite()
		if err != nil {
			return tool.Result{}, err
		}
	} else if err := consumePolicyDispatch(ctx); err != nil {
		return tool.Result{}, err
	}

	result, err := hookrun.InvokeTool(ctx, def, call, func(callCtx context.Context) (tool.Result, error) {
		if synthetic, err, ok := toolbinding.PreparedReply(ctx); ok {
			return synthetic, err
		}
		result, err := toolbinding.Call(callCtx, t.base, tool.CloneCall(call), toolbinding.Dispatch{Model: attachment.Model})
		if admission != nil && callCtx.Err() != nil && err != nil {
			err = errorcode.Wrap(errorcode.UnknownOutcome, "tool result is unconfirmed after cancellation; do not replay", err)
		}
		return result, err
	})
	return result, err
}

func (r *Runtime) wrapToolsForDispatch(ref session.SessionRef, tools []tool.Tool) []tool.Tool {
	out := make([]tool.Tool, 0, len(tools))
	for _, one := range tools {
		if one != nil {
			out = append(out, dispatchTool{base: one, sessions: r.sessions, sessionRef: ref, writes: &r.sessionWrites})
		}
	}
	return out
}
