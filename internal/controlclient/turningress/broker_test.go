package turningress

import (
	"context"
	"strings"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/ports/gateway"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
)

func TestTurnIdentityMatchesKnownIDs(t *testing.T) {
	t.Parallel()

	identity := turnIdentity{handleID: "handle-1", runID: "run-1", turnID: "turn-1"}
	for _, tt := range []struct {
		name string
		env  eventstream.Envelope
		want bool
	}{
		{
			name: "all identifiers match",
			env:  eventstream.Envelope{HandleID: "handle-1", RunID: "run-1", TurnID: "turn-1"},
			want: true,
		},
		{
			name: "source omits identifiers",
			env:  eventstream.Envelope{},
			want: false,
		},
		{
			name: "foreign handle",
			env:  eventstream.Envelope{HandleID: "other", RunID: "run-1", TurnID: "turn-1"},
			want: false,
		},
		{
			name: "foreign run",
			env:  eventstream.Envelope{HandleID: "handle-1", RunID: "other", TurnID: "turn-1"},
			want: false,
		},
		{
			name: "foreign turn",
			env:  eventstream.Envelope{HandleID: "handle-1", RunID: "run-1", TurnID: "other"},
			want: false,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := identity.matches(tt.env); got != tt.want {
				t.Fatalf("identity.matches(%#v) = %t, want %t", tt.env, got, tt.want)
			}
		})
	}
}

func TestBrokerHoldsSourceTerminalUntilProducerChannelCloses(t *testing.T) {
	handle := newBarrierTestHandle()
	broker := New(handle)
	events := broker.Events()
	handle.events <- eventstream.TurnCompleted(handle.HandleID(), handle.RunID(), handle.TurnID(), time.Now())

	select {
	case envelope := <-events:
		t.Fatalf("broker exposed terminal before producer close: %#v", envelope)
	case <-broker.Done():
		t.Fatal("broker crossed producer barrier before ACPEvents closed")
	case <-time.After(30 * time.Millisecond):
	}
	close(handle.events)
	got := collectBrokerBarrierEvents(events)
	if len(got) != 1 || got[0].Lifecycle == nil ||
		got[0].Lifecycle.State != eventstream.LifecycleStateCompleted {
		t.Fatalf("events after producer close = %#v", got)
	}
}

func TestBrokerApprovalSettlementDoesNotReplaceTurnTerminal(t *testing.T) {
	handle := newBarrierTestHandle()
	handle.events <- eventstream.Envelope{
		Kind:              eventstream.KindLifecycle,
		SessionID:         handle.ref.SessionID,
		Scope:             eventstream.ScopeMain,
		ApprovalRequestID: "approval-1",
		Delivery:          &eventstream.Delivery{Mode: eventstream.DeliveryMirror},
		Position: &eventstream.FeedPosition{Durable: &eventstream.DurableFeedPosition{
			Seq: 1,
		}},
		Lifecycle: &eventstream.Lifecycle{
			State:  eventstream.LifecycleStateCompleted,
			Reason: "resolved",
		},
	}
	close(handle.events)

	got := collectBrokerBarrierEvents(New(handle).Events())
	if len(got) != 2 || got[0].ApprovalRequestID != "approval-1" {
		t.Fatalf("events = %#v, want approval settlement followed by Turn terminal", got)
	}
	if !eventstream.IsTurnTerminalLifecycle(got[1]) || got[1].ApprovalRequestID != "" {
		t.Fatalf("last event = %#v, want independent Turn terminal", got[1])
	}
}

func TestBrokerKeepsSubagentControlFactsOutOfTaskStreamFilter(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		env  eventstream.Envelope
	}{
		{
			name: "permission request",
			env: eventstream.Envelope{
				Kind: eventstream.KindRequestPermission,
			},
		},
		{
			name: "approval review",
			env: eventstream.Envelope{
				Kind:           eventstream.KindApprovalReview,
				ApprovalReview: &eventstream.ApprovalReview{Status: "in_progress"},
			},
		},
		{
			name: "approval settlement",
			env: eventstream.Envelope{
				Kind:              eventstream.KindLifecycle,
				ApprovalRequestID: "approval-1",
				Lifecycle:         &eventstream.Lifecycle{State: eventstream.LifecycleStateCompleted},
			},
		},
		{
			name: "participant lifecycle",
			env: eventstream.Envelope{
				Kind:        eventstream.KindParticipant,
				Participant: &eventstream.Participant{State: "attached"},
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			handle := newBarrierTestHandle()
			tt.env.SessionID = handle.ref.SessionID
			tt.env.HandleID = handle.HandleID()
			tt.env.RunID = handle.RunID()
			tt.env.TurnID = handle.TurnID()
			tt.env.Scope = eventstream.ScopeSubagent
			handle.events <- tt.env
			close(handle.events)

			got := collectBrokerBarrierEvents(New(handle).Events())
			if len(got) != 2 || got[0].Kind != tt.env.Kind || !eventstream.IsTurnTerminalLifecycle(got[1]) {
				t.Fatalf("events = %#v, want child control fact followed by Turn terminal", got)
			}
		})
	}
}

