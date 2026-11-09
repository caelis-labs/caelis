package gatewayapp

import (
	"context"
	"errors"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/control/collaboration"
)

type stateOnlyTaskStore struct {
	*sessionfile.TaskStore
	selected [][]string
}

func (s *stateOnlyTaskStore) ListSession(context.Context, session.SessionRef) ([]*task.Entry, error) {
	return nil, errors.New("full Task payload read is not needed for collaboration discovery")
}

func (s *stateOnlyTaskStore) ReadTaskStates(ctx context.Context, ref session.SessionRef, ids []string) ([]task.StateSnapshot, error) {
	s.selected = append(s.selected, append([]string(nil), ids...))
	return s.TaskStore.ReadTaskStates(ctx, ref, ids)
}

func TestCollaborationRecipientRechecksStateGenerationAndEpoch(t *testing.T) {
	store := sessionfile.NewStore(sessionfile.Config{RootDir: t.TempDir()})
	active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "test", UserID: "user", Workspace: session.WorkspaceRef{Key: "work", CWD: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	states := &stateOnlyTaskStore{TaskStore: sessionfile.NewTaskStore(store)}
	binding := session.ParticipantBinding{ID: "participant", Kind: session.ParticipantKindSubagent,
		DelegationID: "task", SessionID: "child", AgentName: "worker", Label: "@worker", AttachmentGeneration: "generation-1"}
	if _, err := store.PutParticipant(t.Context(), session.PutParticipantRequest{SessionRef: active.SessionRef, Binding: binding}); err != nil {
		t.Fatal(err)
	}
	if err := states.Upsert(t.Context(), &task.Entry{TaskID: "task", Kind: task.KindSubagent, Session: active.SessionRef, State: task.StateCompleted}); err != nil {
		t.Fatal(err)
	}
	backend := &collaborationBackend{sessions: store, tasks: states, router: &hostedChildInputRouter{}}
	candidate, err := backend.List(t.Context(), active.SessionID)
	if err != nil || len(candidate) != 2 || !candidate[1].CanDeliver || candidate[1].Generation != "generation-1" {
		t.Fatalf("discovery = %+v, %v", candidate, err)
	}
	current, err := states.Get(t.Context(), "task")
	if err != nil {
		t.Fatal(err)
	}
	current.State, current.Metadata = task.StateUnknownOutcome, map[string]any{"supports_steering": true}
	updated, err := states.Put(t.Context(), task.PutRequest{Entry: current, ExpectedRevision: current.Revision})
	if err != nil {
		t.Fatal(err)
	}
	binding.AttachmentGeneration = "generation-2"
	if _, err := store.PutParticipant(t.Context(), session.PutParticipantRequest{SessionRef: active.SessionRef, Binding: binding}); err != nil {
		t.Fatal(err)
	}
	fresh, err := backend.Recipient(t.Context(), collaboration.Identity{Session: active.SessionID, Member: "worker"})
	if err != nil || fresh.CanDeliver || fresh.State != string(task.StateUnknownOutcome) || fresh.Revision != updated.Revision || fresh.Generation != "generation-2" || fresh.ParticipantID != binding.ID {
		t.Fatalf("claim reused candidate permission/identity = %+v, %v", fresh, err)
	}
	if got := states.selected[len(states.selected)-1]; len(got) != 1 || got[0] != "task" {
		t.Fatalf("recipient read unrelated Task states: %v", got)
	}
	if _, err := store.BindController(t.Context(), session.BindControllerRequest{SessionRef: active.SessionRef, Binding: session.ControllerBinding{Kind: session.ControllerKindACP, EpochID: "epoch-2"}}); err != nil {
		t.Fatal(err)
	}
	parent, err := backend.Recipient(t.Context(), collaboration.Identity{Session: active.SessionID, Member: "parent"})
	if err != nil || parent.Generation != "epoch-2" {
		t.Fatalf("parent epoch = %+v, %v", parent, err)
	}
	if _, err := store.RemoveParticipant(t.Context(), session.RemoveParticipantRequest{SessionRef: active.SessionRef, ParticipantID: binding.ID}); err != nil {
		t.Fatal(err)
	}
	detached, err := backend.Recipient(t.Context(), collaboration.Identity{Session: active.SessionID, Member: "worker"})
	if err != nil || detached.Handle != "" || detached.CanDeliver {
		t.Fatalf("detached recipient still eligible: %+v, %v", detached, err)
	}
}
