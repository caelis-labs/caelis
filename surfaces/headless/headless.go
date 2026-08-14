package headless

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/approval"
	controlclient "github.com/caelis-labs/caelis/control/client"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/projector"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

type ApprovalPolicy string

const (
	ApprovalPolicyAutoDeny   ApprovalPolicy = "auto_deny"
	ApprovalPolicyApproveAll ApprovalPolicy = "approve_all"

	defaultTerminalDrainTimeout = 5 * time.Second
)

type Options struct {
	ApprovalPolicy  ApprovalPolicy
	ResolveApproval func(context.Context, ApprovalRequest) (approval.Decision, error)
	// ObserveEnvelope receives each target-filtered main-Turn Envelope before
	// the headless reducer consumes it. It is intended for structured streaming
	// output; returning an error explicitly cancels the Turn and the reducer
	// drains toward its terminal producer barrier for a bounded interval before
	// detaching observation. Control retains the accepted Turn lifetime.
	ObserveEnvelope func(eventstream.Envelope) error
}

// ApprovalRequest is the headless resolver input for one permission Envelope.
// The resolver selects a decision only; the Surface forwards RequestID unchanged
// to Control so it never chooses a runtime endpoint or approval waiter.
type ApprovalRequest struct {
	RequestID eventstream.ApprovalRequestID
	Payload   *approval.Payload
}

type Result struct {
	Output         string
	LastCursor     string
	PromptTokens   int
	Usage          eventstream.UsageSnapshot
	LifecycleState string
	StopReason     string
	Target         controlclient.TurnTarget
}

// RunSessionOnce executes one main Turn through the product Session client.
// Feed bootstrap, target filtering, reconnect recovery, approval routing, and
// explicit cancellation stay in control/client; this Surface only reduces the
// typed Turn stream into non-interactive output.
func RunSessionOnce(
	ctx context.Context,
	starter controlclient.SessionTurnStarter,
	request controlclient.SessionTurnStartRequest,
	opts Options,
) (Result, error) {
	return runSessionOnce(
		ctx,
		starter,
		request,
		opts,
		defaultTerminalDrainTimeout,
	)
}

func runSessionOnce(
	ctx context.Context,
	starter controlclient.SessionTurnStarter,
	request controlclient.SessionTurnStartRequest,
	opts Options,
	terminalDrainTimeout time.Duration,
) (Result, error) {
	if starter == nil {
		return Result{}, errors.New("headless: Session turn client is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	turn, err := starter.Start(ctx, request)
	if err != nil {
		return Result{}, err
	}
	if turn == nil {
		return Result{}, errors.New("headless: Session turn client returned no Turn")
	}
	defer turn.Close()

	out := Result{Target: turn.Target()}
	var assistant assistantOutputReducer
	var observedErr error
	var cancelErr error
	var drainErr error
	cancelRequested := false
	contextDone := ctx.Done()
	observeEnvelope := opts.ObserveEnvelope
	terminalSeen := false
	var drainTimer *time.Timer
	var drainTimeout <-chan time.Time
	defer func() {
		if drainTimer != nil {
			drainTimer.Stop()
		}
	}()
	cancelTurn := func(reason string) {
		if cancelRequested {
			return
		}
		cancelRequested = true
		cancelCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		cancelErr = turn.Cancel(cancelCtx, reason)
		cancel()
		drainDuration := terminalDrainTimeout
		if deadline, ok := ctx.Deadline(); ok {
			remaining := time.Until(deadline)
			if remaining < drainDuration {
				drainDuration = remaining
			}
		}
		if drainDuration < 0 {
			drainDuration = 0
		}
		drainTimer = time.NewTimer(drainDuration)
		drainTimeout = drainTimer.C
	}
	for !terminalSeen {
		select {
		case env, ok := <-turn.Events():
			if !ok {
				terminalSeen = true
				continue
			}
			if env.Cursor != "" {
				out.LastCursor = env.Cursor
			}
			if observeEnvelope != nil {
				if observeErr := observeEnvelope(eventstream.CloneEnvelope(env)); observeErr != nil {
					if observedErr == nil {
						observedErr = observeErr
					}
					observeEnvelope = nil
					cancelTurn("headless output observer failed")
				}
			}
			if eventErr := envelopeError(env); eventErr != nil && observedErr == nil {
				// Keep draining through the terminal producer barrier. Returning
				// on the error Envelope would detach observation before Control
				// has released the Turn execution lease.
				observedErr = eventErr
			}
			if usage := eventstream.UsageSnapshotFromEnvelope(env); usage != nil && isMainScope(env) {
				out.Usage = *usage
				if usage.PromptTokens > 0 {
					out.PromptTokens = usage.PromptTokens
				}
			} else if env.Kind == eventstream.KindRequestPermission {
				if ctx.Err() != nil {
					cancelTurn("headless context cancelled")
					continue
				}
				payload := projector.ApprovalPayloadFromPermission(env.Permission)
				approvalReq := ApprovalRequest{RequestID: env.ApprovalRequestID, Payload: payload}
				decision, resolveErr := resolveApproval(ctx, opts, approvalReq)
				if resolveErr != nil {
					if observedErr == nil {
						observedErr = resolveErr
					}
					cancelTurn("headless approval resolver failed")
					continue
				}
				if resolveErr := turn.ResolveApproval(
					ctx,
					controlClientApprovalResolution(approvalReq, decision),
				); resolveErr != nil {
					if observedErr == nil {
						observedErr = resolveErr
					}
					cancelTurn("headless approval resolution failed")
					continue
				}
			} else if isMainSessionUpdate(env) {
				if text, ok := assistant.Observe(env); ok {
					out.Output = text
				}
			}
			terminalSeen = eventstream.IsTurnTerminalLifecycle(env)
			if terminalSeen && env.Lifecycle != nil {
				out.LifecycleState = strings.TrimSpace(env.Lifecycle.State)
				out.StopReason = strings.TrimSpace(env.Lifecycle.StopReason)
				if out.StopReason == "" {
					out.StopReason = strings.TrimSpace(env.Lifecycle.Reason)
				}
			}
		case <-contextDone:
			contextDone = nil
			cancelTurn("headless context cancelled")
		case <-drainTimeout:
			drainTimeout = nil
			drainErr = errors.New("headless: timed out waiting for cancelled Turn terminal")
			terminalSeen = true
		}
	}
	if out.LastCursor == "" {
		out.LastCursor = turn.LastCursor()
	}
	return out, errors.Join(
		observedErr,
		turn.Err(),
		ctx.Err(),
		cancelErr,
		drainErr,
	)
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

func controlClientApprovalResolution(
	req ApprovalRequest,
	decision approval.Decision,
) controlclient.ApprovalResolution {
	response := approval.RuntimeResponseFromFinalReview(approval.FinalizeReviewResult(req.Payload, decision))
	return controlclient.ApprovalResolution{
		RequestID:  req.RequestID,
		Outcome:    response.Outcome,
		OptionID:   response.OptionID,
		Approved:   response.Approved,
		Reason:     response.Reason,
		ReviewText: response.ReviewText,
	}
}
