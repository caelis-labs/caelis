package headless

import (
	"context"
	"errors"
	"strings"

	"github.com/caelis-labs/caelis/ports/gateway"
	"github.com/caelis-labs/caelis/ports/session"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/projector"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
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
	Session      session.Session
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
	var assistant schema.FinalAssistantAccumulator
	for env := range projector.ACPEventsFromGatewayHandle(result.Handle) {
		if env.Cursor != "" {
			out.LastCursor = env.Cursor
		}
		if err := envelopeError(env); err != nil {
			return out, err
		}
		if usage := eventstream.UsageSnapshotFromEnvelope(env); usage != nil && isMainScope(env) {
			if usage.PromptTokens > 0 {
				out.PromptTokens = usage.PromptTokens
			}
			continue
		}
		if env.Kind == eventstream.KindRequestPermission {
			decision, err := resolveApproval(ctx, opts, projector.ApprovalPayloadFromPermission(env.Permission))
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
		if !isMainSessionUpdate(env) {
			continue
		}
		update := assistant.ObserveUpdate(env.Update)
		if update.Assistant && update.Text != "" {
			out.Output = update.Text
		}
	}
	return out, nil
}

func isMainSessionUpdate(env eventstream.Envelope) bool {
	return env.Kind == eventstream.KindSessionUpdate &&
		env.Update != nil &&
		isMainScope(env)
}

func isMainScope(env eventstream.Envelope) bool {
	return env.Scope == "" || env.Scope == eventstream.ScopeMain
}

func envelopeError(env eventstream.Envelope) error {
	if env.Err != nil {
		return env.Err
	}
	if env.Kind == eventstream.KindError && strings.TrimSpace(env.Error) != "" {
		return errors.New(strings.TrimSpace(env.Error))
	}
	return nil
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
