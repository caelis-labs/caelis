package local

import (
	"context"
	"fmt"
	"strings"
	"time"

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
	turnResult, err := r.controllers.RunTurn(ctx, turnReq)
	if err != nil && isMissingACPControllerRun(err) {
		agent := firstNonEmpty(strings.TrimSpace(session.Controller.AgentName), strings.TrimSpace(session.Controller.ControllerID), strings.TrimSpace(session.Controller.Label))
		contextPrelude, contextSeq := r.buildControllerHandoffContext(ctx, session, ref, session.Controller)
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
		accumulator := acpNarrativeAccumulator{}
		for event, seqErr := range turnResult.Handle.Events() {
			if seqErr != nil {
				r.setRunState(ref.SessionID, sdkruntime.RunState{
					Status:      interruptedOrFailedStatus(ctx, seqErr),
					ActiveRunID: runID,
					LastError:   seqErr.Error(),
					UpdatedAt:   r.now(),
				})
				handle.publishError(seqErr)
				return
			}
			normalized := normalizeEvent(session, turnID, event)
			if normalized == nil {
				continue
			}
			publishEvent := normalized
			if persistedEvent, liveEvent, ok := accumulator.normalize(normalized); ok {
				if persistedEvent == nil {
					continue
				}
				normalized = persistedEvent
				if liveEvent != nil {
					publishEvent = liveEvent
				} else {
					publishEvent = nil
				}
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
	}
	r.setRunState(ref.SessionID, sdkruntime.RunState{
		Status:      sdkruntime.RunLifecycleStatusCompleted,
		ActiveRunID: runID,
		UpdatedAt:   r.now(),
	})
}

type acpNarrativeAccumulator struct {
	assistantText string
	reasoningText string
}

func (a *acpNarrativeAccumulator) normalize(event *sdksession.Event) (*sdksession.Event, *sdksession.Event, bool) {
	if a == nil || !isACPControllerNarrativeChunk(event) {
		return event, nil, false
	}
	updateType := strings.TrimSpace(event.Protocol.UpdateType)
	raw := narrativeEventText(event, updateType)
	cumulative, delta := a.append(updateType, raw)
	if cumulative == "" && delta == "" {
		return nil, nil, true
	}
	if delta == "" {
		return nil, nil, true
	}
	persisted := sdksession.CloneEvent(event)
	setNarrativeEventText(persisted, updateType, cumulative)
	live := sdksession.CloneEvent(event)
	live.ID = ""
	setNarrativeEventText(live, updateType, delta)
	return persisted, live, true
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

func isACPControllerNarrativeChunk(event *sdksession.Event) bool {
	if event == nil || event.Protocol == nil || event.Scope == nil {
		return false
	}
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(event.Scope.Source)), "acp") {
		return false
	}
	if event.Scope.Participant.ID != "" {
		return false
	}
	switch strings.TrimSpace(event.Protocol.UpdateType) {
	case string(sdksession.ProtocolUpdateTypeAgentMessage), string(sdksession.ProtocolUpdateTypeAgentThought):
		return true
	default:
		return false
	}
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
	return event.Text
}

func setNarrativeEventText(event *sdksession.Event, updateType string, text string) {
	if event == nil {
		return
	}
	event.Text = text
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
			"explanation": strings.TrimSpace(event.Text),
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

func (r *Runtime) PromptACPParticipant(ctx context.Context, req sdkruntime.PromptACPParticipantRequest) (sdksession.Session, error) {
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
	turnResult, err := r.controllers.PromptParticipant(ctx, sdkcontroller.ParticipantPromptRequest{
		SessionRef:    ref,
		Session:       session,
		ParticipantID: strings.TrimSpace(req.ParticipantID),
		Input:         strings.TrimSpace(req.Input),
		ContentParts:  req.ContentParts,
	})
	if err != nil {
		return sdksession.Session{}, err
	}
	if turnResult.Handle != nil {
		for event, seqErr := range turnResult.Handle.Events() {
			if seqErr != nil {
				return sdksession.Session{}, seqErr
			}
			normalized := normalizeEvent(session, strings.TrimSpace(req.ParticipantID), event)
			if normalized == nil {
				continue
			}
			if sdksession.IsCanonicalHistoryEvent(normalized) {
				if _, err := r.sessions.AppendEvent(ctx, sdksession.AppendEventRequest{
					SessionRef: ref,
					Event:      normalized,
				}); err != nil {
					return sdksession.Session{}, err
				}
			}
		}
	}
	return r.sessions.Session(ctx, ref)
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
		contextPrelude, contextSeq := r.buildControllerHandoffContext(ctx, session, ref, from)
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

func (r *Runtime) buildControllerHandoffContext(
	ctx context.Context,
	session sdksession.Session,
	ref sdksession.SessionRef,
	from sdksession.ControllerBinding,
) (string, int) {
	seq := from.ContextSyncSeq + 1
	var b strings.Builder
	b.WriteString("Caelis controller handoff context. Continue the existing Caelis session; do not treat this as a fresh conversation.\n")
	b.WriteString("session_id: ")
	b.WriteString(strings.TrimSpace(session.SessionID))
	b.WriteString("\nworkspace: ")
	b.WriteString(strings.TrimSpace(session.CWD))
	b.WriteString("\nprevious_controller: ")
	b.WriteString(firstNonEmpty(strings.TrimSpace(from.AgentName), strings.TrimSpace(from.Label), strings.TrimSpace(from.ControllerID), string(from.Kind)))
	b.WriteString("\ncontext_sync_seq: ")
	fmt.Fprintf(&b, "%d", seq)
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
			if taskID := strings.TrimSpace(participant.DelegationID); taskID != "" {
				b.WriteString(" task_id=")
				b.WriteString(taskID)
			}
		}
	}
	if r != nil && r.sessions != nil {
		if events, err := r.sessions.Events(ctx, sdksession.EventsRequest{SessionRef: ref}); err == nil {
			tail := canonicalTextTail(events, 24)
			if len(tail) > 0 {
				b.WriteString("\ncanonical_tail:")
				for _, line := range tail {
					b.WriteString("\n- ")
					b.WriteString(line)
				}
			}
		}
	}
	return b.String(), seq
}

func canonicalTextTail(events []*sdksession.Event, limit int) []string {
	if limit <= 0 || len(events) == 0 {
		return nil
	}
	out := make([]string, 0, limit)
	for i := len(events) - 1; i >= 0 && len(out) < limit; i-- {
		event := events[i]
		if event == nil || !sdksession.IsCanonicalHistoryEvent(event) {
			continue
		}
		role := strings.TrimSpace(string(event.Type))
		text := strings.TrimSpace(event.Text)
		if text == "" && event.Message != nil {
			text = strings.TrimSpace(event.Message.TextContent())
		}
		if text == "" {
			continue
		}
		text = strings.ReplaceAll(text, "\n", " ")
		if len(text) > 500 {
			text = text[:500]
		}
		out = append(out, role+": "+text)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
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
	resp, err := r.requester.RequestApproval(ctx, sdkruntime.ApprovalRequest{
		SessionRef: sdksession.NormalizeSessionRef(r.sessionRef),
		Session:    sdksession.CloneSession(r.session),
		RunID:      strings.TrimSpace(r.runID),
		TurnID:     strings.TrimSpace(r.turnID),
		Mode:       strings.TrimSpace(req.Mode),
		Tool: sdktool.Definition{
			Name:        firstNonEmpty(req.ToolCall.Name, req.ToolCall.Title, "ACP_TOOL"),
			Description: firstNonEmpty(req.ToolCall.Title, req.ToolCall.Kind, "ACP controller requested permission"),
		},
		Call: sdktool.Call{
			ID:   strings.TrimSpace(req.ToolCall.ID),
			Name: firstNonEmpty(req.ToolCall.Name, req.ToolCall.Title, "ACP_TOOL"),
		},
		Approval: &sdksession.ProtocolApproval{
			ToolCall: sdksession.ProtocolToolCall{
				ID:     strings.TrimSpace(req.ToolCall.ID),
				Kind:   strings.TrimSpace(req.ToolCall.Kind),
				Title:  strings.TrimSpace(req.ToolCall.Title),
				Status: strings.TrimSpace(req.ToolCall.Status),
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
