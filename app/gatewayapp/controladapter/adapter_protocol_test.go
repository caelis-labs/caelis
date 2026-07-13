package controladapter

import (
	"context"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
	controlclientport "github.com/caelis-labs/caelis/ports/controlclient"
	"github.com/caelis-labs/caelis/ports/gateway"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

func TestAdapterReplayReturnsStableClientProtocol(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	startedAt := time.Unix(123, 0)
	updatedAt := time.Unix(456, 0)
	ref := session.SessionRef{AppName: "caelis", UserID: "user-1", WorkspaceKey: "ws", SessionID: "session-1"}
	gw := &protocolGatewayService{
		feed: &protocolSessionFeed{
			events:   []eventstream.Envelope{{Kind: eventstream.KindSessionUpdate, Cursor: "cursor-1", SessionID: "session-1"}},
			boundary: "cursor-1",
		},
		controlState: gateway.ControlPlaneState{
			SessionRef: ref,
			RunState: agent.RunState{
				Status:          agent.RunLifecycleStatusWaitingApproval,
				ActiveRunID:     "run-1",
				WaitingApproval: true,
				UpdatedAt:       updatedAt,
			},
			HasActiveTurn: true,
		},
		active: []gateway.ActiveTurnState{{
			SessionRef: ref,
			Kind:       gateway.ActiveTurnKindKernel,
			HandleID:   "handle-1",
			RunID:      "run-1",
			TurnID:     "turn-1",
			StartedAt:  startedAt,
		}},
	}
	driver := newProtocolTestAdapter(t, gw, session.Session{SessionRef: ref})

	result, err := driver.Replay(ctx, eventstream.ReplayRequest{SessionID: "session-1", Cursor: "cursor-0", Limit: 25})
	if err != nil {
		t.Fatalf("Replay() error = %v", err)
	}
	if gw.feed.request.Cursor != "cursor-0" || gw.feed.request.SessionID != "session-1" {
		t.Fatalf("feed subscribe request = %+v, want signed cursor and explicit session", gw.feed.request)
	}
	if result.SessionID != "session-1" || result.NextCursor != "cursor-1" || !result.Durable || !result.HasLiveHandle {
		t.Fatalf("replay result = %+v", result)
	}
	if result.RunState.Status != eventstream.RunStateWaitingApproval || !result.RunState.WaitingApproval || result.RunState.HandleID != "handle-1" || !result.RunState.StartedAt.Equal(startedAt) {
		t.Fatalf("run state = %+v, want waiting approval with active handle", result.RunState)
	}
}

func TestAdapterListSessionSnapshotsReturnsProtocolRows(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	updatedAt := time.Unix(789, 0)
	ref := session.SessionRef{AppName: "caelis", UserID: "user-1", WorkspaceKey: "ws", SessionID: "session-1"}
	gw := &protocolGatewayService{
		listResult: session.SessionList{
			Sessions: []session.SessionSummary{{
				SessionRef: ref,
				CWD:        "/tmp/ws",
				Title:      "Investigate gateway protocol",
				UpdatedAt:  updatedAt,
			}},
			NextCursor: "next-page",
		},
	}
	driver := newProtocolTestAdapter(t, gw, session.Session{SessionRef: ref})

	result, err := driver.ListSessionSnapshots(ctx, schema.SessionListRequest{Cursor: "page-1", CWD: "/tmp/ws"})
	if err != nil {
		t.Fatalf("ListSessionSnapshots() error = %v", err)
	}
	if gw.listReq.Cursor != "page-1" || gw.listReq.Limit != clientProtocolSessionListLimit || gw.listReq.WorkspaceKey != "" {
		t.Fatalf("gateway list request = %+v", gw.listReq)
	}
	if result.NextCursor != "next-page" || len(result.Sessions) != 1 {
		t.Fatalf("session list = %+v", result)
	}
	row := result.Sessions[0]
	if row.SessionID != "session-1" || row.Title != "Investigate gateway protocol" || row.CWD != "/tmp/ws" || row.UpdatedAt != updatedAt.UTC().Format(time.RFC3339) {
		t.Fatalf("session row = %+v", row)
	}

	_, err = driver.ListSessionSnapshots(ctx, schema.SessionListRequest{Cursor: "page-2"})
	if err != nil {
		t.Fatalf("ListSessionSnapshots() fallback error = %v", err)
	}
	if gw.listReq.Cursor != "page-2" || gw.listReq.WorkspaceKey != "" {
		t.Fatalf("fallback gateway list request = %+v, want no ambient workspace", gw.listReq)
	}
}

func TestAdapterRunStateReturnsIdleWithoutSession(t *testing.T) {
	t.Parallel()

	driver, err := NewAdapter(context.Background(), &RuntimeStack{
		Gateway: gatewayRuntimeDepsForTest(&protocolGatewayService{}),
		Session: SessionRuntimeDeps{
			AppName:   "caelis",
			UserID:    "user-1",
			Workspace: session.WorkspaceRef{Key: "ws"},
		},
	}, "", "gui", "")
	if err != nil {
		t.Fatalf("NewAdapter() error = %v", err)
	}
	state, err := driver.RunState(context.Background())
	if err != nil {
		t.Fatalf("RunState() error = %v", err)
	}
	if state.Status != eventstream.RunStateIdle || state.HasActiveTurn {
		t.Fatalf("RunState() = %+v, want idle", state)
	}
}

func newProtocolTestAdapter(t *testing.T, gw *protocolGatewayService, activeSession session.Session) *Adapter {
	t.Helper()
	driver, err := NewAdapter(context.Background(), &RuntimeStack{
		Gateway:      gatewayRuntimeDepsForTest(gw),
		ControlFeeds: protocolFeedRegistry{feed: gw.feed},
		Session: SessionRuntimeDeps{
			AppName:   "caelis",
			UserID:    "user-1",
			Workspace: session.WorkspaceRef{Key: "ws", CWD: "/tmp/ws"},
			StartFn: func(context.Context, string, string) (session.Session, error) {
				return activeSession, nil
			},
		},
	}, activeSession.SessionID, "gui", "")
	if err != nil {
		t.Fatalf("NewAdapter() error = %v", err)
	}
	return driver
}

type protocolGatewayService struct {
	listReq      gateway.ListSessionsRequest
	listResult   session.SessionList
	replayReq    gateway.ReplayEventsRequest
	replayResult gateway.ReplayEventsResult
	controlReq   gateway.ControlPlaneStateRequest
	controlState gateway.ControlPlaneState
	active       []gateway.ActiveTurnState
	feed         *protocolSessionFeed
}

type protocolFeedRegistry struct{ feed *protocolSessionFeed }

func (r protocolFeedRegistry) Session(session.SessionRef) (controlclientport.SessionFeed, error) {
	if r.feed == nil {
		return &protocolSessionFeed{}, nil
	}
	return r.feed, nil
}

type protocolSessionFeed struct {
	request  controlclientport.SubscribeRequest
	events   []eventstream.Envelope
	boundary string
}

func (*protocolSessionFeed) Prime(context.Context) error        { return nil }
func (*protocolSessionFeed) Publish(eventstream.Envelope) error { return nil }
func (*protocolSessionFeed) Attach(<-chan eventstream.Envelope) {}
func (f *protocolSessionFeed) Boundary() (*eventstream.FeedPosition, string) {
	return nil, f.boundary
}
func (f *protocolSessionFeed) Subscribe(_ context.Context, req controlclientport.SubscribeRequest) (controlclientport.SubscribeResult, error) {
	f.request = req
	return controlclientport.SubscribeResult{
		Subscription:   newProtocolFeedSubscription(f.events),
		Mode:           controlclientport.ResumeModeExact,
		BoundaryCursor: f.boundary,
	}, nil
}
func (f *protocolSessionFeed) SubscribeFromNow() (controlclientport.FeedSubscription, error) {
	return newProtocolFeedSubscription(f.events), nil
}

type protocolFeedSubscription struct {
	events chan eventstream.Envelope
	done   chan struct{}
}

func newProtocolFeedSubscription(events []eventstream.Envelope) *protocolFeedSubscription {
	subscription := &protocolFeedSubscription{events: make(chan eventstream.Envelope), done: make(chan struct{})}
	go func() {
		defer close(subscription.events)
		for _, envelope := range events {
			subscription.events <- eventstream.CloneEnvelope(envelope)
		}
		close(subscription.done)
	}()
	return subscription
}

func (s *protocolFeedSubscription) Events() <-chan eventstream.Envelope { return s.events }
func (s *protocolFeedSubscription) BackfillDone() <-chan struct{}       { return s.done }
func (*protocolFeedSubscription) Close() error                          { return nil }
func (*protocolFeedSubscription) Err() error                            { return nil }
func (*protocolFeedSubscription) LastCursor() string                    { return "" }

func (g *protocolGatewayService) Streams() stream.Service { return nil }
func (g *protocolGatewayService) BeginTurn(context.Context, gateway.BeginTurnRequest) (gateway.BeginTurnResult, error) {
	return gateway.BeginTurnResult{}, nil
}
func (g *protocolGatewayService) SubmitActiveTurn(context.Context, gateway.SubmitActiveTurnRequest) error {
	return nil
}
func (g *protocolGatewayService) Interrupt(context.Context, gateway.InterruptRequest) error {
	return nil
}
func (g *protocolGatewayService) ResumeSession(context.Context, gateway.ResumeSessionRequest) (session.LoadedSession, error) {
	return session.LoadedSession{}, nil
}
func (g *protocolGatewayService) ListSessions(_ context.Context, req gateway.ListSessionsRequest) (session.SessionList, error) {
	g.listReq = req
	return g.listResult, nil
}
func (g *protocolGatewayService) ReplayEvents(_ context.Context, req gateway.ReplayEventsRequest) (gateway.ReplayEventsResult, error) {
	g.replayReq = req
	return g.replayResult, nil
}
func (g *protocolGatewayService) ControlPlaneState(_ context.Context, req gateway.ControlPlaneStateRequest) (gateway.ControlPlaneState, error) {
	g.controlReq = req
	return g.controlState, nil
}
func (g *protocolGatewayService) HandoffController(context.Context, gateway.HandoffControllerRequest) (session.Session, error) {
	return session.Session{}, nil
}
func (g *protocolGatewayService) AttachParticipant(context.Context, gateway.AttachParticipantRequest) (session.Session, error) {
	return session.Session{}, nil
}
func (g *protocolGatewayService) PromptParticipant(context.Context, gateway.PromptParticipantRequest) (gateway.BeginTurnResult, error) {
	return gateway.BeginTurnResult{}, nil
}
func (g *protocolGatewayService) StartParticipant(context.Context, gateway.StartParticipantRequest) (gateway.BeginTurnResult, error) {
	return gateway.BeginTurnResult{}, nil
}
func (g *protocolGatewayService) DetachParticipant(context.Context, gateway.DetachParticipantRequest) (session.Session, error) {
	return session.Session{}, nil
}
func (g *protocolGatewayService) ActiveTurns() []gateway.ActiveTurnState {
	return append([]gateway.ActiveTurnState(nil), g.active...)
}
