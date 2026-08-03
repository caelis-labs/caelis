package controlclient

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/sessionvisibility"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
)

func TestClientListSessionsFillsLimitAcrossManagedPages(t *testing.T) {
	t.Parallel()

	store := &pagedSessionDirectoryStore{}
	client := &Client{config: ClientConfig{
		Authorizer: sessionDirectoryAllowAuthorizer{},
		Sessions:   store,
	}}
	listed, err := client.ListSessions(context.Background(), Principal{ID: "owner"}, ListSessionsRequest{
		WorkspaceKey: "workspace", Limit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Sessions) != 2 || listed.Sessions[0].SessionID != "visible-1" || listed.Sessions[1].SessionID != "visible-2" {
		t.Fatalf("ListSessions() = %#v, want two visible Sessions", listed)
	}
	if len(store.requests) != 3 || store.requests[0].Limit != 2 || store.requests[1].Limit != 2 || store.requests[2].Limit != 1 {
		t.Fatalf("ListSessions requests = %#v, want remaining visible limit across raw pages", store.requests)
	}
	for _, request := range store.requests {
		if request.UserID != "owner" || request.WorkspaceKey != "workspace" {
			t.Fatalf("ListSessions request = %#v, want principal and workspace scope", request)
		}
	}
}

type sessionDirectoryAllowAuthorizer struct{}

func (sessionDirectoryAllowAuthorizer) Authorize(context.Context, Principal, Action, string) error {
	return nil
}

type pagedSessionDirectoryStore struct {
	requests []session.ListSessionsRequest
}

func (s *pagedSessionDirectoryStore) ListSessions(_ context.Context, request session.ListSessionsRequest) (session.SessionList, error) {
	s.requests = append(s.requests, request)
	switch request.Cursor {
	case "":
		return session.SessionList{
			Sessions:   []session.SessionSummary{managedSessionSummary("managed-1")},
			NextCursor: "page-1",
		}, nil
	case "page-1":
		return session.SessionList{
			Sessions:   []session.SessionSummary{{SessionRef: session.SessionRef{SessionID: "visible-1"}}},
			NextCursor: "page-2",
		}, nil
	case "page-2":
		return session.SessionList{
			Sessions: []session.SessionSummary{
				managedSessionSummary("managed-2"),
				{SessionRef: session.SessionRef{SessionID: "visible-2"}},
			},
		}, nil
	default:
		return session.SessionList{}, nil
	}
}

func managedSessionSummary(sessionID string) session.SessionSummary {
	return session.SessionSummary{
		SessionRef: session.SessionRef{SessionID: sessionID},
		Metadata:   map[string]any{sessionvisibility.MetadataSystemManagedAgent: "subagent"},
	}
}

func TestClientReconnectKeepsSystemManagedSessionAvailableToInternalTurnClients(t *testing.T) {
	t.Parallel()

	subscription := &visibilityTestSubscription{}
	state := &visibilityReconnectReader{result: ReconnectResult{
		State: SessionState{
			SessionID: "system-child",
			Metadata:  map[string]any{sessionvisibility.MetadataSystemManagedAgent: "subagent"},
		},
		Subscription: subscription,
	}}
	client := &Client{config: ClientConfig{Authorizer: sessionDirectoryAllowAuthorizer{}, State: state}}

	got, err := client.Reconnect(context.Background(), Principal{
		ID: "owner", Roles: []string{RoleSystemSessionRuntime},
	}, ReconnectRequest{SessionID: "system-child"})
	if err != nil {
		t.Fatal(err)
	}
	if got.State.SessionID != "system-child" || got.Subscription != subscription {
		t.Fatalf("Reconnect() = %#v, want system-managed feed for internal Turn delivery", got)
	}
	if got := subscription.closeCalls.Load(); got != 0 {
		t.Fatalf("subscription Close() calls = %d, want 0", got)
	}
}

func TestClientReconnectHidesSystemManagedSessionFromOrdinaryPrincipal(t *testing.T) {
	t.Parallel()

	subscription := &visibilityTestSubscription{}
	state := &visibilityReconnectReader{result: ReconnectResult{
		State: SessionState{
			SessionID: "system-child",
			Metadata:  map[string]any{sessionvisibility.MetadataSystemManagedAgent: "subagent"},
		},
		Subscription: subscription,
	}}
	client := &Client{config: ClientConfig{Authorizer: sessionDirectoryAllowAuthorizer{}, State: state}}

	_, err := client.Reconnect(context.Background(), Principal{ID: "owner"}, ReconnectRequest{SessionID: "system-child"})
	if !errors.Is(err, session.ErrSessionNotFound) {
		t.Fatalf("Reconnect() error = %v, want session not found", err)
	}
	if got := subscription.closeCalls.Load(); got != 1 {
		t.Fatalf("subscription Close() calls = %d, want 1", got)
	}
}

func TestClientReconnectReturnsUserVisibleSession(t *testing.T) {
	t.Parallel()

	subscription := &visibilityTestSubscription{}
	want := ReconnectResult{
		State:        SessionState{SessionID: "visible"},
		Subscription: subscription,
	}
	state := &visibilityReconnectReader{result: want}
	client := &Client{config: ClientConfig{Authorizer: sessionDirectoryAllowAuthorizer{}, State: state}}

	got, err := client.Reconnect(context.Background(), Principal{ID: "owner"}, ReconnectRequest{SessionID: "visible"})
	if err != nil {
		t.Fatal(err)
	}
	if got.State.SessionID != want.State.SessionID || got.Subscription != subscription {
		t.Fatalf("Reconnect() = %#v, want visible result", got)
	}
	if got := subscription.closeCalls.Load(); got != 0 {
		t.Fatalf("subscription Close() calls = %d, want 0", got)
	}
}

type visibilityReconnectReader struct {
	result ReconnectResult
	err    error
}

func (r *visibilityReconnectReader) State(context.Context, StateRequest) (SessionState, error) {
	return r.result.State, r.err
}

func (r *visibilityReconnectReader) Reconnect(context.Context, ReconnectRequest) (ReconnectResult, error) {
	return r.result, r.err
}

type visibilityTestSubscription struct {
	closeCalls atomic.Int32
}

func (*visibilityTestSubscription) Backfill() <-chan eventstream.Envelope {
	ch := make(chan eventstream.Envelope)
	close(ch)
	return ch
}

func (*visibilityTestSubscription) Events() <-chan eventstream.Envelope {
	ch := make(chan eventstream.Envelope)
	close(ch)
	return ch
}

func (*visibilityTestSubscription) BackfillDone() <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

func (s *visibilityTestSubscription) Close() error {
	s.closeCalls.Add(1)
	return nil
}

func (*visibilityTestSubscription) Err() error         { return nil }
func (*visibilityTestSubscription) LastCursor() string { return "" }
