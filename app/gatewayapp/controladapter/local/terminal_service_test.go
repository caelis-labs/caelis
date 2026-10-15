package local

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	inmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
	"github.com/caelis-labs/caelis/agent-sdk/task/terminal"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/taskstream"
	"github.com/caelis-labs/caelis/control/sessionprofile"
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

type scopedTerminalTaskDirectory struct {
	terminalTaskDirectory
	owner appserver.Principal
}

func (d scopedTerminalTaskDirectory) List(ctx context.Context, p taskstream.Principal, req taskstream.ListRequest) (taskstream.ListResult, error) {
	if p.ID != d.owner.ID {
		return taskstream.ListResult{}, appserver.ErrUnauthorized
	}
	return d.terminalTaskDirectory.List(ctx, p, req)
}

func TestTerminalMutationUsesNativeSessionScopeAndLeaseOwner(t *testing.T) {
	store, err := sessionprofile.Open(filepath.Join(t.TempDir(), "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal := appserver.Principal{ID: "owner"}
	scope, _ := appserver.SessionScope(principal)
	if err := store.PutBinding(t.Context(), sessionprofile.Binding{Scope: scope, SessionID: "session-1", CreationDigest: "creation", Profile: sessionprofile.Profile{Version: "role/1", Instructions: "test", Model: "test", Execution: "tools-only", ToolsVersion: "tools/1"}}); err != nil {
		t.Fatal(err)
	}
	sessions := inmemory.NewStore(inmemory.Config{})
	if _, err := sessions.StartSession(t.Context(), session.StartSessionRequest{AppName: "test", UserID: "owner", PreferredSessionID: "session-1"}); err != nil {
		t.Fatal(err)
	}
	streams := &recordingTerminalStreams{}
	service, err := NewTerminalService(scopedTerminalTaskDirectory{owner: principal}, streams, appserver.SessionAuthorizer{Sessions: sessions, SessionProfiles: store})
	if err != nil {
		t.Fatal(err)
	}
	req := appserver.TerminalRequest{SessionID: "session-1", TerminalID: "tool-call-1"}
	for _, mutate := range []func(context.Context, appserver.Principal, appserver.TerminalRequest) error{service.KillTerminal, service.ReleaseTerminal} {
		if err := mutate(t.Context(), principal, req); err != nil {
			t.Fatal(err)
		}
		if streams.ref != (terminal.Ref{SessionID: "session-1", TaskID: "task-1", TerminalID: "runtime-terminal-1"}) {
			t.Fatalf("native target changed: %+v", streams.ref)
		}
		wrong := principal
		wrong.ID = "foreign"
		streams.ref = terminal.Ref{}
		if err := mutate(t.Context(), wrong, req); !errors.Is(err, appserver.ErrUnauthorized) || streams.ref.TaskID != "" {
			t.Fatalf("foreign connection reached native terminal: %+v, %v", streams.ref, err)
		}
	}
	if err := store.ArchiveBinding(t.Context(), scope, "session-1"); err != nil {
		t.Fatal(err)
	}
	for _, observe := range []func(context.Context, appserver.Principal, appserver.TerminalRequest) error{
		func(ctx context.Context, p appserver.Principal, r appserver.TerminalRequest) error {
			_, err := service.TerminalOutput(ctx, p, r)
			return err
		},
		func(ctx context.Context, p appserver.Principal, r appserver.TerminalRequest) error {
			_, err := service.WaitTerminal(ctx, p, r)
			return err
		},
	} {
		if err := observe(t.Context(), principal, req); err != nil {
			t.Fatalf("revoked observation: %v", err)
		}
	}
	for _, mutate := range []func(context.Context, appserver.Principal, appserver.TerminalRequest) error{service.KillTerminal, service.ReleaseTerminal} {
		streams.ref = terminal.Ref{}
		if err := mutate(t.Context(), principal, req); !errors.Is(err, appserver.ErrSessionClosed) || streams.ref.TaskID != "" {
			t.Fatalf("revoked mutation reached terminal: %+v, %v", streams.ref, err)
		}
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
