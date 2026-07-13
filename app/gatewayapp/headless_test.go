package gatewayapp

import (
	"context"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/ports/gateway"
	"github.com/caelis-labs/caelis/protocol/acp/control"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	acpprojector "github.com/caelis-labs/caelis/protocol/acp/projector"
	"github.com/caelis-labs/caelis/surfaces/headless"
)

func runHeadlessOnceForGatewayAppTest(ctx context.Context, stack *Stack, activeSession session.Session, surface string, input string, opts headless.Options) (headless.Result, error) {
	return headless.RunOnce(ctx, gatewayHeadlessStarter{
		turns:      stack.KernelTurns(),
		sessionRef: activeSession.SessionRef,
		surface:    surface,
	}, control.Submission{Text: input}, opts)
}

type gatewayHeadlessStarter struct {
	turns      gateway.TurnService
	sessionRef session.SessionRef
	surface    string
}

func (s gatewayHeadlessStarter) Submit(ctx context.Context, submission control.Submission) (control.Turn, error) {
	result, err := s.turns.BeginTurn(ctx, gateway.BeginTurnRequest{
		SessionRef:   s.sessionRef,
		Input:        strings.TrimSpace(submission.Text),
		DisplayInput: strings.TrimSpace(submission.DisplayText),
		Surface:      strings.TrimSpace(s.surface),
		Metadata: map[string]any{
			"submission_mode": string(submission.Mode),
		},
	})
	if err != nil {
		return nil, err
	}
	if result.Handle == nil {
		return nil, nil
	}
	return gatewayHeadlessTurn{handle: result.Handle}, nil
}

type gatewayHeadlessTurn struct {
	handle gateway.TurnHandle
}

func (t gatewayHeadlessTurn) HandleID() string { return t.handle.HandleID() }
func (t gatewayHeadlessTurn) RunID() string    { return t.handle.RunID() }
func (t gatewayHeadlessTurn) TurnID() string   { return t.handle.TurnID() }
func (t gatewayHeadlessTurn) Events() <-chan eventstream.Envelope {
	return acpprojector.ACPEventsFromGatewayHandle(t.handle)
}
func (t gatewayHeadlessTurn) SubmitApproval(ctx context.Context, decision control.ApprovalDecision) error {
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
func (t gatewayHeadlessTurn) Cancel()      { _ = t.handle.Cancel() }
func (t gatewayHeadlessTurn) Close() error { return t.handle.Close() }
