package runtime

import (
	"context"
	"github.com/caelis-labs/caelis/agent-sdk/internal/hookrun"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"

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
	gates      *toolCallGates
	writes     *sessionWriteQueue
}

func (t dispatchTool) Definition() tool.Definition { return tool.CloneDefinition(t.base.Definition()) }
func (t dispatchTool) RuntimeTaskResultSource(toolbinding.Token) bool {
	return toolbinding.IsTaskResultSource(t.base)
}
func (t dispatchTool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	admission, err := codeModeAdmissionFor(ctx, call)
	if err != nil {
		return tool.Result{}, err
	}
	def := t.base.Definition()
	if t.gates != nil && !def.Capabilities.ParallelSafe {
		release, err := t.gates.acquire(ctx, def.Name)
		if err != nil {
			return tool.Result{}, err
		}
		defer release()
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
		err = consumePolicyDispatch(ctx, t.base)
		releaseAdmission()
		releaseWrite()
		if err != nil {
			return tool.Result{}, err
		}
	} else if err := consumePolicyDispatch(ctx, t.base); err != nil {
		return tool.Result{}, err
	}

	result, err := hookrun.InvokeTool(ctx, def, call, func(callCtx context.Context) (tool.Result, error) {
		if synthetic := toolbinding.PreparedResult(ctx); synthetic != nil {
			return *synthetic, nil
		}
		return t.base.Call(callCtx, tool.CloneCall(call))
	})
	if admission != nil && ctx.Err() != nil && (err != nil || result.IsError) && tool.EffectClassOf(def) != tool.EffectReadOnly {
		err = errorcode.Wrap(errorcode.UnknownOutcome, "tool result is unconfirmed after cancellation; do not replay", err)
	}
	return result, err
}
func (r *Runtime) wrapToolsForDispatch(ctx context.Context, ref session.SessionRef, tools []tool.Tool) []tool.Tool {
	gates, _ := ctx.Value(toolCallGatesKey{}).(*toolCallGates)
	out := make([]tool.Tool, 0, len(tools))
	for _, one := range tools {
		if one != nil {
			out = append(out, dispatchTool{base: one, sessions: r.sessions, sessionRef: ref, gates: gates, writes: &r.sessionWrites})
		}
	}
	return out
}
