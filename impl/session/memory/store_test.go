package inmemory

import (
	"context"
	"testing"

	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
)

func TestStoreAppendAndListCanonicalEvents(t *testing.T) {
	t.Parallel()

	store := NewStore(Config{
		SessionIDGenerator: func() string { return "sess-1" },
		EventIDGenerator:   func() string { return "evt-1" },
	})
	ctx := context.Background()

	createdSession, err := store.GetOrCreate(ctx, session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: session.WorkspaceRef{
			Key: "ws-1",
			CWD: "/tmp/ws",
		},
	})
	if err != nil {
		t.Fatalf("GetOrCreate() error = %v", err)
	}

	_, err = store.AppendEvent(ctx, createdSession.SessionRef, &session.Event{
		Message: ptrMessage(model.NewTextMessage(model.RoleAssistant, "hello")),
		Text:    "hello",
	})
	if err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}

	_, err = store.AppendEvent(ctx, createdSession.SessionRef, session.MarkNotice(&session.Event{
		Message: ptrMessage(model.NewTextMessage(model.RoleSystem, "warn: retrying")),
	}, "warn", "retrying"))
	if err != nil {
		t.Fatalf("AppendEvent(notice) error = %v", err)
	}

	events, err := store.Events(ctx, session.EventsRequest{
		SessionRef: createdSession.SessionRef,
	})
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	if got, want := len(events), 1; got != want {
		t.Fatalf("len(events) = %d, want %d", got, want)
	}
	if got := events[0].Text; got != "hello" {
		t.Fatalf("event text = %q, want %q", got, "hello")
	}

	allEvents, err := store.Events(ctx, session.EventsRequest{
		SessionRef:       createdSession.SessionRef,
		IncludeTransient: true,
	})
	if err != nil {
		t.Fatalf("Events(include transient) error = %v", err)
	}
	if got, want := len(allEvents), 1; got != want {
		t.Fatalf("len(allEvents) = %d, want %d; memory store must not persist transient notices", got, want)
	}
}

func TestStoreUpdateState(t *testing.T) {
	t.Parallel()

	store := NewStore(Config{
		SessionIDGenerator: func() string { return "sess-1" },
	})
	ctx := context.Background()
	createdSession, err := store.GetOrCreate(ctx, session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
	})
	if err != nil {
		t.Fatalf("GetOrCreate() error = %v", err)
	}

	err = store.UpdateState(ctx, createdSession.SessionRef, func(state map[string]any) (map[string]any, error) {
		state["mode"] = "chat"
		return state, nil
	})
	if err != nil {
		t.Fatalf("UpdateState() error = %v", err)
	}

	state, err := store.SnapshotState(ctx, createdSession.SessionRef)
	if err != nil {
		t.Fatalf("SnapshotState() error = %v", err)
	}
	if got := state["mode"]; got != "chat" {
		t.Fatalf("state[mode] = %v, want %q", got, "chat")
	}
}

func TestStoreStateOperationsRepairNilState(t *testing.T) {
	t.Parallel()

	store := NewStore(Config{
		SessionIDGenerator: func() string { return "sess-1" },
	})
	ctx := context.Background()
	createdSession, err := store.GetOrCreate(ctx, session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
	})
	if err != nil {
		t.Fatalf("GetOrCreate() error = %v", err)
	}

	store.mu.Lock()
	store.sessions[createdSession.SessionID].state = nil
	store.mu.Unlock()

	state, err := store.SnapshotState(ctx, createdSession.SessionRef)
	if err != nil {
		t.Fatalf("SnapshotState() error = %v", err)
	}
	if state == nil || len(state) != 0 {
		t.Fatalf("SnapshotState() = %#v, want repaired empty state", state)
	}

	store.mu.Lock()
	store.sessions[createdSession.SessionID].state = nil
	store.mu.Unlock()
	if err := store.UpdateState(ctx, createdSession.SessionRef, func(state map[string]any) (map[string]any, error) {
		if state == nil {
			t.Fatal("UpdateState() received nil state, want empty map")
		}
		state["mode"] = "chat"
		return state, nil
	}); err != nil {
		t.Fatalf("UpdateState() error = %v", err)
	}
	state, err = store.SnapshotState(ctx, createdSession.SessionRef)
	if err != nil {
		t.Fatalf("SnapshotState(after update) error = %v", err)
	}
	if got := state["mode"]; got != "chat" {
		t.Fatalf("state[mode] = %v, want chat", got)
	}

	if err := store.ReplaceState(ctx, createdSession.SessionRef, nil); err != nil {
		t.Fatalf("ReplaceState(nil) error = %v", err)
	}
	state, err = store.SnapshotState(ctx, createdSession.SessionRef)
	if err != nil {
		t.Fatalf("SnapshotState(after replace nil) error = %v", err)
	}
	if state == nil || len(state) != 0 {
		t.Fatalf("state after ReplaceState(nil) = %#v, want empty map", state)
	}
}

func TestStoreControllerAndParticipantBindings(t *testing.T) {
	t.Parallel()

	service := NewService(NewStore(Config{
		SessionIDGenerator: func() string { return "sess-1" },
	}))
	ctx := context.Background()
	createdSession, err := service.StartSession(ctx, session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}

	createdSession, err = service.BindController(ctx, session.BindControllerRequest{
		SessionRef: createdSession.SessionRef,
		Binding: session.ControllerBinding{
			Kind:         session.ControllerKindACP,
			ControllerID: "copilot",
			EpochID:      "ep-1",
			Source:       "user_select",
		},
	})
	if err != nil {
		t.Fatalf("BindController() error = %v", err)
	}
	if got := createdSession.Controller.ControllerID; got != "copilot" {
		t.Fatalf("controller id = %q, want %q", got, "copilot")
	}

	createdSession, err = service.PutParticipant(ctx, session.PutParticipantRequest{
		SessionRef: createdSession.SessionRef,
		Binding: session.ParticipantBinding{
			ID:            "part-1",
			Kind:          session.ParticipantKindACP,
			Role:          session.ParticipantRoleSidecar,
			Source:        "user_attach",
			ControllerRef: "ep-1",
		},
	})
	if err != nil {
		t.Fatalf("PutParticipant() error = %v", err)
	}
	if got, want := len(createdSession.Participants), 1; got != want {
		t.Fatalf("len(participants) = %d, want %d", got, want)
	}

	createdSession, err = service.RemoveParticipant(ctx, session.RemoveParticipantRequest{
		SessionRef:    createdSession.SessionRef,
		ParticipantID: "part-1",
	})
	if err != nil {
		t.Fatalf("RemoveParticipant() error = %v", err)
	}
	if got := len(createdSession.Participants); got != 0 {
		t.Fatalf("len(participants) = %d, want 0", got)
	}
}

func ptrMessage(message model.Message) *model.Message {
	return &message
}
