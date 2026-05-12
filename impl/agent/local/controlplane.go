package local

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/ports/agent"
	"github.com/OnslaughtSnail/caelis/ports/compact"
	"github.com/OnslaughtSnail/caelis/ports/controller"
	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/ports/tool"
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
				turnReq.ContextPrelude = ""
				turnReq.ContextSyncSeq = 0
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
				stateErr := seqErr
				if replayErr := r.persistInterruptedACPAssistantReplay(context.WithoutCancel(ctx), activeSession, ref, turnID, &accumulator, seqErr); replayErr != nil {
					stateErr = errors.Join(seqErr, replayErr)
				}
				r.setRunState(ref.SessionID, agent.RunState{
					Status:      interruptedOrFailedStatus(ctx, seqErr),
					ActiveRunID: runID,
					LastError:   stateErr.Error(),
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
			if session.IsCanonicalHistoryEvent(normalized) {
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
			if err := r.handleControllerPlanEvent(ctx, ref, normalized); err != nil {
				r.setRunState(ref.SessionID, agent.RunState{
					Status:      interruptedOrFailedStatus(ctx, err),
					ActiveRunID: runID,
					LastError:   err.Error(),
					UpdatedAt:   r.now(),
				})
				handle.publishError(err)
				return
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

type acpNarrativeAccumulator struct {
	assistantText      string
	reasoningText      string
	lastNarrativeEvent *session.Event
	lastAssistantEvent *session.Event
}

func (a *acpNarrativeAccumulator) normalize(event *session.Event) (*session.Event, *session.Event, bool) {
	if a == nil || !isACPControllerNarrativeChunk(event) {
		return event, nil, false
	}
	updateType := strings.TrimSpace(event.Protocol.UpdateType)
	raw := narrativeEventText(event, updateType)
	a.lastNarrativeEvent = session.CloneEvent(event)
	if updateType == string(session.ProtocolUpdateTypeAgentMessage) {
		a.lastAssistantEvent = session.CloneEvent(event)
	}
	cumulative, delta := a.append(updateType, raw)
	if cumulative == "" && delta == "" {
		return nil, nil, true
	}
	if delta == "" {
		return nil, nil, true
	}
	live := session.CloneEvent(event)
	live.ID = ""
	live.Visibility = session.VisibilityUIOnly
	setNarrativeEventText(live, updateType, delta)
	return nil, live, true
}

func (a *acpNarrativeAccumulator) append(updateType string, text string) (string, string) {
	switch strings.TrimSpace(updateType) {
	case string(session.ProtocolUpdateTypeAgentThought):
		cumulative, delta := appendNarrativeText(a.reasoningText, text)
		a.reasoningText = cumulative
		return cumulative, delta
	default:
		cumulative, delta := appendNarrativeText(a.assistantText, text)
		a.assistantText = cumulative
		return cumulative, delta
	}
}

func (a *acpNarrativeAccumulator) finalAssistantEvent() *session.Event {
	if a == nil || strings.TrimSpace(a.assistantText) == "" || a.lastAssistantEvent == nil {
		return nil
	}
	event := session.CloneEvent(a.lastAssistantEvent)
	event.ID = ""
	event.Visibility = session.VisibilityCanonical
	event.Type = session.EventTypeAssistant
	setNarrativeEventText(event, string(session.ProtocolUpdateTypeAgentMessage), a.assistantText)
	return event
}

func (r *Runtime) persistInterruptedACPAssistantReplay(
	ctx context.Context,
	activeSession session.Session,
	ref session.SessionRef,
	turnID string,
	accumulator *acpNarrativeAccumulator,
	cause error,
) error {
	if r == nil || r.sessions == nil || accumulator == nil {
		return nil
	}
	event := accumulator.interruptedAssistantReplayEvent(activeSession, turnID, cause)
	if event == nil {
		return nil
	}
	_, err := r.sessions.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef: ref,
		Event:      event,
	})
	return err
}

func (a *acpNarrativeAccumulator) interruptedAssistantReplayEvent(
	session session.Session,
	turnID string,
	cause error,
) *session.Event {
	if a == nil {
		return nil
	}
	answerText := a.assistantText
	reasoningText := a.reasoningText
	if strings.TrimSpace(answerText) == "" && strings.TrimSpace(reasoningText) == "" {
		return nil
	}
	template := a.lastAssistantEvent
	if template == nil {
		template = a.lastNarrativeEvent
	}
	return buildInterruptedAssistantReplayEvent(template, session, turnID, answerText, reasoningText, cause)
}

func isACPControllerNarrativeChunk(event *session.Event) bool {
	if event == nil || event.Protocol == nil || event.Scope == nil {
		return false
	}
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(event.Scope.Source)), "acp") {
		return false
	}
	switch strings.TrimSpace(event.Protocol.UpdateType) {
	case string(session.ProtocolUpdateTypeAgentMessage), string(session.ProtocolUpdateTypeAgentThought):
		return true
	default:
		return false
	}
}

