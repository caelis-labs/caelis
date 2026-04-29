package inmemory

import (
	"context"
	"testing"

	sdkmodel "github.com/OnslaughtSnail/caelis/sdk/model"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
)

func TestStoreAppendAndListCanonicalEvents(t *testing.T) {
	t.Parallel()

	store := NewStore(Config{
		SessionIDGenerator: func() string { return "sess-1" },
		EventIDGenerator:   func() string { return "evt-1" },
	})
	ctx := context.Background()

	session, err := store.GetOrCreate(ctx, sdksession.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: sdksession.WorkspaceRef{
			Key: "ws-1",
			CWD: "/tmp/ws",
		},
	})
	if err != nil {
		t.Fatalf("GetOrCreate() error = %v", err)
	}

	_, err = store.AppendEvent(ctx, session.SessionRef, &sdksession.Event{
		Message: ptrMessage(sdkmodel.NewTextMessage(sdkmodel.RoleAssistant, "hello")),
		Text:    "hello",
	})
	if err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}

	_, err = store.AppendEvent(ctx, session.SessionRef, sdksession.MarkNotice(&sdksession.Event{
		Message: ptrMessage(sdkmodel.NewTextMessage(sdkmodel.RoleSystem, "warn: retrying")),
	}, "warn", "retrying"))
	if err != nil {
		t.Fatalf("AppendEvent(notice) error = %v", err)
	}

	events, err := store.Events(ctx, sdksession.EventsRequest{
		SessionRef: session.SessionRef,
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
}

func TestStoreUpdateState(t *testing.T) {
	t.Parallel()

	store := NewStore(Config{
		SessionIDGenerator: func() string { return "sess-1" },
	})
	ctx := context.Background()
	session, err := store.GetOrCreate(ctx, sdksession.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
	})
	if err != nil {
		t.Fatalf("GetOrCreate() error = %v", err)
	}

	err = store.UpdateState(ctx, session.SessionRef, func(state map[string]any) (map[string]any, error) {
		state["mode"] = "chat"
		return state, nil
	})
	if err != nil {
		t.Fatalf("UpdateState() error = %v", err)
	}

	state, err := store.SnapshotState(ctx, session.SessionRef)
	if err != nil {
		t.Fatalf("SnapshotState() error = %v", err)
	}
	if got := state["mode"]; got != "chat" {
		t.Fatalf("state[mode] = %v, want %q", got, "chat")
	}
}

func TestStoreControllerAndParticipantBindings(t *testing.T) {
	t.Parallel()

	service := NewService(NewStore(Config{
		SessionIDGenerator: func() string { return "sess-1" },
	}))
	ctx := context.Background()
	session, err := service.StartSession(ctx, sdksession.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}

	session, err = service.BindController(ctx, sdksession.BindControllerRequest{
		SessionRef: session.SessionRef,
		Binding: sdksession.ControllerBinding{
			Kind:         sdksession.ControllerKindACP,
			ControllerID: "copilot",
			EpochID:      "ep-1",
			Source:       "user_select",
		},
	})
	if err != nil {
		t.Fatalf("BindController() error = %v", err)
	}
	if got := session.Controller.ControllerID; got != "copilot" {
		t.Fatalf("controller id = %q, want %q", got, "copilot")
	}

	session, err = service.PutParticipant(ctx, sdksession.PutParticipantRequest{
		SessionRef: session.SessionRef,
		Binding: sdksession.ParticipantBinding{
			ID:            "part-1",
			Kind:          sdksession.ParticipantKindACP,
			Role:          sdksession.ParticipantRoleSidecar,
			Source:        "user_attach",
			ControllerRef: "ep-1",
		},
	})
	if err != nil {
		t.Fatalf("PutParticipant() error = %v", err)
	}
	if got, want := len(session.Participants), 1; got != want {
		t.Fatalf("len(participants) = %d, want %d", got, want)
	}

	session, err = service.RemoveParticipant(ctx, sdksession.RemoveParticipantRequest{
		SessionRef:    session.SessionRef,
		ParticipantID: "part-1",
	})
	if err != nil {
		t.Fatalf("RemoveParticipant() error = %v", err)
	}
	if got := len(session.Participants); got != 0 {
		t.Fatalf("len(participants) = %d, want 0", got)
	}
}

func ptrMessage(message sdkmodel.Message) *sdkmodel.Message {
	return &message
}
