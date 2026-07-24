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
	var assistant assistantOutputReducer
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
		if text, ok := assistant.Observe(env); ok {
			out.Output = text
		}
	}
	return out, nil
}

// assistantOutputReducer keeps exact ACP delta semantics for live updates while
// allowing one canonical final snapshot to replace its transient delivery.
// Final is the semantic boundary: EventID is intentionally not required because
// SDK-only and other process-local producers may not have durable identity.
type assistantOutputReducer struct {
	assistant schema.FinalAssistantAccumulator
}

func (r *assistantOutputReducer) Observe(env eventstream.Envelope) (string, bool) {
	if r == nil || env.Update == nil {
		return "", false
	}
	if env.Final && assistantMessageUpdate(env.Update) {
		// Canonical Session projection carries the completed message snapshot,
		// while an earlier transient projection carries exact live deltas. Reset
		// only at that typed final boundary so repeated real deltas remain data.
		r.assistant.Reset()
	}
	update := r.assistant.ObserveUpdate(env.Update)
	return update.Text, update.Assistant && update.Text != ""
}

func assistantMessageUpdate(update schema.Update) bool {
	switch typed := update.(type) {
	case schema.ContentChunk:
		return typed.SessionUpdate == schema.UpdateAgentMessage
	case *schema.ContentChunk:
		return typed != nil && typed.SessionUpdate == schema.UpdateAgentMessage
	default:
		return false
	}
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
