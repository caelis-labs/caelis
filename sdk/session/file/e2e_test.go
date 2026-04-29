package file

import (
	"context"
	"testing"

	sdkmodel "github.com/OnslaughtSnail/caelis/sdk/model"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
)

func TestSessionServiceE2E(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	ctx := context.Background()
	service := NewService(NewStore(Config{
		RootDir:            root,
		SessionIDGenerator: func() string { return "parent-1" },
	}))

	parent, err := service.StartSession(ctx, sdksession.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: sdksession.WorkspaceRef{
			Key: "ws-1",
			CWD: "/tmp/project",
		},
		Title: "Parent Session",
	})
	if err != nil {
		t.Fatalf("StartSession(parent) error = %v", err)
	}

	if _, err := service.AppendEvent(ctx, sdksession.AppendEventRequest{
		SessionRef: parent.SessionRef,
		Event: &sdksession.Event{
			Message: ptrMessage(sdkmodel.NewTextMessage(sdkmodel.RoleUser, "hello")),
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
	parent, err = service.PutParticipant(ctx, sdksession.PutParticipantRequest{
		SessionRef: parent.SessionRef,
		Binding: sdksession.ParticipantBinding{
			ID:            "sub-1",
			Kind:          sdksession.ParticipantKindSubagent,
			Role:          sdksession.ParticipantRoleDelegated,
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
	child, err := childService.StartSession(ctx, sdksession.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: sdksession.WorkspaceRef{
			Key: "ws-1",
			CWD: "/tmp/project",
		},
		PreferredSessionID: "child-1",
		Title:              "Child Session",
	})
	if err != nil {
		t.Fatalf("StartSession(child) error = %v", err)
	}
	if _, err := childService.AppendEvent(ctx, sdksession.AppendEventRequest{
		SessionRef: child.SessionRef,
		Event: &sdksession.Event{
			Message: ptrMessage(sdkmodel.NewTextMessage(sdkmodel.RoleAssistant, "child running")),
			Text:    "child running",
		},
	}); err != nil {
		t.Fatalf("AppendEvent(child) error = %v", err)
	}

	reopened := NewService(NewStore(Config{RootDir: root}))
	loadedParent, err := reopened.LoadSession(ctx, sdksession.LoadSessionRequest{
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

	loadedChild, err := reopened.LoadSession(ctx, sdksession.LoadSessionRequest{
		SessionRef: child.SessionRef,
	})
	if err != nil {
		t.Fatalf("LoadSession(child) error = %v", err)
	}
	if got, want := len(loadedChild.Events), 1; got != want {
		t.Fatalf("len(child events) = %d, want %d", got, want)
	}
	if got := loadedChild.Events[0].Text; got != "child running" {
		t.Fatalf("child event text = %q, want %q", got, "child running")
	}

	list, err := reopened.ListSessions(ctx, sdksession.ListSessionsRequest{
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
