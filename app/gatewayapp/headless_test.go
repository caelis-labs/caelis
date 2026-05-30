package gatewayapp

import (
	"context"

	"github.com/OnslaughtSnail/caelis/kernel"
	"github.com/OnslaughtSnail/caelis/ports/session"
)

type gatewayHeadlessStarter interface {
	BeginTurn(context.Context, kernel.BeginTurnRequest) (kernel.BeginTurnResult, error)
}

type gatewayHeadlessResult struct {
	Session      session.Session
	Output       string
	LastCursor   string
	PromptTokens int
}

func runGatewayHeadlessOnce(ctx context.Context, starter gatewayHeadlessStarter, req kernel.BeginTurnRequest) (gatewayHeadlessResult, error) {
	result, err := starter.BeginTurn(ctx, req)
	if err != nil {
		return gatewayHeadlessResult{}, err
	}
	if result.Handle == nil {
		return gatewayHeadlessResult{Session: result.Session}, nil
	}
	defer result.Handle.Close()

	out := gatewayHeadlessResult{Session: result.Session}
	for env := range result.Handle.Events() {
		out.LastCursor = env.Cursor
		if env.Err != nil {
			return out, env.Err
		}
		if env.Event.Kind == kernel.EventKindApprovalRequested {
			if err := result.Handle.Submit(ctx, kernel.SubmitRequest{
				Kind: kernel.SubmissionKindApproval,
				Approval: &kernel.ApprovalDecision{
					Approved: false,
					Outcome:  string(kernel.ApprovalStatusRejected),
				},
			}); err != nil {
				return out, err
			}
			continue
		}
		if text := kernel.AssistantText(env.Event); text != "" {
			out.Output = text
		}
		if prompt := kernel.PromptTokens(env.Event); prompt > 0 {
			out.PromptTokens = prompt
		}
	}
	return out, nil
}
