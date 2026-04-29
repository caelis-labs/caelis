package terminal

import (
	"context"
	"fmt"
	"strings"

	"github.com/OnslaughtSnail/caelis/acp"
	schema "github.com/OnslaughtSnail/caelis/acp/schema"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
	sdkstream "github.com/OnslaughtSnail/caelis/sdk/stream"
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
	Streams sdkstream.Service
}

func (a LocalTerminalAdapter) Output(ctx context.Context, req TerminalOutputRequest) (TerminalOutputResponse, error) {
	if a.Streams == nil {
		return TerminalOutputResponse{}, fmt.Errorf("acpbridge/terminal: stream service is required")
	}
	snap, err := a.Streams.Read(ctx, sdkstream.ReadRequest{
		Ref: sdkstream.Ref{
			SessionID:  strings.TrimSpace(req.SessionID),
			TerminalID: strings.TrimSpace(req.TerminalID),
		},
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
	controller, ok := a.Streams.(sdkstream.Controller)
	if !ok || controller == nil {
		return TerminalWaitForExitResponse{}, fmt.Errorf("acpbridge/terminal: terminal wait is unsupported")
	}
	snap, err := controller.Wait(ctx, sdkstream.Ref{
		SessionID:  strings.TrimSpace(req.SessionID),
		TerminalID: strings.TrimSpace(req.TerminalID),
	})
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
	controller, ok := a.Streams.(sdkstream.Controller)
	if !ok || controller == nil {
		return fmt.Errorf("acpbridge/terminal: terminal kill is unsupported")
	}
	return controller.Kill(ctx, sdkstream.Ref{
		SessionID:  strings.TrimSpace(req.SessionID),
		TerminalID: strings.TrimSpace(req.TerminalID),
	})
}

func (a LocalTerminalAdapter) Release(ctx context.Context, req TerminalReleaseRequest) error {
	controller, ok := a.Streams.(sdkstream.Controller)
	if !ok || controller == nil {
		return fmt.Errorf("acpbridge/terminal: terminal release is unsupported")
	}
	return controller.Release(ctx, sdkstream.Ref{
		SessionID:  strings.TrimSpace(req.SessionID),
		TerminalID: strings.TrimSpace(req.TerminalID),
	})
}

func terminalSnapshotOutput(snap sdkstream.Snapshot) string {
	var out strings.Builder
	for _, frame := range snap.Frames {
		out.WriteString(frame.Text)
	}
	return out.String()
}

func RefFromEvent(event *sdksession.Event) (sdkstream.Ref, bool) {
	if event == nil {
		return sdkstream.Ref{}, false
	}
	ref := sdkstream.Ref{
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
	if ref.TerminalID == "" && event.Protocol != nil && event.Protocol.ToolCall != nil && event.Protocol.ToolCall.RawOutput != nil {
		if terminalID, _ := event.Protocol.ToolCall.RawOutput["terminal_id"].(string); strings.TrimSpace(terminalID) != "" {
			ref.TerminalID = strings.TrimSpace(terminalID)
		}
	}
	if ref.TerminalID == "" {
		return sdkstream.Ref{}, false
	}
	return ref, true
}

func ContentFromEvent(event *sdksession.Event) []ToolCallContent {
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
