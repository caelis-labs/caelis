package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

const approvalResolutionRecoveryTimeout = 5 * time.Second

type activeRun struct {
	ref     session.SessionRef
	session session.Session
	handle  *runner
}

func (r *Runtime) registerActiveRun(ref session.SessionRef, activeSession session.Session, handle *runner) {
	if r == nil || handle == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.activeRunners[handle.RunID()] = activeRun{ref: session.NormalizeSessionRef(ref), session: session.CloneSession(activeSession), handle: handle}
}

func (r *Runtime) unregisterActiveRun(runID string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	delete(r.activeRunners, strings.TrimSpace(runID))
	r.mu.Unlock()
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
	state, err := r.persistedRunState(ctx, ref, runID)
	if err != nil {
		return agent.RunResult{}, err
	}
	detail := "no live execution is attached"
	if state.Status != "" {
		detail = fmt.Sprintf("durable status is %s", state.Status)
	}
	return agent.RunResult{}, &agent.RunNotAttachableError{SessionRef: ref, RunID: runID, Detail: detail}
}

func (r *Runtime) requestDurableApproval(
	ctx context.Context,
	req agent.ApprovalRequest,
	requester agent.ApprovalRequester,
) (agent.ApprovalResponse, error) {
	if r == nil || r.sessions == nil {
		return agent.ApprovalResponse{}, errors.New("agent-sdk/runtime: durable approval service is unavailable")
	}
	now := r.now()
	token := session.ClonePauseToken(session.PauseToken{
		Schema:  session.ExecutionJournalSchemaVersion,
		TokenID: r.nextID("pause", nil), SessionID: req.SessionRef.SessionID, RunID: req.RunID, TurnID: req.TurnID,
		ToolCallID: req.Call.ID, ToolName: req.Tool.Name, Revision: 1, Status: session.PauseTokenPending,
		Input: req.Call.Input, Approval: req.Approval, Metadata: req.Metadata, CreatedAt: now, UpdatedAt: now,
	})
	waiter := make(chan agent.ApprovalResponse, 1)
	if err := session.ValidatePauseTokenTransition(session.PauseToken{}, token); err != nil {
		return agent.ApprovalResponse{}, err
	}
	r.mu.Lock()
	r.approvalWaiters[token.TokenID] = waiter
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.approvalWaiters, token.TokenID)
		r.mu.Unlock()
	}()
	if err := r.appendPauseToken(ctx, req.SessionRef, token); err != nil {
		return agent.ApprovalResponse{}, err
	}
	if err := r.transitionRunTurnJournal(ctx, req.SessionRef, req.RunID, req.TurnID, session.ExecutionWaitingApproval, "approval required"); err != nil {
		return agent.ApprovalResponse{}, err
	}
	r.setRunState(req.SessionRef.SessionID, agent.RunState{
		Status: agent.RunLifecycleStatusWaitingApproval, ActiveRunID: req.RunID, WaitingApproval: true, PauseTokenID: token.TokenID, UpdatedAt: r.now(),
	})
	if requester != nil {
		// The durable pause token is the reusable Runtime correlation identity.
		// Keep it on the normalized request rather than hiding it in metadata or
		// adding a field to the ACP request_permission wire payload.
		req.PauseTokenID = token.TokenID
		decision, err := requester.RequestApproval(ctx, req)
		if err != nil {
			_ = r.cancelPauseToken(context.WithoutCancel(ctx), req.SessionRef, token, err.Error())
			return agent.ApprovalResponse{}, err
		}
		if err := r.ResolveApproval(ctx, agent.ResolveApprovalRequest{SessionRef: req.SessionRef, TokenID: token.TokenID, Decision: decision}); err != nil {
			return agent.ApprovalResponse{}, err
		}
	}
	select {
	case decision := <-waiter:
		if err := r.transitionRunTurnJournal(context.WithoutCancel(ctx), req.SessionRef, req.RunID, req.TurnID, session.ExecutionStarted, "approval resolved"); err != nil {
			return agent.ApprovalResponse{}, err
		}
		r.setRunState(req.SessionRef.SessionID, agent.RunState{Status: agent.RunLifecycleStatusRunning, ActiveRunID: req.RunID, UpdatedAt: r.now()})
		return decision, nil
	case <-ctx.Done():
		_ = r.cancelPauseToken(context.WithoutCancel(ctx), req.SessionRef, token, ctx.Err().Error())
		return agent.ApprovalResponse{}, ctx.Err()
	}
}

