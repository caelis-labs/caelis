package file

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestPendingApprovalsUsesPersistedIndexWithoutReadingEventLog(t *testing.T) {
	root := t.TempDir()
	service := NewStore(Config{RootDir: root, SessionIDGenerator: func() string { return "session-indexed" }})
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
	pending, err := NewStore(Config{RootDir: root}).PendingApprovals(ctx)
	if err != nil {
		t.Fatalf("PendingApprovals() error = %v, want persisted-index read", err)
	}
	if len(pending) != 1 || pending[0].SessionRef != active.SessionRef || pending[0].Request.ApprovalRequestID != "approval-indexed" {
		t.Fatalf("PendingApprovals() = %#v, want indexed request", pending)
	}
}

func TestPendingApprovalsSettlementRemovesPersistedIndexEntry(t *testing.T) {
	root := t.TempDir()
	service := NewStore(Config{RootDir: root, SessionIDGenerator: func() string { return "session-settled" }})
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

	reopened := NewStore(Config{RootDir: root})
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

func TestSettlePendingApprovalRejectsStaleSnapshotAfterLiveResolutionAndLeaseRelease(t *testing.T) {
	root := t.TempDir()
	service := NewStore(Config{RootDir: root, SessionIDGenerator: func() string { return "session-settlement-cas" }})
	ctx := context.Background()
	active, err := service.StartSession(ctx, session.StartSessionRequest{AppName: "caelis", UserID: "user-1"})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := service.AcquireSessionLease(ctx, session.AcquireSessionLeaseRequest{
		SessionRef: active.SessionRef, OwnerID: "runtime-1", TTL: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	runtimeGuard := session.MutationGuard{
		Authority: session.MutationAuthorityRuntime, LeaseID: lease.LeaseID,
		OwnerID: lease.OwnerID, FencingToken: lease.FencingToken,
	}
	if _, err := service.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef: active.SessionRef, MutationGuard: runtimeGuard,
		Event: pendingApprovalTestEvent("approval-settlement-cas"),
	}); err != nil {
		t.Fatal(err)
	}
	pending, err := service.PendingApprovals(ctx)
	if err != nil || len(pending) != 1 {
		t.Fatalf("PendingApprovals() = %#v, %v, want one candidate", pending, err)
	}
	stale := pending[0]
	if stale.Revision == 0 || stale.Request.ID == "" || stale.Request.Seq == 0 {
		t.Fatalf("candidate identity = %#v, want revision/event/seq CAS", stale)
	}
	realSettlement := &session.Event{
		Type: session.EventTypeLifecycle, Visibility: session.VisibilityMirror,
		ApprovalRequestID: "approval-settlement-cas",
		Lifecycle:         &session.EventLifecycle{Status: "completed", Reason: "selected"},
	}
	if _, err := service.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef:    active.SessionRef,
		MutationGuard: session.ControlMutationGuard(session.ControlMutationPurposeApproval),
		Event:         realSettlement,
	}); err != nil {
		t.Fatal(err)
	}
	if err := service.ReleaseSessionLease(ctx, session.ReleaseSessionLeaseRequest{
		SessionRef: active.SessionRef, LeaseID: lease.LeaseID, OwnerID: lease.OwnerID,
		ExpectedLeaseRevision: lease.Revision,
	}); err != nil {
		t.Fatal(err)
	}
	expectedRevision := stale.Revision
	result, err := service.SettlePendingApproval(ctx, session.SettlePendingApprovalRequest{
		SessionRef: active.SessionRef, ExpectedRevision: &expectedRevision,
		MutationGuard:          session.ControlMutationGuard(session.ControlMutationPurposeLifecycle),
		ApprovalRequestID:      stale.Request.ApprovalRequestID,
		ExpectedRequestEventID: stale.Request.ID,
		ExpectedRequestSeq:     stale.Request.Seq,
		Settlement: &session.Event{
			Type: session.EventTypeLifecycle, Visibility: session.VisibilityMirror,
			ApprovalRequestID: stale.Request.ApprovalRequestID,
			Lifecycle:         &session.EventLifecycle{Status: "interrupted", Reason: "startup_recovery"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Settled || result.Event != nil {
		t.Fatalf("SettlePendingApproval(stale) = %#v, want no-op", result)
	}
	page, err := service.EventsPage(ctx, session.EventPageRequest{
		SessionRef: active.SessionRef, Visibility: session.EventPageAllDurable,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 2 || page.Events[1].Lifecycle == nil || page.Events[1].Lifecycle.Reason != "selected" {
		t.Fatalf("events after stale recovery = %#v, want request plus real settlement only", page.Events)
	}
}

func TestPendingApprovalsRebuildsLegacyIndexWithoutSemanticMutation(t *testing.T) {
	root := t.TempDir()
	store := NewStore(Config{RootDir: root, SessionIDGenerator: func() string { return "session-legacy-index" }})
	service := store
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

	pending, err := NewStore(Config{RootDir: root}).PendingApprovals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].Request.ApprovalRequestID != "approval-legacy" {
		t.Fatalf("legacy PendingApprovals() = %#v, want rebuilt request", pending)
	}
	after, err := NewStore(Config{RootDir: root}).LoadSession(ctx, session.LoadSessionRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("derived-index repair changed semantic Session\nafter:  %#v\nbefore: %#v", after, before)
	}
	if err := os.Rename(eventLogPath(documentPath), eventLogPath(documentPath)+".hidden"); err != nil {
		t.Fatal(err)
	}
	pending, err = NewStore(Config{RootDir: root}).PendingApprovals(ctx)
	if err != nil || len(pending) != 1 {
		t.Fatalf("persisted rebuilt index = %#v, %v, want one without event log", pending, err)
	}
}

func TestPendingApprovalIndexRecoversWithCommittedWAL(t *testing.T) {
	root := t.TempDir()
	store := NewStore(Config{RootDir: root, SessionIDGenerator: func() string { return "session-wal-index" }})
	service := store
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

	reopened := NewStore(Config{RootDir: root})
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

func TestPendingApprovalsReleasesLocksBetweenLegacySessions(t *testing.T) {
	root := t.TempDir()
	setupStore := NewStore(Config{RootDir: root})
	setup := setupStore
	ctx := context.Background()
	sessions := make([]session.Session, 0, 3)
	for i := 0; i < 3; i++ {
		active, err := setup.StartSession(ctx, session.StartSessionRequest{
			AppName: "caelis", UserID: "user-1", Workspace: session.WorkspaceRef{Key: "ws-1"},
			PreferredSessionID: fmt.Sprintf("legacy-session-%d", i), Title: fmt.Sprintf("legacy %d", i),
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := setup.AppendEvent(ctx, session.AppendEventRequest{
			SessionRef: active.SessionRef, Event: pendingApprovalTestEvent(fmt.Sprintf("approval-%d", i)),
		}); err != nil {
			t.Fatal(err)
		}
		sessions = append(sessions, active)
	}
	lease, err := setup.AcquireSessionLease(ctx, session.AcquireSessionLeaseRequest{
		SessionRef: sessions[1].SessionRef, OwnerID: "runtime-1", TTL: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, active := range sessions {
		documentPath := rolloutDocumentPath(root, active.WorkspaceKey, active.CreatedAt, active.SessionID)
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
	}

	recoveryStore := NewStore(Config{RootDir: root})
	firstSessionDone := make(chan struct{})
	releaseRecovery := make(chan struct{})
	var pauseOnce sync.Once
	recoveryStore.approvalRecoverySessionDone = func(session.SessionRef) {
		pauseOnce.Do(func() {
			close(firstSessionDone)
			<-releaseRecovery
		})
	}
	type recoveryResult struct {
		pending []session.PendingApproval
		err     error
	}
	recoveryDone := make(chan recoveryResult, 1)
	go func() {
		pending, err := recoveryStore.PendingApprovals(context.Background())
		recoveryDone <- recoveryResult{pending: pending, err: err}
	}()
	select {
	case <-firstSessionDone:
	case <-time.After(5 * time.Second):
		t.Fatal("legacy recovery did not finish its first Session")
	}

	interactions := NewStore(Config{RootDir: root})
	interactionCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	listed, err := interactions.ListSessions(interactionCtx, session.ListSessionsRequest{
		AppName: "caelis", UserID: "user-1", WorkspaceKey: "ws-1",
	})
	if err != nil || len(listed.Sessions) != len(sessions) {
		t.Fatalf("ListSessions during paused recovery = %d, %v", len(listed.Sessions), err)
	}
	heartbeat, err := interactions.HeartbeatSessionLease(interactionCtx, session.HeartbeatSessionLeaseRequest{
		SessionRef: sessions[1].SessionRef, LeaseID: lease.LeaseID, OwnerID: lease.OwnerID,
		ExpectedLeaseRevision: lease.Revision, TTL: time.Minute,
	})
	if err != nil || heartbeat.Revision != lease.Revision+1 {
		t.Fatalf("HeartbeatSessionLease during paused recovery = %#v, %v", heartbeat, err)
	}

	close(releaseRecovery)
	select {
	case result := <-recoveryDone:
		if result.err != nil || len(result.pending) != len(sessions) {
			t.Fatalf("PendingApprovals() = %d, %v, want %d", len(result.pending), result.err, len(sessions))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("legacy recovery did not complete")
	}
}

func TestPendingApprovalsUsesStableNamespaceSnapshotWithoutRecoveryScanAmplification(t *testing.T) {
	// This high-cardinality test exercises snapshot/recovery ordering; crash
	// durability and sync failure classification have dedicated coverage.
	root := t.TempDir()
	base := time.Date(2026, time.July, 13, 12, 0, 0, 0, time.UTC)
	var recoveryScans atomic.Int64
	setupStore := newLogicalTestStore(t, Config{RootDir: root, Clock: func() time.Time { return base }})
	setupStore.transactionRecoveryScan = func() { recoveryScans.Add(1) }
	setup := setupStore
	ctx := context.Background()
	const sessionCount = 130
	var target session.Session
	for i := 0; i < sessionCount; i++ {
		active, err := setup.StartSession(ctx, session.StartSessionRequest{
			AppName: "caelis", UserID: "user-1", Workspace: session.WorkspaceRef{Key: "ws-1"},
			PreferredSessionID: fmt.Sprintf("approval-snapshot-%03d", i),
		})
		if err != nil {
			t.Fatal(err)
		}
		if i == sessionCount-1 {
			target = active
		}
	}
	if _, err := setup.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef: target.SessionRef, Event: pendingApprovalTestEvent("approval-stable-snapshot"),
	}); err != nil {
		t.Fatal(err)
	}

	recoveryStore := newLogicalTestStore(t, Config{RootDir: root})
	recoveryStore.transactionRecoveryScan = func() { recoveryScans.Add(1) }
	var reorderOnce sync.Once
	recoveryStore.approvalRecoverySessionDone = func(session.SessionRef) {
		reorderOnce.Do(func() {
			mutator := newLogicalTestStore(t, Config{RootDir: root, Clock: func() time.Time { return base.Add(time.Hour) }})
			if _, err := mutator.ReplaceState(context.Background(), session.ReplaceStateRequest{
				SessionRef:    target.SessionRef,
				State:         map[string]any{"reordered": true},
				MutationGuard: session.ControlMutationGuard(session.ControlMutationPurposeTest),
			}); err != nil {
				t.Errorf("reorder target Session: %v", err)
			}
		})
	}
	pending, err := recoveryStore.PendingApprovals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].SessionRef.SessionID != target.SessionID || pending[0].Request.ApprovalRequestID != "approval-stable-snapshot" {
		t.Fatalf("PendingApprovals() = %#v, want reordered tail Session", pending)
	}
	if got := recoveryScans.Load(); got != 1 {
		t.Fatalf("transaction recovery scans = %d, want one root initialization for %d Sessions", got, sessionCount)
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