func isACPControllerUserEcho(event *session.Event) bool {
	if event == nil || event.Scope == nil {
		return false
	}
	if session.EventTypeOf(event) != session.EventTypeUser {
		return false
	}
	if event.Scope.Participant.ID != "" {
		return false
	}
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(event.Scope.Source)), "acp")
}

func isACPParticipantUserEcho(event *session.Event) bool {
	if event == nil || event.Scope == nil {
		return false
	}
	if session.EventTypeOf(event) != session.EventTypeUser {
		return false
	}
	if strings.TrimSpace(event.Scope.Participant.ID) == "" {
		return false
	}
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(event.Scope.Source)), "acp")
}

func narrativeEventText(event *session.Event, updateType string) string {
	if event == nil {
		return ""
	}
	if event.Message != nil {
		switch strings.TrimSpace(updateType) {
		case string(session.ProtocolUpdateTypeAgentThought):
			if text := event.Message.ReasoningText(); text != "" {
				return text
			}
		default:
			if text := event.Message.TextContent(); text != "" {
				return text
			}
		}
	}
	return session.EventText(event)
}

func setNarrativeEventText(event *session.Event, updateType string, text string) {
	if event == nil {
		return
	}
	event.Text = text
	if event.Protocol != nil {
		protocol := session.CloneEventProtocol(*event.Protocol)
		if protocol.Update == nil {
			protocol.Update = &session.ProtocolUpdate{}
		}
		protocol.Update.SessionUpdate = strings.TrimSpace(updateType)
		protocol.Update.Content = session.ProtocolTextContent(text)
		event.Protocol = &protocol
	}
	switch strings.TrimSpace(updateType) {
	case string(session.ProtocolUpdateTypeAgentThought):
		message := model.NewReasoningMessage(model.RoleAssistant, text, model.ReasoningVisibilityVisible)
		event.Message = &message
	default:
		message := model.NewTextMessage(model.RoleAssistant, text)
		event.Message = &message
	}
}

func appendNarrativeText(existing string, incoming string) (string, string) {
	if incoming == "" {
		return existing, ""
	}
	if existing == "" {
		return incoming, incoming
	}
	if strings.HasPrefix(incoming, existing) {
		delta := incoming[len(existing):]
		return incoming, delta
	}
	if strings.HasPrefix(existing, incoming) {
		return existing, ""
	}
	return existing + incoming, incoming
}

func (r *Runtime) handleControllerPlanEvent(ctx context.Context, ref session.SessionRef, event *session.Event) error {
	if r == nil || r.sessions == nil || event == nil || event.Protocol == nil || event.Protocol.Plan == nil {
		return nil
	}
	entries := event.Protocol.Plan.Entries
	if len(entries) == 0 {
		return nil
	}
	return r.sessions.UpdateState(ctx, ref, func(state map[string]any) (map[string]any, error) {
		if state == nil {
			state = map[string]any{}
		}
		out := make([]map[string]any, 0, len(entries))
		for _, item := range entries {
			out = append(out, map[string]any{
				"content": strings.TrimSpace(item.Content),
				"status":  strings.TrimSpace(item.Status),
			})
		}
		state["plan"] = map[string]any{
			"version":     1,
			"entries":     out,
			"explanation": strings.TrimSpace(session.EventText(event)),
		}
		return state, nil
	})
}

func (r *Runtime) AttachACPParticipant(ctx context.Context, req agent.AttachACPParticipantRequest) (session.Session, error) {
	if r == nil || r.controllers == nil {
		return session.Session{}, fmt.Errorf("impl/agent/local: ACP controller backend is not configured")
	}
	ref := session.NormalizeSessionRef(req.SessionRef)
	activeSession, err := r.sessions.Session(ctx, ref)
	if err != nil {
		return session.Session{}, err
	}
	activeSession, err = r.ensureSessionController(ctx, activeSession)
	if err != nil {
		return session.Session{}, err
	}
	binding, err := r.controllers.Attach(ctx, controller.AttachRequest{
		SessionRef: ref,
		Session:    activeSession,
		Agent:      strings.TrimSpace(req.Agent),
		Role:       req.Role,
		Source:     strings.TrimSpace(req.Source),
		Label:      strings.TrimSpace(req.Label),
	})
	if err != nil {
		return session.Session{}, err
	}
	activeSession, err = r.sessions.PutParticipant(ctx, session.PutParticipantRequest{
		SessionRef: ref,
		Binding:    binding,
	})
	if err != nil {
		return session.Session{}, err
	}
	if _, err := r.sessions.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef: ref,
		Event:      participantLifecycleEvent(activeSession, binding, "attached", r.now()),
	}); err != nil {
		return session.Session{}, err
	}
	return r.sessions.Session(ctx, ref)
}

