package local

import (
	"context"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/task/terminal"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/taskstream"
)

func TestTerminalServiceResolvesDisplayIDThroughTaskDirectory(t *testing.T) {
	exitCode := 7
	streams := &recordingTerminalStreams{snapshot: terminal.Snapshot{
		Output: "hello\n", ExitCode: &exitCode,
	}}
	service, err := NewTerminalService(terminalTaskDirectory{}, streams)
	if err != nil {
		t.Fatal(err)
	}
	request := appserver.TerminalRequest{SessionID: "session-1", TerminalID: "tool-call-1"}
	output, err := service.TerminalOutput(context.Background(), appserver.Principal{ID: "owner"}, request)
	if err != nil {
		t.Fatal(err)
	}
	if output.Output != "hello\n" || output.ExitStatus == nil || output.ExitStatus.ExitCode == nil || *output.ExitStatus.ExitCode != 7 {
		t.Fatalf("TerminalOutput() = %#v", output)
	}
	if streams.ref != (terminal.Ref{SessionID: "session-1", TaskID: "task-1", TerminalID: "runtime-terminal-1"}) {
		t.Fatalf("resolved ref = %#v", streams.ref)
	}
	if _, err := service.WaitTerminal(context.Background(), appserver.Principal{ID: "owner"}, request); err != nil {
		t.Fatal(err)
	}
	if err := service.KillTerminal(context.Background(), appserver.Principal{ID: "owner"}, request); err != nil {
		t.Fatal(err)
	}
	if err := service.ReleaseTerminal(context.Background(), appserver.Principal{ID: "owner"}, request); err != nil {
		t.Fatal(err)
	}
}

type terminalTaskDirectory struct{}

func (terminalTaskDirectory) List(_ context.Context, principal taskstream.Principal, req taskstream.ListRequest) (taskstream.ListResult, error) {
	if principal.ID != "owner" || req.SessionID != "session-1" {
		return taskstream.ListResult{}, context.Canceled
	}
	return taskstream.ListResult{Tasks: []taskstream.TaskDescriptor{{
		SessionID: "session-1", TaskID: "task-1", Handle: "command-1", CurrentTurnID: "runtime-terminal-1",
		ParentTool: taskstream.ParentTool{ToolCallID: "tool-call-1"},
	}}}, nil
}
func (terminalTaskDirectory) Events(context.Context, taskstream.Principal, taskstream.ReadRequest) (taskstream.ReadResult, error) {
	return taskstream.ReadResult{}, nil
}
func (terminalTaskDirectory) Subscribe(context.Context, taskstream.Principal, taskstream.SubscribeRequest) (taskstream.SubscribeResult, error) {
	return taskstream.SubscribeResult{}, nil
}

type recordingTerminalStreams struct {
	ref      terminal.Ref
	snapshot terminal.Snapshot
}

func (s *recordingTerminalStreams) Read(_ context.Context, ref terminal.Ref) (terminal.Snapshot, error) {
	s.ref = ref
	return s.snapshot, nil
}
func (s *recordingTerminalStreams) Wait(_ context.Context, ref terminal.Ref) (terminal.Snapshot, error) {
	s.ref = ref
	return s.snapshot, nil
}
func (s *recordingTerminalStreams) Kill(_ context.Context, ref terminal.Ref) error {
	s.ref = ref
	return nil
}
func (s *recordingTerminalStreams) Release(_ context.Context, ref terminal.Ref) error {
	s.ref = ref
	return nil
}

var _ terminal.Controller = (*recordingTerminalStreams)(nil)
