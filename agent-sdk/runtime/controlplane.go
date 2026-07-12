package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/controller"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func (r *Runtime) ensureSessionController(ctx context.Context, activeSession session.Session) (session.Session, error) {
	return r.ensureSessionControllerWithGuard(ctx, activeSession, session.RuntimeMutationGuard(ctx))
}

func (r *Runtime) ensureSessionControllerWithGuard(
	ctx context.Context,
	activeSession session.Session,
	mutationGuard session.MutationGuard,
) (session.Session, error) {
	if r == nil || r.sessions == nil {
		return session.Session{}, fmt.Errorf("agent-sdk/runtime: session service is unavailable")
	}
	if activeSession.Controller.Kind != "" {
		return session.CloneSession(activeSession), nil
	}
	return r.sessions.BindController(ctx, session.BindControllerRequest{
		SessionRef:    activeSession.SessionRef,
		MutationGuard: mutationGuard,
		Binding:       r.kernelControllerBinding("runtime"),
	})
}

func (r *Runtime) kernelControllerBinding(source string) session.ControllerBinding {
	return session.ControllerBinding{
		Kind:         session.ControllerKindKernel,
		ControllerID: "sdk-kernel",
		AgentName:    "local",
		Label:        "SDK Kernel",
		EpochID:      r.nextID("kernel", nil),
		AttachedAt:   r.now(),
		Source:       firstNonEmpty(strings.TrimSpace(source), "runtime"),
	}
}

func (r *Runtime) runACPControllerTurn(
	ctx context.Context,
	cancel context.CancelFunc,
	activeSession session.Session,
	ref session.SessionRef,
	req agent.RunRequest,
) (agent.RunResult, error) {
	if r == nil || r.controllers == nil {
		cancel()
		return agent.RunResult{}, fmt.Errorf("agent-sdk/runtime: ACP controller backend is not configured")
	}
	runID := r.nextID("run", r.runIDGenerator)
	turnID := r.nextID("turn", nil)
	ctx = withLifecycleScope(ctx, lifecycleScope{sessionRef: ref, runID: runID, turnID: turnID})
	if err := r.beginRun(ref, runID); err != nil {
		cancel()
		return agent.RunResult{}, err
	}
	if err := r.recoverIncompleteExecutionJournal(ctx, ref); err != nil {
		cancel()
		r.setRunState(ref.SessionID, agent.RunState{Status: agent.RunLifecycleStatusFailed, ActiveRunID: runID, LastError: err.Error(), UpdatedAt: r.now()})
		return agent.RunResult{}, err
	}
	if err := r.startRunTurnJournal(ctx, ref, runID, turnID); err != nil {
		cancel()
		r.setRunState(ref.SessionID, agent.RunState{Status: agent.RunLifecycleStatusFailed, ActiveRunID: runID, LastError: err.Error(), UpdatedAt: r.now()})
		return agent.RunResult{}, err
	}
	r.setRunState(ref.SessionID, agent.RunState{
		Status:      agent.RunLifecycleStatusRunning,
		ActiveRunID: runID,
		UpdatedAt:   r.now(),
	})
	handle := newRunner(runID, cancel)
	handle.setCancelHook(func() error {
		return r.transitionRunTurnJournal(context.WithoutCancel(ctx), ref, runID, turnID, session.ExecutionCancelRequested, "run cancellation requested")
	})
	r.registerActiveRun(ref, activeSession, handle)
	go func() {
		defer cancel()
		r.executeACPControllerTurn(ctx, activeSession, ref, req, runID, turnID, handle)
	}()
	return agent.RunResult{
		Session: activeSession,
		Handle:  handle,
	}, nil
}

