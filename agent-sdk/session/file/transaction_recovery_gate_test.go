package file

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestTransactionRecoveryScanIsAmortizedAndPendingMarkerForcesRecovery(t *testing.T) {
	root := t.TempDir()
	store := NewStore(Config{RootDir: root, SessionIDGenerator: func() string { return "recovery-gate-session" }})
	var scans atomic.Int64
	store.transactionRecoveryScan = func() { scans.Add(1) }
	ctx := context.Background()
	active, err := store.StartSession(ctx, session.StartSessionRequest{AppName: "caelis", UserID: "user-1"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		if _, err := store.Session(ctx, active.SessionRef); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ListSessions(ctx, session.ListSessionsRequest{}); err != nil {
			t.Fatal(err)
		}
	}
	if got := scans.Load(); got != 1 {
		t.Fatalf("recovery scans before WAL = %d, want one", got)
	}

	store.transactionFault = func(phase string) error {
		if phase == "after_commit" {
			return errors.New("simulated process loss after WAL commit")
		}
		return nil
	}
	message := model.NewTextMessage(model.RoleUser, "recover from root marker")
	if _, err := store.AppendEvent(ctx, session.AppendEventRequest{SessionRef: active.SessionRef, Event: &session.Event{
		ID: "recovery-gate-event", Type: session.EventTypeUser, Message: &message,
	}}); !session.IsCommitted(err) {
		t.Fatalf("AppendEvent() error = %v, want committed error", err)
	}
	if _, err := os.Stat(store.transactionRecoveryMarkerPath()); err != nil {
		t.Fatalf("pending recovery marker: %v", err)
	}
	store.transactionFault = nil
	loaded, err := store.LoadSession(ctx, session.LoadSessionRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Events) != 1 || loaded.Events[0].ID != "recovery-gate-event" {
		t.Fatalf("recovered events = %#v, want committed WAL event", loaded.Events)
	}
	if got := scans.Load(); got != 2 {
		t.Fatalf("recovery scans after marked WAL = %d, want two", got)
	}
	if _, err := os.Stat(store.transactionRecoveryMarkerPath()); !os.IsNotExist(err) {
		t.Fatalf("recovery marker after apply error = %v, want absent", err)
	}
	if _, err := store.Session(ctx, active.SessionRef); err != nil {
		t.Fatal(err)
	}
	if got := scans.Load(); got != 2 {
		t.Fatalf("recovery scans after clean read = %d, want unchanged", got)
	}
}
