package appserver

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

func TestStateServiceReturnsTypedConsistentBootstrapBySessionID(t *testing.T) {
	codec, err := NewCursorCodec(CursorCodecConfig{Secret: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	feeds := newTestFeedRegistry(t, FeedRegistryConfig{CursorCodec: codec})
	feed, err := feeds.Session(session.SessionRef{SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := feed.Publish(terminalEnvelope("live")); err != nil {
		t.Fatal(err)
	}
	sessions := &stateSessionReader{session: session.Session{
		SessionRef: session.SessionRef{SessionID: "session-1", WorkspaceKey: "workspace-a"},
		Revision:   7, CWD: "/workspace/a", Title: "Session A", Metadata: map[string]any{"display": map[string]any{"color": "blue"}},
		Controller:   session.ControllerBinding{Kind: session.ControllerKindACP, EpochID: "epoch-1"},
		Participants: []session.ParticipantBinding{{ID: "participant-1", Kind: session.ParticipantKindACP}},
	}}
	runtime := staticRuntimeStateReader{state: RuntimeState{
		Run: RunState{Active: true, Status: "waiting_approval", HandleID: "handle-1", RunID: "run-1", TurnID: "turn-1", WaitingApproval: true},
		Approval: ApprovalState{Active: &ActiveApproval{
			RequestID: "approval-1", Scope: eventstream.ScopeMain,
			Permission: &session.ProtocolApproval{ToolCall: session.ProtocolToolCall{ID: "call-1", Name: "Write"}},
		}, QueuedCount: 2},
	}}
	service, err := NewStateService(StateServiceConfig{Sessions: sessions, Runtime: runtime, Feeds: feeds})
	if err != nil {
		t.Fatal(err)
	}
	state, err := service.State(context.Background(), StateRequest{SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	if sessions.firstRef.SessionID != "session-1" || sessions.firstRef.WorkspaceKey != "" {
		t.Fatalf("initial state lookup ref = %#v, want global SessionID only", sessions.firstRef)
	}
	if state.SessionID != "session-1" || state.Revision != 7 || state.WorkspaceKey != "workspace-a" || state.BoundaryCursor == "" {
		t.Fatalf("state identity/boundary = %#v", state)
	}
	if state.Approval.Active == nil || state.Approval.Active.RequestID != "approval-1" || state.Approval.QueuedCount != 2 {
		t.Fatalf("approval bootstrap = %#v", state.Approval)
	}
	if state.Capabilities.ClientManagedTerminal || !state.Capabilities.CaelisTerminalStream || state.Capabilities.GoalBootstrapSupported || state.Capabilities.ManageLoopBootstrapSupported {
		t.Fatalf("capabilities = %#v", state.Capabilities)
	}
}

func TestStateServiceDoesNotStarveWhileSessionRevisionChanges(t *testing.T) {
	codec, err := NewCursorCodec(CursorCodecConfig{Secret: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	feeds := newTestFeedRegistry(t, FeedRegistryConfig{CursorCodec: codec})
	sessions := &changingStateSessionReader{}
	service, err := NewStateService(StateServiceConfig{
		Sessions: sessions, Runtime: staticRuntimeStateReader{}, Feeds: feeds,
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err := service.State(context.Background(), StateRequest{SessionID: "session-1"})
	if err != nil {
		t.Fatalf("State error = %v, want bounded successful bootstrap", err)
	}
	if state.SessionID != "session-1" || state.Revision == 0 {
		t.Fatalf("State = %#v, want one coherent observed revision", state)
	}
}

func TestStateServicePreparesExplicitReconnectButKeepsInspectPure(t *testing.T) {
	codec, err := NewCursorCodec(CursorCodecConfig{Secret: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	feeds := newTestFeedRegistry(t, FeedRegistryConfig{CursorCodec: codec})
	sessions := &stateSessionReader{session: session.Session{
		SessionRef: session.SessionRef{SessionID: "session-1"}, Revision: 7,
	}}
	prepareCalls := 0
	retainCalls := 0
	releaseCalls := 0
	service, err := NewStateService(StateServiceConfig{
		Sessions: sessions, Runtime: staticRuntimeStateReader{}, Feeds: feeds,
		PrepareReconnect: func(context.Context, session.SessionRef) error {
			prepareCalls++
			sessions.session.Revision++
			return nil
		},
		RetainObservation: func(ref session.SessionRef) (func(), error) {
			if ref.SessionID != "session-1" {
				t.Fatalf("RetainObservation() ref = %#v", ref)
			}
			retainCalls++
			return func() { releaseCalls++ }, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	inspected, err := service.State(context.Background(), StateRequest{SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	if inspected.Revision != 7 || prepareCalls != 0 || retainCalls != 0 || releaseCalls != 0 {
		t.Fatalf(
			"State() = revision %d prepare %d retain %d release %d, want pure revision 7",
			inspected.Revision, prepareCalls, retainCalls, releaseCalls,
		)
	}
	reconnected, err := service.Reconnect(context.Background(), ReconnectRequest{SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	if reconnected.Subscription == nil {
		t.Fatal("Reconnect() returned no subscription")
	}
	if reconnected.State.Revision != 8 || prepareCalls != 1 || retainCalls != 1 || releaseCalls != 0 {
		t.Fatalf(
			"Reconnect() = revision %d prepare %d retain %d release %d, want one live observation",
			reconnected.State.Revision, prepareCalls, retainCalls, releaseCalls,
		)
	}
	if err := reconnected.Subscription.Close(); err != nil {
		t.Fatal(err)
	}
	if err := reconnected.Subscription.Close(); err != nil {
		t.Fatal(err)
	}
	if releaseCalls != 1 {
		t.Fatalf("Reconnect subscription released observation %d times, want once", releaseCalls)
	}
}

func TestStateServiceReconnectSucceedsDuringContinuousPublish(t *testing.T) {
	codec, err := NewCursorCodec(CursorCodecConfig{Secret: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	reader := &checkpointPageReader{
		active: session.Session{SessionRef: session.SessionRef{SessionID: "session-1"}, Revision: 1},
		events: []*session.Event{durableProtocolEvent(1, "durable history")},
	}
	feeds := newTestFeedRegistry(t, FeedRegistryConfig{Reader: reader, CursorCodec: codec})
	feed, err := feeds.Session(session.SessionRef{SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	var published atomic.Int64
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
				if feed.Publish(terminalEnvelope("continuous output")) == nil {
					published.Add(1)
				}
			}
		}
	}()
	deadline := time.Now().Add(time.Second)
	for published.Load() < 100 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	defer func() {
		close(stop)
		<-done
	}()

	service, err := NewStateService(StateServiceConfig{
		Sessions: readerSessionLookup{reader}, Runtime: staticRuntimeStateReader{}, Feeds: feeds,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	state, err := service.State(ctx, StateRequest{SessionID: "session-1"})
	if err != nil {
		t.Fatalf("State during continuous Publish = %v", err)
	}
	if state.SessionID != "session-1" || state.BoundaryCursor == "" {
		t.Fatalf("State during continuous Publish = %#v", state)
	}
}

func TestStateServiceAcceptsCheckpointCoveredByNonProjectingCanonicalEvent(t *testing.T) {
	codec, err := NewCursorCodec(CursorCodecConfig{Secret: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	reader := &checkpointPageReader{active: session.Session{
		SessionRef: session.SessionRef{SessionID: "session-1"}, Revision: 2,
	}, events: []*session.Event{
		durableProtocolEvent(1, "accepted before Agent completion"),
		{
			ID: "agent-completion-context", SessionID: "session-1", Seq: 2,
			Type: session.EventTypeContext, Visibility: session.VisibilityCanonical,
			Actor: session.ActorRef{Kind: session.ActorKindParticipant, ID: "reviewer-agent"},
			Text:  "Subagent @reviewer is completed.",
		},
	}}
	feeds := newTestFeedRegistry(t, FeedRegistryConfig{Reader: reader, CursorCodec: codec})
	feed, err := feeds.Session(session.SessionRef{SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := feed.Publish(projectedEnvelope(1, "accepted before Agent completion")); err != nil {
		t.Fatal(err)
	}
	service, err := NewStateService(StateServiceConfig{
		Sessions: readerSessionLookup{reader}, Runtime: staticRuntimeStateReader{}, Feeds: feeds,
	})
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := reader.EventCheckpoint(context.Background(), session.SessionRef{SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	if checkpoint.ThroughSeq != 2 || checkpoint.LastClientReplayEvent == nil || checkpoint.LastClientReplayEvent.Seq != 1 {
		t.Fatalf("checkpoint = %#v, want durable high-water 2 and projectable feed tail 1", checkpoint)
	}

	state, err := service.State(context.Background(), StateRequest{SessionID: "session-1"})
	if err != nil {
		t.Fatalf("State() error = %v, want checkpoint ThroughSeq to cover the accepted durable feed", err)
	}
	if state.Revision != 2 {
		t.Fatalf("State() revision = %d, want 2", state.Revision)
	}
}

func TestReconnectStateUsesExactFeedCursorAndBoundary(t *testing.T) {
	codec, err := NewCursorCodec(CursorCodecConfig{Secret: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	reader := &checkpointPageReader{active: session.Session{
		SessionRef: session.SessionRef{SessionID: "session-1"}, Revision: 2,
	}, events: []*session.Event{durableProtocolEvent(1, "one")}}
	feeds := newTestFeedRegistry(t, FeedRegistryConfig{Reader: reader, CursorCodec: codec})
	feed, err := feeds.Session(session.SessionRef{SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := feed.Publish(projectedEnvelope(1, "one")); err != nil {
		t.Fatal(err)
	}
	_, firstCursor := feed.Boundary()
	reader.setEvents(durableProtocolEvent(1, "one"), durableProtocolEvent(2, "two"))
	if err := feed.Publish(projectedEnvelope(2, "two")); err != nil {
		t.Fatal(err)
	}
	service, err := NewStateService(StateServiceConfig{Sessions: readerSessionLookup{reader}, Runtime: staticRuntimeStateReader{}, Feeds: feeds})
	if err != nil {
		t.Fatal(err)
	}
	exact, err := service.Reconnect(context.Background(), ReconnectRequest{SessionID: "session-1", Cursor: firstCursor})
	if err != nil {
		t.Fatal(err)
	}
	defer exact.Subscription.Close()
	decoded, err := codec.Decode("session-1", exact.State.BoundaryCursor)
	if err != nil || exact.State.BoundaryPosition == nil {
		t.Fatalf("exact boundary cursor/position = %q / %#v, decode=%#v err=%v", exact.State.BoundaryCursor, exact.State.BoundaryPosition, decoded, err)
	}
	appended := receiveFeedEvents(t, exact.Subscription, 1)
	if appended[0].EventID != "event-2" {
		t.Fatalf("exact append = %#v", appended)
	}
	_, currentCursor := feed.Boundary()
	current, err := service.Reconnect(context.Background(), ReconnectRequest{SessionID: "session-1", Cursor: currentCursor})
	if err != nil {
		t.Fatal(err)
	}
	defer current.Subscription.Close()
	if current.State.BoundaryCursor == "" {
		t.Fatalf("current state = %#v", current.State)
	}
}

type readerSessionLookup struct{ reader *checkpointPageReader }

func (r readerSessionLookup) Session(context.Context, session.SessionRef) (session.Session, error) {
	return session.CloneSession(r.reader.active), nil
}

type stateSessionReader struct {
	session  session.Session
	firstRef session.SessionRef
	lastRef  session.SessionRef
}

func (r *stateSessionReader) Session(_ context.Context, ref session.SessionRef) (session.Session, error) {
	if r.firstRef.SessionID == "" {
		r.firstRef = ref
	}
	r.lastRef = ref
	return session.CloneSession(r.session), nil
}

type changingStateSessionReader struct{ revision uint64 }

func (r *changingStateSessionReader) Session(_ context.Context, ref session.SessionRef) (session.Session, error) {
	r.revision++
	return session.Session{SessionRef: session.SessionRef{SessionID: ref.SessionID}, Revision: r.revision}, nil
}

type staticRuntimeStateReader struct{ state RuntimeState }

func (r staticRuntimeStateReader) ControlClientRuntimeState(context.Context, session.SessionRef) (RuntimeState, error) {
	return r.state, nil
}
