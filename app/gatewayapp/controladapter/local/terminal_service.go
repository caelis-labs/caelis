package local

import (
	"context"
	"errors"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/task/terminal"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/taskstream"
)

// TerminalService resolves ACP display terminal IDs through the authorized
// Task directory, then controls the Session-routed Runtime stream.
type TerminalService struct {
	tasks     taskstream.Service
	terminals terminal.Controller
}

func NewTerminalService(tasks taskstream.Service, terminals terminal.Controller) (*TerminalService, error) {
	if tasks == nil || terminals == nil {
		return nil, errors.New("app/gatewayapp/controladapter/local: Task directory and terminal control are required")
	}
	return &TerminalService{tasks: tasks, terminals: terminals}, nil
}

func (s *TerminalService) TerminalOutput(ctx context.Context, principal appserver.Principal, req appserver.TerminalRequest) (appserver.TerminalOutput, error) {
	ref, err := s.resolve(ctx, principal, req)
	if err != nil {
		return appserver.TerminalOutput{}, err
	}
	snapshot, err := s.terminals.Read(ctx, ref)
	if err != nil {
		return appserver.TerminalOutput{}, err
	}
	result := appserver.TerminalOutput{Output: terminalSnapshotOutput(snapshot), Truncated: snapshot.Truncated}
	if snapshot.ExitCode != nil {
		code := *snapshot.ExitCode
		result.ExitStatus = &appserver.TerminalExitStatus{ExitCode: &code}
	}
	return result, nil
}

func (s *TerminalService) WaitTerminal(ctx context.Context, principal appserver.Principal, req appserver.TerminalRequest) (appserver.TerminalExitStatus, error) {
	ref, err := s.resolve(ctx, principal, req)
	if err != nil {
		return appserver.TerminalExitStatus{}, err
	}
	snapshot, err := s.terminals.Wait(ctx, ref)
	if err != nil {
		return appserver.TerminalExitStatus{}, err
	}
	result := appserver.TerminalExitStatus{}
	if snapshot.ExitCode != nil {
		code := *snapshot.ExitCode
		result.ExitCode = &code
	}
	return result, nil
}

func (s *TerminalService) KillTerminal(ctx context.Context, principal appserver.Principal, req appserver.TerminalRequest) error {
	ref, err := s.resolve(ctx, principal, req)
	if err != nil {
		return err
	}
	return s.terminals.Kill(ctx, ref)
}

func (s *TerminalService) ReleaseTerminal(ctx context.Context, principal appserver.Principal, req appserver.TerminalRequest) error {
	ref, err := s.resolve(ctx, principal, req)
	if err != nil {
		return err
	}
	return s.terminals.Release(ctx, ref)
}

func (s *TerminalService) resolve(ctx context.Context, principal appserver.Principal, req appserver.TerminalRequest) (terminal.Ref, error) {
	sessionID := strings.TrimSpace(req.SessionID)
	terminalID := strings.TrimSpace(req.TerminalID)
	if sessionID == "" || terminalID == "" {
		return terminal.Ref{}, errorcode.New(errorcode.InvalidArgument, "appserver terminal requires Session and terminal IDs")
	}
	list, err := s.tasks.List(ctx, taskstream.Principal{ID: principal.ID, Roles: append([]string(nil), principal.Roles...)}, taskstream.ListRequest{SessionID: sessionID})
	if err != nil {
		return terminal.Ref{}, err
	}
	for _, task := range list.Tasks {
		if terminalID != strings.TrimSpace(task.ParentTool.ToolCallID) &&
			terminalID != strings.TrimSpace(task.TaskID) && terminalID != strings.TrimSpace(task.Handle) &&
			terminalID != strings.TrimSpace(task.CurrentTurnID) {
			continue
		}
		resolvedTerminalID := strings.TrimSpace(task.CurrentTurnID)
		if resolvedTerminalID == "" {
			resolvedTerminalID = terminalID
		}
		return terminal.NormalizeRef(terminal.Ref{SessionID: sessionID, TaskID: task.TaskID, TerminalID: resolvedTerminalID}), nil
	}
	return terminal.Ref{}, errorcode.New(errorcode.NotFound, "appserver terminal was not found in the Session Task directory")
}

func terminalSnapshotOutput(snapshot terminal.Snapshot) string {
	if snapshot.Output != "" {
		return snapshot.Output
	}
	if strings.TrimSpace(snapshot.FinalResult) == "(no output)" {
		return ""
	}
	return snapshot.FinalResult
}

var _ appserver.TerminalService = (*TerminalService)(nil)
