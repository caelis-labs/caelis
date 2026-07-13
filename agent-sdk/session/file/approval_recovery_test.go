package file

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestPendingApprovalsUsesPersistedIndexWithoutReadingEventLog(t *testing.T) {
	root := t.TempDir()
	service := NewService(NewStore(Config{RootDir: root, SessionIDGenerator: func() string { return "session-indexed" }}))
	ctx := context.Background()
	active, err := service.StartSession(ctx, session.StartSessionRequest{
		AppName: "caelis", UserID: "user-1", Workspace: session.WorkspaceRef{Key: "ws-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := pendingApprovalTestEvent("approval-indexed")
	if _, err := service.AppendEvent(ctx, session.AppendEventRequest{SessionRef: active.SessionRef, Event: request}); err != nil {
		t.Fatal(err)
	}

	documentPath := rolloutDocumentPath(root, "ws-1", active.CreatedAt, active.SessionID)
	if err := os.Rename(eventLogPath(documentPath), eventLogPath(documentPath)+".hidden"); err != nil {
		t.Fatal(err)
	}
	pending, err := NewService(NewStore(Config{RootDir: root})).PendingApprovals(ctx)
	if err != nil {
		t.Fatalf("PendingApprovals() error = %v, want persisted-index read", err)
	}
	if len(pending) != 1 || pending[0].SessionRef != active.SessionRef || pending[0].Request.ApprovalRequestID != "approval-indexed" {
		t.Fatalf("PendingApprovals() = %#v, want indexed request", pending)
	}
}

func TestPendingApprovalsSettlementRemovesPersistedIndexEntry(t *testing.T) {
	root := t.TempDir()
	service := NewService(NewStore(Config{RootDir: root, SessionIDGenerator: func() string { return "session-settled" }}))
	ctx := context.Background()
	active, err := service.StartSession(ctx, session.StartSessionRequest{AppName: "caelis", UserID: "user-1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.AppendEvent(ctx, session.AppendEventRequest{SessionRef: active.SessionRef, Event: pendingApprovalTestEvent("approval-settled")}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AppendEvent(ctx, session.AppendEventRequest{SessionRef: active.SessionRef, Event: &session.Event{
		Type: session.EventTypeLifecycle, Visibility: session.VisibilityMirror, ApprovalRequestID: "approval-settled",
		Lifecycle: &session.EventLifecycle{Status: "interrupted", Reason: "test"},
	}}); err != nil {
		t.Fatal(err)
	}

	reopened := NewService(NewStore(Config{RootDir: root}))
	pending, err := reopened.PendingApprovals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("PendingApprovals() = %#v, want no settled request", pending)
	}
	page, err := reopened.EventsPage(ctx, session.EventPageRequest{
		SessionRef: active.SessionRef, Visibility: session.EventPageAllDurable,
	})
	if err != nil || len(page.Events) != 2 {
		t.Fatalf("EventsPage() = %#v, %v, want request and settlement", page.Events, err)
	}
}

func TestPendingApprovalsRebuildsLegacyIndexWithoutSemanticMutation(t *testing.T) {
	root := t.TempDir()
	store := NewStore(Config{RootDir: root, SessionIDGenerator: func() string { return "session-legacy-index" }})
	service := NewService(store)
	ctx := context.Background()
	active, err := service.StartSession(ctx, session.StartSessionRequest{
		AppName: "caelis", UserID: "user-1", Workspace: session.WorkspaceRef{Key: "ws-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.AppendEvent(ctx, session.AppendEventRequest{SessionRef: active.SessionRef, Event: pendingApprovalTestEvent("approval-legacy")}); err != nil {
		t.Fatal(err)
	}
	before, err := service.LoadSession(ctx, session.LoadSessionRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}

	documentPath := rolloutDocumentPath(root, "ws-1", active.CreatedAt, active.SessionID)
	raw, err := os.ReadFile(documentPath)
	if err != nil {
		t.Fatal(err)
	}
	var document persistedDocument
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	document.PendingApprovals = nil
	writePersistedDocument(t, documentPath, document)

	pending, err := NewService(NewStore(Config{RootDir: root})).PendingApprovals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].Request.ApprovalRequestID != "approval-legacy" {
		t.Fatalf("legacy PendingApprovals() = %#v, want rebuilt request", pending)
	}
	after, err := NewService(NewStore(Config{RootDir: root})).LoadSession(ctx, session.LoadSessionRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("derived-index repair changed semantic Session\nafter:  %#v\nbefore: %#v", after, before)
	}
	if err := os.Rename(eventLogPath(documentPath), eventLogPath(documentPath)+".hidden"); err != nil {
		t.Fatal(err)
	}
	pending, err = NewService(NewStore(Config{RootDir: root})).PendingApprovals(ctx)
	if err != nil || len(pending) != 1 {
		t.Fatalf("persisted rebuilt index = %#v, %v, want one without event log", pending, err)
	}
}

func TestPendingApprovalIndexRecoversWithCommittedWAL(t *testing.T) {
	root := t.TempDir()
	store := NewStore(Config{RootDir: root, SessionIDGenerator: func() string { return "session-wal-index" }})
	service := NewService(store)
	ctx := context.Background()
	active, err := service.StartSession(ctx, session.StartSessionRequest{AppName: "caelis", UserID: "user-1"})
	if err != nil {
		t.Fatal(err)
	}
	store.transactionFault = func(phase string) error {
		if phase == "after_commit" {
			return errors.New("simulated crash after committed WAL")
		}
		return nil
	}
	if _, err := service.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef: active.SessionRef, Event: pendingApprovalTestEvent("approval-wal"),
	}); !session.IsCommitted(err) {
		t.Fatalf("AppendEvent() error = %v, want committed error", err)
	}

	reopened := NewService(NewStore(Config{RootDir: root}))
	pending, err := reopened.PendingApprovals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].Request.ApprovalRequestID != "approval-wal" {
		t.Fatalf("recovered PendingApprovals() = %#v, want WAL request", pending)
	}
	page, err := reopened.EventsPage(ctx, session.EventPageRequest{
		SessionRef: active.SessionRef, Visibility: session.EventPageAllDurable,
	})
	if err != nil || len(page.Events) != 1 || page.Events[0].ApprovalRequestID != "approval-wal" {
		t.Fatalf("recovered EventsPage() = %#v, %v, want one request", page.Events, err)
	}
}

func pendingApprovalTestEvent(requestID string) *session.Event {
	return &session.Event{
		Type: session.EventTypeCustom, Visibility: session.VisibilityMirror, ApprovalRequestID: requestID,
		Protocol: &session.EventProtocol{
			Method:     session.ProtocolMethodRequestPermission,
			Permission: &session.ProtocolApproval{ToolCall: session.ProtocolToolCall{ID: "call-" + requestID, Name: "WRITE"}},
		},
	}
}
