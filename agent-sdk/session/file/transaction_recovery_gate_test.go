package file

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

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

	writePending([]byte("invalid JSON"))
	if _, err := other.Session(ctx, fresh.SessionRef); err != nil {
		t.Fatalf("unrelated read after WAL corruption: %v", err)
	}
	if _, err := other.StartSession(ctx, req); !errors.Is(err, session.ErrUnreadableSession) || errors.Is(err, session.ErrUnsupportedLegacyFormat) {
		t.Fatalf("corrupt WAL error: %v", err)
	}
}

func TestRecoveryReservesPhysicalWALPathsAndFinishesCurrentTransaction(t *testing.T) {
	ctx := t.Context()
	at := time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)
	store := NewStore(Config{RootDir: t.TempDir(), Clock: func() time.Time { return at }})
	req := session.StartSessionRequest{AppName: "app", UserID: "user", PreferredSessionID: "z-healthy", Workspace: session.WorkspaceRef{Key: "."}}
	healthy, err := store.StartSession(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	msg := model.NewTextMessage(model.RoleUser, "recovered")
	store.transactionFault = func(phase string) error {
		if phase == "after_commit" {
			return errors.New("interrupted after WAL publication")
		}
		return nil
	}
	_, err = store.AppendEventsAndUpdateState(ctx, session.AppendEventsAndUpdateStateRequest{
		SessionRef: healthy.SessionRef, TransactionID: "receipt", MutationDigest: "digest",
		Events: []*session.Event{{ID: "fact", Type: session.EventTypeUser, Message: &msg}},
		UpdateState: func(_ []*session.Event, state map[string]any) (map[string]any, error) {
			state["committed"] = true
			return state, nil
		},
	})
	if !session.IsCommitted(err) {
		t.Fatalf("prepare valid WAL: %v", err)
	}
	healthyPath := store.newDocumentPath(healthy)
	claimed := healthy
	claimed.SessionID = "claimed-by-payload"
	forged, err := json.Marshal(persistedTransaction{Kind: transactionKind, Version: transactionVersion, Document: persistedDocument{Kind: documentKind, Version: documentVersion, Session: claimed}})
	if err != nil {
		t.Fatal(err)
	}
	bad := map[string][]byte{"a/orphan": []byte("{truncated"), "b-forged": forged}
	for id, raw := range bad {
		physical := healthy
		physical.SessionID = id
		if err := os.WriteFile(transactionPath(store.newDocumentPath(physical)), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	at = at.Add(24 * time.Hour)
	reader := NewStore(Config{RootDir: store.rootDir, Clock: func() time.Time { return at }})
	fault := errors.New("document sync failed")
	reader.durability.syncFile = func(f *os.File) error { return fault }
	if _, err := reader.ListSessions(ctx, session.ListSessionsRequest{}); !errors.Is(err, fault) || errors.Is(err, session.ErrUnreadableSession) {
		t.Fatalf("recovery swallowed infrastructure failure: %v", err)
	}
	reader.durability = durabilityOps{}
	loaded, err := reader.LoadSession(ctx, session.LoadSessionRequest{SessionRef: healthy.SessionRef})
	if err != nil || loaded.Session.Revision != 1 || loaded.State["committed"] != true || len(loaded.Events) != 1 {
		t.Fatalf("current WAL did not recover: %#v, %v", loaded, err)
	}
	if got, ok := session.ModelMessageOf(loaded.Events[0]); !ok || !reflect.DeepEqual(got, msg) || loaded.Events[0].ID != "fact" || loaded.Events[0].Seq != 1 {
		t.Fatalf("recovered event = %#v", loaded.Events[0])
	}
	if _, err := os.Stat(transactionPath(healthyPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("valid WAL cleanup: %v", err)
	}
	for id, want := range bad {
		req.PreferredSessionID = id
		if _, err := store.StartSession(ctx, req); !errors.Is(err, session.ErrUnreadableSession) {
			t.Fatalf("reusing %q from prior Store: %v", id, err)
		}
		physical := healthy
		physical.SessionID = id
		raw, err := os.ReadFile(transactionPath(store.newDocumentPath(physical)))
		if err != nil || !bytes.Equal(raw, want) {
			t.Fatalf("changed retained WAL for %q: %v", id, err)
		}
	}
	req.PreferredSessionID = claimed.SessionID
	if _, err := reader.StartSession(ctx, req); err != nil {
		t.Fatalf("WAL payload reserved another Session ID: %v", err)
	}
}

func TestSameSecondRolloutCollisionPreservesExactOwner(t *testing.T) {
	ctx := t.Context()
	at := time.Now()
	store := NewStore(Config{RootDir: t.TempDir(), Clock: func() time.Time { return at }})
	req := session.StartSessionRequest{AppName: "app", UserID: "u", PreferredSessionID: "one/two"}
	owner, err := store.StartSession(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	path := store.newDocumentPath(owner)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	req.PreferredSessionID = "one_two"
	if _, err := store.StartSession(ctx, req); !errors.Is(err, session.ErrInvalidSession) {
		t.Fatalf("colliding create: %v", err)
	}
	forged := owner
	forged.SessionID = req.PreferredSessionID
	wal, err := json.Marshal(persistedTransaction{Kind: transactionKind, Version: transactionVersion, Document: persistedDocument{Kind: documentKind, Version: documentVersion, Session: forged}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(transactionPath(path), wal, 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.markTransactionRecoveryPending(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Session(ctx, owner.SessionRef); !errors.Is(err, session.ErrUnreadableSession) {
		t.Fatalf("forged WAL shared a filename with indexed owner: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("owner document changed: %v", err)
	}
	at = at.Add(time.Second)
	if _, err := store.StartSession(ctx, req); err != nil {
		t.Fatalf("unrelated exact ID could not use its own path: %v", err)
	}
	req.PreferredSessionID = "escaped"
	req.Workspace.Key = ".."
	if _, err := store.StartSession(ctx, req); !errors.Is(err, session.ErrInvalidSession) {
		t.Fatalf("workspace outside Store root: %v", err)
	}
}