func (r *Runtime) DetachACPParticipant(ctx context.Context, req agent.DetachACPParticipantRequest) (session.Session, error) {
	if r == nil || r.controllers == nil {
		return session.Session{}, fmt.Errorf("impl/agent/local: ACP controller backend is not configured")
	}
	ref := session.NormalizeSessionRef(req.SessionRef)
	activeSession, err := r.sessions.Session(ctx, ref)
	if err != nil {
		return session.Session{}, err
	}
	activeSession, err = r.ensureSessionController(ctx, activeSession)
	if err != nil {
		return session.Session{}, err
	}
	binding, _ := participantBinding(activeSession, req.ParticipantID)
	if err := r.controllers.Detach(ctx, controller.DetachRequest{
		SessionRef:    ref,
		Session:       activeSession,
		ParticipantID: strings.TrimSpace(req.ParticipantID),
		Source:        strings.TrimSpace(req.Source),
	}); err != nil {
		return session.Session{}, err
	}
	activeSession, err = r.sessions.RemoveParticipant(ctx, session.RemoveParticipantRequest{
		SessionRef:    ref,
		ParticipantID: strings.TrimSpace(req.ParticipantID),
	})
	if err != nil {
		return session.Session{}, err
	}
	if binding.ID != "" {
		if _, err := r.sessions.AppendEvent(ctx, session.AppendEventRequest{
			SessionRef: ref,
			Event:      participantLifecycleEvent(activeSession, binding, "detached", r.now()),
		}); err != nil {
			return session.Session{}, err
		}
	}
	return r.sessions.Session(ctx, ref)
}

func (r *Runtime) PromptACPParticipant(ctx context.Context, req agent.PromptACPParticipantRequest) (agent.RunResult, error) {
	if r == nil || r.controllers == nil {
		return agent.RunResult{}, fmt.Errorf("impl/agent/local: ACP controller backend is not configured")
	}
	ref := session.NormalizeSessionRef(req.SessionRef)
	activeSession, err := r.sessions.Session(ctx, ref)
	if err != nil {
		return agent.RunResult{}, err
	}
	activeSession, err = r.ensureSessionController(ctx, activeSession)
	if err != nil {
		return agent.RunResult{}, err
	}
	binding, _ := participantBinding(activeSession, strings.TrimSpace(req.ParticipantID))
	contextPrelude := r.buildParticipantPromptContext(ctx, activeSession, ref, binding)
	turnID := r.nextID("participant-turn", nil)
	runID := r.nextID("participant-run", nil)
	runCtx, cancel := context.WithCancel(ctx)
	handle := newRunner(runID, cancel)
	go r.executeACPParticipantTurn(runCtx, activeSession, ref, req, binding, contextPrelude, runID, turnID, handle)
	return agent.RunResult{
		Session: activeSession,
		Handle:  handle,
	}, nil
}

