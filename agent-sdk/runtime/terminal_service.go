package runtime

import (
	"context"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/terminal"
)

// terminalService is deliberately not a TaskStream service. It exposes only a
// bounded current/final command fallback plus process control for compatibility
// terminal APIs. Transient delivery and replay are Control spool concerns.
type terminalService struct {
	tasks *taskRuntime
}

func newTerminalService(tasks *taskRuntime) *terminalService {
	return &terminalService{tasks: tasks}
}

func (s *terminalService) Read(ctx context.Context, ref terminal.Ref) (terminal.Snapshot, error) {
	ref = terminal.NormalizeRef(ref)
	task, err := s.resolveTerminalCommand(ctx, ref)
	if err != nil {
		return terminal.Snapshot{}, err
	}
	commandSession := task.observableCommandSession()
	var status sandbox.SessionStatus
	if commandSession != nil {
		status, err = commandSession.Status(ctx)
	} else {
		status, err = commandStatusWithoutSession(task)
	}
	if err != nil {
		return terminal.Snapshot{}, err
	}
	task.mu.Lock()
	callback := task.outputState.callback
	task.mu.Unlock()
	if commandSession != nil && !callback {
		if err := s.tasks.syncCommandOutput(ctx, task, status); err != nil {
			return terminal.Snapshot{}, err
		}
	}
	task.mu.Lock()
	snapshot := terminalCommandSnapshotLocked(task, status)
	task.mu.Unlock()
	return snapshot, nil
}

func (s *terminalService) Wait(ctx context.Context, ref terminal.Ref) (terminal.Snapshot, error) {
	ref = terminal.NormalizeRef(ref)
	task, err := s.resolveTerminalCommand(ctx, ref)
	if err != nil {
		return terminal.Snapshot{}, err
	}
	for {
		if task.commandOutcomeUnattached() || task.observableCommandSession() == nil {
			return s.Read(ctx, ref)
		}
		observed, waitErr := s.tasks.waitCommandCompletion(ctx, task, taskWaitMaxYield)
		if waitErr != nil {
			return terminal.Snapshot{}, waitErr
		}
		if !observed.Running {
			return terminalSnapshotFromTaskSnapshot(observed), nil
		}
	}
}

func (s *terminalService) Kill(ctx context.Context, ref terminal.Ref) error {
	task, err := s.resolveTerminalCommand(ctx, terminal.NormalizeRef(ref))
	if err != nil {
		return err
	}
	if task.session == nil {
		return fmt.Errorf("command Task has no live sandbox Session")
	}
	return task.session.Terminate(ctx)
}

func (s *terminalService) Release(ctx context.Context, ref terminal.Ref) error {
	task, err := s.resolveTerminalCommand(ctx, terminal.NormalizeRef(ref))
	if err != nil {
		return err
	}
	if task.session == nil {
		return nil
	}
	status, err := task.session.Status(ctx)
	if err != nil {
		return err
	}
	if status.Running {
		return task.session.Terminate(ctx)
	}
	return nil
}

func (s *terminalService) resolveTerminalCommand(ctx context.Context, ref terminal.Ref) (*commandTask, error) {
	if s == nil || s.tasks == nil {
		return nil, fmt.Errorf("terminal service is unavailable")
	}
	if err := terminal.ValidateRef(ref); err != nil {
		return nil, err
	}
	return s.tasks.lookupCommand(ctx, session.SessionRef{SessionID: ref.SessionID}, ref.TaskID)
}

func commandStatusWithoutSession(task *commandTask) (sandbox.SessionStatus, error) {
	if task == nil {
		return sandbox.SessionStatus{}, fmt.Errorf("command Task is required")
	}
	task.mu.Lock()
	defer task.mu.Unlock()
	if task.running || !taskapi.IsTerminalState(task.state) {
		return sandbox.SessionStatus{}, fmt.Errorf("command Task %q has no observable sandbox Session", task.ref.TaskID)
	}
	return sandbox.SessionStatus{
		Terminal: sandbox.TerminalRef{
			SessionID: strings.TrimSpace(task.ref.SessionID), TerminalID: strings.TrimSpace(task.ref.TerminalID),
		},
		Running: false, StartedAt: task.createdAt, UpdatedAt: task.createdAt,
	}, nil
}

func terminalCommandSnapshotLocked(task *commandTask, status sandbox.SessionStatus) terminal.Snapshot {
	if task == nil {
		return terminal.Snapshot{}
	}
	resultText := firstNonBlankTaskOutput(taskRawStringValue(task.result["result"]), task.output, noOutputPlaceholder)
	ref := terminal.Ref{
		SessionID: strings.TrimSpace(task.sessionRef.SessionID), TaskID: strings.TrimSpace(task.ref.TaskID),
		TerminalID: firstNonEmpty(strings.TrimSpace(status.Terminal.TerminalID), strings.TrimSpace(task.ref.TerminalID)),
	}
	snapshot := terminal.Snapshot{
		Ref: ref, State: string(task.state), Running: task.running,
		SupportsInput: status.SupportsInput && task.running, StartedAt: status.StartedAt, UpdatedAt: status.UpdatedAt,
	}
	snapshot.Output = task.output
	snapshot.Truncated = task.outputState.frontier.base > 0
	if !task.running {
		snapshot.FinalResult = resultText
		if code, ok := parseIntArgValue(task.result["exit_code"]); ok {
			snapshot.ExitCode = &code
		}
	}
	return terminal.CloneSnapshot(snapshot)
}

func terminalSnapshotFromTaskSnapshot(snapshot taskapi.Snapshot) terminal.Snapshot {
	result := terminal.Snapshot{
		Ref:   terminal.Ref{SessionID: snapshot.Ref.SessionID, TaskID: snapshot.Ref.TaskID, TerminalID: snapshot.Ref.TerminalID},
		State: string(snapshot.State), Running: snapshot.Running, SupportsInput: snapshot.SupportsInput,
		StartedAt: snapshot.CreatedAt, UpdatedAt: snapshot.UpdatedAt,
	}
	result.FinalResult = firstNonBlankTaskOutput(taskRawStringValue(snapshot.Result["result"]), noOutputPlaceholder)
	if code, ok := parseIntArgValue(snapshot.Result["exit_code"]); ok {
		result.ExitCode = &code
	}
	return result
}

var _ terminal.Controller = (*terminalService)(nil)
