package file

import (
	"context"
	"errors"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
)

func TestRepairWorkspaceKeysRewritesDurableSessionFenceAndTasks(t *testing.T) {
	ctx := context.Background()
	store := NewStore(Config{RootDir: t.TempDir()})
	active, err := store.StartSession(ctx, session.StartSessionRequest{
		AppName: "caelis", UserID: "user-1", PreferredSessionID: "session-1",
		Workspace: session.WorkspaceRef{Key: "legacy", CWD: "/tmp/project-a"},
	})
	if err != nil {
		t.Fatal(err)
	}
	appended, err := store.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef: active.SessionRef,
		Event:      &session.Event{Type: session.EventTypeNotice, Text: "preserved"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := NewTaskStore(store).Upsert(ctx, &taskapi.Entry{
		TaskID: "task-1", Kind: taskapi.KindCommand, Session: active.SessionRef, State: taskapi.StateCompleted,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcquireSessionFence(ctx, session.AcquireSessionFenceRequest{
		SessionRef: active.SessionRef, OwnerID: "host-1",
	}); err != nil {
		t.Fatal(err)
	}

	repair := WorkspaceKeyRepair{
		SessionID: "session-1", ExpectedWorkspaceKey: "legacy", ReplacementWorkspaceKey: "/tmp/project-a",
	}
	report, err := store.RepairWorkspaceKeys(ctx, []WorkspaceKeyRepair{repair})
	if err != nil {
		t.Fatal(err)
	}
	if report.RepairedSessions != 1 || report.RepairedTasks != 1 {
		t.Fatalf("repair report = %#v", report)
	}
	newRef := active.SessionRef
	newRef.WorkspaceKey = "/tmp/project-a"
	loaded, err := store.LoadSession(ctx, session.LoadSessionRequest{SessionRef: newRef})
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Events) != 1 || loaded.Events[0].ID != appended.ID {
		t.Fatalf("events after repair = %#v", loaded.Events)
	}
	if _, err := store.Session(ctx, active.SessionRef); !errors.Is(err, session.ErrSessionNotFound) {
		t.Fatalf("old Session ref error = %v, want not found", err)
	}
	doc, err := store.readDocument("session-1")
	if err != nil {
		t.Fatal(err)
	}
	if doc.Fence == nil || doc.Fence.SessionRef.WorkspaceKey != newRef.WorkspaceKey {
		t.Fatalf("persisted fence = %#v", doc.Fence)
	}
	task, err := NewTaskStore(store).Get(ctx, "task-1")
	if err != nil {
		t.Fatal(err)
	}
	if task.Session.WorkspaceKey != newRef.WorkspaceKey {
		t.Fatalf("Task Session = %#v", task.Session)
	}

	report, err = store.RepairWorkspaceKeys(ctx, []WorkspaceKeyRepair{repair})
	if err != nil {
		t.Fatal(err)
	}
	if report != (WorkspaceKeyRepairReport{}) {
		t.Fatalf("idempotent repair report = %#v", report)
	}
}

func TestRepairWorkspaceKeysResumesPersistedPlan(t *testing.T) {
	ctx := context.Background()
	store := NewStore(Config{RootDir: t.TempDir()})
	active, err := store.StartSession(ctx, session.StartSessionRequest{
		AppName: "caelis", UserID: "user-1", PreferredSessionID: "session-1",
		Workspace: session.WorkspaceRef{Key: "legacy", CWD: "/tmp/project-a"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := NewTaskStore(store).Upsert(ctx, &taskapi.Entry{
		TaskID: "task-1", Kind: taskapi.KindCommand, Session: active.SessionRef, State: taskapi.StateCompleted,
	}); err != nil {
		t.Fatal(err)
	}
	repair := WorkspaceKeyRepair{
		SessionID: active.SessionID, ExpectedWorkspaceKey: "legacy", ReplacementWorkspaceKey: "/tmp/project-a",
	}
	if err := store.mu.LockContext(ctx); err != nil {
		t.Fatal(err)
	}
	err = store.withRootWriteLockContext(ctx, func() error {
		if err := store.writeWorkspaceKeyRepairMarker(ctx, []WorkspaceKeyRepair{repair}); err != nil {
			return err
		}
		_, err := store.repairSessionWorkspaceKey(ctx, repair)
		return err
	})
	store.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	pending, err := store.PendingWorkspaceKeyRepairs(ctx)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending repairs = %#v, %v", pending, err)
	}
	report, err := store.RepairWorkspaceKeys(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.RepairedSessions != 0 || report.RepairedTasks != 1 {
		t.Fatalf("resumed report = %#v", report)
	}
	pending, err = store.PendingWorkspaceKeyRepairs(ctx)
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending repairs after resume = %#v, %v", pending, err)
	}
}