func (r *Runtime) executeACPParticipantTurn(
	ctx context.Context,
	activeSession session.Session,
	ref session.SessionRef,
	req agent.PromptACPParticipantRequest,
	binding session.ParticipantBinding,
	contextPrelude string,
	runID string,
	turnID string,
	handle *runner,
) {
	defer handle.finish()
	participantID := strings.TrimSpace(req.ParticipantID)
	if userEvent := participantPromptUserEvent(activeSession, binding, turnID, strings.TrimSpace(req.Source), req.Input, req.ContentParts, r.now()); userEvent != nil {
		persisted, err := r.sessions.AppendEvent(ctx, session.AppendEventRequest{
			SessionRef: ref,
			Event:      userEvent,
		})
		if err != nil {
			handle.publishError(err)
			return
		}
		handle.publishEvent(persisted)
	}
	turnResult, err := r.controllers.PromptParticipant(ctx, controller.ParticipantPromptRequest{
		SessionRef:        ref,
		Session:           activeSession,
		TurnID:            turnID,
		ParticipantID:     participantID,
		Input:             strings.TrimSpace(req.Input),
		ContentParts:      req.ContentParts,
		ContextPrelude:    contextPrelude,
		Stream:            req.Stream,
		Mode:              r.policyMode(agent.AgentSpec{}),
		ApprovalRequester: controllerApprovalRequester{requester: req.ApprovalRequester, sessionRef: ref, session: activeSession, runID: runID, turnID: turnID},
	})
	if err != nil {
		handle.publishError(err)
		return
	}
	if turnResult.Handle == nil {
		return
	}
	handle.setCancelHook(func() error {
		return turnResult.Handle.Cancel().Err
	})
	defer turnResult.Handle.Close()
	accumulator := acpNarrativeAccumulator{}
	for event, seqErr := range turnResult.Handle.Events() {
		if seqErr != nil {
			handle.publishError(seqErr)
			return
		}
		normalized := normalizeEvent(activeSession, turnID, event)
		if normalized == nil {
			continue
		}
		if isACPParticipantUserEcho(normalized) {
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
		if session.IsCanonicalHistoryEvent(normalized) {
			persisted, appendErr := r.sessions.AppendEvent(ctx, session.AppendEventRequest{
				SessionRef: ref,
				Event:      normalized,
			})
			if appendErr != nil {
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
			handle.publishError(appendErr)
			return
		}
		handle.publishEvent(persisted)
	}
	if err := r.updateParticipantContextCheckpoint(ctx, ref, participantID); err != nil {
		handle.publishError(err)
		return
	}
}

func participantPromptUserEvent(
	activeSession session.Session,
	binding session.ParticipantBinding,
	turnID string,
	source string,
	input string,
	parts []model.ContentPart,
	now time.Time,
) *session.Event {
	if strings.TrimSpace(input) == "" && len(parts) == 0 {
		return nil
	}
	message := model.MessageFromTextAndContentParts(model.RoleUser, strings.TrimSpace(input), parts)
	label := participantBindingLabel(binding)
	meta := map[string]any{}
	if label != "" {
		meta["mention"] = label
		meta["handle"] = strings.TrimPrefix(label, "@")
	}
	if agent := strings.TrimSpace(binding.AgentName); agent != "" {
		meta["agent"] = agent
	}
	kind := binding.Kind
	if kind == "" {
		kind = session.ParticipantKindACP
	}
	role := binding.Role
	if role == "" {
		role = session.ParticipantRoleSidecar
	}
	return &session.Event{
		Type:       session.EventTypeUser,
		Visibility: session.VisibilityCanonical,
		Time:       now,
		Actor:      session.ActorRef{Kind: session.ActorKindUser, Name: "user"},
		Scope: &session.EventScope{
			TurnID: strings.TrimSpace(turnID),
			Source: firstNonEmpty(strings.TrimSpace(source), "acp_participant"),
			Controller: session.ControllerRef{
				Kind:    activeSession.Controller.Kind,
				ID:      activeSession.Controller.ControllerID,
				EpochID: activeSession.Controller.EpochID,
			},
			Participant: session.ParticipantRef{
				ID:   strings.TrimSpace(binding.ID),
				Kind: kind,
				Role: role,
			},
		},
		Message: &message,
		Text:    message.TextContent(),
		Protocol: &session.EventProtocol{
			UpdateType: string(session.ProtocolUpdateTypeUserMessage),
			Update: &session.ProtocolUpdate{
				SessionUpdate: string(session.ProtocolUpdateTypeUserMessage),
				Content:       session.ProtocolTextContent(message.TextContent()),
			},
		},
		Meta: meta,
	}
}

func (r *Runtime) HandoffController(ctx context.Context, req agent.HandoffControllerRequest) (session.Session, error) {
	ref := session.NormalizeSessionRef(req.SessionRef)
	activeSession, err := r.sessions.Session(ctx, ref)
	if err != nil {
		return session.Session{}, err
	}
	activeSession, err = r.ensureSessionController(ctx, activeSession)
	if err != nil {
		return session.Session{}, err
	}
	from := session.CloneControllerBinding(activeSession.Controller)
	kind := req.Kind
	if kind == "" {
		kind = session.ControllerKindKernel
	}
	var to session.ControllerBinding
	switch kind {
	case session.ControllerKindACP:
		if r.controllers == nil {
			return session.Session{}, fmt.Errorf("impl/agent/local: ACP controller backend is not configured")
		}
		sinceSeq := 0
		if from.Kind == session.ControllerKindACP && sameControllerAgent(from, req.Agent) {
			sinceSeq = from.ContextSyncSeq
		}
		contextPrelude, contextSeq := r.buildControllerHandoffContext(ctx, activeSession, ref, from, sinceSeq, "")
		to, err = r.controllers.Activate(ctx, controller.HandoffRequest{
			SessionRef:     ref,
			Session:        activeSession,
			Agent:          strings.TrimSpace(req.Agent),
			Source:         strings.TrimSpace(req.Source),
			Reason:         strings.TrimSpace(req.Reason),
			ContextPrelude: contextPrelude,
			ContextSyncSeq: contextSeq,
		})
		if err != nil {
			return session.Session{}, err
		}
	default:
		if r.controllers != nil && from.Kind == session.ControllerKindACP {
			if err := r.controllers.Deactivate(ctx, ref); err != nil {
				return session.Session{}, err
			}
		}
		to = r.kernelControllerBinding(firstNonEmpty(strings.TrimSpace(req.Source), "handoff"))
	}

	activeSession, err = r.sessions.BindController(ctx, session.BindControllerRequest{
		SessionRef: ref,
		Binding:    to,
	})
	if err != nil {
		return session.Session{}, err
	}
	if _, err := r.sessions.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef: ref,
		Event:      handoffEvent(from, to, strings.TrimSpace(req.Reason), r.now()),
	}); err != nil {
		return session.Session{}, err
	}
	return r.sessions.Session(ctx, ref)
}

