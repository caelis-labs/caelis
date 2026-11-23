package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

const approvalResolutionRecoveryTimeout = 5 * time.Second

type approvalKey struct {
	sessionID string
	requestID string
}

func keyForApproval(record session.ApprovalRecord) approvalKey {
	return approvalKey{record.SessionID, record.RequestID}
}

// approvalWaiter retains the existing producer's context so a cancelled owner
// cannot settle permission merely because its callback has not returned yet.
type approvalWaiter struct {
	responses     chan approval.Response
	stopRequester context.CancelFunc
	owner         context.Context
	blockingRunID string
}

type activeRun struct {
	ref               session.SessionRef
	session           session.Session
	handle            *runner
	turnID            string
	spec              agent.AgentSpec
	approvalRequester approval.Requester
}

func (r *Runtime) registerActiveRun(ref session.SessionRef, activeSession session.Session, turnID string, handle *runner, requests ...agent.RunRequest) {
	if r == nil || handle == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.activeRunners[handle.RunID()] = activeRun{ref: session.NormalizeSessionRef(ref), session: session.CloneSession(activeSession), turnID: strings.TrimSpace(turnID), handle: handle}
	if len(requests) > 0 {
		active := r.activeRunners[handle.RunID()]
		active.approvalRequester = requests[0].ApprovalRequester
		active.spec = cloneAgentSpec(requests[0].AgentSpec)
		r.activeRunners[handle.RunID()] = active
	}
}

func (r *Runtime) unregisterActiveRun(runID string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	delete(r.activeRunners, strings.TrimSpace(runID))
	r.mu.Unlock()
}

func (r *Runtime) activeRunForSession(ref session.SessionRef) activeRun {
	if r == nil {
		return activeRun{}
	}
	sessionID := strings.TrimSpace(session.NormalizeSessionRef(ref).SessionID)
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, candidate := range r.activeRunners {
		if candidate.handle != nil && strings.TrimSpace(candidate.ref.SessionID) == sessionID {
			return candidate
		}
	}
	return activeRun{}
}

// AttachLiveRun reattaches to one run still live in this Runtime process. A
// persisted non-live run is reported explicitly rather than being replayed
// from an unsafe execution point.
func (r *Runtime) AttachLiveRun(ctx context.Context, req agent.AttachLiveRunRequest) (agent.RunResult, error) {
	ref := session.NormalizeSessionRef(req.SessionRef)
	runID := strings.TrimSpace(req.RunID)
	if r == nil || runID == "" {
		return agent.RunResult{}, &agent.RunNotAttachableError{SessionRef: ref, RunID: runID, Detail: "run_id is required"}
	}
	r.mu.RLock()
	active, ok := r.activeRunners[runID]
	r.mu.RUnlock()
	if ok && active.handle != nil && active.ref.SessionID == ref.SessionID {
		return agent.RunResult{Session: session.CloneSession(active.session), Handle: active.handle}, nil
	}
	return agent.RunResult{}, &agent.RunNotAttachableError{SessionRef: ref, RunID: runID, Detail: "no live execution is attached"}
}