// ResolveApproval durably records one decision before waking the live run.
func (r *Runtime) ResolveApproval(ctx context.Context, req agent.ResolveApprovalRequest) error {
	ref := session.NormalizeSessionRef(req.SessionRef)
	token, err := r.pauseToken(ctx, ref, req.TokenID)
	if err != nil {
		return err
	}
	if token.Status == session.PauseTokenResolved {
		if pauseDecisionEqual(token, req.Decision) {
			r.deliverApprovalDecision(token)
			return nil
		}
		return fmt.Errorf("agent-sdk/runtime: pause token %q already has a different resolution", token.TokenID)
	}
	if token.Status != session.PauseTokenPending {
		return fmt.Errorf("agent-sdk/runtime: pause token %q is %s", token.TokenID, token.Status)
	}
	next := token
	next.Revision++
	next.Status = session.PauseTokenResolved
	next.Outcome = strings.TrimSpace(req.Decision.Outcome)
	next.OptionID = strings.TrimSpace(req.Decision.OptionID)
	next.Approved = req.Decision.Approved
	next.Reason = strings.TrimSpace(req.Decision.Reason)
	next.ReviewText = strings.TrimSpace(req.Decision.ReviewText)
	next.UpdatedAt = r.now()
	if err := session.ValidatePauseTokenTransition(token, next); err != nil {
		return err
	}
	if err := r.appendPauseTokenWithGuard(ctx, ref, next, session.ControlMutationGuard(session.ControlMutationPurposeApproval)); err != nil {
		if !session.IsCommitted(err) {
			return err
		}
		recoveryCtx, cancel := boundedApprovalRecoveryContext(ctx)
		defer cancel()
		durable, loadErr := r.pauseToken(recoveryCtx, ref, next.TokenID)
		if loadErr != nil {
			return errors.Join(err, loadErr)
		}
		if durable.Status != session.PauseTokenResolved || !pauseDecisionEqual(durable, req.Decision) {
			return err
		}
		r.deliverApprovalDecision(durable)
		return nil
	}
	r.deliverApprovalDecision(next)
	return nil
}

func (r *Runtime) deliverApprovalDecision(token session.PauseToken) {
	if r == nil || token.Status != session.PauseTokenResolved {
		return
	}
	decision := agent.ApprovalResponse{
		Outcome:    token.Outcome,
		OptionID:   token.OptionID,
		Approved:   token.Approved,
		Reason:     token.Reason,
		ReviewText: token.ReviewText,
	}
	r.mu.RLock()
	waiter := r.approvalWaiters[token.TokenID]
	r.mu.RUnlock()
	if waiter != nil {
		select {
		case waiter <- decision:
		default:
		}
	}
}

func (r *Runtime) cancelPauseToken(ctx context.Context, ref session.SessionRef, token session.PauseToken, reason string) error {
	next := session.ClonePauseToken(token)
	next.Revision++
	next.Status = session.PauseTokenCancelled
	next.Reason = strings.TrimSpace(reason)
	next.UpdatedAt = r.now()
	if err := session.ValidatePauseTokenTransition(token, next); err != nil {
		return err
	}
	return r.appendPauseToken(ctx, ref, next)
}

func (r *Runtime) appendPauseToken(ctx context.Context, ref session.SessionRef, token session.PauseToken) error {
	return r.appendPauseTokenWithGuard(ctx, ref, token, session.RuntimeMutationGuard(ctx))
}

func (r *Runtime) appendPauseTokenWithGuard(ctx context.Context, ref session.SessionRef, token session.PauseToken, guard session.MutationGuard) error {
	_, err := r.sessions.AppendEvent(ctx, session.AppendEventRequest{SessionRef: ref, MutationGuard: guard, Event: pauseTokenEvent(token)})
	return err
}

func boundedApprovalRecoveryContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(ctx), approvalResolutionRecoveryTimeout)
}

