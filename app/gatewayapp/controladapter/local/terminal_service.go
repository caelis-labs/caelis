package local

import (
	"context"
	"errors"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/taskstream"
)

// TerminalService resolves ACP display terminal IDs through the authorized
// Task directory, then controls the Session-routed Runtime stream.
type TerminalService struct {
	tasks   taskstream.Service
	streams stream.Controller
}

func NewTerminalService(tasks taskstream.Service, streams stream.Controller) (*TerminalService, error) {
	if tasks == nil || streams == nil {
		return nil, errors.New("app/gatewayapp/controladapter/local: Task directory and terminal streams are required")
	}
	return &TerminalService{tasks: tasks, streams: streams}, nil
}

func (s *TerminalService) TerminalOutput(ctx context.Context, principal appserver.Principal, req appserver.TerminalRequest) (appserver.TerminalOutput, error) {
	ref, err := s.resolve(ctx, principal, req)
	if err != nil {
		return appserver.TerminalOutput{}, err
	}
	snapshot, err := s.streams.Read(ctx, stream.ReadRequest{Ref: ref})
	if err != nil {
		return appserver.TerminalOutput{}, err
	}
	result := appserver.TerminalOutput{Output: terminalSnapshotOutput(snapshot), Truncated: snapshot.TruncatedBefore > 0}
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
	snapshot, err := s.streams.Wait(ctx, ref)
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
	return s.streams.Kill(ctx, ref)
}

func (s *TerminalService) ReleaseTerminal(ctx context.Context, principal appserver.Principal, req appserver.TerminalRequest) error {
	ref, err := s.resolve(ctx, principal, req)
	if err != nil {
		return err
	}
	return s.streams.Release(ctx, ref)
}

func (s *TerminalService) resolve(ctx context.Context, principal appserver.Principal, req appserver.TerminalRequest) (stream.Ref, error) {
	sessionID := strings.TrimSpace(req.SessionID)
	terminalID := strings.TrimSpace(req.TerminalID)
	if sessionID == "" || terminalID == "" {
		return stream.Ref{}, errorcode.New(errorcode.InvalidArgument, "appserver terminal requires Session and terminal IDs")
	}
	list, err := s.tasks.List(ctx, taskstream.Principal{ID: principal.ID, Roles: append([]string(nil), principal.Roles...)}, taskstream.ListRequest{SessionID: sessionID})
	if err != nil {
		return stream.Ref{}, err
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
		return stream.NormalizeRef(stream.Ref{SessionID: sessionID, TaskID: task.TaskID, TerminalID: resolvedTerminalID}), nil
	}
	return stream.Ref{}, errorcode.New(errorcode.NotFound, "appserver terminal was not found in the Session Task directory")
}

func terminalSnapshotOutput(snapshot stream.Snapshot) string {
	var result strings.Builder
	for _, frame := range snapshot.Frames {
		result.WriteString(frame.Text)
	}
	if result.Len() > 0 {
		return result.String()
	}
	if strings.TrimSpace(snapshot.FinalText) == "(no output)" {
		return ""
	}
	return snapshot.FinalText
}

var _ appserver.TerminalService = (*TerminalService)(nil)