func (r *Runtime) requestDurableApproval(
	ctx context.Context,
	req approval.Request,
	requester approval.Requester,
) (approval.Response, error) {
	if r == nil || r.sessions == nil {
		return approval.Response{}, errors.New("agent-sdk/runtime: durable approval service is unavailable")
	}
	now := r.now()
	tokenID := strings.TrimSpace(req.ApprovalRequestID)
	if tokenID == "" {
		tokenID = r.nextID("approval", nil)
	}
	token := session.CloneApprovalRecord(session.ApprovalRecord{
		Authorization: session.CloneApprovalAuthorization(req.Authorization),
		Schema:        session.ApprovalSchemaVersion,
		RequestID:     tokenID, SessionID: req.SessionRef.SessionID, RunID: req.RunID, TurnID: req.TurnID,
		ToolCallID: req.Call.ID, ToolName: req.Tool.Name, ItemID: req.Execution.ItemID, Revision: 1, Status: session.ApprovalRecordPending,
		Input: req.Call.Input, Approval: req.Approval, Metadata: req.Metadata, CreatedAt: now, UpdatedAt: now,
		Presentation: approval.ProtocolApprovalFromPayload(approval.DisplayPayloadFromRuntimeRequest(req)),
	})
	requestCtx, stopRequester := context.WithCancel(ctx)
	defer stopRequester()
	waiter := approvalWaiter{responses: make(chan approval.Response, 1), owner: ctx, stopRequester: stopRequester}
	if err := session.ValidateApprovalRecordTransition(session.ApprovalRecord{}, token); err != nil {
		return approval.Response{}, err
	}
	r.mu.Lock()
	// Register delivery before the append so a reader of the committed record can resolve it immediately.
	key := keyForApproval(token)
	if _, exists := r.approvalWaiters[key]; exists {
		r.mu.Unlock()
		return approval.Response{}, fmt.Errorf("approval request %q already has a live waiter", token.RequestID)
	}
	r.approvalWaiters[key] = waiter
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.approvalWaiters, key)
		r.mu.Unlock()
	}()
	if err := r.appendApprovalRecord(ctx, req.SessionRef, token); err != nil {
		return approval.Response{}, err
	}
	// Only a durable foreground approval may appear as actionable in RunState.
	if req.OperationTaskID == "" {
		waiter.blockingRunID = req.RunID
		r.mu.Lock()
		r.approvalWaiters[key] = waiter
		r.mu.Unlock()
	}
	abandon := func(err error) error {
		return r.joinApprovalCleanup(err, r.cancelPendingApprovalRecord(context.WithoutCancel(ctx), req.SessionRef, token, err.Error()))
	}
	if requester != nil {
		// The durable approval record is the reusable Runtime correlation identity.
		// Keep it on the normalized request rather than hiding it in metadata or
		// adding a field to the ACP request_permission wire payload.
		req.ApprovalRequestID = token.RequestID
		decision, err := requester.RequestApproval(requestCtx, req)
		if err == nil {
			err = r.ResolveApproval(ctx, approval.ResolveRequest{SessionRef: req.SessionRef, RequestID: token.RequestID, Decision: decision})
		}
		if err != nil {
			cleanupErr := abandon(err)
			// A competing resolver may have committed before delivering its
			// response, including while the requester was being cancelled.
			recoveryCtx, cancel := boundedApprovalRecoveryContext(ctx)
			defer cancel()
			winner, loadErr := r.approvalRecord(recoveryCtx, req.SessionRef, token.RequestID)
			if loadErr == nil && winner.Status == session.ApprovalRecordResolved {
				return responseFromApprovalRecord(winner), nil
			}
			return approval.Response{}, errors.Join(cleanupErr, loadErr)
		}
	} else if req.OnAdmission != nil {
		if err := req.OnAdmission(ctx); err != nil {
			return approval.Response{}, abandon(err)
		}
	}
	select {
	case decision := <-waiter.responses:
		return decision, nil
	case <-ctx.Done():
		// Cancellation is not a continue-after-tool-error path. Only cancel the
		// approval record; the live producer owns cancellation and drain.
		_ = r.cancelPendingApprovalRecord(context.WithoutCancel(ctx), req.SessionRef, token, ctx.Err().Error())
		return approval.Response{}, ctx.Err()
	}
}

func (r *Runtime) cancelPendingApprovalRecord(ctx context.Context, ref session.SessionRef, token session.ApprovalRecord, reason string) error {
	if r == nil {
		return errors.New("agent-sdk/runtime: runtime is unavailable")
	}
	current, err := r.approvalRecord(ctx, ref, token.RequestID)
	if err != nil {
		// Fall back to the caller's pending snapshot when the store read fails
		// mid-cleanup; the original token is still the best cancel base we have.
		current = token
	} else if current.Status != session.ApprovalRecordPending {
		// Already resolved/cancelled by ResolveApproval or a concurrent path.
		return nil
	}
	return r.cancelApprovalRecord(ctx, ref, current, reason)
}

func (r *Runtime) joinApprovalCleanup(primary error, cleanup error) error {
	if cleanup == nil {
		return primary
	}
	if primary == nil {
		return cleanup
	}
	return errors.Join(primary, cleanup)
}

