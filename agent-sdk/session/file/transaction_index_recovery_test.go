package file

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestStoreIndexFailureKeepsWALUntilRestartRepairsIndex(t *testing.T) {
	root := t.TempDir()
	at := time.Date(2026, time.July, 13, 8, 9, 10, 0, time.UTC)
	store := NewStore(Config{
		RootDir: root,
		Clock:   func() time.Time { return at },
	})
	ctx := context.Background()
	active, err := store.GetOrCreate(ctx, session.StartSessionRequest{
		AppName: "caelis", UserID: "user", PreferredSessionID: "session-index-recovery",
		Workspace: session.WorkspaceRef{Key: "workspace", CWD: "/tmp/workspace"},
	})
	if err != nil {
		t.Fatalf("GetOrCreate() error = %v", err)
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
	reopened := NewService(reopenedStore)
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
	list, err := reopenedStore.List(ctx, session.ListSessionsRequest{})
	if err != nil || len(list.Sessions) != 1 || list.Sessions[0].SessionID != active.SessionID {
		t.Fatalf("List(after index repair) = %#v, %v, want recovered Session", list, err)
	}
}

func TestStoreCreateKeepsZeroEventWALUntilIndexRecovery(t *testing.T) {
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
	committed, err := store.GetOrCreate(context.Background(), request)
	if !session.IsCommitted(err) {
		t.Fatalf("GetOrCreate() error = %v, want committed index failure", err)
	}
	if committed.SessionID != request.PreferredSessionID || committed.Title != request.Title || !reflect.DeepEqual(committed.Metadata, request.Metadata) {
		t.Fatalf("GetOrCreate() committed Session = %#v, want original identity", committed)
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
	loaded, err := reopened.GetOrCreate(context.Background(), retry)
	if err != nil {
		t.Fatalf("GetOrCreate(recovery retry) error = %v", err)
	}
	if !reflect.DeepEqual(loaded, committed) {
		t.Fatalf("GetOrCreate(recovery retry) rebuilt Session\ngot:  %#v\nwant: %#v", loaded, committed)
	}
	paths, err := reopened.listDocumentPaths()
	if err != nil || len(paths) != 1 || paths[0] != documentPath {
		t.Fatalf("document paths = %#v, %v, want one original document", paths, err)
	}
	loadedDocument, err := NewService(reopened).LoadSession(context.Background(), session.LoadSessionRequest{SessionRef: committed.SessionRef})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	if len(loadedDocument.Events) != 0 || len(loadedDocument.State) != 0 {
		t.Fatalf("zero-event recovery introduced durable data: events=%#v state=%#v", loadedDocument.Events, loadedDocument.State)
	}
	list, err := reopened.List(context.Background(), session.ListSessionsRequest{})
	if err != nil || len(list.Sessions) != 1 || list.Sessions[0].Title != request.Title {
		t.Fatalf("List(after create recovery) = %#v, %v, want original Session once", list, err)
	}
	if _, err := os.Stat(transactionPath(documentPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Stat(recovered zero-event WAL) error = %v, want removed", err)
	}
}
