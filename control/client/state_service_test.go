package controlclient

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
)

func TestStateServiceReturnsTypedConsistentBootstrapBySessionID(t *testing.T) {
	codec, err := eventstream.NewCursorCodec(eventstream.CursorCodecConfig{Secret: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	feeds, err := NewFeedRegistry(FeedRegistryConfig{CursorCodec: codec})
	if err != nil {
		t.Fatal(err)
	}
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
			Permission: &session.ProtocolApproval{ToolCall: session.ProtocolToolCall{ID: "call-1", Name: "WRITE"}},
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
	codec, err := eventstream.NewCursorCodec(eventstream.CursorCodecConfig{Secret: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	feeds, err := NewFeedRegistry(FeedRegistryConfig{CursorCodec: codec})
	if err != nil {
		t.Fatal(err)
	}
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

func TestStateServiceReconnectSucceedsDuringContinuousPublish(t *testing.T) {
	codec, err := eventstream.NewCursorCodec(eventstream.CursorCodecConfig{Secret: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	reader := &checkpointPageReader{
		active: session.Session{SessionRef: session.SessionRef{SessionID: "session-1"}, Revision: 1},
		events: []*session.Event{durableProtocolEvent(1, "durable history")},
	}
	feeds, err := NewFeedRegistry(FeedRegistryConfig{
		Reader: reader, CursorCodec: codec, RingEvents: 100_000, RingBytes: 64 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
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

func TestStateServiceMapsCheckpointLagToRevisionConflict(t *testing.T) {
	codec, err := eventstream.NewCursorCodec(eventstream.CursorCodecConfig{Secret: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	reader := &checkpointPageReader{active: session.Session{
		SessionRef: session.SessionRef{SessionID: "session-1"}, Revision: 1,
	}, events: []*session.Event{durableProtocolEvent(1, "accepted before checkpoint")}}
	feeds, err := NewFeedRegistry(FeedRegistryConfig{Reader: reader, CursorCodec: codec})
	if err != nil {
		t.Fatal(err)
	}
	feed, err := feeds.Session(session.SessionRef{SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := feed.Publish(projectedEnvelope(1, "accepted before checkpoint")); err != nil {
		t.Fatal(err)
	}
	// Model the observed bootstrap window after Feed acceptance but before the
	// checkpoint reader makes that durable position visible.
	reader.events = nil
	service, err := NewStateService(StateServiceConfig{
		Sessions: readerSessionLookup{reader}, Runtime: staticRuntimeStateReader{}, Feeds: feeds,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = service.State(context.Background(), StateRequest{SessionID: "session-1"})
	if !errors.Is(err, ErrStateRevisionConflict) || errorcode.CodeOf(err) != errorcode.Conflict {
		t.Fatalf("State error = %v (code %q), want ErrStateRevisionConflict", err, errorcode.CodeOf(err))
	}
}

func TestReconnectStateUsesExactFeedCutModeGapAndBoundary(t *testing.T) {
	codec, err := eventstream.NewCursorCodec(eventstream.CursorCodecConfig{Secret: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	reader := &checkpointPageReader{active: session.Session{
		SessionRef: session.SessionRef{SessionID: "session-1"}, Revision: 2,
	}, events: []*session.Event{durableProtocolEvent(1, "one")}}
	feeds, err := NewFeedRegistry(FeedRegistryConfig{Reader: reader, CursorCodec: codec, RingEvents: 1})
	if err != nil {
		t.Fatal(err)
	}
	feed, err := feeds.Session(session.SessionRef{SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := feed.Publish(projectedEnvelope(1, "one")); err != nil {
		t.Fatal(err)
	}
	_, firstCursor := feed.Boundary()
	reader.events = append(reader.events, durableProtocolEvent(2, "two"))
	if err := feed.Publish(projectedEnvelope(2, "two")); err != nil {
		t.Fatal(err)
	}
	service, err := NewStateService(StateServiceConfig{Sessions: readerSessionLookup{reader}, Runtime: staticRuntimeStateReader{}, Feeds: feeds})
	if err != nil {
		t.Fatal(err)
	}
	fallback, err := service.Reconnect(context.Background(), ReconnectRequest{SessionID: "session-1", Cursor: firstCursor})
	if err != nil {
		t.Fatal(err)
	}
	defer fallback.Subscription.Close()
	if fallback.State.ResumeMode != ResumeModeDurableFallback || !fallback.State.TransientGap {
		t.Fatalf("fallback state = %#v", fallback.State)
	}
	decoded, err := codec.Decode("session-1", fallback.State.BoundaryCursor)
	if err != nil || decoded.Durable == nil || fallback.State.BoundaryPosition == nil || fallback.State.BoundaryPosition.Durable == nil ||
		eventstream.CompareDurablePosition(*decoded.Durable, *fallback.State.BoundaryPosition.Durable) != 0 {
		t.Fatalf("fallback boundary cursor/position = %q / %#v, decode=%#v err=%v", fallback.State.BoundaryCursor, fallback.State.BoundaryPosition, decoded, err)
	}
	backfill := receiveEnvelopes(t, fallback.Subscription.Backfill(), 1)
	if backfill[0].EventID != "event-2" {
		t.Fatalf("fallback backfill = %#v", backfill)
	}
	_, currentCursor := feed.Boundary()
	exact, err := service.Reconnect(context.Background(), ReconnectRequest{SessionID: "session-1", Cursor: currentCursor})
	if err != nil {
		t.Fatal(err)
	}
	defer exact.Subscription.Close()
	if exact.State.ResumeMode != ResumeModeExact || exact.State.TransientGap {
		t.Fatalf("exact state = %#v", exact.State)
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
