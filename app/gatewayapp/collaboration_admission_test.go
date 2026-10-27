package gatewayapp

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/runtime"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/control/collaboration"
	"github.com/caelis-labs/caelis/internal/controlplane"
)

// Only remote receipt timing is simulated. The mailbox loop, Runtime binding
// validation and StartSubagent participant commit use their production owners.
type runtimeReceiptBackend struct {
	engine           *runtime.Runtime
	ref              session.SessionRef
	source           session.ParticipantBinding
	entered, release chan struct{}
}

func (b *runtimeReceiptBackend) List(context.Context, string) ([]collaboration.Thread, error) {
	return []collaboration.Thread{{ID: "parent", SessionID: b.ref.SessionID, Handle: "parent", CanDeliver: true}, {ID: b.source.DelegationID, SessionID: b.source.SessionID, Handle: "source"}}, nil
}
func (b *runtimeReceiptBackend) Deliver(ctx context.Context, _ string, messages []collaboration.Message) error {
	entries := make([]agent.AgentInputBatchEntry, len(messages))
	for i, message := range messages {
		entries[i] = agent.AgentInputBatchEntry{Participant: &b.source, Message: agent.AgentCommunicationInput{Input: message.Text}}
	}
	return b.engine.SubmitAgentInputBatch(ctx, b.ref, agent.AgentInputParent, entries, func(ctx context.Context, _ session.Session, _ []agent.AgentCommunicationInput) error {
		close(b.entered)
		select {
		case <-b.release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
}

type admissionSubagent struct{}

func (admissionSubagent) Spawn(_ context.Context, spawn agent.SubagentSpawnContext, _ delegation.Request) (delegation.Anchor, delegation.Result, error) {
	return delegation.Anchor{TaskID: spawn.TaskID, AgentID: spawn.TaskID, SessionID: "child-" + spawn.TaskID}, delegation.Result{State: delegation.StateCompleted}, nil
}
func (admissionSubagent) Wait(context.Context, delegation.Anchor, int) (delegation.Result, error) {
	return delegation.Result{State: delegation.StateCompleted}, nil
}
func (admissionSubagent) Cancel(context.Context, delegation.Anchor) error { return nil }

func TestMailboxReceiptWaitDoesNotBlockRuntimeStartThread(t *testing.T) {
	sessions := sessionfile.NewStore(sessionfile.Config{RootDir: t.TempDir()})
	active, err := sessions.StartSession(t.Context(), session.StartSessionRequest{AppName: "test", UserID: "user", Workspace: session.WorkspaceRef{Key: "workspace", CWD: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	router, err := controlplane.NewContextRouter(sessions)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := runtime.New(runtime.Config{ControllerContextRouter: router, Sessions: sessions, AgentFactory: chat.Factory{}, Subagents: admissionSubagent{}, TaskStore: sessionfile.NewTaskStore(sessions)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.StartSubagent(t.Context(), active.SessionRef, "helper", "first", "test"); err != nil {
		t.Fatal(err)
	}
	active, err = sessions.Session(t.Context(), active.SessionRef)
	if err != nil || len(active.Participants) != 1 {
		t.Fatalf("first attachment = %+v, %v", active.Participants, err)
	}
	backend := &runtimeReceiptBackend{engine: engine, ref: active.SessionRef, source: active.Participants[0], entered: make(chan struct{}), release: make(chan struct{})}
	service, err := collaboration.Open(filepath.Join(t.TempDir(), "mail.sqlite"), backend)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = service.Close() }()
	if _, err := service.Send(t.Context(), collaboration.Identity{Session: active.SessionID, Member: "source"}, "parent", "report", ""); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); service.Run(ctx, nil) }()
	defer func() { cancel(); <-done }()
	select {
	case <-backend.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("mail did not reach Runtime admission")
	}
	started := make(chan error, 1)
	go func() {
		_, err := engine.StartSubagent(t.Context(), active.SessionRef, "helper", "nested thread", "test")
		started <- err
	}()
	select {
	case err := <-started:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("StartThread waits for the parent receipt that depends on that tool completing")
	}
	close(backend.release)
}
