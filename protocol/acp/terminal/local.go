package terminal

import (
	"context"
	"fmt"
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/stream"
)

// LocalTerminalAdapter serves ACP terminal/* requests from a local stream
// service. It intentionally returns cumulative output because ACP clients may
// call terminal/output without tracking local stream cursors.
type LocalTerminalAdapter struct {
	Streams    stream.Service
	ResolveRef func(sessionID string, terminalID string) (stream.Ref, bool)
}

func (a LocalTerminalAdapter) Output(ctx context.Context, req TerminalOutputRequest) (TerminalOutputResponse, error) {
	if a.Streams == nil {
		return TerminalOutputResponse{}, fmt.Errorf("protocol/acp/terminal: stream service is required")
	}
	snap, err := a.Streams.Read(ctx, stream.ReadRequest{
		Ref: a.requestRef(req.SessionID, req.TerminalID),
	})
	if err != nil {
		return TerminalOutputResponse{}, err
	}
	resp := TerminalOutputResponse{
		Output:    terminalSnapshotOutput(snap),
		Truncated: false,
	}
	if snap.ExitCode != nil {
		code := *snap.ExitCode
		resp.ExitStatus = &TerminalExitStatus{ExitCode: &code}
	}
	return resp, nil
}

func (a LocalTerminalAdapter) WaitForExit(ctx context.Context, req TerminalWaitForExitRequest) (TerminalWaitForExitResponse, error) {
	controller, ok := a.Streams.(stream.Controller)
	if !ok || controller == nil {
		return TerminalWaitForExitResponse{}, fmt.Errorf("protocol/acp/terminal: terminal wait is unsupported")
	}
	snap, err := controller.Wait(ctx, a.requestRef(req.SessionID, req.TerminalID))
	if err != nil {
		return TerminalWaitForExitResponse{}, err
	}
	resp := TerminalWaitForExitResponse{}
	if snap.ExitCode != nil {
		code := *snap.ExitCode
		resp.ExitCode = &code
	}
	return resp, nil
}

func (a LocalTerminalAdapter) Kill(ctx context.Context, req TerminalKillRequest) error {
	controller, ok := a.Streams.(stream.Controller)
	if !ok || controller == nil {
		return fmt.Errorf("protocol/acp/terminal: terminal kill is unsupported")
	}
	return controller.Kill(ctx, a.requestRef(req.SessionID, req.TerminalID))
}

func (a LocalTerminalAdapter) Release(ctx context.Context, req TerminalReleaseRequest) error {
	controller, ok := a.Streams.(stream.Controller)
	if !ok || controller == nil {
		return fmt.Errorf("protocol/acp/terminal: terminal release is unsupported")
	}
	return controller.Release(ctx, a.requestRef(req.SessionID, req.TerminalID))
}

func (a LocalTerminalAdapter) requestRef(sessionID string, terminalID string) stream.Ref {
	sessionID = strings.TrimSpace(sessionID)
	terminalID = strings.TrimSpace(terminalID)
	if a.ResolveRef != nil {
		if ref, ok := a.ResolveRef(sessionID, terminalID); ok {
			return stream.NormalizeRef(ref)
		}
	}
	return stream.Ref{
		SessionID:  sessionID,
		TerminalID: terminalID,
	}
}

func terminalSnapshotOutput(snap stream.Snapshot) string {
	var out strings.Builder
	for _, frame := range snap.Frames {
		out.WriteString(frame.Text)
	}
	if text := out.String(); text != "" {
		return text
	}
	if strings.TrimSpace(snap.FinalText) == "(no output)" {
		return ""
	}
	return snap.FinalText
}

var _ TerminalAdapter = LocalTerminalAdapter{}
