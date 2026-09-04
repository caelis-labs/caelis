package gatewayapp

import (
	"context"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/terminal"
)

// hostTaskStreamService routes only command-terminal fallback/control to the
// Runtime owning a Session. Transient Task output is read from Control's spool.
type hostTaskStreamService struct {
	host      *runtimeComposition
	registry  *sessionRuntimeRegistry
	lifecycle taskOutputLifecycle
}

func (s hostTaskStreamService) Read(ctx context.Context, ref terminal.Ref) (terminal.Snapshot, error) {
	controller, err := s.controller(ref.SessionID)
	if err != nil {
		return terminal.Snapshot{}, err
	}
	return controller.Read(ctx, ref)
}

func (s hostTaskStreamService) Wait(ctx context.Context, ref terminal.Ref) (terminal.Snapshot, error) {
	controller, err := s.controller(ref.SessionID)
	if err != nil {
		return terminal.Snapshot{}, err
	}
	return controller.Wait(ctx, ref)
}

func (s hostTaskStreamService) Kill(ctx context.Context, ref terminal.Ref) error {
	controller, err := s.controller(ref.SessionID)
	if err != nil {
		return err
	}
	return controller.Kill(ctx, ref)
}

func (s hostTaskStreamService) Release(ctx context.Context, ref terminal.Ref) error {
	controller, err := s.controller(ref.SessionID)
	if err != nil {
		return err
	}
	if err := controller.Release(ctx, ref); err != nil {
		return err
	}
	if s.lifecycle != nil {
		_ = s.lifecycle.ReleaseTask(context.WithoutCancel(ctx), taskRefFromTerminal(ref))
	}
	return nil
}

func taskRefFromTerminal(ref terminal.Ref) task.Ref {
	ref = terminal.NormalizeRef(ref)
	return task.Ref{SessionID: ref.SessionID, TaskID: ref.TaskID, TerminalID: ref.TerminalID}
}

func (s hostTaskStreamService) controller(sessionID string) (terminal.Controller, error) {
	composition := s.host
	if composition == nil {
		return nil, taskStreamRuntimeUnavailable()
	}
	sessionID = strings.TrimSpace(sessionID)
	if s.registry != nil {
		runtime, ok := s.registry.loaded(sessionID)
		if !ok || runtime == nil || runtime.instance == nil {
			return nil, taskStreamRuntimeUnavailable()
		}
		composition = &runtime.instance.runtimeComposition
	}
	provider := composition.KernelTerminals()
	if provider == nil || provider.Terminals() == nil {
		return nil, taskStreamRuntimeUnavailable()
	}
	return provider.Terminals(), nil
}

func taskStreamRuntimeUnavailable() error {
	return errorcode.New(errorcode.Unavailable, "gatewayapp: Task Runtime terminal control is unavailable")
}

var _ terminal.Controller = hostTaskStreamService{}
