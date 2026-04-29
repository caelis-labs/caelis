package headless

import (
	"context"

	"github.com/OnslaughtSnail/caelis/gateway"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
)

type Starter interface {
	BeginTurn(context.Context, gateway.BeginTurnRequest) (gateway.BeginTurnResult, error)
}

type ApprovalPolicy string

const (
	ApprovalPolicyAutoDeny   ApprovalPolicy = "auto_deny"
	ApprovalPolicyApproveAll ApprovalPolicy = "approve_all"
)

type Options struct {
	ApprovalPolicy  ApprovalPolicy
	ResolveApproval func(context.Context, *gateway.ApprovalPayload) (gateway.ApprovalDecision, error)
}

type Result struct {
	Session      sdksession.Session
	Output       string
	LastCursor   string
	PromptTokens int
}

func RunOnce(ctx context.Context, starter Starter, req gateway.BeginTurnRequest, opts Options) (Result, error) {
	result, err := starter.BeginTurn(ctx, req)
	if err != nil {
		return Result{}, err
	}
	if result.Handle == nil {
		return Result{Session: result.Session}, nil
	}
	defer result.Handle.Close()

	out := Result{Session: result.Session}
	for env := range result.Handle.Events() {
		out.LastCursor = env.Cursor
		if env.Err != nil {
			return out, env.Err
		}
		if env.Event.Kind == gateway.EventKindApprovalRequested {
			decision, err := resolveApproval(ctx, opts, env.Event.ApprovalPayload)
			if err != nil {
				return out, err
			}
			if err := result.Handle.Submit(ctx, gateway.SubmitRequest{
				Kind:     gateway.SubmissionKindApproval,
				Approval: &decision,
			}); err != nil {
				return out, err
			}
			continue
		}
		if text := gateway.AssistantText(env.Event); text != "" {
			out.Output = text
		}
		if prompt := gateway.PromptTokens(env.Event); prompt > 0 {
			out.PromptTokens = prompt
		}
	}
	return out, nil
}

func resolveApproval(ctx context.Context, opts Options, req *gateway.ApprovalPayload) (gateway.ApprovalDecision, error) {
	if opts.ResolveApproval != nil {
		return opts.ResolveApproval(ctx, req)
	}
	if opts.ApprovalPolicy == ApprovalPolicyApproveAll {
		return gateway.ApprovalDecision{Approved: true, Outcome: string(gateway.ApprovalStatusApproved)}, nil
	}
	return gateway.ApprovalDecision{Approved: false, Outcome: string(gateway.ApprovalStatusRejected)}, nil
}
