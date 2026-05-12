package inmemory

import (
	"context"
	"testing"

	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
)

func TestSessionServiceE2E(t *testing.T) {
	t.Parallel()

	service := NewService(NewStore(Config{
		SessionIDGenerator: func() string { return "sess-e2e" },
	}))
	ctx := context.Background()

	createdSession, err := service.StartSession(ctx, session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: session.WorkspaceRef{
			Key: "ws-1",
			CWD: "/tmp/project",
		},
		Title: "Session E2E",
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}

	if _, err := service.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef: createdSession.SessionRef,
		Event: &session.Event{
			Message: ptrMessage(model.NewTextMessage(model.RoleUser, "hello")),
			Text:    "hello",
		},
	}); err != nil {
		t.Fatalf("AppendEvent(user) error = %v", err)
	}
	if _, err := service.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef: createdSession.SessionRef,
		Event: &session.Event{
			Message: ptrMessage(model.NewTextMessage(model.RoleAssistant, "world")),
			Text:    "world",
		},
	}); err != nil {
		t.Fatalf("AppendEvent(assistant) error = %v", err)
	}

	if err := service.UpdateState(ctx, createdSession.SessionRef, func(state map[string]any) (map[string]any, error) {
		state["controller"] = "kernel"
		return state, nil
	}); err != nil {
		t.Fatalf("UpdateState() error = %v", err)
	}

	loaded, err := service.LoadSession(ctx, session.LoadSessionRequest{
		SessionRef: createdSession.SessionRef,
	})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	if got, want := len(loaded.Events), 2; got != want {
		t.Fatalf("len(loaded.Events) = %d, want %d", got, want)
	}
	if got := loaded.Events[1].Text; got != "world" {
		t.Fatalf("assistant text = %q, want %q", got, "world")
	}
	if got := loaded.State["controller"]; got != "kernel" {
		t.Fatalf("state[controller] = %v, want %q", got, "kernel")
	}

	list, err := service.ListSessions(ctx, session.ListSessionsRequest{
		AppName:      "caelis",
		UserID:       "user-1",
		WorkspaceKey: "ws-1",
	})
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if got, want := len(list.Sessions), 1; got != want {
		t.Fatalf("len(list.Sessions) = %d, want %d", got, want)
	}
	if got := list.Sessions[0].SessionID; got != "sess-e2e" {
		t.Fatalf("session id = %q, want %q", got, "sess-e2e")
	}
}
