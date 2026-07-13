package controladapter

import (
	"context"
	"strings"
	"sync"

	controlclientport "github.com/caelis-labs/caelis/ports/controlclient"
	"github.com/caelis-labs/caelis/ports/gateway"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
)

type gatewayTurn struct {
	handle gateway.TurnHandle
	feed   *liveFeedBroker

	subscription controlclientport.FeedSubscription
	eventsOnce   sync.Once
	events       <-chan eventstream.Envelope
}

func (t *gatewayTurn) HandleID() string { return t.handle.HandleID() }
func (t *gatewayTurn) RunID() string    { return t.handle.RunID() }
func (t *gatewayTurn) TurnID() string   { return t.handle.TurnID() }
func (t *gatewayTurn) Events() <-chan eventstream.Envelope {
	if t == nil {
		return eventstream.EnsureTerminalLifecycle(nil, "", "", "")
	}
	if t.subscription != nil {
		t.eventsOnce.Do(func() { t.events = t.turnSubscriptionEvents() })
		return t.events
	}
	return t.feed.Events()
}

func (t *gatewayTurn) turnSubscriptionEvents() <-chan eventstream.Envelope {
	out := make(chan eventstream.Envelope)
	go func() {
		defer close(out)
		defer t.subscription.Close()
		for envelope := range t.subscription.Events() {
			out <- envelope
			if t.isMainTerminal(envelope) {
				return
			}
		}
	}()
	return out
}

func (t *gatewayTurn) isMainTerminal(envelope eventstream.Envelope) bool {
	if envelope.Lifecycle == nil || (envelope.Scope != "" && envelope.Scope != eventstream.ScopeMain) {
		return false
	}
	if id := strings.TrimSpace(envelope.HandleID); id != "" && id != t.HandleID() {
		return false
	}
	if id := strings.TrimSpace(envelope.RunID); id != "" && id != t.RunID() {
		return false
	}
	if id := strings.TrimSpace(envelope.TurnID); id != "" && id != t.TurnID() {
		return false
	}
	switch strings.TrimSpace(envelope.Lifecycle.State) {
	case eventstream.LifecycleStateCompleted, eventstream.LifecycleStateFailed,
		eventstream.LifecycleStateInterrupted, eventstream.LifecycleStateCancelled:
		return true
	default:
		return false
	}
}

func (t *gatewayTurn) SubmitApproval(ctx context.Context, decision ApprovalDecision) error {
	return t.handle.Submit(ctx, gateway.SubmitRequest{
		Kind: gateway.SubmissionKindApproval,
		Approval: &gateway.ApprovalDecision{
			RequestID:  decision.RequestID,
			Outcome:    strings.TrimSpace(decision.Outcome),
			OptionID:   strings.TrimSpace(decision.OptionID),
			Approved:   decision.Approved,
			Reason:     strings.TrimSpace(decision.Reason),
			ReviewText: strings.TrimSpace(decision.ReviewText),
		},
	})
}

func (t *gatewayTurn) Cancel() {
	t.feed.Cancel()
	_ = t.handle.Cancel()
}

func (t *gatewayTurn) Close() error {
	if t.subscription != nil {
		_ = t.subscription.Close()
	}
	t.feed.Close()
	return t.handle.Close()
}
