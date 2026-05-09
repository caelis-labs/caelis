package local

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	sdkcompact "github.com/OnslaughtSnail/caelis/sdk/compact"
	sdkcontroller "github.com/OnslaughtSnail/caelis/sdk/controller"
	sdkmodel "github.com/OnslaughtSnail/caelis/sdk/model"
	sdkruntime "github.com/OnslaughtSnail/caelis/sdk/runtime"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
	sdktool "github.com/OnslaughtSnail/caelis/sdk/tool"
)

func (r *Runtime) Controllers() sdkcontroller.Backend {
	if r == nil {
		return nil
	}
	return r.controllers
}

func (r *Runtime) ACPControllerStatus(ctx context.Context, ref sdksession.SessionRef) (sdkcontroller.ControllerStatus, bool, error) {
	if r == nil || r.controllers == nil {
		return sdkcontroller.ControllerStatus{}, false, nil
	}
	provider, ok := r.controllers.(sdkcontroller.ControllerStatusProvider)
	if !ok || provider == nil {
		return sdkcontroller.ControllerStatus{}, false, nil
	}
	return provider.ControllerStatus(ctx, sdksession.NormalizeSessionRef(ref))
}

func (r *Runtime) SetACPControllerModel(ctx context.Context, req sdkcontroller.SetControllerModelRequest) (sdkcontroller.ControllerStatus, error) {
	if r == nil || r.controllers == nil {
		return sdkcontroller.ControllerStatus{}, fmt.Errorf("sdk/runtime/local: ACP controller backend is not configured")
	}
	configurator, ok := r.controllers.(sdkcontroller.ControllerConfigurator)
	if !ok || configurator == nil {
		return sdkcontroller.ControllerStatus{}, fmt.Errorf("sdk/runtime/local: ACP controller backend does not expose model configuration")
	}
	req.SessionRef = sdksession.NormalizeSessionRef(req.SessionRef)
	req.Model = strings.TrimSpace(req.Model)
	req.ReasoningEffort = strings.TrimSpace(req.ReasoningEffort)
	return configurator.SetControllerModel(ctx, req)
}

func (r *Runtime) SetACPControllerMode(ctx context.Context, req sdkcontroller.SetControllerModeRequest) (sdkcontroller.ControllerStatus, error) {
	if r == nil || r.controllers == nil {
		return sdkcontroller.ControllerStatus{}, fmt.Errorf("sdk/runtime/local: ACP controller backend is not configured")
	}
	configurator, ok := r.controllers.(sdkcontroller.ControllerConfigurator)
	if !ok || configurator == nil {
		return sdkcontroller.ControllerStatus{}, fmt.Errorf("sdk/runtime/local: ACP controller backend does not expose mode configuration")
	}
	req.SessionRef = sdksession.NormalizeSessionRef(req.SessionRef)
	req.Mode = strings.TrimSpace(req.Mode)
	return configurator.SetControllerMode(ctx, req)
}

func (r *Runtime) ensureSessionController(ctx context.Context, session sdksession.Session) (sdksession.Session, error) {
	if r == nil || r.sessions == nil {
		return sdksession.Session{}, fmt.Errorf("sdk/runtime/local: session service is unavailable")
	}
	if session.Controller.Kind != "" {
		return sdksession.CloneSession(session), nil
	}
	return r.sessions.BindController(ctx, sdksession.BindControllerRequest{
		SessionRef: session.SessionRef,
		Binding:    r.kernelControllerBinding("runtime"),
	})
}

