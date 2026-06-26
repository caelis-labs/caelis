package controladapter

import (
	"context"
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/gateway"
	"github.com/OnslaughtSnail/caelis/protocol/acp/eventstream"
	acpprojector "github.com/OnslaughtSnail/caelis/protocol/acp/projector"
)

type gatewayTurn struct {
	handle gateway.TurnHandle
}

func (t gatewayTurn) HandleID() string { return t.handle.HandleID() }
func (t gatewayTurn) RunID() string    { return t.handle.RunID() }
func (t gatewayTurn) TurnID() string   { return t.handle.TurnID() }
func (t gatewayTurn) Events() <-chan eventstream.Envelope {
	if acpHandle, ok := t.handle.(gateway.ACPEventStreamHandle); ok && acpHandle != nil {
		return acpHandle.ACPEvents()
	}
	events := t.handle.Events()
	out := make(chan eventstream.Envelope, 32)
	go func() {
		defer close(out)
		for env := range events {
			for _, projected := range acpprojector.ProjectGatewayEventEnvelope(env) {
				out <- projected
			}
		}
	}()
	return out
}

func (t gatewayTurn) SubmitApproval(ctx context.Context, decision ApprovalDecision) error {
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

func (t gatewayTurn) Cancel()      { _ = t.handle.Cancel() }
func (t gatewayTurn) Close() error { return t.handle.Close() }
