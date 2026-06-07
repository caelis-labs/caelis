package session

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInMemoryServiceCreate(t *testing.T) {
	svc := InMemoryService()
	ctx := context.Background()
	sess, err := svc.Create(ctx, CreateRequest{
		AppName: "app", UserID: "user", WorkspaceKey: "ws", Title: "test",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if sess.Ref.SessionID == "" {
		t.Error("expected non-empty SessionID")
	}
	if sess.Title != "test" {
		t.Errorf("got title %q, want %q", sess.Title, "test")
	}
}

func TestInMemoryServiceGet(t *testing.T) {
	svc := InMemoryService()
	ctx := context.Background()
	created, _ := svc.Create(ctx, CreateRequest{
		AppName: "app", UserID: "user", WorkspaceKey: "ws",
	})
	got, err := svc.Get(ctx, created.Ref)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Ref.SessionID != created.Ref.SessionID {
		t.Errorf("got %q, want %q", got.Ref.SessionID, created.Ref.SessionID)
	}
}

func TestInMemoryServiceGetNotFound(t *testing.T) {
	svc := InMemoryService()
	_, err := svc.Get(context.Background(), Ref{"app", "user", "ws", "missing"})
	if err == nil {
		t.Error("expected error for missing session")
	}
}

func TestInMemoryServiceList(t *testing.T) {
	svc := InMemoryService()
	ctx := context.Background()
	_, err := svc.Create(ctx, CreateRequest{AppName: "app", UserID: "u1", WorkspaceKey: "ws"})
	require.NoError(t, err)
	_, err = svc.Create(ctx, CreateRequest{AppName: "app", UserID: "u1", WorkspaceKey: "ws"})
	require.NoError(t, err)
	_, err = svc.Create(ctx, CreateRequest{AppName: "app", UserID: "u2", WorkspaceKey: "ws"})
	require.NoError(t, err)
	resp, err := svc.List(ctx, ListRequest{AppName: "app", UserID: "u1"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(resp.Sessions) != 2 {
		t.Errorf("got %d, want 2", len(resp.Sessions))
	}
}

func TestInMemoryServiceFork(t *testing.T) {
	svc := InMemoryService()
	ctx := context.Background()
	orig, _ := svc.Create(ctx, CreateRequest{
		AppName: "app", UserID: "user", WorkspaceKey: "ws", Title: "original",
	})
	_, err := svc.AppendEvent(ctx, orig.Ref, Event{
		Kind:       EventKindUser,
		Visibility: VisibilityCanonical,
		UserPayload: &UserPayload{
			Parts: []EventPart{{Kind: PartKindText, Text: "hello"}},
		},
	})
	require.NoError(t, err)
	forked, err := svc.Fork(ctx, ForkRequest{Source: orig.Ref, Title: "forked"})
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	if forked.Ref.SessionID == orig.Ref.SessionID {
		t.Error("forked session should have different ID")
	}
	evts, _ := svc.Events(ctx, EventsRequest{SessionRef: forked.Ref})
	if len(evts) != 1 {
		t.Errorf("got %d events, want 1", len(evts))
	}
}

func TestInMemoryServiceDelete(t *testing.T) {
	svc := InMemoryService()
	ctx := context.Background()
	sess, _ := svc.Create(ctx, CreateRequest{AppName: "app", UserID: "u", WorkspaceKey: "ws"})
	if err := svc.Delete(ctx, sess.Ref); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	_, err := svc.Get(ctx, sess.Ref)
	if err == nil {
		t.Error("expected error after delete")
	}
}

func TestInMemoryServiceAppendEvent(t *testing.T) {
	svc := InMemoryService()
	ctx := context.Background()
	sess, _ := svc.Create(ctx, CreateRequest{AppName: "app", UserID: "u", WorkspaceKey: "ws"})
	evt, err := svc.AppendEvent(ctx, sess.Ref, Event{
		Kind:       EventKindUser,
		Visibility: VisibilityCanonical,
		UserPayload: &UserPayload{
			Parts: []EventPart{{Kind: PartKindText, Text: "hello"}},
		},
	})
	if err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	if evt.ID == "" {
		t.Error("expected non-empty event ID")
	}
	if evt.CreatedAt.IsZero() {
		t.Error("expected non-zero CreatedAt")
	}
}

func TestInMemoryServiceEvents(t *testing.T) {
	svc := InMemoryService()
	ctx := context.Background()
	sess, _ := svc.Create(ctx, CreateRequest{AppName: "app", UserID: "u", WorkspaceKey: "ws"})
	_, err := svc.AppendEvent(ctx, sess.Ref, Event{
		Kind: EventKindUser, Visibility: VisibilityCanonical,
		UserPayload: &UserPayload{Parts: []EventPart{{Kind: PartKindText, Text: "a"}}},
	})
	require.NoError(t, err)
	_, err = svc.AppendEvent(ctx, sess.Ref, Event{
		Kind: EventKindAssistant, Visibility: VisibilityCanonical,
		AssistantPayload: &AssistantPayload{Parts: []EventPart{{Kind: PartKindText, Text: "b"}}},
	})
	require.NoError(t, err)
	_, err = svc.AppendEvent(ctx, sess.Ref, Event{
		Kind: EventKindUser, Visibility: VisibilityCanonical,
		UserPayload: &UserPayload{Parts: []EventPart{{Kind: PartKindText, Text: "c"}}},
	})
	require.NoError(t, err)
	evts, err := svc.Events(ctx, EventsRequest{SessionRef: sess.Ref})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(evts) != 3 {
		t.Errorf("got %d, want 3", len(evts))
	}
	// Filter by kind.
	evts, _ = svc.Events(ctx, EventsRequest{
		SessionRef: sess.Ref,
		Kinds:      []EventKind{EventKindUser},
	})
	if len(evts) != 2 {
		t.Errorf("got %d user events, want 2", len(evts))
	}
}

func TestInMemoryServiceUpdateState(t *testing.T) {
	svc := InMemoryService()
	ctx := context.Background()
	sess, _ := svc.Create(ctx, CreateRequest{
		AppName: "app", UserID: "u", WorkspaceKey: "ws",
		State: State{"k1": "v1"},
	})
	err := svc.UpdateState(ctx, sess.Ref, func(s State) (State, error) {
		s["k2"] = "v2"
		return s, nil
	})
	if err != nil {
		t.Fatalf("UpdateState: %v", err)
	}
	got, _ := svc.Get(ctx, sess.Ref)
	if got.State["k2"] != "v2" {
		t.Errorf("got %q, want %q", got.State["k2"], "v2")
	}
}
