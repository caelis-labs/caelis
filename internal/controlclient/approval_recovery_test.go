package controlclient

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	inmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
)

func TestSweepAbandonedApprovalsInterruptsPromptOnce(t *testing.T) {
	ctx := context.Background()
	service := inmemory.NewService(inmemory.NewStore(inmemory.Config{SessionIDGenerator: func() string { return "session-1" }}))
	active, err := service.StartSession(ctx, session.StartSessionRequest{AppName: "caelis", UserID: "user-1"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.AppendEvent(ctx, session.AppendEventRequest{SessionRef: active.SessionRef, Event: &session.Event{
		Type: session.EventTypeCustom, Visibility: session.VisibilityMirror, ApprovalRequestID: "approval-1",
		Protocol: &session.EventProtocol{Method: session.ProtocolMethodRequestPermission, Permission: &session.ProtocolApproval{
			ToolCall: session.ProtocolToolCall{ID: "call-1", Name: "WRITE"},
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.AcquireSessionLease(ctx, session.AcquireSessionLeaseRequest{
		SessionRef: active.SessionRef, OwnerID: "abandoned-runtime", TTL: time.Minute,
	}); err != nil {
		t.Fatal(err)
	}
	if err := SweepAbandonedApprovals(ctx, service); err != nil {
		t.Fatal(err)
	}
	if err := SweepAbandonedApprovals(ctx, service); err != nil {
		t.Fatal(err)
	}
	page, err := service.EventsPage(ctx, session.EventPageRequest{SessionRef: active.SessionRef, Visibility: session.EventPageClientReplay})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 2 || page.Events[1].Lifecycle == nil || page.Events[1].Lifecycle.Status != "interrupted" || page.Events[1].ApprovalRequestID != "approval-1" {
		t.Fatalf("recovered approval events = %#v", page.Events)
	}
}

func TestSweepAbandonedApprovalsContinuesPastTwoHundredSessions(t *testing.T) {
	ctx := context.Background()
	nextID := 0
	service := inmemory.NewService(inmemory.NewStore(inmemory.Config{SessionIDGenerator: func() string {
		nextID++
		return fmt.Sprintf("session-%03d", nextID)
	}}))
	var target session.Session
	for i := 0; i < 205; i++ {
		active, err := service.StartSession(ctx, session.StartSessionRequest{AppName: "caelis", UserID: "user-1"})
		if err != nil {
			t.Fatal(err)
		}
		if i == 204 {
			target = active
		}
	}
	if _, err := service.AppendEvent(ctx, session.AppendEventRequest{SessionRef: target.SessionRef, Event: &session.Event{
		Type: session.EventTypeCustom, Visibility: session.VisibilityMirror, ApprovalRequestID: "approval-last-page",
		Protocol: &session.EventProtocol{Method: session.ProtocolMethodRequestPermission, Permission: &session.ProtocolApproval{
			ToolCall: session.ProtocolToolCall{ID: "call-last-page", Name: "WRITE"},
		}},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := SweepAbandonedApprovals(ctx, service); err != nil {
		t.Fatal(err)
	}
	page, err := service.EventsPage(ctx, session.EventPageRequest{SessionRef: target.SessionRef, Visibility: session.EventPageClientReplay})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 2 || page.Events[1].Lifecycle == nil || page.Events[1].ApprovalRequestID != "approval-last-page" {
		t.Fatalf("last-page recovered events = %#v", page.Events)
	}
}
