package controladapter

import (
	"context"
	"strings"

	"github.com/caelis-labs/caelis/ports/gateway"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
)

type gatewayTurn struct {
	handle gateway.TurnHandle
	feed   *liveFeedBroker
}

func (t *gatewayTurn) HandleID() string { return t.handle.HandleID() }
func (t *gatewayTurn) RunID() string    { return t.handle.RunID() }
func (t *gatewayTurn) TurnID() string   { return t.handle.TurnID() }
func (t *gatewayTurn) Events() <-chan eventstream.Envelope {
	return t.feed.Events()
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
	t.feed.Close()
	return t.handle.Close()
}
