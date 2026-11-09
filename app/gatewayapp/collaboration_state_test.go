package gatewayapp

import (
	"context"
	"errors"
	"fmt"
	"reflect"
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

func TestCollaborationRecipientPreservesMatchingRoster(t *testing.T) {
	for _, member := range []string{"worker", "parent"} {
		for _, firstReady := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/first_ready_%t", member, firstReady), func(t *testing.T) {
				store := sessionfile.NewStore(sessionfile.Config{RootDir: t.TempDir()})
				active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "test", UserID: "user", Workspace: session.WorkspaceRef{Key: "work", CWD: t.TempDir()}})
				if err != nil {
					t.Fatal(err)
				}
				states := &stateOnlyTaskStore{TaskStore: sessionfile.NewTaskStore(store)}
				for i, ready := range []bool{firstReady, !firstReady} {
					id := fmt.Sprintf("task-%d", i)
					state := task.StateUnknownOutcome
					if ready {
						state = task.StateCompleted
					}
					if err := states.Upsert(t.Context(), &task.Entry{TaskID: id, Handle: id, Kind: task.KindSubagent, Session: active.SessionRef, State: state, Metadata: map[string]any{"supports_steering": true}}); err != nil {
						t.Fatal(err)
					}
					// Legal persisted bindings have distinct IDs/Tasks; the
					// Session store does not require labels to be unique.
					if _, err := store.PutParticipant(t.Context(), session.PutParticipantRequest{SessionRef: active.SessionRef,
						Binding: session.ParticipantBinding{ID: "p-" + id, Kind: session.ParticipantKindSubagent, DelegationID: id,
							SessionID: "child-" + id, Label: "@" + member, AttachmentGeneration: "g-" + id}}); err != nil {
						t.Fatal(err)
					}
				}
				backend := &collaborationBackend{sessions: store, tasks: states, router: &hostedChildInputRouter{}}
				roster, err := backend.List(t.Context(), active.SessionID)
				if err != nil {
					t.Fatal(err)
				}
				var want []collaboration.Thread
				for _, thread := range roster {
					if thread.Handle == member {
						want = append(want, thread)
					}
				}
				got, err := backend.Recipient(t.Context(), collaboration.Identity{Session: active.SessionID, Member: member})
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("focused observations differ from original roster: got=%+v want=%+v err=%v", got, want, err)
				}
				if ids := states.selected[len(states.selected)-1]; !reflect.DeepEqual(ids, []string{"task-0", "task-1"}) {
					t.Fatalf("focused read did not select both matching Tasks: %v", ids)
				}
			})
		}
	}
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
	if err != nil || len(fresh) != 1 || fresh[0].CanDeliver || fresh[0].State != string(task.StateUnknownOutcome) || fresh[0].Revision != updated.Revision || fresh[0].Generation != "generation-2" || fresh[0].ParticipantID != binding.ID {
		t.Fatalf("claim reused candidate permission/identity = %+v, %v", fresh, err)
	}
	if got := states.selected[len(states.selected)-1]; len(got) != 1 || got[0] != "task" {
		t.Fatalf("recipient read unrelated Task states: %v", got)
	}
	if _, err := store.BindController(t.Context(), session.BindControllerRequest{SessionRef: active.SessionRef, Binding: session.ControllerBinding{Kind: session.ControllerKindACP, EpochID: "epoch-2"}}); err != nil {
		t.Fatal(err)
	}
	taskReads := len(states.selected)
	parent, err := backend.Recipient(t.Context(), collaboration.Identity{Session: active.SessionID, Member: "parent"})
	if err != nil || len(parent) != 1 || parent[0].Generation != "epoch-2" {
		t.Fatalf("parent epoch = %+v, %v", parent, err)
	}
	if len(states.selected) != taskReads {
		t.Fatal("ordinary parent lookup read unrelated Task states")
	}
	if _, err := store.RemoveParticipant(t.Context(), session.RemoveParticipantRequest{SessionRef: active.SessionRef, ParticipantID: binding.ID}); err != nil {
		t.Fatal(err)
	}
	detached, err := backend.Recipient(t.Context(), collaboration.Identity{Session: active.SessionID, Member: "worker"})
	if err != nil || len(detached) != 0 {
		t.Fatalf("detached recipient still eligible: %+v, %v", detached, err)
	}
}