func pauseTokenEvent(token session.PauseToken) *session.Event {
	token = session.ClonePauseToken(token)
	return &session.Event{
		IdempotencyKey: fmt.Sprintf("pause-token:%s:%d", token.TokenID, token.Revision),
		Type:           session.EventTypeLifecycle, Visibility: session.VisibilityJournal, Time: token.UpdatedAt,
		Actor:     session.ActorRef{Kind: session.ActorKindSystem, ID: "runtime", Name: "runtime"},
		Lifecycle: &session.EventLifecycle{Status: string(token.Status), Reason: token.Reason},
		Journal:   &session.ExecutionJournalEntry{Schema: session.ExecutionJournalSchemaVersion, Kind: session.JournalKindPauseToken, PauseToken: &token},
	}
}

func (r *Runtime) pauseToken(ctx context.Context, ref session.SessionRef, tokenID string) (session.PauseToken, error) {
	events, err := r.sessions.Events(ctx, session.EventsRequest{SessionRef: ref, IncludeTransient: true})
	if err != nil {
		return session.PauseToken{}, err
	}
	tokenID = strings.TrimSpace(tokenID)
	var latest session.PauseToken
	for _, event := range events {
		if event == nil || event.Journal == nil || event.Journal.PauseToken == nil || event.Journal.PauseToken.TokenID != tokenID {
			continue
		}
		candidate := session.ClonePauseToken(*event.Journal.PauseToken)
		if candidate.Revision > latest.Revision {
			latest = candidate
		}
	}
	if latest.Revision == 0 {
		return session.PauseToken{}, fmt.Errorf("agent-sdk/runtime: pause token %q not found", tokenID)
	}
	return latest, nil
}

func pauseDecisionEqual(token session.PauseToken, decision agent.ApprovalResponse) bool {
	return token.Outcome == strings.TrimSpace(decision.Outcome) && token.OptionID == strings.TrimSpace(decision.OptionID) && token.Approved == decision.Approved && token.Reason == strings.TrimSpace(decision.Reason) && token.ReviewText == strings.TrimSpace(decision.ReviewText)
}

func (r *Runtime) persistedRunState(ctx context.Context, ref session.SessionRef, runID string) (agent.RunState, error) {
	events, err := r.sessions.Events(ctx, session.EventsRequest{SessionRef: ref, IncludeTransient: true})
	if err != nil {
		return agent.RunState{}, err
	}
	runID = strings.TrimSpace(runID)
	var latest session.ExecutionRecord
	var latestSeq uint64
	for _, event := range events {
		if event == nil || event.Journal == nil || event.Journal.Execution == nil {
			continue
		}
		record := session.NormalizeExecutionRecord(*event.Journal.Execution)
		if record.Kind != session.JournalKindRun || (runID != "" && record.RunID != runID) {
			continue
		}
		if runID != "" {
			if record.Revision > latest.Revision {
				latest = record
			}
		} else if event.Seq > latestSeq {
			latest = record
			latestSeq = event.Seq
		}
	}
	if latest.Revision == 0 {
		return agent.RunState{}, session.ErrSessionNotFound
	}
	state := agent.RunState{ActiveRunID: latest.RunID, UpdatedAt: latest.UpdatedAt}
	switch latest.Status {
	case session.ExecutionPrepared, session.ExecutionStarted, session.ExecutionCancelRequested:
		state.Status = agent.RunLifecycleStatusRunning
	case session.ExecutionWaitingApproval:
		state.Status = agent.RunLifecycleStatusWaitingApproval
		state.WaitingApproval = true
	case session.ExecutionSucceeded:
		state.Status = agent.RunLifecycleStatusCompleted
	case session.ExecutionFailed:
		state.Status = agent.RunLifecycleStatusFailed
		state.LastError = firstNonEmpty(latest.Error, latest.Reason)
	case session.ExecutionCancelled, session.ExecutionInterrupted, session.ExecutionUnknownOutcome:
		state.Status = agent.RunLifecycleStatusInterrupted
		state.LastError = firstNonEmpty(latest.Error, latest.Reason)
	}
	if state.WaitingApproval {
		var latestPause session.PauseToken
		var latestPauseSeq uint64
		for _, event := range events {
			if event == nil || event.Journal == nil || event.Journal.PauseToken == nil {
				continue
			}
			token := event.Journal.PauseToken
			if token.RunID == latest.RunID && event.Seq > latestPauseSeq {
				latestPause = session.ClonePauseToken(*token)
				latestPauseSeq = event.Seq
			}
		}
		if latestPause.Status == session.PauseTokenPending {
			state.PauseTokenID = latestPause.TokenID
		}
	}
	return state, nil
}
