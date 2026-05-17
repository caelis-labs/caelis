package local

import (
	"context"
	"fmt"
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/agent"
	"github.com/OnslaughtSnail/caelis/ports/controller"
	"github.com/OnslaughtSnail/caelis/ports/session"
)

func (r *Runtime) Controllers() controller.Backend {
	if r == nil {
		return nil
	}
	return r.controllers
}

func (r *Runtime) ACPControllerStatus(ctx context.Context, ref session.SessionRef) (controller.ControllerStatus, bool, error) {
	if r == nil || r.controllers == nil {
		return controller.ControllerStatus{}, false, nil
	}
	provider, ok := r.controllers.(controller.ControllerStatusProvider)
	if !ok || provider == nil {
		return controller.ControllerStatus{}, false, nil
	}
	return provider.ControllerStatus(ctx, session.NormalizeSessionRef(ref))
}

func (r *Runtime) SetACPControllerModel(ctx context.Context, req controller.SetControllerModelRequest) (controller.ControllerStatus, error) {
	if r == nil || r.controllers == nil {
		return controller.ControllerStatus{}, fmt.Errorf("impl/agent/local: ACP controller backend is not configured")
	}
	configurator, ok := r.controllers.(controller.ControllerConfigurator)
	if !ok || configurator == nil {
		return controller.ControllerStatus{}, fmt.Errorf("impl/agent/local: ACP controller backend does not expose model configuration")
	}
	req.SessionRef = session.NormalizeSessionRef(req.SessionRef)
	req.Model = strings.TrimSpace(req.Model)
	req.ReasoningEffort = strings.TrimSpace(req.ReasoningEffort)
	return configurator.SetControllerModel(ctx, req)
}

func (r *Runtime) SetACPControllerMode(ctx context.Context, req controller.SetControllerModeRequest) (controller.ControllerStatus, error) {
	if r == nil || r.controllers == nil {
		return controller.ControllerStatus{}, fmt.Errorf("impl/agent/local: ACP controller backend is not configured")
	}
	configurator, ok := r.controllers.(controller.ControllerConfigurator)
	if !ok || configurator == nil {
		return controller.ControllerStatus{}, fmt.Errorf("impl/agent/local: ACP controller backend does not expose mode configuration")
	}
	req.SessionRef = session.NormalizeSessionRef(req.SessionRef)
	req.Mode = strings.TrimSpace(req.Mode)
	return configurator.SetControllerMode(ctx, req)
}