func (r *Runtime) buildControllerTurnContext(
	ctx context.Context,
	activeSession session.Session,
	ref session.SessionRef,
	excludeTurnID string,
) (string, int) {
	binding := session.CloneControllerBinding(activeSession.Controller)
	if binding.Kind != session.ControllerKindACP {
		return "", binding.ContextSyncSeq
	}
	contextPrelude, contextSeq := r.buildControllerHandoffContext(ctx, activeSession, ref, binding, binding.ContextSyncSeq, excludeTurnID)
	if contextSeq <= binding.ContextSyncSeq {
		return "", binding.ContextSyncSeq
	}
	return contextPrelude, contextSeq
}

func (r *Runtime) buildControllerHandoffContext(
	ctx context.Context,
	activeSession session.Session,
	ref session.SessionRef,
	from session.ControllerBinding,
	sinceSeq int,
	excludeTurnID string,
) (string, int) {
	shared := r.buildSharedDialogueDeltaExcludingTurn(ctx, ref, sinceSeq, excludeTurnID)
	var b strings.Builder
	b.WriteString("Caelis controller handoff context. Continue the existing Caelis session; do not treat this as a fresh conversation.\n")
	b.WriteString("session_id: ")
	b.WriteString(strings.TrimSpace(activeSession.SessionID))
	b.WriteString("\nworkspace: ")
	b.WriteString(strings.TrimSpace(activeSession.CWD))
	b.WriteString("\nprevious_controller: ")
	b.WriteString(firstNonEmpty(strings.TrimSpace(from.AgentName), strings.TrimSpace(from.Label), strings.TrimSpace(from.ControllerID), string(from.Kind)))
	b.WriteString("\ncontext_sync_seq: ")
	fmt.Fprintf(&b, "%d", shared.Checkpoint)
	if len(activeSession.Participants) > 0 {
		b.WriteString("\nchild_handles:")
		for _, participant := range activeSession.Participants {
			if participant.Kind != session.ParticipantKindSubagent || participant.Role != session.ParticipantRoleDelegated {
				continue
			}
			handle := strings.TrimSpace(participant.Label)
			if handle == "" {
				continue
			}
			b.WriteString("\n- ")
			b.WriteString(handle)
			if agent := strings.TrimSpace(participant.AgentName); agent != "" {
				b.WriteString(" agent=")
				b.WriteString(agent)
			}
		}
	}
	appendSharedDialogueDelta(&b, shared)
	return b.String(), shared.Checkpoint
}

func (r *Runtime) buildParticipantPromptContext(
	ctx context.Context,
	activeSession session.Session,
	ref session.SessionRef,
	binding session.ParticipantBinding,
) string {
	shared := r.buildSharedDialogueDelta(ctx, ref, binding.ContextSyncSeq)
	var b strings.Builder
	b.WriteString("Caelis shared public dialogue context. Use this as background for the current side-agent request; do not treat it as a fresh session.\n")
	if sessionID := strings.TrimSpace(activeSession.SessionID); sessionID != "" {
		b.WriteString("session_id: ")
		b.WriteString(sessionID)
		b.WriteString("\n")
	}
	if cwd := strings.TrimSpace(activeSession.CWD); cwd != "" {
		b.WriteString("workspace: ")
		b.WriteString(cwd)
		b.WriteString("\n")
	}
	if target := firstNonEmpty(strings.TrimSpace(binding.Label), strings.TrimSpace(binding.AgentName), strings.TrimSpace(binding.ID)); target != "" {
		b.WriteString("target_agent: ")
		b.WriteString(target)
		b.WriteString("\n")
	}
	appendSharedDialogueDelta(&b, shared)
	return strings.TrimSpace(b.String())
}

