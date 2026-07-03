package controladapter

import (
	"context"
	"strings"
	"sync"

	"github.com/caelis-labs/caelis/ports/gateway"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	acpprojector "github.com/caelis-labs/caelis/protocol/acp/projector"
)

type gatewayTurn struct {
	handle     gateway.TurnHandle
	eventsOnce sync.Once
	events     <-chan eventstream.Envelope
}

func (t *gatewayTurn) HandleID() string { return t.handle.HandleID() }
func (t *gatewayTurn) RunID() string    { return t.handle.RunID() }
func (t *gatewayTurn) TurnID() string   { return t.handle.TurnID() }
func (t *gatewayTurn) Events() <-chan eventstream.Envelope {
	t.eventsOnce.Do(t.startEvents)
	return t.events
}

func (t *gatewayTurn) startEvents() {
	t.events = acpprojector.ACPEventsFromGatewayHandle(t.handle)
}

func (t *gatewayTurn) SubmitApproval(ctx context.Context, decision ApprovalDecision) error {
	return t.handle.Submit(ctx, gateway.SubmitRequest{
		Kind: gateway.SubmissionKindApproval,
		Approval: &gateway.ApprovalDecision{
			Outcome:    strings.TrimSpace(decision.Outcome),
			OptionID:   strings.TrimSpace(decision.OptionID),
			Approved:   decision.Approved,
			Reason:     strings.TrimSpace(decision.Reason),
			ReviewText: strings.TrimSpace(decision.ReviewText),
		},
	})
}

func (t *gatewayTurn) Cancel()      { _ = t.handle.Cancel() }
func (t *gatewayTurn) Close() error { return t.handle.Close() }