func (r *Runtime) executeACPControllerTurn(
	ctx context.Context,
	activeSession session.Session,
	ref session.SessionRef,
	req agent.RunRequest,
	runID string,
	turnID string,
	handle *runner,
) {
	defer handle.finish()
	defer r.unregisterActiveRun(runID)
	completed := false
	defer func() {
		status := session.ExecutionFailed
		reason := "controller turn failed"
		if completed {
			status = session.ExecutionSucceeded
			reason = ""
		} else if ctx.Err() != nil {
			status = session.ExecutionCancelled
			reason = ctx.Err().Error()
		}
		if err := r.transitionRunTurnJournal(context.WithoutCancel(ctx), ref, runID, turnID, status, reason); err != nil {
			r.setRunState(ref.SessionID, agent.RunState{Status: agent.RunLifecycleStatusFailed, ActiveRunID: runID, LastError: err.Error(), UpdatedAt: r.now()})
			handle.publishError(err)
		}
	}()

	userEvent := buildUserEvent(activeSession, turnID, req.Input, req.DisplayInput, req.ContentParts)
	if userEvent != nil {
		persisted, err := r.sessions.AppendEvent(ctx, session.AppendEventRequest{
			SessionRef:    ref,
			MutationGuard: session.RuntimeMutationGuard(ctx),
			Event:         userEvent,
		})
		if err != nil {
			r.setRunState(ref.SessionID, agent.RunState{
				Status:      interruptedOrFailedStatus(ctx, err),
				ActiveRunID: runID,
				LastError:   err.Error(),
				UpdatedAt:   r.now(),
			})
			handle.publishError(err)
			return
		}
		handle.publishEvent(persisted)
	}

	turnReq := controller.TurnRequest{
		SessionRef:        ref,
		Session:           activeSession,
		TurnID:            turnID,
		Input:             req.Input,
		ContentParts:      req.ContentParts,
		Stream:            req.Request.StreamEnabled(false),
		Mode:              r.policyMode(req.AgentSpec),
		ApprovalRequester: controllerApprovalRequester{requester: req.ApprovalRequester, sessionRef: ref, session: activeSession, runID: runID, turnID: turnID},
	}
	contextPrelude, contextSeq, err := r.buildControllerTurnContext(ctx, activeSession, ref, turnID)
	if err != nil {
		r.setRunState(ref.SessionID, agent.RunState{Status: agent.RunLifecycleStatusFailed, ActiveRunID: runID, LastError: err.Error(), UpdatedAt: r.now()})
		handle.publishError(err)
		return
	}
	if contextSeq > activeSession.Controller.ContextSyncSeq {
		turnReq.ContextPrelude = contextPrelude
		turnReq.ContextSyncSeq = contextSeq
	}
	var turnResult controller.TurnResult
	err = r.executeLifecycle(ctx, r.lifecycleEvent(ctx, agent.LifecycleRun, "acp", ""), func(runCtx context.Context) error {
		return r.executeLifecycle(runCtx, r.lifecycleEvent(runCtx, agent.LifecycleTurn, "acp", ""), func(turnCtx context.Context) error {
			var turnErr error
			turnResult, turnErr = r.controllers.RunTurn(turnCtx, turnReq)
			if turnErr != nil && isMissingACPControllerRun(turnErr) {
				if r.controllerRecovery == nil {
					return fmt.Errorf("agent-sdk/runtime: controller recovery coordinator is unavailable")
				}
				activeSession, turnErr = r.controllerRecovery.ReattachController(turnCtx, controller.RecoveryRequest{
					SessionRef:    ref,
					Session:       activeSession,
					ExcludeTurnID: turnID,
				})
				if turnErr == nil {
					turnReq.Session = activeSession
					turnReq.ContextPrelude, turnReq.ContextSyncSeq, turnErr = r.buildControllerTurnContext(turnCtx, activeSession, ref, turnID)
					if turnErr == nil {
						turnResult, turnErr = r.controllers.RunTurn(turnCtx, turnReq)
					}
				}
			}
			return turnErr
		})
	})
	if err != nil {
		r.setRunState(ref.SessionID, agent.RunState{
			Status:      interruptedOrFailedStatus(ctx, err),
			ActiveRunID: runID,
			LastError:   err.Error(),
			UpdatedAt:   r.now(),
		})
		handle.publishError(err)
		return
	}
	if turnResult.Handle != nil {
		var toolFactOrdinal uint64
		handle.setCancelHook(func() error {
			journalErr := r.transitionRunTurnJournal(context.WithoutCancel(ctx), ref, runID, turnID, session.ExecutionCancelRequested, "run cancellation requested")
			return errors.Join(journalErr, turnResult.Handle.Cancel().Err)
		})
		defer turnResult.Handle.Close()
		if err := r.forwardControllerEvents(ctx, agent.ControllerEventForwardRequest{
			ActiveSession: activeSession,
			SessionRef:    ref,
			MutationGuard: session.RuntimeMutationGuard(ctx),
			TurnID:        turnID,
			Source:        turnResult.Handle,
			Publisher:     handle,
			Normalize: func(active session.Session, turn string, event *session.Event) *session.Event {
				normalized := normalizeEvent(active, turn, event)
				if scopeRuntimeToolFactIdentity(normalized, runID, turnID, toolFactOrdinal+1) {
					toolFactOrdinal++
				}
				return normalized
			},
			IsUserEcho: isACPControllerUserEcho,
		}); err != nil {
			r.setRunState(ref.SessionID, agent.RunState{
				Status:      interruptedOrFailedStatus(ctx, err),
				ActiveRunID: runID,
				LastError:   err.Error(),
				UpdatedAt:   r.now(),
			})
			handle.publishError(err)
			return
		}
		if err := r.updateControllerContextCheckpoint(ctx, ref); err != nil {
			r.setRunState(ref.SessionID, agent.RunState{
				Status:      interruptedOrFailedStatus(ctx, err),
				ActiveRunID: runID,
				LastError:   err.Error(),
				UpdatedAt:   r.now(),
			})
			handle.publishError(err)
			return
		}
	}
	completed = true
	r.setRunState(ref.SessionID, agent.RunState{
		Status:      agent.RunLifecycleStatusCompleted,
		ActiveRunID: runID,
		UpdatedAt:   r.now(),
	})
}
