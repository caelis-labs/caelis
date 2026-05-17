package local

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/ports/agent"
	"github.com/OnslaughtSnail/caelis/ports/controller"
	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
)

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
		SessionRef:     ref,
		Session:        activeSession,
		TurnID:         turnID,
		ParticipantID:  participantID,
		Input:          strings.TrimSpace(req.Input),
		ContentParts:   req.ContentParts,
		ContextPrelude: contextPrelude,
		Stream:         req.Stream,
		Mode:           r.policyMode(agent.AgentSpec{}),
		ApprovalRequester: controllerApprovalRequester{
			requester:            req.ApprovalRequester,
			sessionRef:           ref,
			session:              activeSession,
			runID:                runID,
			turnID:               turnID,
			participantID:        strings.TrimSpace(binding.ID),
			participantKind:      strings.TrimSpace(string(binding.Kind)),
			participantSessionID: strings.TrimSpace(binding.SessionID),
		},
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
		if shouldPersistExternalACPEvent(normalized) {
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
