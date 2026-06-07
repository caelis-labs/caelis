package app

import (
	"context"
	"fmt"
	"iter"
	"testing"

	"github.com/OnslaughtSnail/caelis/agent"
	"github.com/OnslaughtSnail/caelis/gateway"
	"github.com/OnslaughtSnail/caelis/model"
	"github.com/OnslaughtSnail/caelis/runner"
	"github.com/OnslaughtSnail/caelis/sandbox"
	"github.com/OnslaughtSnail/caelis/session"
)

func TestNewRuntimeProvidesGatewayTurnPath(t *testing.T) {
	rt, err := NewRuntime(RuntimeConfig{Agent: staticAgent{name: "test-agent"}})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	if rt.Gateway == nil {
		t.Fatal("Gateway is nil")
	}

	ctx := context.Background()
	sess, err := rt.Gateway.CreateSession(ctx, gateway.CreateSessionRequest{
		AppName: "app", UserID: "u", WorkspaceKey: "ws",
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	turn, err := rt.Gateway.BeginTurn(ctx, gateway.TurnRequest{SessionRef: sess.Ref})
	if err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}
	if turn.TurnID == "" {
		t.Fatal("TurnID is empty")
	}

	err = rt.Gateway.Submit(ctx, gateway.SubmitRequest{
		TurnID:      turn.TurnID,
		UserMessage: model.Message{Role: model.RoleUser, Content: []model.Part{{Text: "hi"}}},
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	replay, err := rt.Gateway.Replay(ctx, gateway.ReplayRequest{SessionRef: sess.Ref})
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(replay.Events) != 2 {
		t.Fatalf("replay events = %d, want 2", len(replay.Events))
	}
	if replay.Events[0].Kind != string(session.EventKindUser) {
		t.Fatalf("event 0 kind = %q", replay.Events[0].Kind)
	}
	if replay.Events[1].Kind != string(session.EventKindAssistant) {
		t.Fatalf("event 1 kind = %q", replay.Events[1].Kind)
	}
}

func TestNewRuntimeRoutesRequestedSandboxBackend(t *testing.T) {
	ctx := context.Background()
	svc := session.InMemoryService()
	secure := &recordingAppSandboxBackend{name: "secure"}
	rt, err := NewRuntime(RuntimeConfig{
		Agent:           sandboxAwareAgent{name: "test-agent"},
		SessionStore:    svc,
		SandboxBackends: []sandbox.Backend{&recordingAppSandboxBackend{name: "host"}, secure},
	})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}

	sess, err := svc.Create(ctx, session.CreateRequest{
		AppName: "app", UserID: "u", WorkspaceKey: "ws",
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	for _, err := range rt.Runner.Run(ctx, runner.RunRequest{
		SessionRef:  sess.Ref,
		Metadata:    map[string]any{"sandbox_backend": "secure"},
		UserMessage: model.Message{Role: model.RoleUser, Content: []model.Part{{Text: "hi"}}},
	}) {
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	}
	if secure.fileSystems != 1 {
		t.Fatalf("secure FileSystem calls = %d, want 1", secure.fileSystems)
	}
}

func TestNewRuntimeRejectsDuplicateSandboxBackends(t *testing.T) {
	_, err := NewRuntime(RuntimeConfig{
		Agent: staticAgent{name: "test-agent"},
		SandboxBackends: []sandbox.Backend{
			&recordingAppSandboxBackend{name: "host"},
			&recordingAppSandboxBackend{name: "host"},
		},
	})
	if err == nil {
		t.Fatal("NewRuntime error = nil, want duplicate sandbox backend error")
	}
}

type staticAgent struct {
	name string
}

func (a staticAgent) Name() string {
	return a.name
}

func (a staticAgent) Description() string {
	return ""
}

func (a staticAgent) SubAgents() []agent.Agent {
	return nil
}

func (a staticAgent) FindAgent(string) agent.Agent {
	return nil
}

func (a staticAgent) Run(agent.InvocationContext) iter.Seq2[session.Event, error] {
	return func(yield func(session.Event, error) bool) {
		yield(session.Event{
			Kind:       session.EventKindAssistant,
			Visibility: session.VisibilityCanonical,
			AssistantPayload: &session.AssistantPayload{
				Parts: []session.EventPart{{Kind: session.PartKindText, Text: "ok"}},
			},
		}, nil)
	}
}

type sandboxAwareAgent struct {
	name string
}

func (a sandboxAwareAgent) Name() string { return a.name }

func (a sandboxAwareAgent) Description() string { return "" }

func (a sandboxAwareAgent) SubAgents() []agent.Agent { return nil }

func (a sandboxAwareAgent) FindAgent(string) agent.Agent { return nil }

func (a sandboxAwareAgent) Prepare(req agent.PrepareRequest) agent.Agent {
	if req.ToolContext == nil || req.ToolContext.FileSystem() == nil {
		panic("missing sandbox tool context")
	}
	return a
}

func (a sandboxAwareAgent) Run(agent.InvocationContext) iter.Seq2[session.Event, error] {
	return staticAgent(a).Run(nil)
}

type recordingAppSandboxBackend struct {
	name        string
	fileSystems int
}

func (b *recordingAppSandboxBackend) Name() string { return b.name }

func (b *recordingAppSandboxBackend) Describe(context.Context) (sandbox.Descriptor, error) {
	return sandbox.Descriptor{Name: b.name}, nil
}

func (b *recordingAppSandboxBackend) Run(context.Context, sandbox.CommandRequest) (sandbox.CommandResult, error) {
	return sandbox.CommandResult{ExitCode: 0}, nil
}

func (b *recordingAppSandboxBackend) FileSystem(context.Context, sandbox.Constraints) (sandbox.FileSystem, error) {
	b.fileSystems++
	return noopAppFileSystem{}, nil
}

func (b *recordingAppSandboxBackend) Status(context.Context) (sandbox.Status, error) {
	return sandbox.Status{Running: true}, nil
}

func (b *recordingAppSandboxBackend) Close() error { return nil }

type noopAppFileSystem struct{}

func (noopAppFileSystem) Read(string) ([]byte, error) { return nil, fmt.Errorf("not implemented") }

func (noopAppFileSystem) Write(string, []byte) error { return fmt.Errorf("not implemented") }

func (noopAppFileSystem) List(string) ([]string, error) { return nil, fmt.Errorf("not implemented") }

func (noopAppFileSystem) Exists(string) (bool, error) { return false, nil }

func (noopAppFileSystem) Delete(string) error { return fmt.Errorf("not implemented") }

func (noopAppFileSystem) Stat(string) (sandbox.FileInfo, error) {
	return sandbox.FileInfo{}, fmt.Errorf("not implemented")
}
