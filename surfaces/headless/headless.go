package headless

import (
	"context"
	"errors"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/protocol/acp/control"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/projector"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

type Starter interface {
	Submit(context.Context, control.Submission) (control.Turn, error)
}

type ApprovalPolicy string

const (
	ApprovalPolicyAutoDeny   ApprovalPolicy = "auto_deny"
	ApprovalPolicyApproveAll ApprovalPolicy = "approve_all"
)

type Options struct {
	ApprovalPolicy  ApprovalPolicy
	ResolveApproval func(context.Context, ApprovalRequest) (approval.Decision, error)
}

// ApprovalRequest is the headless resolver input for one permission Envelope.
// The resolver selects a decision only; the Surface forwards RequestID unchanged
// to Control so it never chooses a runtime endpoint or approval waiter.
type ApprovalRequest struct {
	RequestID eventstream.ApprovalRequestID
	Payload   *approval.Payload
}

type Result struct {
	Output       string
	LastCursor   string
	PromptTokens int
}

func RunOnce(ctx context.Context, starter Starter, submission control.Submission, opts Options) (Result, error) {
	turn, err := starter.Submit(ctx, submission)
	if err != nil {
		return Result{}, err
	}
	if turn == nil {
		return Result{}, nil
	}
	defer turn.Close()

	out := Result{}
	var assistant schema.FinalAssistantAccumulator
	for env := range turn.Events() {
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
			payload := projector.ApprovalPayloadFromPermission(env.Permission)
			approvalReq := ApprovalRequest{RequestID: env.ApprovalRequestID, Payload: payload}
			decision, err := resolveApproval(ctx, opts, approvalReq)
			if err != nil {
				return out, err
			}
			if err := turn.SubmitApproval(ctx, controlApprovalDecision(approvalReq, decision)); err != nil {
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

func resolveApproval(ctx context.Context, opts Options, req ApprovalRequest) (approval.Decision, error) {
	if opts.ResolveApproval != nil {
		return opts.ResolveApproval(ctx, req)
	}
	if opts.ApprovalPolicy == ApprovalPolicyApproveAll {
		return approval.Decision{Approved: true, Outcome: string(approval.StatusApproved)}, nil
	}
	return approval.Decision{Approved: false, Outcome: string(approval.StatusRejected)}, nil
}

func controlApprovalDecision(req ApprovalRequest, decision approval.Decision) control.ApprovalDecision {
	response := approval.RuntimeResponseFromFinalReview(approval.FinalizeReviewResult(req.Payload, decision))
	return control.ApprovalDecision{
		RequestID:  req.RequestID,
		Outcome:    response.Outcome,
		OptionID:   response.OptionID,
		Approved:   response.Approved,
		Reason:     response.Reason,
		ReviewText: response.ReviewText,
	}
}
