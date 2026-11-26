package gatewayapp

import (
	"context"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/task"
)

// hostTaskStreamService routes only command-terminal fallback/control to the
// Runtime owning a Session. Transient Task output is read from Control's spool.
type hostTaskStreamService struct {
	registry  *sessionRuntimeRegistry
	lifecycle taskOutputLifecycle
}

func (s hostTaskStreamService) Read(ctx context.Context, ref task.TerminalRef) (task.TerminalSnapshot, error) {
	controller, err := s.controller(ref.SessionID)
	if err != nil {
		return task.TerminalSnapshot{}, err
	}
	return controller.Read(ctx, ref)
}

func (s hostTaskStreamService) Wait(ctx context.Context, ref task.TerminalRef) (task.TerminalSnapshot, error) {
	controller, err := s.controller(ref.SessionID)
	if err != nil {
		return task.TerminalSnapshot{}, err
	}
	return controller.Wait(ctx, ref)
}

func (s hostTaskStreamService) Kill(ctx context.Context, ref task.TerminalRef) error {
	controller, err := s.controller(ref.SessionID)
	if err != nil {
		return err
	}
	return controller.Kill(ctx, ref)
}

func (s hostTaskStreamService) Release(ctx context.Context, ref task.TerminalRef) error {
	controller, err := s.controller(ref.SessionID)
	if err != nil {
		return err
	}
	if err := controller.Release(ctx, ref); err != nil {
		return err
	}
	_ = s.lifecycle.ReleaseTask(context.WithoutCancel(ctx), taskRefFromTerminal(ref))
	return nil
}

func taskRefFromTerminal(ref task.TerminalRef) task.Ref {
	ref = task.NormalizeTerminalRef(ref)
	return task.Ref{SessionID: ref.SessionID, TaskID: ref.TaskID, TerminalID: ref.TerminalID}
}

func (s hostTaskStreamService) controller(sessionID string) (task.TerminalController, error) {
	runtime, ok := s.registry.loaded(sessionID)
	if !ok || runtime.instance == nil {
		return nil, taskStreamRuntimeUnavailable()
	}
	controller := runtime.instance.KernelTerminals()
	if controller == nil {
		return nil, taskStreamRuntimeUnavailable()
	}
	return controller, nil
}

func taskStreamRuntimeUnavailable() error {
	return errorcode.New(errorcode.Unavailable, "gatewayapp: Task Runtime terminal control is unavailable")
}

var _ task.TerminalController = hostTaskStreamService{}
