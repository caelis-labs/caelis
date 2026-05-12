package file

import (
	"context"
	"testing"

	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
)

func TestSessionServiceIntegration(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	ctx := context.Background()
	service := NewService(NewStore(Config{
		RootDir:            root,
		SessionIDGenerator: func() string { return "parent-1" },
	}))

	parent, err := service.StartSession(ctx, session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: session.WorkspaceRef{
			Key: "ws-1",
			CWD: "/tmp/project",
		},
		Title: "Parent Session",
	})
	if err != nil {
		t.Fatalf("StartSession(parent) error = %v", err)
	}

	if _, err := service.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef: parent.SessionRef,
		Event: &session.Event{
			Message: ptrMessage(model.NewTextMessage(model.RoleUser, "hello")),
			Text:    "hello",
		},
	}); err != nil {
		t.Fatalf("AppendEvent(user) error = %v", err)
	}
	if err := service.UpdateState(ctx, parent.SessionRef, func(state map[string]any) (map[string]any, error) {
		state["controller"] = "kernel"
		return state, nil
	}); err != nil {
		t.Fatalf("UpdateState(parent) error = %v", err)
	}
	parent, err = service.PutParticipant(ctx, session.PutParticipantRequest{
		SessionRef: parent.SessionRef,
		Binding: session.ParticipantBinding{
			ID:            "sub-1",
			Kind:          session.ParticipantKindSubagent,
			Role:          session.ParticipantRoleDelegated,
			SessionID:     "child-1",
			Source:        "spawn",
			DelegationID:  "dlg-1",
			ControllerRef: "ep-1",
		},
	})
	if err != nil {
		t.Fatalf("PutParticipant(parent) error = %v", err)
	}

	childStore := NewStore(Config{
		RootDir:            root,
		SessionIDGenerator: func() string { return "child-1" },
	})
	childService := NewService(childStore)
	child, err := childService.StartSession(ctx, session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: session.WorkspaceRef{
			Key: "ws-1",
			CWD: "/tmp/project",
		},
		PreferredSessionID: "child-1",
		Title:              "Child Session",
	})
	if err != nil {
		t.Fatalf("StartSession(child) error = %v", err)
	}
	if _, err := childService.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef: child.SessionRef,
		Event: &session.Event{
			Message: ptrMessage(model.NewTextMessage(model.RoleAssistant, "child running")),
			Text:    "child running",
		},
	}); err != nil {
		t.Fatalf("AppendEvent(child) error = %v", err)
	}

	reopened := NewService(NewStore(Config{RootDir: root}))
	loadedParent, err := reopened.LoadSession(ctx, session.LoadSessionRequest{
		SessionRef: parent.SessionRef,
	})
	if err != nil {
		t.Fatalf("LoadSession(parent) error = %v", err)
	}
	if got, want := len(loadedParent.Events), 1; got != want {
		t.Fatalf("len(parent events) = %d, want %d", got, want)
	}
	if got := loadedParent.State["controller"]; got != "kernel" {
		t.Fatalf("parent state[controller] = %v, want %q", got, "kernel")
	}
	if got, want := len(loadedParent.Session.Participants), 1; got != want {
		t.Fatalf("len(parent participants) = %d, want %d", got, want)
	}
	if got := loadedParent.Session.Participants[0].SessionID; got != "child-1" {
		t.Fatalf("parent child session anchor = %q, want %q", got, "child-1")
	}

	loadedChild, err := reopened.LoadSession(ctx, session.LoadSessionRequest{
		SessionRef: child.SessionRef,
	})
	if err != nil {
		t.Fatalf("LoadSession(child) error = %v", err)
	}
	if got, want := len(loadedChild.Events), 1; got != want {
		t.Fatalf("len(child events) = %d, want %d", got, want)
	}
	if got := session.EventText(loadedChild.Events[0]); got != "child running" {
		t.Fatalf("child event text = %q, want %q", got, "child running")
	}

	list, err := reopened.ListSessions(ctx, session.ListSessionsRequest{
		AppName:      "caelis",
		UserID:       "user-1",
		WorkspaceKey: "ws-1",
	})
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if got, want := len(list.Sessions), 2; got != want {
		t.Fatalf("len(list.Sessions) = %d, want %d", got, want)
	}
}
