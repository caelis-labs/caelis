package runtime

import (
	"context"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
)

// This process-local binding authorizes validation of one specific child call.
// After command handoff, validation uses the same target and source checks
// without depending on the submitting script's Host.
type codeModeAdmissionBinding struct {
	callID         string
	name           string
	parent         tool.ParentCall
	validate       func(context.Context) (func(), error)
	validateTarget func(context.Context) (func(), error)
}

func (t runtimeCodeModeTool) targetAdmission(name string, target codeModeTarget) func(context.Context) (func(), error) {
	lookup, validate, checkBinding := t.lookup, t.validate, t.checkBinding
	digest := target.digest
	binding := tool.BindingForDefinition(target.definition)
	isMCP := tool.IsMCPDefinition(target.definition)
	return func(ctx context.Context) (func(), error) {
		if isMCP && checkBinding != nil {
			if err := checkBinding(ctx, binding); err != nil {
				return nil, err
			}
		}
		release := func() {}
		if validate != nil {
			guard, err := validate(ctx)
			if guard != nil {
				release = guard
			}
			if err != nil {
				release()
				return nil, err
			}
		}
		one, found, err := lookup(ctx, name)
		if err == nil && found && one != nil {
			def := one.Definition()
			if def.Name == name && codeModeDefinitionDigest(def) == digest {
				return release, nil
			}
		}
		release()
		if err != nil {
			return nil, err
		}
		return nil, tool.NewError(tool.ErrorCodeStaleRevision, "Code Mode source/schema changed")
	}
}

// claimCodeModeBinding keeps canonical binding comparison and the frozen call
// validation in one Session write transaction. Search receipts use the same queue.
// Lock order is target execution gate, Session writer, snapshot guard, Host mutex;
// neither approval nor external tool I/O holds the Session writer.
func (t dispatchTool) claimCodeModeBinding(ctx context.Context, call tool.Call) (func(), error) {
	if call.TargetBinding == nil {
		return func() {}, nil
	}
	release, err := t.writes.acquire(ctx, t.sessionRef)
	if err != nil {
		return nil, err
	}
	events, err := t.sessions.Events(ctx, session.EventsRequest{SessionRef: t.sessionRef, IncludeTransient: true})
	if err == nil {
		err = validateRecordedCodeModeBinding(events, call)
	}
	if err != nil {
		release()
		return nil, err
	}
	return release, nil
}

// validateRecordedCodeModeBinding keeps intent acceptance and final dispatch on
// the same recorded binding order. The current intent never refreshes itself.
func validateRecordedCodeModeBinding(events []*session.Event, call tool.Call) error {
	if call.TargetBinding == nil {
		return nil
	}
	filtered := make([]*session.Event, 0, len(events))
	for _, event := range events {
		if event != nil && (event.Tool == nil || event.Tool.Execution != call.Execution) {
			filtered = append(filtered, event)
		}
	}
	if prior, known := codeModeBindings(filtered)[call.Name]; known && prior != *call.TargetBinding {
		return tool.NewError(tool.ErrorCodeStaleRevision, "tool directory refreshed; binding is stale")
	}
	return nil
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
		return nil, codemode.NewPublicPermissionError("Code Mode child requires its original live Host admission binding")
	}
	return binding, nil
}
