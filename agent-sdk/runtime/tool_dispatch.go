package runtime

import (
	"context"
	"reflect"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/internal/hookrun"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/toolbinding"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

type toolDispatch func(context.Context, tool.Call, toolbinding.Dispatch) (tool.Result, error)

// dispatchTool owns the existing final dispatch checks. Accepted intent and
// complete outcome are Session tool facts, never a second execution lifecycle.
type dispatchTool struct {
	before     func(context.Context, tool.Call, toolbinding.Dispatch, toolDispatch) (tool.Result, error)
	base       tool.Tool
	sessions   session.Reader
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
	if t.before != nil {
		return t.before(ctx, call, attachment, t.invoke)
	}
	return t.invoke(ctx, call, attachment)
}

func (t dispatchTool) invoke(ctx context.Context, call tool.Call, attachment toolbinding.Dispatch) (tool.Result, error) {
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

	observation := toolbinding.Observed(ctx)
	around := len(hookrun.FromContext(ctx).Hooks.ToolInvoke) != 0
	var original tool.Result
	result, err := hookrun.InvokeTool(ctx, def, call, func(callCtx context.Context) (tool.Result, error) {
		if synthetic, err, ok := toolbinding.PreparedReply(ctx); ok {
			return synthetic, err
		}
		result, err := toolbinding.Call(callCtx, t.base, tool.CloneCall(call), toolbinding.Dispatch{Model: attachment.Model})
		if around && observation.BuiltinDisplay != nil {
			original, _ = tool.CloneResult(result, nil)
		}
		if admission != nil && callCtx.Err() != nil && err != nil {
			err = errorcode.Wrap(errorcode.UnknownOutcome, "tool result is unconfirmed after cancellation; do not replay", err)
		}
		return result, err
	})
	if around && observation.BuiltinDisplay != nil && !reflect.DeepEqual(original.Content, result.Content) {
		observation.BuiltinDisplay = nil
	}
	return result, err
}