func (r *Runtime) updateControllerContextCheckpoint(ctx context.Context, ref session.SessionRef) error {
	if r == nil || r.sessions == nil {
		return nil
	}
	activeSession, err := r.sessions.Session(ctx, ref)
	if err != nil {
		return err
	}
	binding := session.CloneControllerBinding(activeSession.Controller)
	binding.ContextSyncSeq = r.sharedDialogueCheckpoint(ctx, ref)
	_, err = r.sessions.BindController(ctx, session.BindControllerRequest{
		SessionRef: ref,
		Binding:    binding,
	})
	return err
}

func (r *Runtime) updateParticipantContextCheckpoint(ctx context.Context, ref session.SessionRef, participantID string) error {
	if r == nil || r.sessions == nil {
		return nil
	}
	participantID = strings.TrimSpace(participantID)
	if participantID == "" {
		return nil
	}
	activeSession, err := r.sessions.Session(ctx, ref)
	if err != nil {
		return err
	}
	binding, ok := participantBinding(activeSession, participantID)
	if !ok {
		return nil
	}
	binding.ContextSyncSeq = r.sharedDialogueCheckpoint(ctx, ref)
	_, err = r.sessions.PutParticipant(ctx, session.PutParticipantRequest{
		SessionRef: ref,
		Binding:    binding,
	})
	return err
}

func (r *Runtime) sharedDialogueCheckpoint(ctx context.Context, ref session.SessionRef) int {
	if r == nil || r.sessions == nil {
		return 0
	}
	events, err := r.sessions.Events(ctx, session.EventsRequest{SessionRef: ref})
	if err != nil {
		return 0
	}
	return sharedDialogueCheckpoint(events)
}

type sharedDialogueDelta struct {
	Checkpoint int
	Entries    []sharedDialogueEntry
}

type sharedDialogueEntry struct {
	Seq  int
	Role string
	Text string
}

func (r *Runtime) buildSharedDialogueDelta(ctx context.Context, ref session.SessionRef, sinceSeq int) sharedDialogueDelta {
	return r.buildSharedDialogueDeltaExcludingTurn(ctx, ref, sinceSeq, "")
}

func (r *Runtime) buildSharedDialogueDeltaExcludingTurn(ctx context.Context, ref session.SessionRef, sinceSeq int, excludeTurnID string) sharedDialogueDelta {
	if r == nil || r.sessions == nil {
		return sharedDialogueDelta{}
	}
	events, err := r.sessions.Events(ctx, session.EventsRequest{SessionRef: ref})
	if err != nil {
		return sharedDialogueDelta{}
	}
	return sharedDialogueDeltaFromEventsExcludingTurn(events, sinceSeq, excludeTurnID)
}

func sharedDialogueDeltaFromEvents(events []*session.Event, sinceSeq int) sharedDialogueDelta {
	return sharedDialogueDeltaFromEventsExcludingTurn(events, sinceSeq, "")
}

func sharedDialogueDeltaFromEventsExcludingTurn(events []*session.Event, sinceSeq int, excludeTurnID string) sharedDialogueDelta {
	if sinceSeq < 0 {
		sinceSeq = 0
	}
	excludeTurnID = strings.TrimSpace(excludeTurnID)
	latestCompactSeq := latestCompactEventSeq(events)
	startAfter := sinceSeq
	if latestCompactSeq > 0 && sinceSeq < latestCompactSeq {
		startAfter = latestCompactSeq - 1
	}
	out := sharedDialogueDelta{Checkpoint: sharedDialogueCheckpointExcludingTurn(events, excludeTurnID)}
	for i, event := range events {
		seq := i + 1
		if seq <= startAfter {
			continue
		}
		if latestCompactSeq > 0 && seq < latestCompactSeq {
			continue
		}
		if excludeTurnID != "" && event != nil && event.Scope != nil && strings.TrimSpace(event.Scope.TurnID) == excludeTurnID {
			continue
		}
		if !isSharedDialogueDeltaEvent(event, latestCompactSeq, seq) {
			continue
		}
		text := sharedDialogueText(event)
		if text == "" {
			continue
		}
		out.Entries = append(out.Entries, sharedDialogueEntry{
			Seq:  seq,
			Role: sharedDialogueRole(event),
			Text: text,
		})
	}
	return out
}

func appendSharedDialogueDelta(b *strings.Builder, delta sharedDialogueDelta) {
	if b == nil {
		return
	}
	if delta.Checkpoint > 0 {
		b.WriteString("\nshared_ledger_checkpoint: ")
		fmt.Fprintf(b, "%d", delta.Checkpoint)
	}
	b.WriteString("\nshared_dialogue_delta:")
	if len(delta.Entries) == 0 {
		b.WriteString("\n(none)")
		return
	}
	for _, entry := range delta.Entries {
		b.WriteString("\n[")
		fmt.Fprintf(b, "%d", entry.Seq)
		b.WriteString("] ")
		b.WriteString(entry.Role)
		b.WriteString(":\n")
		b.WriteString(entry.Text)
	}
}

