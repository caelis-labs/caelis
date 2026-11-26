package file

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestStoreIndexFailureKeepsWALUntilRestartRepairsIndex(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	at := time.Date(2026, time.July, 13, 8, 9, 10, 0, time.UTC)
	store := NewStore(Config{
		RootDir: root,
		Clock:   func() time.Time { return at },
	})
	ctx := context.Background()
	active, err := store.StartSession(ctx, session.StartSessionRequest{
		AppName: "caelis", UserID: "user", PreferredSessionID: "session-index-recovery",
		Workspace: session.WorkspaceRef{Key: "workspace", CWD: "/tmp/workspace"},
	})

	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}

	indexPath := store.sessionIndexPath()
	breakSessionIndexAfterDocumentRename(t, indexPath)
	message := model.NewTextMessage(model.RoleUser, "canonical content survives index recovery")
	stateCalls := 0
	request := func() session.AppendEventsAndUpdateStateRequest {
		expected := uint64(0)
		return session.AppendEventsAndUpdateStateRequest{
			SessionRef:       active.SessionRef,
			ExpectedRevision: &expected,
			TransactionID:    "transaction-index-recovery",
			MutationDigest:   "cursor-v1",
			Events: []*session.Event{{
				ID: "event-index-recovery", Type: session.EventTypeUser, Message: &message,
			}},
			UpdateState: func(_ []*session.Event, state map[string]any) (map[string]any, error) {
				stateCalls++
				state["cursor"] = "one"
				return state, nil
			},
		}
	}
	if _, err := store.AppendEventsAndUpdateState(ctx, request()); !session.IsCommitted(err) {
		t.Fatalf("AppendEventsAndUpdateState() error = %v, want committed index failure", err)
	} else if !strings.Contains(strings.ToLower(err.Error()), "session index") {
		t.Fatalf("AppendEventsAndUpdateState() error = %v, want session index detail", err)
	}

	documentPath := rolloutDocumentPath(root, "workspace", at, active.SessionID)
	if _, err := os.Stat(transactionPath(documentPath)); err != nil {
		t.Fatalf("Stat(committed WAL) error = %v, want WAL retained", err)
	}
	if _, err := os.Stat(store.transactionRecoveryMarkerPath()); err != nil {
		t.Fatalf("Stat(root recovery marker) error = %v, want marker retained", err)
	}

	restoreSessionIndex(t, indexPath)
	reopenedStore := NewStore(Config{RootDir: root, Clock: func() time.Time { return at }})
	reopened := reopenedStore
	loaded, err := reopened.LoadSession(ctx, session.LoadSessionRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatalf("LoadSession(recovery) error = %v", err)
	}
	if loaded.Session.Revision != 1 || len(loaded.Events) != 1 || loaded.Events[0].ID != "event-index-recovery" || loaded.Events[0].Seq != 1 {
		t.Fatalf("recovered Session/events = revision %d events %#v, want one canonical event", loaded.Session.Revision, loaded.Events)
	}
	if got := loaded.State["cursor"]; got != "one" {
		t.Fatalf("recovered state cursor = %#v, want one", got)
	}
	replayed, ok := session.ModelMessageOf(loaded.Events[0])
	if !ok || !reflect.DeepEqual(replayed, message) {
		t.Fatalf("recovered model context = %#v, want %#v", replayed, message)
	}
	if _, err := os.Stat(transactionPath(documentPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Stat(recovered WAL) error = %v, want removed", err)
	}
	if _, err := os.Stat(reopenedStore.transactionRecoveryMarkerPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Stat(recovery marker) error = %v, want removed", err)
	}

	if _, err := reopened.AppendEventsAndUpdateState(ctx, request()); err != nil {
		t.Fatalf("AppendEventsAndUpdateState(idempotent retry) error = %v", err)
	}
	afterRetry, err := reopened.LoadSession(ctx, session.LoadSessionRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatalf("LoadSession(after retry) error = %v", err)
	}
	if stateCalls != 1 || afterRetry.Session.Revision != 1 || len(afterRetry.Events) != 1 || !reflect.DeepEqual(afterRetry.State, loaded.State) {
		t.Fatalf("retry rebuilt durable fact: calls=%d revision=%d events=%#v state=%#v", stateCalls, afterRetry.Session.Revision, afterRetry.Events, afterRetry.State)
	}
	list, err := reopenedStore.ListSessions(ctx, session.ListSessionsRequest{})
	if err != nil || len(list.Sessions) != 1 || list.Sessions[0].SessionID != active.SessionID {
		t.Fatalf("List(after index repair) = %#v, %v, want recovered Session", list, err)
	}
}

