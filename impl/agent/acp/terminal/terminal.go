package terminal

import (
	"context"
	"fmt"
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/ports/stream"
	"github.com/OnslaughtSnail/caelis/protocol/acp"
	"github.com/OnslaughtSnail/caelis/protocol/acp/schema"
)

type TerminalOutputRequest = schema.TerminalOutputRequest
type TerminalExitStatus = schema.TerminalExitStatus
type TerminalOutputResponse = schema.TerminalOutputResponse
type TerminalWaitForExitRequest = schema.TerminalWaitForExitRequest
type TerminalWaitForExitResponse = schema.TerminalWaitForExitResponse
type TerminalKillRequest = schema.TerminalKillRequest
type TerminalReleaseRequest = schema.TerminalReleaseRequest
type ToolCallContent = schema.ToolCallContent

type LocalTerminalAdapter struct {
	Streams    stream.Service
	ResolveRef func(sessionID string, terminalID string) (stream.Ref, bool)
}

func (a LocalTerminalAdapter) Output(ctx context.Context, req TerminalOutputRequest) (TerminalOutputResponse, error) {
	if a.Streams == nil {
		return TerminalOutputResponse{}, fmt.Errorf("impl/agent/acp/terminal: stream service is required")
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
		return TerminalWaitForExitResponse{}, fmt.Errorf("impl/agent/acp/terminal: terminal wait is unsupported")
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
		return fmt.Errorf("impl/agent/acp/terminal: terminal kill is unsupported")
	}
	return controller.Kill(ctx, a.requestRef(req.SessionID, req.TerminalID))
}

func (a LocalTerminalAdapter) Release(ctx context.Context, req TerminalReleaseRequest) error {
	controller, ok := a.Streams.(stream.Controller)
	if !ok || controller == nil {
		return fmt.Errorf("impl/agent/acp/terminal: terminal release is unsupported")
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
	return snap.FinalText
}

func RefFromEvent(event *session.Event) (stream.Ref, bool) {
	if event == nil {
		return stream.Ref{}, false
	}
	ref := stream.Ref{
		SessionID: strings.TrimSpace(event.SessionID),
	}
	if event.Meta != nil {
		if taskID, _ := event.Meta["task_id"].(string); strings.TrimSpace(taskID) != "" {
			ref.TaskID = strings.TrimSpace(taskID)
		}
		if terminalID, _ := event.Meta["terminal_id"].(string); strings.TrimSpace(terminalID) != "" {
			ref.TerminalID = strings.TrimSpace(terminalID)
		}
	}
	if ref.TerminalID == "" && event.Protocol != nil && event.Protocol.ToolCall != nil {
		for _, item := range event.Protocol.ToolCall.Content {
			if terminalID := strings.TrimSpace(item.TerminalID); terminalID != "" {
				ref.TerminalID = terminalID
				break
			}
		}
	}
	if ref.TerminalID == "" {
		if update := session.ProtocolUpdateOf(event); update != nil {
			for _, item := range session.ProtocolToolCallContentOf(update) {
				if terminalID := strings.TrimSpace(item.TerminalID); terminalID != "" {
					ref.TerminalID = terminalID
					break
				}
			}
		}
	}
	if ref.TerminalID == "" {
		return stream.Ref{}, false
	}
	return ref, true
}

func ContentFromEvent(event *session.Event) []ToolCallContent {
	ref, ok := RefFromEvent(event)
	if !ok {
		return nil
	}
	return []ToolCallContent{{
		Type:       "terminal",
		TerminalID: ref.TerminalID,
	}}
}

var _ acp.TerminalAdapter = LocalTerminalAdapter{}