func (r *Runtime) ensureSessionController(ctx context.Context, activeSession session.Session) (session.Session, error) {
	if r == nil || r.sessions == nil {
		return session.Session{}, fmt.Errorf("impl/agent/local: session service is unavailable")
	}
	if activeSession.Controller.Kind != "" {
		return session.CloneSession(activeSession), nil
	}
	return r.sessions.BindController(ctx, session.BindControllerRequest{
		SessionRef: activeSession.SessionRef,
		Binding:    r.kernelControllerBinding("runtime"),
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
	session session.Session,
	ref session.SessionRef,
	req agent.RunRequest,
) (agent.RunResult, error) {
	if r == nil || r.controllers == nil {
		return agent.RunResult{}, fmt.Errorf("impl/agent/local: ACP controller backend is not configured")
	}
	runID := r.nextID("run", r.runIDGenerator)
	turnID := r.nextID("turn", nil)
	r.setRunState(ref.SessionID, agent.RunState{
		Status:      agent.RunLifecycleStatusRunning,
		ActiveRunID: runID,
		UpdatedAt:   r.now(),
	})
	runCtx, cancel := context.WithCancel(ctx)
	handle := newRunner(runID, cancel)
	go r.executeACPControllerTurn(runCtx, session, ref, req, runID, turnID, handle)
	return agent.RunResult{
		Session: session,
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

	userEvent := buildUserEvent(activeSession, turnID, req.Input, req.ContentParts)
	if userEvent != nil {
		persisted, err := r.sessions.AppendEvent(ctx, session.AppendEventRequest{
			SessionRef: ref,
			Event:      userEvent,
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
	if contextPrelude, contextSeq := r.buildControllerTurnContext(ctx, activeSession, ref, turnID); contextSeq > activeSession.Controller.ContextSyncSeq {
		turnReq.ContextPrelude = contextPrelude
		turnReq.ContextSyncSeq = contextSeq
	}
	turnResult, err := r.controllers.RunTurn(ctx, turnReq)
	if err != nil && isMissingACPControllerRun(err) {
		agent := firstNonEmpty(strings.TrimSpace(activeSession.Controller.AgentName), strings.TrimSpace(activeSession.Controller.ControllerID), strings.TrimSpace(activeSession.Controller.Label))
		contextPrelude, contextSeq := r.buildControllerHandoffContext(ctx, activeSession, ref, activeSession.Controller, activeSession.Controller.ContextSyncSeq, turnID)
		binding, activateErr := r.controllers.Activate(ctx, controller.HandoffRequest{
			SessionRef:     ref,
			Session:        activeSession,
			Agent:          agent,
			Source:         "controller_rehydrate",
			Reason:         "controller process rehydrate",
			ContextPrelude: contextPrelude,
			ContextSyncSeq: contextSeq,
		})
		if activateErr == nil {
			var bindErr error
			activeSession, bindErr = r.sessions.BindController(ctx, session.BindControllerRequest{SessionRef: ref, Binding: binding})
			if bindErr == nil {
				turnReq.Session = activeSession
				if binding.ContextSyncSeq < contextSeq {
					turnReq.ContextPrelude, turnReq.ContextSyncSeq = r.buildControllerHandoffContext(ctx, activeSession, ref, binding, binding.ContextSyncSeq, turnID)
				} else {
					turnReq.ContextPrelude = ""
					turnReq.ContextSyncSeq = 0
				}
				turnResult, err = r.controllers.RunTurn(ctx, turnReq)
			} else {
				err = bindErr
			}
		} else {
			err = activateErr
		}
	}
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
		handle.setCancelHook(func() error {
			return turnResult.Handle.Cancel().Err
		})
		defer turnResult.Handle.Close()
		accumulator := acpNarrativeAccumulator{}
		for event, seqErr := range turnResult.Handle.Events() {
			if seqErr != nil {
				r.setRunState(ref.SessionID, agent.RunState{
					Status:      interruptedOrFailedStatus(ctx, seqErr),
					ActiveRunID: runID,
					LastError:   seqErr.Error(),
					UpdatedAt:   r.now(),
				})
				handle.publishError(seqErr)
				return
			}
			normalized := normalizeEvent(activeSession, turnID, event)
			if normalized == nil {
				continue
			}
			if isACPControllerUserEcho(normalized) {
				continue
			}
			publishEvent := normalized
			if persistedEvent, liveEvent, ok := accumulator.normalize(normalized); ok {
				if liveEvent != nil {
					handle.publishEvent(liveEvent)
				}
				if persistedEvent == nil {
					continue
				}
				normalized = persistedEvent
				publishEvent = nil
			}
			if shouldPersistExternalACPEvent(normalized) {
				persisted, appendErr := r.sessions.AppendEvent(ctx, session.AppendEventRequest{
					SessionRef: ref,
					Event:      normalized,
				})
				if appendErr != nil {
					r.setRunState(ref.SessionID, agent.RunState{
						Status:      interruptedOrFailedStatus(ctx, appendErr),
						ActiveRunID: runID,
						LastError:   appendErr.Error(),
						UpdatedAt:   r.now(),
					})
					handle.publishError(appendErr)
					return
				}
				normalized = persisted
			}
			if publishEvent == nil {
				publishEvent = normalized
			}
			handle.publishEvent(publishEvent)
		}
		if finalEvent := accumulator.finalAssistantEvent(); finalEvent != nil {
			persisted, appendErr := r.sessions.AppendEvent(ctx, session.AppendEventRequest{
				SessionRef: ref,
				Event:      finalEvent,
			})
			if appendErr != nil {
				r.setRunState(ref.SessionID, agent.RunState{
					Status:      interruptedOrFailedStatus(ctx, appendErr),
					ActiveRunID: runID,
					LastError:   appendErr.Error(),
					UpdatedAt:   r.now(),
				})
				handle.publishError(appendErr)
				return
			}
			handle.publishEvent(persisted)
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
	r.setRunState(ref.SessionID, agent.RunState{
		Status:      agent.RunLifecycleStatusCompleted,
		ActiveRunID: runID,
		UpdatedAt:   r.now(),
	})
}