func TestStoreCreateKeepsZeroEventWALUntilIndexRecovery(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	at := time.Date(2026, time.July, 13, 11, 12, 13, 0, time.UTC)
	store := NewStore(Config{RootDir: root, Clock: func() time.Time { return at }})
	indexBroken := false
	store.transactionFault = func(phase string) error {
		if phase == "after_document" && !indexBroken {
			breakSessionIndexAfterDocumentRename(t, store.sessionIndexPath())
			indexBroken = true
		}
		return nil
	}
	request := session.StartSessionRequest{
		AppName: "caelis", UserID: "user", PreferredSessionID: "session-create-recovery",
		Title: "original title", Metadata: map[string]any{"source": "original"},
		Workspace: session.WorkspaceRef{Key: "workspace", CWD: "/tmp/workspace"},
	}
	committed, err := store.StartSession(context.Background(), request)
	if !session.IsCommitted(err) {
		t.Fatalf("StartSession() error = %v, want committed index failure", err)
	}
	if committed.SessionID != request.PreferredSessionID || committed.Title != request.Title || !reflect.DeepEqual(committed.Metadata, request.Metadata) {
		t.Fatalf("StartSession() committed Session = %#v, want original identity", committed)
	}

	documentPath := rolloutDocumentPath(root, "workspace", at, committed.SessionID)
	if _, err := os.Stat(transactionPath(documentPath)); err != nil {
		t.Fatalf("Stat(zero-event WAL) error = %v, want retained WAL", err)
	}
	store.transactionFault = nil
	restoreSessionIndex(t, store.sessionIndexPath())

	reopened := NewStore(Config{RootDir: root, Clock: func() time.Time { return at.Add(time.Hour) }})
	retry := request
	retry.Title = "must not replace original"
	retry.Metadata = map[string]any{"source": "retry"}
	loaded, err := reopened.StartSession(context.Background(), retry)
	if err != nil {
		t.Fatalf("StartSession(recovery retry) error = %v", err)
	}
	if !reflect.DeepEqual(loaded, committed) {
		t.Fatalf("StartSession(recovery retry) rebuilt Session\ngot:  %#v\nwant: %#v", loaded, committed)
	}
	foundPath, err := reopened.findDocumentPath(committed.SessionID, "")
	if err != nil || foundPath != documentPath {
		t.Fatalf("document path = %q, %v, want one original document", foundPath, err)
	}
	loadedDocument, err := reopened.LoadSession(context.Background(), session.LoadSessionRequest{SessionRef: committed.SessionRef})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	if len(loadedDocument.Events) != 0 || len(loadedDocument.State) != 0 {
		t.Fatalf("zero-event recovery introduced durable data: events=%#v state=%#v", loadedDocument.Events, loadedDocument.State)
	}
	list, err := reopened.ListSessions(context.Background(), session.ListSessionsRequest{})
	if err != nil || len(list.Sessions) != 1 || list.Sessions[0].Title != request.Title {
		t.Fatalf("List(after create recovery) = %#v, %v, want original Session once", list, err)
	}
	if _, err := os.Stat(transactionPath(documentPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Stat(recovered zero-event WAL) error = %v, want removed", err)
	}
}

func TestTransactionRecoveryScanIsAmortizedAndPendingMarkerForcesRecovery(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
