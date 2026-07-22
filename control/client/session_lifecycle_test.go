package controlclient

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
)

func TestCloseSessionRequiresQuiescentLeaseAndIsIdempotent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	service := sessionmemory.NewStore(sessionmemory.Config{})
	active, err := service.StartSession(ctx, session.StartSessionRequest{
		AppName: "caelis", UserID: "owner", PreferredSessionID: "session-close",
	})
	if err != nil {
		t.Fatal(err)
	}
	leaseStore := any(service).(session.SessionLeaseService)
	lease, err := leaseStore.AcquireSessionLease(ctx, session.AcquireSessionLeaseRequest{
		SessionRef: active.SessionRef, OwnerID: "runtime-1", TTL: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CloseSession(ctx, service, active, "premature"); !errors.Is(err, session.ErrLeaseConflict) {
		t.Fatalf("CloseSession() under runtime lease = %v, want ErrLeaseConflict", err)
	}
	if isClosed, err := IsSessionClosed(ctx, service, active.SessionRef); err != nil || isClosed {
		t.Fatalf("IsSessionClosed() after rejected close = %v, %v", isClosed, err)
	}
	if err := leaseStore.ReleaseSessionLease(ctx, session.ReleaseSessionLeaseRequest{
		SessionRef: active.SessionRef, LeaseID: lease.LeaseID, OwnerID: lease.OwnerID, ExpectedLeaseRevision: lease.Revision,
	}); err != nil {
		t.Fatalf("release runtime lease: %v", err)
	}

	closed, err := CloseSession(ctx, service, active, "done")
	if err != nil {
		t.Fatalf("CloseSession() after runtime quiescence = %v", err)
	}
	if closed.Revision != active.Revision+1 {
		t.Fatalf("closed revision = %d, want %d", closed.Revision, active.Revision+1)
	}
	isClosed, err := IsSessionClosed(ctx, service, active.SessionRef)
	if err != nil || !isClosed {
		t.Fatalf("IsSessionClosed() = %v, %v", isClosed, err)
	}
	page, err := any(service).(session.PagedReader).EventsPage(ctx, session.EventPageRequest{
		SessionRef: active.SessionRef, Visibility: session.EventPageClientReplay,
	})
	if err != nil {
		t.Fatal(err)
	}
	events := page.Events
	if len(events) != 1 || events[0].Lifecycle == nil || events[0].Lifecycle.Status != "closed" {
		t.Fatalf("close events = %#v", events)
	}
	repeated, err := CloseSession(ctx, service, closed, "ignored")
	if err != nil || repeated.Revision != closed.Revision {
		t.Fatalf("repeated CloseSession() = revision %d, %v", repeated.Revision, err)
	}

	authorizer := SessionAuthorizer{Sessions: service}
	principal := Principal{ID: "owner"}
	if err := authorizer.Authorize(ctx, principal, ActionPrompt, active.SessionID); !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("closed prompt authorization = %v", err)
	}
	if err := authorizer.Authorize(ctx, principal, ActionSessionInspect, active.SessionID); err != nil {
		t.Fatalf("closed inspect authorization = %v", err)
	}
	if err := authorizer.Authorize(ctx, principal, ActionSessionClose, active.SessionID); err != nil {
		t.Fatalf("repeated close authorization = %v", err)
	}
}