func isSharedDialogueDeltaEvent(event *session.Event, latestCompactSeq int, seq int) bool {
	if event == nil || !session.IsCanonicalHistoryEvent(event) {
		return false
	}
	if latestCompactSeq > 0 && seq == latestCompactSeq && compact.IsCompactEvent(event) {
		return true
	}
	return isSharedDialogueEvent(event)
}

func latestCompactEventSeq(events []*session.Event) int {
	for i := len(events) - 1; i >= 0; i-- {
		if compact.IsCompactEvent(events[i]) {
			return i + 1
		}
	}
	return 0
}

func sharedDialogueCheckpoint(events []*session.Event) int {
	return sharedDialogueCheckpointExcludingTurn(events, "")
}

func sharedDialogueCheckpointExcludingTurn(events []*session.Event, excludeTurnID string) int {
	excludeTurnID = strings.TrimSpace(excludeTurnID)
	checkpoint := 0
	latestCompactSeq := latestCompactEventSeq(events)
	for i, event := range events {
		seq := i + 1
		if latestCompactSeq > 0 && seq < latestCompactSeq {
			continue
		}
		if excludeTurnID != "" && event != nil && event.Scope != nil && strings.TrimSpace(event.Scope.TurnID) == excludeTurnID {
			continue
		}
		if isSharedDialogueDeltaEvent(event, latestCompactSeq, seq) {
			checkpoint = seq
		}
	}
	return checkpoint
}

func isSharedDialogueEvent(event *session.Event) bool {
	if event == nil || !session.IsCanonicalHistoryEvent(event) {
		return false
	}
	switch session.EventTypeOf(event) {
	case session.EventTypeUser, session.EventTypeAssistant:
		return true
	default:
		return false
	}
}

func sharedDialogueRole(event *session.Event) string {
	if event == nil {
		return ""
	}
	if compact.IsCompactEvent(event) {
		return "compact"
	}
	role := strings.TrimSpace(string(session.EventTypeOf(event)))
	actor := strings.TrimSpace(event.Actor.Name)
	if actor == "" {
		actor = strings.TrimSpace(event.Actor.ID)
	}
	if actor == "" || strings.EqualFold(actor, role) {
		return role
	}
	return role + "(" + actor + ")"
}

func sharedDialogueText(event *session.Event) string {
	if event == nil {
		return ""
	}
	return strings.TrimSpace(session.EventText(event))
}

func sameControllerAgent(binding session.ControllerBinding, agent string) bool {
	agent = strings.TrimSpace(agent)
	if agent == "" {
		return false
	}
	for _, candidate := range []string{binding.AgentName, binding.Label, binding.ControllerID} {
		if strings.EqualFold(strings.TrimSpace(candidate), agent) {
			return true
		}
	}
	return false
}

func participantBinding(activeSession session.Session, participantID string) (session.ParticipantBinding, bool) {
	participantID = strings.TrimSpace(participantID)
	for _, item := range activeSession.Participants {
		if strings.TrimSpace(item.ID) == participantID {
			return session.CloneParticipantBinding(item), true
		}
	}
	return session.ParticipantBinding{}, false
}

func participantBindingLabel(binding session.ParticipantBinding) string {
	return firstNonEmpty(strings.TrimSpace(binding.Label), strings.TrimSpace(binding.AgentName), strings.TrimSpace(binding.ID))
}

func participantLifecycleEvent(activeSession session.Session, binding session.ParticipantBinding, action string, now time.Time) *session.Event {
	text := strings.TrimSpace(action + " participant " + firstNonEmpty(binding.Label, binding.ID))
	return &session.Event{
		Type:       session.EventTypeParticipant,
		Visibility: session.VisibilityCanonical,
		Time:       now,
		Actor:      session.ActorRef{Kind: session.ActorKindSystem, Name: "runtime"},
		Text:       text,
		Protocol: &session.EventProtocol{
			Participant: &session.ProtocolParticipant{Action: strings.TrimSpace(action)},
		},
		Scope: &session.EventScope{
			Source: "control_plane",
			Controller: session.ControllerRef{
				Kind:    activeSession.Controller.Kind,
				ID:      activeSession.Controller.ControllerID,
				EpochID: activeSession.Controller.EpochID,
			},
			Participant: session.ParticipantRef{
				ID:           binding.ID,
				Kind:         binding.Kind,
				Role:         binding.Role,
				DelegationID: binding.DelegationID,
			},
			ACP: session.ACPRef{
				SessionID: strings.TrimSpace(binding.SessionID),
			},
		},
		Meta: map[string]any{
			"participant_id": binding.ID,
			"label":          binding.Label,
			"session_id":     binding.SessionID,
			"controller_ref": binding.ControllerRef,
		},
	}
}

