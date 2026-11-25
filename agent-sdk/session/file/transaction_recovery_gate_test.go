package file

import (
	"bytes"
	"context"
	"encoding/json"
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

func TestRetiredCommittedWALOnlyBlocksItsOwnSession(t *testing.T) {
	ctx := t.Context()
	root := t.TempDir()
	store := NewStore(Config{RootDir: root})
	req := session.StartSessionRequest{AppName: "caelis", UserID: "user", PreferredSessionID: "old"}
	old, err := store.StartSession(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	document, err := store.readDocument(old.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	path, err := store.resolveWritePath(old)
	if err != nil {
		t.Fatal(err)
	}
	record := map[string]any{
		"kind": transactionKind, "version": transactionVersion, "document": document,
		"events": []json.RawMessage{json.RawMessage(`{"id":"retired","user_message":{"text":"old"}}`)},
	}
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	writePending := func(data []byte) {
		t.Helper()
		if err := os.WriteFile(transactionPath(path), data, 0600); err != nil {
			t.Fatal(err)
		}
		if err := store.markTransactionRecoveryPending(); err != nil {
			t.Fatal(err)
		}
	}
	// Cover a creation WAL that reached disk before its derived index row.
	db, err := store.openSessionIndex()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("DELETE FROM sessions WHERE session_id = ?", old.SessionID); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	writePending(raw)
	other := NewStore(Config{RootDir: root})
	fresh, err := other.StartSession(ctx, session.StartSessionRequest{AppName: "caelis", UserID: "user", PreferredSessionID: "new"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Session(ctx, fresh.SessionRef); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadSession(ctx, session.LoadSessionRequest{SessionRef: old.SessionRef}); !errors.Is(err, session.ErrUnsupportedLegacyFormat) {
		t.Fatalf("old Session load: %v", err)
	}
	if _, err := other.StartSession(ctx, req); !errors.Is(err, session.ErrUnsupportedLegacyFormat) {
		t.Fatalf("old Session reuse: %v", err)
	}
	if _, err := store.PendingApprovals(ctx); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(transactionPath(path))
	if err != nil || !bytes.Equal(saved, raw) {
		t.Fatalf("retired WAL changed: %v", err)
	}

	// An unreadable transaction cannot safely be attributed to an old Session.
	writePending([]byte("invalid JSON"))
	if _, err := other.Session(ctx, fresh.SessionRef); err == nil || errors.Is(err, session.ErrUnsupportedLegacyFormat) {
		t.Fatalf("corrupt WAL error: %v", err)
	}
}