// ResolveApproval durably records one decision before waking the live run.
func (r *Runtime) ResolveApproval(ctx context.Context, req approval.ResolveRequest) error {
	ref := session.NormalizeSessionRef(req.SessionRef)
	token, err := r.approvalRecord(ctx, ref, req.RequestID)
	if err != nil {
		return err
	}
	if token.Authorization != nil {
		r.authorizationMu.Lock()
		defer r.authorizationMu.Unlock()
		token, err = r.approvalRecord(ctx, ref, req.RequestID)
		if err != nil {
			return err
		}
	}
	decision, err := approval.ResolveNativeResponse(token.Approval, req.Decision)
	if err != nil {
		return fmt.Errorf("agent-sdk/runtime: invalid approval decision: %w", err)
	}
	req.Decision = decision
	if token.Status == session.ApprovalRecordResolved {
		if approvalDecisionEqual(token, req.Decision) {
			r.deliverApprovalDecision(token)
			return nil
		}
		return fmt.Errorf("agent-sdk/runtime: approval record %q already has a different resolution", token.RequestID)
	}
	if token.Status != session.ApprovalRecordPending {
		return fmt.Errorf("agent-sdk/runtime: approval record %q is %s", token.RequestID, token.Status)
	}
	if token.Authorization != nil {
		active, err := r.sessions.Session(ctx, ref)
		if err != nil {
			return err
		}
		if _, err := r.checkAuthorization(ctx, active, token.Authorization); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	r.mu.RLock()
	waiter := r.approvalWaiters[keyForApproval(token)]
	live := waiter.responses != nil && waiter.owner != nil && waiter.owner.Err() == nil
	r.mu.RUnlock()
	if !live {
		return errors.New("approval no longer has a live owner")
	}
	next := token
	next.Revision++
	next.Status = session.ApprovalRecordResolved
	next.Outcome = strings.TrimSpace(req.Decision.Outcome)
	next.OptionID = strings.TrimSpace(req.Decision.OptionID)
	next.Approved = req.Decision.Approved
	next.Reason = strings.TrimSpace(req.Decision.Reason)
	next.ReviewText = strings.TrimSpace(req.Decision.ReviewText)
	next.UpdatedAt = r.now()
	if next.Approved && next.Authorization != nil {
		persistence, ok := next.Authorization.Choice(next.OptionID)
		if !ok {
			return errors.New("selected approval option has no typed authorization meaning")
		}
		if persistence == session.AuthorizationPolicy {
			if r.persistPolicyConsent == nil {
				return errors.New("persistent Policy configuration is unavailable")
			}
			scope := next.Authorization.Scope
			scope.SessionID, scope.SessionIncarnation = "", ""
			ruleID, revision, err := r.persistPolicyConsent(ctx, next.RequestID, scope)
			if err != nil {
				return fmt.Errorf("persistent policy settlement stopped this tool invocation without dispatch: %w", err)
			}
			next.PolicyRuleID, next.PolicyConfigurationRevision = ruleID, revision
		}
	}
	if err := session.ValidateApprovalRecordTransition(token, next); err != nil {
		return err
	}
	settlementError := func(err error) error {
		if next.PolicyRuleID != "" {
			return fmt.Errorf("policy rule %s committed at configuration revision %d; Session settlement failed and tool was not dispatched: %w", next.PolicyRuleID, next.PolicyConfigurationRevision, err)
		}
		return err
	}
	if err := r.appendApprovalRecordWithGuard(ctx, ref, next, session.RuntimeMutationGuard(waiter.owner)); err != nil {
		if !session.IsCommitted(err) && !errors.Is(err, session.ErrEventConflict) {
			return settlementError(err)
		}
		recoveryCtx, cancel := boundedApprovalRecoveryContext(ctx)
		defer cancel()
		durable, loadErr := r.approvalRecord(recoveryCtx, ref, next.RequestID)
		if loadErr != nil {
			return settlementError(errors.Join(err, loadErr))
		}
		if durable.Status != session.ApprovalRecordResolved || !approvalDecisionEqual(durable, req.Decision) {
			return settlementError(err)
		}
		r.deliverApprovalDecision(durable)
		return nil
	}
	r.deliverApprovalDecision(next)
	return nil
}

func responseFromApprovalRecord(token session.ApprovalRecord) approval.Response {
	return approval.Response{
		Outcome:    token.Outcome,
		OptionID:   token.OptionID,
		Approved:   token.Approved,
		Reason:     token.Reason,
		ReviewText: token.ReviewText,
	}
}

func (r *Runtime) deliverApprovalDecision(token session.ApprovalRecord) {
	if r == nil || token.Status != session.ApprovalRecordResolved {
		return
	}
	r.mu.RLock()
	waiter := r.approvalWaiters[keyForApproval(token)]
	r.mu.RUnlock()
	if waiter.responses != nil {
		select {
		case waiter.responses <- responseFromApprovalRecord(token):
		default:
		}
		if waiter.stopRequester != nil {
			waiter.stopRequester()
		}
	}
}

func (r *Runtime) cancelApprovalRecord(ctx context.Context, ref session.SessionRef, token session.ApprovalRecord, reason string) error {
	next := session.CloneApprovalRecord(token)
	next.Revision++
	next.Status = session.ApprovalRecordCancelled
	next.Reason = strings.TrimSpace(reason)
	next.UpdatedAt = r.now()
	if err := session.ValidateApprovalRecordTransition(token, next); err != nil {
		return err
	}
	return r.appendApprovalRecord(ctx, ref, next)
}

func (r *Runtime) appendApprovalRecord(ctx context.Context, ref session.SessionRef, token session.ApprovalRecord) error {
	return r.appendApprovalRecordWithGuard(ctx, ref, token, session.RuntimeMutationGuard(ctx))
}

func (r *Runtime) appendApprovalRecordWithGuard(ctx context.Context, ref session.SessionRef, token session.ApprovalRecord, guard session.MutationGuard) error {
	_, err := r.sessions.AppendEvent(ctx, session.AppendEventRequest{SessionRef: ref, MutationGuard: guard, Event: approvalRecordEvent(token)})
	return err
}

func boundedApprovalRecoveryContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(ctx), approvalResolutionRecoveryTimeout)
}