func isMissingACPControllerRun(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "no active acp controller")
}

func handoffEvent(from session.ControllerBinding, to session.ControllerBinding, reason string, now time.Time) *session.Event {
	text := "handoff to " + firstNonEmpty(to.Label, to.ControllerID)
	meta := map[string]any{
		"from": map[string]any{
			"kind":              from.Kind,
			"id":                strings.TrimSpace(from.ControllerID),
			"agent":             strings.TrimSpace(from.AgentName),
			"remote_session_id": strings.TrimSpace(from.RemoteSessionID),
			"context_sync_seq":  from.ContextSyncSeq,
		},
		"to": map[string]any{
			"kind":              to.Kind,
			"id":                strings.TrimSpace(to.ControllerID),
			"agent":             strings.TrimSpace(to.AgentName),
			"remote_session_id": strings.TrimSpace(to.RemoteSessionID),
			"context_sync_seq":  to.ContextSyncSeq,
		},
	}
	if strings.TrimSpace(reason) != "" {
		meta["reason"] = strings.TrimSpace(reason)
	}
	return &session.Event{
		Type:       session.EventTypeHandoff,
		Visibility: session.VisibilityCanonical,
		Time:       now,
		Actor:      session.ActorRef{Kind: session.ActorKindSystem, Name: "runtime"},
		Text:       text,
		Protocol: &session.EventProtocol{
			Handoff: &session.ProtocolHandoff{Phase: "activation"},
		},
		Scope: &session.EventScope{
			Source: "handoff",
			Controller: session.ControllerRef{
				Kind:    to.Kind,
				ID:      strings.TrimSpace(to.ControllerID),
				EpochID: strings.TrimSpace(to.EpochID),
			},
		},
		Meta: meta,
	}
}

type controllerApprovalRequester struct {
	requester  agent.ApprovalRequester
	sessionRef session.SessionRef
	session    session.Session
	runID      string
	turnID     string
}

func (r controllerApprovalRequester) RequestControllerApproval(ctx context.Context, req controller.ApprovalRequest) (controller.ApprovalResponse, error) {
	if r.requester == nil {
		return controller.ApprovalResponse{}, nil
	}
	options := make([]session.ProtocolApprovalOption, 0, len(req.Options))
	for _, item := range req.Options {
		options = append(options, session.ProtocolApprovalOption{
			ID:   strings.TrimSpace(item.ID),
			Name: strings.TrimSpace(item.Name),
			Kind: strings.TrimSpace(item.Kind),
		})
	}
	toolName := firstNonEmpty(req.ToolCall.Name, req.ToolCall.Title, "ACP_TOOL")
	rawInput := maps.Clone(req.ToolCall.RawInput)
	var callInput json.RawMessage
	if len(rawInput) > 0 {
		if data, marshalErr := json.Marshal(rawInput); marshalErr == nil {
			callInput = data
		}
	}
	resp, err := r.requester.RequestApproval(ctx, agent.ApprovalRequest{
		SessionRef: session.NormalizeSessionRef(r.sessionRef),
		Session:    session.CloneSession(r.session),
		RunID:      strings.TrimSpace(r.runID),
		TurnID:     strings.TrimSpace(r.turnID),
		Mode:       strings.TrimSpace(req.Mode),
		Tool: tool.Definition{
			Name:        toolName,
			Description: firstNonEmpty(req.ToolCall.Title, req.ToolCall.Kind, "ACP controller requested permission"),
		},
		Call: tool.Call{
			ID:    strings.TrimSpace(req.ToolCall.ID),
			Name:  toolName,
			Input: callInput,
		},
		Approval: &session.ProtocolApproval{
			ToolCall: session.ProtocolToolCall{
				ID:       strings.TrimSpace(req.ToolCall.ID),
				Name:     toolName,
				Kind:     strings.TrimSpace(req.ToolCall.Kind),
				Title:    strings.TrimSpace(req.ToolCall.Title),
				Status:   strings.TrimSpace(req.ToolCall.Status),
				RawInput: rawInput,
			},
			Options: options,
		},
		Metadata: map[string]any{
			"agent": strings.TrimSpace(req.Agent),
		},
	})
	if err != nil {
		return controller.ApprovalResponse{}, err
	}
	return controller.ApprovalResponse{
		Outcome:  strings.TrimSpace(resp.Outcome),
		OptionID: strings.TrimSpace(resp.OptionID),
		Approved: resp.Approved,
	}, nil
}
