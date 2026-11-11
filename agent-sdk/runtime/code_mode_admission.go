package runtime

import (
	"context"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
)

// This process-local binding belongs to one Host call, not its short-lived
// context. Existing Task continuations carry it explicitly while retaining
// their own Run lifetime, cancellation, approval and receipt ownership.
type codeModeAdmissionBinding struct {
	callID   string
	name     string
	parent   tool.ParentCall
	validate func(context.Context) (func(), error)
}

func isCodeModeChild(call tool.Call) bool {
	return call.Parent != nil && call.Parent.Kind == codemode.ToolKind
}

func codeModeAdmissionFor(ctx context.Context, call tool.Call) (*codeModeAdmissionBinding, error) {
	if !isCodeModeChild(call) {
		return nil, nil
	}
	binding, _ := ctx.Value(codeModeAdmissionKey{}).(*codeModeAdmissionBinding)
	if binding == nil || binding.validate == nil || binding.callID != call.ID || binding.name != call.Name || binding.parent != *call.Parent {
		return nil, tool.NewError(tool.ErrorCodePermissionDenied, "Code Mode child requires its original live Host admission binding")
	}
	return binding, nil
}