func approvalRecordEvent(token session.ApprovalRecord) *session.Event {
	token = session.CloneApprovalRecord(token)
	return &session.Event{
		IdempotencyKey: fmt.Sprintf("approval-record:%s:%d", token.RequestID, token.Revision),
		Type:           session.EventTypeLifecycle, Visibility: session.VisibilityJournal, Time: token.UpdatedAt,
		Actor:    session.ActorRef{Kind: session.ActorKindSystem, ID: "runtime", Name: "runtime"},
		Approval: &token,
	}
}

func (r *Runtime) approvalRecord(ctx context.Context, ref session.SessionRef, tokenID string) (session.ApprovalRecord, error) {
	events, err := r.sessions.Events(ctx, session.EventsRequest{SessionRef: ref, IncludeTransient: true})
	if err != nil {
		return session.ApprovalRecord{}, err
	}
	tokenID = strings.TrimSpace(tokenID)
	var latest session.ApprovalRecord
	for _, event := range events {
		if event == nil || event.Approval == nil || event.Approval.RequestID != tokenID {
			continue
		}
		candidate := session.CloneApprovalRecord(*event.Approval)
		if candidate.Revision > latest.Revision {
			latest = candidate
		}
	}
	if latest.Revision == 0 {
		return session.ApprovalRecord{}, fmt.Errorf("agent-sdk/runtime: approval record %q not found", tokenID)
	}
	return latest, nil
}

func approvalDecisionEqual(token session.ApprovalRecord, decision approval.Response) bool {
	return token.Outcome == strings.TrimSpace(decision.Outcome) && token.OptionID == strings.TrimSpace(decision.OptionID) && token.Approved == decision.Approved && token.Reason == strings.TrimSpace(decision.Reason) && token.ReviewText == strings.TrimSpace(decision.ReviewText)
}