func TestBrokerDropsSubagentTaskStreamObservation(t *testing.T) {
	t.Parallel()

	handle := newBarrierTestHandle()
	handle.events <- eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: handle.ref.SessionID,
		HandleID:  handle.HandleID(),
		RunID:     handle.RunID(),
		TurnID:    handle.TurnID(),
		Scope:     eventstream.ScopeSubagent,
		ScopeID:   "task-1",
		Delivery:  &eventstream.Delivery{Mode: eventstream.DeliveryTransient},
	}
	close(handle.events)

	got := collectBrokerBarrierEvents(New(handle).Events())
	if len(got) != 1 || !eventstream.IsTurnTerminalLifecycle(got[0]) {
		t.Fatalf("events = %#v, want only Turn terminal", got)
	}
}

func TestBrokerLateAttachDoesNotPreserveSuccessAfterDrainCancellation(t *testing.T) {
	handle := newBarrierTestHandle()
	handle.events <- eventstream.TurnCompleted(handle.HandleID(), handle.RunID(), handle.TurnID(), time.Now())
	handle.events <- eventstream.Error(context.Canceled)
	close(handle.events)

	// The producer is already complete when the Control delivery broker attaches.
	// Buffered cancellation truth must replace the earlier success candidate.
	got := collectBrokerBarrierEvents(New(handle).Events())
	if len(got) != 2 || got[0].Kind != eventstream.KindError || got[1].Lifecycle == nil ||
		got[1].Lifecycle.State != eventstream.LifecycleStateCancelled {
		t.Fatalf("late attach events = %#v, want error then cancelled terminal", got)
	}
	for _, envelope := range got {
		if envelope.Lifecycle != nil && envelope.Lifecycle.State == eventstream.LifecycleStateCompleted {
			t.Fatalf("late attach preserved synthesized success: %#v", got)
		}
	}
}

func TestBrokerRejectsDurableMainEnvelopeWithoutPosition(t *testing.T) {
	handle := newBarrierTestHandle()
	handle.events <- eventstream.Envelope{
		Kind:      eventstream.KindNotice,
		SessionID: handle.ref.SessionID,
		Scope:     eventstream.ScopeMain,
		Delivery:  &eventstream.Delivery{Mode: eventstream.DeliveryCanonical},
		Notice:    "invalid durable ingress",
	}
	close(handle.events)

	events := collectBrokerBarrierEvents(New(handle).Events())
	if len(events) != 2 || events[0].Kind != eventstream.KindError || events[0].Err == nil {
		t.Fatalf("invalid ingress events = %#v, want error and failed terminal", events)
	}
	if !strings.Contains(events[0].Err.Error(), "durable envelope requires a durable position") {
		t.Fatalf("invalid ingress error = %v, want durable-position contract", events[0].Err)
	}
	if events[1].Lifecycle == nil || events[1].Lifecycle.State != eventstream.LifecycleStateFailed {
		t.Fatalf("invalid ingress terminal = %#v, want failed", events[1])
	}
}

type barrierTestHandle struct {
	events chan eventstream.Envelope
	ref    session.SessionRef
}

func newBarrierTestHandle() *barrierTestHandle {
	return &barrierTestHandle{
		events: make(chan eventstream.Envelope, 4),
		ref:    session.SessionRef{SessionID: "session-1"},
	}
}

func (*barrierTestHandle) HandleID() string                 { return "handle-1" }
func (*barrierTestHandle) RunID() string                    { return "run-1" }
func (*barrierTestHandle) TurnID() string                   { return "turn-1" }
func (h *barrierTestHandle) SessionRef() session.SessionRef { return h.ref }
func (*barrierTestHandle) CreatedAt() time.Time             { return time.Unix(100, 0) }
func (h *barrierTestHandle) ACPEvents() <-chan eventstream.Envelope {
	return h.events
}
func (*barrierTestHandle) Submit(context.Context, gateway.SubmitRequest) error { return nil }
func (*barrierTestHandle) Cancel() agent.CancelResult {
	return agent.CancelResult{Status: agent.CancelStatusCancelled}
}
func (*barrierTestHandle) Close() error { return nil }

func collectBrokerBarrierEvents(events <-chan eventstream.Envelope) []eventstream.Envelope {
	var out []eventstream.Envelope
	for envelope := range events {
		out = append(out, envelope)
	}
	return out
}