func (r *Runtime) kernelControllerBinding(source string) sdksession.ControllerBinding {
	return sdksession.ControllerBinding{
		Kind:         sdksession.ControllerKindKernel,
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
	session sdksession.Session,
	ref sdksession.SessionRef,
	req sdkruntime.RunRequest,
) (sdkruntime.RunResult, error) {
	if r == nil || r.controllers == nil {
		return sdkruntime.RunResult{}, fmt.Errorf("sdk/runtime/local: ACP controller backend is not configured")
	}
	runID := r.nextID("run", r.runIDGenerator)
	turnID := r.nextID("turn", nil)
	r.setRunState(ref.SessionID, sdkruntime.RunState{
		Status:      sdkruntime.RunLifecycleStatusRunning,
		ActiveRunID: runID,
		UpdatedAt:   r.now(),
	})
	runCtx, cancel := context.WithCancel(ctx)
	handle := newRunner(runID, cancel)
	go r.executeACPControllerTurn(runCtx, session, ref, req, runID, turnID, handle)
	return sdkruntime.RunResult{
		Session: session,
		Handle:  handle,
	}, nil
}

func (r *Runtime) executeACPControllerTurn(
	ctx context.Context,
	session sdksession.Session,
	ref sdksession.SessionRef,
	req sdkruntime.RunRequest,
	runID string,
	turnID string,
	handle *runner,
) {
	defer handle.finish()

	userEvent := buildUserEvent(session, turnID, req.Input, req.ContentParts)
	if userEvent != nil {
		persisted, err := r.sessions.AppendEvent(ctx, sdksession.AppendEventRequest{
			SessionRef: ref,
			Event:      userEvent,
		})
		if err != nil {
			r.setRunState(ref.SessionID, sdkruntime.RunState{
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

	turnReq := sdkcontroller.TurnRequest{
		SessionRef:        ref,
		Session:           session,
		TurnID:            turnID,
		Input:             req.Input,
		ContentParts:      req.ContentParts,
		Stream:            req.Request.StreamEnabled(false),
		Mode:              r.policyMode(req.AgentSpec),
		ApprovalRequester: controllerApprovalRequester{requester: req.ApprovalRequester, sessionRef: ref, session: session, runID: runID, turnID: turnID},
	}
	if contextPrelude, contextSeq := r.buildControllerTurnContext(ctx, session, ref, turnID); contextSeq > session.Controller.ContextSyncSeq {
		turnReq.ContextPrelude = contextPrelude
		turnReq.ContextSyncSeq = contextSeq
	}
	turnResult, err := r.controllers.RunTurn(ctx, turnReq)
	if err != nil && isMissingACPControllerRun(err) {
		agent := firstNonEmpty(strings.TrimSpace(session.Controller.AgentName), strings.TrimSpace(session.Controller.ControllerID), strings.TrimSpace(session.Controller.Label))
		contextPrelude, contextSeq := r.buildControllerHandoffContext(ctx, session, ref, session.Controller, session.Controller.ContextSyncSeq, turnID)
		binding, activateErr := r.controllers.Activate(ctx, sdkcontroller.HandoffRequest{
			SessionRef:     ref,
			Session:        session,
			Agent:          agent,
			Source:         "controller_rehydrate",
			Reason:         "controller process rehydrate",
			ContextPrelude: contextPrelude,
			ContextSyncSeq: contextSeq,
		})
		if activateErr == nil {
			var bindErr error
			session, bindErr = r.sessions.BindController(ctx, sdksession.BindControllerRequest{SessionRef: ref, Binding: binding})
			if bindErr == nil {
				turnReq.Session = session
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
		r.setRunState(ref.SessionID, sdkruntime.RunState{
			Status:      interruptedOrFailedStatus(ctx, err),
			ActiveRunID: runID,
			LastError:   err.Error(),
			UpdatedAt:   r.now(),
		})
		handle.publishError(err)
		return
	}
	if turnResult.Handle != nil {
		handle.setCancelHook(turnResult.Handle.Cancel)
		defer turnResult.Handle.Close()
		accumulator := acpNarrativeAccumulator{}
		for event, seqErr := range turnResult.Handle.Events() {
			if seqErr != nil {
				stateErr := seqErr
				if replayErr := r.persistInterruptedACPAssistantReplay(context.WithoutCancel(ctx), session, ref, turnID, &accumulator, seqErr); replayErr != nil {
					stateErr = errors.Join(seqErr, replayErr)
				}
				r.setRunState(ref.SessionID, sdkruntime.RunState{
					Status:      interruptedOrFailedStatus(ctx, seqErr),
					ActiveRunID: runID,
					LastError:   stateErr.Error(),
					UpdatedAt:   r.now(),
				})
				handle.publishError(seqErr)
				return
			}
			normalized := normalizeEvent(session, turnID, event)
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
			if sdksession.IsCanonicalHistoryEvent(normalized) {
				persisted, appendErr := r.sessions.AppendEvent(ctx, sdksession.AppendEventRequest{
					SessionRef: ref,
					Event:      normalized,
				})
				if appendErr != nil {
					r.setRunState(ref.SessionID, sdkruntime.RunState{
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
				r.setRunState(ref.SessionID, sdkruntime.RunState{
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
			persisted, appendErr := r.sessions.AppendEvent(ctx, sdksession.AppendEventRequest{
				SessionRef: ref,
				Event:      finalEvent,
			})
			if appendErr != nil {
				r.setRunState(ref.SessionID, sdkruntime.RunState{
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
			r.setRunState(ref.SessionID, sdkruntime.RunState{
				Status:      interruptedOrFailedStatus(ctx, err),
				ActiveRunID: runID,
				LastError:   err.Error(),
				UpdatedAt:   r.now(),
			})
			handle.publishError(err)
			return
		}
	}
	r.setRunState(ref.SessionID, sdkruntime.RunState{
		Status:      sdkruntime.RunLifecycleStatusCompleted,
		ActiveRunID: runID,
		UpdatedAt:   r.now(),
	})
}

type acpNarrativeAccumulator struct {
	assistantText      string
	reasoningText      string
	lastNarrativeEvent *sdksession.Event
	lastAssistantEvent *sdksession.Event
}

func (a *acpNarrativeAccumulator) normalize(event *sdksession.Event) (*sdksession.Event, *sdksession.Event, bool) {
	if a == nil || !isACPControllerNarrativeChunk(event) {
		return event, nil, false
	}
	updateType := strings.TrimSpace(event.Protocol.UpdateType)
	raw := narrativeEventText(event, updateType)
	a.lastNarrativeEvent = sdksession.CloneEvent(event)
	if updateType == string(sdksession.ProtocolUpdateTypeAgentMessage) {
		a.lastAssistantEvent = sdksession.CloneEvent(event)
	}
	cumulative, delta := a.append(updateType, raw)
	if cumulative == "" && delta == "" {
		return nil, nil, true
	}
	if delta == "" {
		return nil, nil, true
	}
	live := sdksession.CloneEvent(event)
	live.ID = ""
	live.Visibility = sdksession.VisibilityUIOnly
	setNarrativeEventText(live, updateType, delta)
	return nil, live, true
}

func (a *acpNarrativeAccumulator) append(updateType string, text string) (string, string) {
	switch strings.TrimSpace(updateType) {
	case string(sdksession.ProtocolUpdateTypeAgentThought):
		cumulative, delta := appendNarrativeText(a.reasoningText, text)
		a.reasoningText = cumulative
		return cumulative, delta
	default:
		cumulative, delta := appendNarrativeText(a.assistantText, text)
		a.assistantText = cumulative
		return cumulative, delta
	}
}

func (a *acpNarrativeAccumulator) finalAssistantEvent() *sdksession.Event {
	if a == nil || strings.TrimSpace(a.assistantText) == "" || a.lastAssistantEvent == nil {
		return nil
	}
	event := sdksession.CloneEvent(a.lastAssistantEvent)
	event.ID = ""
	event.Visibility = sdksession.VisibilityCanonical
	event.Type = sdksession.EventTypeAssistant
	setNarrativeEventText(event, string(sdksession.ProtocolUpdateTypeAgentMessage), a.assistantText)
	return event
}

func (r *Runtime) persistInterruptedACPAssistantReplay(
	ctx context.Context,
	session sdksession.Session,
	ref sdksession.SessionRef,
	turnID string,
	accumulator *acpNarrativeAccumulator,
	cause error,
) error {
	if r == nil || r.sessions == nil || accumulator == nil {
		return nil
	}
	event := accumulator.interruptedAssistantReplayEvent(session, turnID, cause)
	if event == nil {
		return nil
	}
	_, err := r.sessions.AppendEvent(ctx, sdksession.AppendEventRequest{
		SessionRef: ref,
		Event:      event,
	})
	return err
}

func (a *acpNarrativeAccumulator) interruptedAssistantReplayEvent(
	session sdksession.Session,
	turnID string,
	cause error,
) *sdksession.Event {
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

func isACPControllerNarrativeChunk(event *sdksession.Event) bool {
	if event == nil || event.Protocol == nil || event.Scope == nil {
		return false
	}
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(event.Scope.Source)), "acp") {
		return false
	}
	switch strings.TrimSpace(event.Protocol.UpdateType) {
	case string(sdksession.ProtocolUpdateTypeAgentMessage), string(sdksession.ProtocolUpdateTypeAgentThought):
		return true
	default:
		return false
	}
}

func isACPControllerUserEcho(event *sdksession.Event) bool {
	if event == nil || event.Scope == nil {
		return false
	}
	if sdksession.EventTypeOf(event) != sdksession.EventTypeUser {
		return false
	}
	if event.Scope.Participant.ID != "" {
		return false
	}
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(event.Scope.Source)), "acp")
}

func isACPParticipantUserEcho(event *sdksession.Event) bool {
	if event == nil || event.Scope == nil {
		return false
	}
	if sdksession.EventTypeOf(event) != sdksession.EventTypeUser {
		return false
	}
	if strings.TrimSpace(event.Scope.Participant.ID) == "" {
		return false
	}
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(event.Scope.Source)), "acp")
}

func narrativeEventText(event *sdksession.Event, updateType string) string {
	if event == nil {
		return ""
	}
	if event.Message != nil {
		switch strings.TrimSpace(updateType) {
		case string(sdksession.ProtocolUpdateTypeAgentThought):
			if text := event.Message.ReasoningText(); text != "" {
				return text
			}
		default:
			if text := event.Message.TextContent(); text != "" {
				return text
			}
		}
	}
	return sdksession.EventText(event)
}

func setNarrativeEventText(event *sdksession.Event, updateType string, text string) {
	if event == nil {
		return
	}
	event.Text = text
	if event.Protocol != nil {
		protocol := sdksession.CloneEventProtocol(*event.Protocol)
		if protocol.Update == nil {
			protocol.Update = &sdksession.ProtocolUpdate{}
		}
		protocol.Update.SessionUpdate = strings.TrimSpace(updateType)
		protocol.Update.Content = sdksession.ProtocolTextContent(text)
		event.Protocol = &protocol
	}
	switch strings.TrimSpace(updateType) {
	case string(sdksession.ProtocolUpdateTypeAgentThought):
		message := sdkmodel.NewReasoningMessage(sdkmodel.RoleAssistant, text, sdkmodel.ReasoningVisibilityVisible)
		event.Message = &message
	default:
		message := sdkmodel.NewTextMessage(sdkmodel.RoleAssistant, text)
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

func (r *Runtime) handleControllerPlanEvent(ctx context.Context, ref sdksession.SessionRef, event *sdksession.Event) error {
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
			"explanation": strings.TrimSpace(sdksession.EventText(event)),
		}
		return state, nil
	})
}

func (r *Runtime) AttachACPParticipant(ctx context.Context, req sdkruntime.AttachACPParticipantRequest) (sdksession.Session, error) {
	if r == nil || r.controllers == nil {
		return sdksession.Session{}, fmt.Errorf("sdk/runtime/local: ACP controller backend is not configured")
	}
	ref := sdksession.NormalizeSessionRef(req.SessionRef)
	session, err := r.sessions.Session(ctx, ref)
	if err != nil {
		return sdksession.Session{}, err
	}
	session, err = r.ensureSessionController(ctx, session)
	if err != nil {
		return sdksession.Session{}, err
	}
	binding, err := r.controllers.Attach(ctx, sdkcontroller.AttachRequest{
		SessionRef: ref,
		Session:    session,
		Agent:      strings.TrimSpace(req.Agent),
		Role:       req.Role,
		Source:     strings.TrimSpace(req.Source),
		Label:      strings.TrimSpace(req.Label),
	})
	if err != nil {
		return sdksession.Session{}, err
	}
	session, err = r.sessions.PutParticipant(ctx, sdksession.PutParticipantRequest{
		SessionRef: ref,
		Binding:    binding,
	})
	if err != nil {
		return sdksession.Session{}, err
	}
	if _, err := r.sessions.AppendEvent(ctx, sdksession.AppendEventRequest{
		SessionRef: ref,
		Event:      participantLifecycleEvent(session, binding, "attached", r.now()),
	}); err != nil {
		return sdksession.Session{}, err
	}
	return r.sessions.Session(ctx, ref)
}

func (r *Runtime) DetachACPParticipant(ctx context.Context, req sdkruntime.DetachACPParticipantRequest) (sdksession.Session, error) {
	if r == nil || r.controllers == nil {
		return sdksession.Session{}, fmt.Errorf("sdk/runtime/local: ACP controller backend is not configured")
	}
	ref := sdksession.NormalizeSessionRef(req.SessionRef)
	session, err := r.sessions.Session(ctx, ref)
	if err != nil {
		return sdksession.Session{}, err
	}
	session, err = r.ensureSessionController(ctx, session)
	if err != nil {
		return sdksession.Session{}, err
	}
	binding, _ := participantBinding(session, req.ParticipantID)
	if err := r.controllers.Detach(ctx, sdkcontroller.DetachRequest{
		SessionRef:    ref,
		Session:       session,
		ParticipantID: strings.TrimSpace(req.ParticipantID),
		Source:        strings.TrimSpace(req.Source),
	}); err != nil {
		return sdksession.Session{}, err
	}
	session, err = r.sessions.RemoveParticipant(ctx, sdksession.RemoveParticipantRequest{
		SessionRef:    ref,
		ParticipantID: strings.TrimSpace(req.ParticipantID),
	})
	if err != nil {
		return sdksession.Session{}, err
	}
	if binding.ID != "" {
		if _, err := r.sessions.AppendEvent(ctx, sdksession.AppendEventRequest{
			SessionRef: ref,
			Event:      participantLifecycleEvent(session, binding, "detached", r.now()),
		}); err != nil {
			return sdksession.Session{}, err
		}
	}
	return r.sessions.Session(ctx, ref)
}

func (r *Runtime) PromptACPParticipant(ctx context.Context, req sdkruntime.PromptACPParticipantRequest) (sdkruntime.RunResult, error) {
	if r == nil || r.controllers == nil {
		return sdkruntime.RunResult{}, fmt.Errorf("sdk/runtime/local: ACP controller backend is not configured")
	}
	ref := sdksession.NormalizeSessionRef(req.SessionRef)
	session, err := r.sessions.Session(ctx, ref)
	if err != nil {
		return sdkruntime.RunResult{}, err
	}
	session, err = r.ensureSessionController(ctx, session)
	if err != nil {
		return sdkruntime.RunResult{}, err
	}
	binding, _ := participantBinding(session, strings.TrimSpace(req.ParticipantID))
	contextPrelude := r.buildParticipantPromptContext(ctx, session, ref, binding)
	turnID := r.nextID("participant-turn", nil)
	runID := r.nextID("participant-run", nil)
	runCtx, cancel := context.WithCancel(ctx)
	handle := newRunner(runID, cancel)
	go r.executeACPParticipantTurn(runCtx, session, ref, req, binding, contextPrelude, runID, turnID, handle)
	return sdkruntime.RunResult{
		Session: session,
		Handle:  handle,
	}, nil
}

func (r *Runtime) executeACPParticipantTurn(
	ctx context.Context,
	session sdksession.Session,
	ref sdksession.SessionRef,
	req sdkruntime.PromptACPParticipantRequest,
	binding sdksession.ParticipantBinding,
	contextPrelude string,
	runID string,
	turnID string,
	handle *runner,
) {
	defer handle.finish()
	participantID := strings.TrimSpace(req.ParticipantID)
	if userEvent := participantPromptUserEvent(session, binding, turnID, strings.TrimSpace(req.Source), req.Input, req.ContentParts, r.now()); userEvent != nil {
		persisted, err := r.sessions.AppendEvent(ctx, sdksession.AppendEventRequest{
			SessionRef: ref,
			Event:      userEvent,
		})
		if err != nil {
			handle.publishError(err)
			return
		}
		handle.publishEvent(persisted)
	}
	turnResult, err := r.controllers.PromptParticipant(ctx, sdkcontroller.ParticipantPromptRequest{
		SessionRef:        ref,
		Session:           session,
		TurnID:            turnID,
		ParticipantID:     participantID,
		Input:             strings.TrimSpace(req.Input),
		ContentParts:      req.ContentParts,
		ContextPrelude:    contextPrelude,
		Stream:            req.Stream,
		Mode:              r.policyMode(sdkruntime.AgentSpec{}),
		ApprovalRequester: controllerApprovalRequester{requester: req.ApprovalRequester, sessionRef: ref, session: session, runID: runID, turnID: turnID},
	})
	if err != nil {
		handle.publishError(err)
		return
	}
	if turnResult.Handle == nil {
		return
	}
	handle.setCancelHook(turnResult.Handle.Cancel)
	defer turnResult.Handle.Close()
	accumulator := acpNarrativeAccumulator{}
	for event, seqErr := range turnResult.Handle.Events() {
		if seqErr != nil {
			handle.publishError(seqErr)
			return
		}
		normalized := normalizeEvent(session, turnID, event)
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
		if sdksession.IsCanonicalHistoryEvent(normalized) {
			persisted, appendErr := r.sessions.AppendEvent(ctx, sdksession.AppendEventRequest{
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
		persisted, appendErr := r.sessions.AppendEvent(ctx, sdksession.AppendEventRequest{
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
	session sdksession.Session,
	binding sdksession.ParticipantBinding,
	turnID string,
	source string,
	input string,
	parts []sdkmodel.ContentPart,
	now time.Time,
) *sdksession.Event {
	if strings.TrimSpace(input) == "" && len(parts) == 0 {
		return nil
	}
	message := sdkmodel.MessageFromTextAndContentParts(sdkmodel.RoleUser, strings.TrimSpace(input), parts)
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
		kind = sdksession.ParticipantKindACP
	}
	role := binding.Role
	if role == "" {
		role = sdksession.ParticipantRoleSidecar
	}
	return &sdksession.Event{
		Type:       sdksession.EventTypeUser,
		Visibility: sdksession.VisibilityCanonical,
		Time:       now,
		Actor:      sdksession.ActorRef{Kind: sdksession.ActorKindUser, Name: "user"},
		Scope: &sdksession.EventScope{
			TurnID: strings.TrimSpace(turnID),
			Source: firstNonEmpty(strings.TrimSpace(source), "acp_participant"),
			Controller: sdksession.ControllerRef{
				Kind:    session.Controller.Kind,
				ID:      session.Controller.ControllerID,
				EpochID: session.Controller.EpochID,
			},
			Participant: sdksession.ParticipantRef{
				ID:   strings.TrimSpace(binding.ID),
				Kind: kind,
				Role: role,
			},
		},
		Message: &message,
		Text:    message.TextContent(),
		Protocol: &sdksession.EventProtocol{
			UpdateType: string(sdksession.ProtocolUpdateTypeUserMessage),
			Update: &sdksession.ProtocolUpdate{
				SessionUpdate: string(sdksession.ProtocolUpdateTypeUserMessage),
				Content:       sdksession.ProtocolTextContent(message.TextContent()),
			},
		},
		Meta: meta,
	}
}

func (r *Runtime) HandoffController(ctx context.Context, req sdkruntime.HandoffControllerRequest) (sdksession.Session, error) {
	ref := sdksession.NormalizeSessionRef(req.SessionRef)
	session, err := r.sessions.Session(ctx, ref)
	if err != nil {
		return sdksession.Session{}, err
	}
	session, err = r.ensureSessionController(ctx, session)
	if err != nil {
		return sdksession.Session{}, err
	}
	from := sdksession.CloneControllerBinding(session.Controller)
	kind := req.Kind
	if kind == "" {
		kind = sdksession.ControllerKindKernel
	}
	var to sdksession.ControllerBinding
	switch kind {
	case sdksession.ControllerKindACP:
		if r.controllers == nil {
			return sdksession.Session{}, fmt.Errorf("sdk/runtime/local: ACP controller backend is not configured")
		}
		sinceSeq := 0
		if from.Kind == sdksession.ControllerKindACP && sameControllerAgent(from, req.Agent) {
			sinceSeq = from.ContextSyncSeq
		}
		contextPrelude, contextSeq := r.buildControllerHandoffContext(ctx, session, ref, from, sinceSeq, "")
		to, err = r.controllers.Activate(ctx, sdkcontroller.HandoffRequest{
			SessionRef:     ref,
			Session:        session,
			Agent:          strings.TrimSpace(req.Agent),
			Source:         strings.TrimSpace(req.Source),
			Reason:         strings.TrimSpace(req.Reason),
			ContextPrelude: contextPrelude,
			ContextSyncSeq: contextSeq,
		})
		if err != nil {
			return sdksession.Session{}, err
		}
	default:
		if r.controllers != nil && from.Kind == sdksession.ControllerKindACP {
			if err := r.controllers.Deactivate(ctx, ref); err != nil {
				return sdksession.Session{}, err
			}
		}
		to = r.kernelControllerBinding(firstNonEmpty(strings.TrimSpace(req.Source), "handoff"))
	}

	session, err = r.sessions.BindController(ctx, sdksession.BindControllerRequest{
		SessionRef: ref,
		Binding:    to,
	})
	if err != nil {
		return sdksession.Session{}, err
	}
	if _, err := r.sessions.AppendEvent(ctx, sdksession.AppendEventRequest{
		SessionRef: ref,
		Event:      handoffEvent(from, to, strings.TrimSpace(req.Reason), r.now()),
	}); err != nil {
		return sdksession.Session{}, err
	}
	return r.sessions.Session(ctx, ref)
}

func (r *Runtime) buildControllerTurnContext(
	ctx context.Context,
	session sdksession.Session,
	ref sdksession.SessionRef,
	excludeTurnID string,
) (string, int) {
	binding := sdksession.CloneControllerBinding(session.Controller)
	if binding.Kind != sdksession.ControllerKindACP {
		return "", binding.ContextSyncSeq
	}
	contextPrelude, contextSeq := r.buildControllerHandoffContext(ctx, session, ref, binding, binding.ContextSyncSeq, excludeTurnID)
	if contextSeq <= binding.ContextSyncSeq {
		return "", binding.ContextSyncSeq
	}
	return contextPrelude, contextSeq
}

func (r *Runtime) buildControllerHandoffContext(
	ctx context.Context,
	session sdksession.Session,
	ref sdksession.SessionRef,
	from sdksession.ControllerBinding,
	sinceSeq int,
	excludeTurnID string,
) (string, int) {
	shared := r.buildSharedDialogueDeltaExcludingTurn(ctx, ref, sinceSeq, excludeTurnID)
	var b strings.Builder
	b.WriteString("Caelis controller handoff context. Continue the existing Caelis session; do not treat this as a fresh conversation.\n")
	b.WriteString("session_id: ")
	b.WriteString(strings.TrimSpace(session.SessionID))
	b.WriteString("\nworkspace: ")
	b.WriteString(strings.TrimSpace(session.CWD))
	b.WriteString("\nprevious_controller: ")
	b.WriteString(firstNonEmpty(strings.TrimSpace(from.AgentName), strings.TrimSpace(from.Label), strings.TrimSpace(from.ControllerID), string(from.Kind)))
	b.WriteString("\ncontext_sync_seq: ")
	fmt.Fprintf(&b, "%d", shared.Checkpoint)
	if len(session.Participants) > 0 {
		b.WriteString("\nchild_handles:")
		for _, participant := range session.Participants {
			if participant.Kind != sdksession.ParticipantKindSubagent || participant.Role != sdksession.ParticipantRoleDelegated {
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
	session sdksession.Session,
	ref sdksession.SessionRef,
	binding sdksession.ParticipantBinding,
) string {
	shared := r.buildSharedDialogueDelta(ctx, ref, binding.ContextSyncSeq)
	var b strings.Builder
	b.WriteString("Caelis shared public dialogue context. Use this as background for the current side-agent request; do not treat it as a fresh session.\n")
	if sessionID := strings.TrimSpace(session.SessionID); sessionID != "" {
		b.WriteString("session_id: ")
		b.WriteString(sessionID)
		b.WriteString("\n")
	}
	if cwd := strings.TrimSpace(session.CWD); cwd != "" {
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

func (r *Runtime) updateControllerContextCheckpoint(ctx context.Context, ref sdksession.SessionRef) error {
	if r == nil || r.sessions == nil {
		return nil
	}
	session, err := r.sessions.Session(ctx, ref)
	if err != nil {
		return err
	}
	binding := sdksession.CloneControllerBinding(session.Controller)
	binding.ContextSyncSeq = r.sharedDialogueCheckpoint(ctx, ref)
	_, err = r.sessions.BindController(ctx, sdksession.BindControllerRequest{
		SessionRef: ref,
		Binding:    binding,
	})
	return err
}

func (r *Runtime) updateParticipantContextCheckpoint(ctx context.Context, ref sdksession.SessionRef, participantID string) error {
	if r == nil || r.sessions == nil {
		return nil
	}
	participantID = strings.TrimSpace(participantID)
	if participantID == "" {
		return nil
	}
	session, err := r.sessions.Session(ctx, ref)
	if err != nil {
		return err
	}
	binding, ok := participantBinding(session, participantID)
	if !ok {
		return nil
	}
	binding.ContextSyncSeq = r.sharedDialogueCheckpoint(ctx, ref)
	_, err = r.sessions.PutParticipant(ctx, sdksession.PutParticipantRequest{
		SessionRef: ref,
		Binding:    binding,
	})
	return err
}

func (r *Runtime) sharedDialogueCheckpoint(ctx context.Context, ref sdksession.SessionRef) int {
	if r == nil || r.sessions == nil {
		return 0
	}
	events, err := r.sessions.Events(ctx, sdksession.EventsRequest{SessionRef: ref})
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

func (r *Runtime) buildSharedDialogueDelta(ctx context.Context, ref sdksession.SessionRef, sinceSeq int) sharedDialogueDelta {
	return r.buildSharedDialogueDeltaExcludingTurn(ctx, ref, sinceSeq, "")
}

func (r *Runtime) buildSharedDialogueDeltaExcludingTurn(ctx context.Context, ref sdksession.SessionRef, sinceSeq int, excludeTurnID string) sharedDialogueDelta {
	if r == nil || r.sessions == nil {
		return sharedDialogueDelta{}
	}
	events, err := r.sessions.Events(ctx, sdksession.EventsRequest{SessionRef: ref})
	if err != nil {
		return sharedDialogueDelta{}
	}
	return sharedDialogueDeltaFromEventsExcludingTurn(events, sinceSeq, excludeTurnID)
}

func sharedDialogueDeltaFromEvents(events []*sdksession.Event, sinceSeq int) sharedDialogueDelta {
	return sharedDialogueDeltaFromEventsExcludingTurn(events, sinceSeq, "")
}

func sharedDialogueDeltaFromEventsExcludingTurn(events []*sdksession.Event, sinceSeq int, excludeTurnID string) sharedDialogueDelta {
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

func isSharedDialogueDeltaEvent(event *sdksession.Event, latestCompactSeq int, seq int) bool {
	if event == nil || !sdksession.IsCanonicalHistoryEvent(event) {
		return false
	}
	if latestCompactSeq > 0 && seq == latestCompactSeq && sdkcompact.IsCompactEvent(event) {
		return true
	}
	return isSharedDialogueEvent(event)
}

func latestCompactEventSeq(events []*sdksession.Event) int {
	for i := len(events) - 1; i >= 0; i-- {
		if sdkcompact.IsCompactEvent(events[i]) {
			return i + 1
		}
	}
	return 0
}

func sharedDialogueCheckpoint(events []*sdksession.Event) int {
	return sharedDialogueCheckpointExcludingTurn(events, "")
}

func sharedDialogueCheckpointExcludingTurn(events []*sdksession.Event, excludeTurnID string) int {
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

func isSharedDialogueEvent(event *sdksession.Event) bool {
	if event == nil || !sdksession.IsCanonicalHistoryEvent(event) {
		return false
	}
	switch sdksession.EventTypeOf(event) {
	case sdksession.EventTypeUser, sdksession.EventTypeAssistant:
		return true
	default:
		return false
	}
}

func sharedDialogueRole(event *sdksession.Event) string {
	if event == nil {
		return ""
	}
	if sdkcompact.IsCompactEvent(event) {
		return "compact"
	}
	role := strings.TrimSpace(string(sdksession.EventTypeOf(event)))
	actor := strings.TrimSpace(event.Actor.Name)
	if actor == "" {
		actor = strings.TrimSpace(event.Actor.ID)
	}
	if actor == "" || strings.EqualFold(actor, role) {
		return role
	}
	return role + "(" + actor + ")"
}

func sharedDialogueText(event *sdksession.Event) string {
	if event == nil {
		return ""
	}
	return strings.TrimSpace(sdksession.EventText(event))
}

func sameControllerAgent(binding sdksession.ControllerBinding, agent string) bool {
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

func participantBinding(session sdksession.Session, participantID string) (sdksession.ParticipantBinding, bool) {
	participantID = strings.TrimSpace(participantID)
	for _, item := range session.Participants {
		if strings.TrimSpace(item.ID) == participantID {
			return sdksession.CloneParticipantBinding(item), true
		}
	}
	return sdksession.ParticipantBinding{}, false
}

func participantBindingLabel(binding sdksession.ParticipantBinding) string {
	return firstNonEmpty(strings.TrimSpace(binding.Label), strings.TrimSpace(binding.AgentName), strings.TrimSpace(binding.ID))
}

func participantLifecycleEvent(session sdksession.Session, binding sdksession.ParticipantBinding, action string, now time.Time) *sdksession.Event {
	text := strings.TrimSpace(action + " participant " + firstNonEmpty(binding.Label, binding.ID))
	return &sdksession.Event{
		Type:       sdksession.EventTypeParticipant,
		Visibility: sdksession.VisibilityCanonical,
		Time:       now,
		Actor:      sdksession.ActorRef{Kind: sdksession.ActorKindSystem, Name: "runtime"},
		Text:       text,
		Protocol: &sdksession.EventProtocol{
			Participant: &sdksession.ProtocolParticipant{Action: strings.TrimSpace(action)},
		},
		Scope: &sdksession.EventScope{
			Source: "control_plane",
			Controller: sdksession.ControllerRef{
				Kind:    session.Controller.Kind,
				ID:      session.Controller.ControllerID,
				EpochID: session.Controller.EpochID,
			},
			Participant: sdksession.ParticipantRef{
				ID:           binding.ID,
				Kind:         binding.Kind,
				Role:         binding.Role,
				DelegationID: binding.DelegationID,
			},
			ACP: sdksession.ACPRef{
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

func handoffEvent(from sdksession.ControllerBinding, to sdksession.ControllerBinding, reason string, now time.Time) *sdksession.Event {
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
	return &sdksession.Event{
		Type:       sdksession.EventTypeHandoff,
		Visibility: sdksession.VisibilityCanonical,
		Time:       now,
		Actor:      sdksession.ActorRef{Kind: sdksession.ActorKindSystem, Name: "runtime"},
		Text:       text,
		Protocol: &sdksession.EventProtocol{
			Handoff: &sdksession.ProtocolHandoff{Phase: "activation"},
		},
		Scope: &sdksession.EventScope{
			Source: "handoff",
			Controller: sdksession.ControllerRef{
				Kind:    to.Kind,
				ID:      strings.TrimSpace(to.ControllerID),
				EpochID: strings.TrimSpace(to.EpochID),
			},
		},
		Meta: meta,
	}
}

type controllerApprovalRequester struct {
	requester  sdkruntime.ApprovalRequester
	sessionRef sdksession.SessionRef
	session    sdksession.Session
	runID      string
	turnID     string
}

func (r controllerApprovalRequester) RequestControllerApproval(ctx context.Context, req sdkcontroller.ApprovalRequest) (sdkcontroller.ApprovalResponse, error) {
	if r.requester == nil {
		return sdkcontroller.ApprovalResponse{}, nil
	}
	options := make([]sdksession.ProtocolApprovalOption, 0, len(req.Options))
	for _, item := range req.Options {
		options = append(options, sdksession.ProtocolApprovalOption{
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
	resp, err := r.requester.RequestApproval(ctx, sdkruntime.ApprovalRequest{
		SessionRef: sdksession.NormalizeSessionRef(r.sessionRef),
		Session:    sdksession.CloneSession(r.session),
		RunID:      strings.TrimSpace(r.runID),
		TurnID:     strings.TrimSpace(r.turnID),
		Mode:       strings.TrimSpace(req.Mode),
		Tool: sdktool.Definition{
			Name:        toolName,
			Description: firstNonEmpty(req.ToolCall.Title, req.ToolCall.Kind, "ACP controller requested permission"),
		},
		Call: sdktool.Call{
			ID:    strings.TrimSpace(req.ToolCall.ID),
			Name:  toolName,
			Input: callInput,
		},
		Approval: &sdksession.ProtocolApproval{
			ToolCall: sdksession.ProtocolToolCall{
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
		return sdkcontroller.ApprovalResponse{}, err
	}
	return sdkcontroller.ApprovalResponse{
		Outcome:  strings.TrimSpace(resp.Outcome),
		OptionID: strings.TrimSpace(resp.OptionID),
		Approved: resp.Approved,
	}, nil
}
