package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/controller"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/session/userdisplay"
)

const participantLifecycleConfirmTimeout = 2 * time.Second

type participantLifecycleConfirmation uint8

type participantPromptKey struct {
	sessionID     string
	participantID string
}

const (
	participantLifecycleUncertain participantLifecycleConfirmation = iota
	participantLifecycleConfirmed
	participantLifecycleNotApplied
)

func (r *Runtime) AttachParticipant(ctx context.Context, req agent.AttachParticipantRequest) (session.Session, error) {
	if r == nil || r.controllers == nil {
		return session.Session{}, fmt.Errorf("agent-sdk/runtime: ACP controller backend is not configured")
	}
	r.participantMu.Lock()
	defer r.participantMu.Unlock()
	ref := session.NormalizeSessionRef(req.SessionRef)
	activeSession, err := r.sessions.Session(ctx, ref)
	if err != nil {
		return session.Session{}, err
	}
	activeSession, err = r.ensureSessionControllerWithGuard(
		ctx,
		activeSession,
		session.ControlMutationGuardWithRuntimeLease(ctx, session.ControlMutationPurposeCoordinator),
	)
	if err != nil {
		return session.Session{}, err
	}
	lifecycle, ok := r.sessions.(session.ParticipantLifecycleService)
	if !ok {
		return session.Session{}, fmt.Errorf("agent-sdk/runtime: participant lifecycle store does not support atomic event persistence")
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
	lifecycleEvent := participantLifecycleEvent(activeSession, binding, "attached", r.now())
	mutationGuard := session.ControlMutationGuard(session.ControlMutationPurposeParticipant)
	updatedSession, persistedEvent, err := lifecycle.PutParticipantWithEvent(ctx, session.PutParticipantWithEventRequest{
		SessionRef:       ref,
		ExpectedRevision: &activeSession.Revision,
		MutationGuard:    mutationGuard,
		Binding:          binding,
		Event:            lifecycleEvent,
	})
	if err == nil || session.IsCommitted(err) {
		confirmed, outcome, confirmErr := r.confirmParticipantLifecycle(ctx, activeSession, binding, "attached", lifecycleEvent, updatedSession, persistedEvent)
		if confirmErr == nil {
			return confirmed, nil
		}
		if outcome == participantLifecycleNotApplied {
			compensateErr := r.controllers.Detach(context.WithoutCancel(ctx), controller.DetachRequest{
				SessionRef: ref, Session: activeSession,
				ParticipantID: strings.TrimSpace(binding.ID), DelegationID: strings.TrimSpace(binding.DelegationID),
				AttachmentGeneration: strings.TrimSpace(binding.AttachmentGeneration), Source: strings.TrimSpace(req.Source),
			})
			return session.Session{}, errors.Join(err, confirmErr, compensateErr)
		}
		if session.IsCommitted(err) {
			return session.Session{}, errors.Join(err, confirmErr)
		}
		return session.Session{}, confirmErr
	}
	if err != nil {
		_ = r.controllers.Detach(context.WithoutCancel(ctx), controller.DetachRequest{
			SessionRef: ref, Session: activeSession,
			ParticipantID: strings.TrimSpace(binding.ID), DelegationID: strings.TrimSpace(binding.DelegationID),
			AttachmentGeneration: strings.TrimSpace(binding.AttachmentGeneration), Source: strings.TrimSpace(req.Source),
		})
		return session.Session{}, err
	}
	return updatedSession, nil
}

func (r *Runtime) DetachParticipant(ctx context.Context, req agent.DetachParticipantRequest) (session.Session, error) {
	if r == nil || r.controllers == nil {
		return session.Session{}, fmt.Errorf("agent-sdk/runtime: ACP controller backend is not configured")
	}
	r.participantMu.Lock()
	defer r.participantMu.Unlock()
	ref := session.NormalizeSessionRef(req.SessionRef)
	activeSession, err := r.sessions.Session(ctx, ref)
	if err != nil {
		return session.Session{}, err
	}
	activeSession, err = r.ensureSessionControllerWithGuard(
		ctx,
		activeSession,
		session.ControlMutationGuardWithRuntimeLease(ctx, session.ControlMutationPurposeCoordinator),
	)
	if err != nil {
		return session.Session{}, err
	}
	lifecycle, ok := r.sessions.(session.ParticipantLifecycleService)
	if !ok {
		return session.Session{}, fmt.Errorf("agent-sdk/runtime: participant lifecycle store does not support atomic event persistence")
	}
	binding, _ := participantBinding(activeSession, req.ParticipantID)
	mutationGuard := session.ControlMutationGuard(session.ControlMutationPurposeParticipant)
	if err := r.controllers.Detach(ctx, controller.DetachRequest{
		SessionRef: ref, Session: activeSession,
		ParticipantID: strings.TrimSpace(req.ParticipantID), DelegationID: strings.TrimSpace(binding.DelegationID),
		AttachmentGeneration: strings.TrimSpace(binding.AttachmentGeneration), Source: strings.TrimSpace(req.Source),
	}); err != nil {
		return session.Session{}, err
	}
	if binding.ID != "" {
		lifecycleEvent := participantLifecycleEvent(activeSession, binding, "detached", r.now())
		updatedSession, persistedEvent, err := lifecycle.RemoveParticipantWithEvent(ctx, session.RemoveParticipantWithEventRequest{
			SessionRef:           ref,
			ExpectedRevision:     &activeSession.Revision,
			MutationGuard:        mutationGuard,
			ParticipantID:        strings.TrimSpace(req.ParticipantID),
			ExpectedDelegationID: stringPointer(binding.DelegationID),
			Event:                lifecycleEvent,
		})
		if err == nil || session.IsCommitted(err) {
			confirmed, outcome, confirmErr := r.confirmParticipantLifecycle(ctx, activeSession, binding, "detached", lifecycleEvent, updatedSession, persistedEvent)
			if confirmErr == nil {
				return confirmed, nil
			}
			if outcome == participantLifecycleNotApplied {
				latest, loadErr := r.sessions.Session(context.WithoutCancel(ctx), ref)
				if loadErr == nil {
					loadErr = r.restoreDetachedParticipant(context.WithoutCancel(ctx), ref, latest, binding, req.Source, mutationGuard)
				}
				return session.Session{}, errors.Join(err, confirmErr, loadErr)
			}
			if session.IsCommitted(err) {
				return session.Session{}, errors.Join(err, confirmErr)
			}
			return session.Session{}, confirmErr
		}
		if err != nil {
			latest, loadErr := r.sessions.Session(context.WithoutCancel(ctx), ref)
			latestBinding, stillOwned := participantBinding(latest, binding.ID)
			if loadErr == nil && (!stillOwned ||
				strings.TrimSpace(latestBinding.DelegationID) != strings.TrimSpace(binding.DelegationID) ||
				strings.TrimSpace(latestBinding.AttachmentGeneration) != strings.TrimSpace(binding.AttachmentGeneration)) {
				return session.Session{}, err
			}
			if loadErr != nil {
				return session.Session{}, errors.Join(err, loadErr)
			}
			restoreErr := r.restoreDetachedParticipant(context.WithoutCancel(ctx), ref, latest, binding, req.Source, mutationGuard)
			return session.Session{}, errors.Join(err, restoreErr)
		}
		activeSession = updatedSession
		return activeSession, nil
	}
	_, err = r.sessions.RemoveParticipant(ctx, session.RemoveParticipantRequest{
		SessionRef:       ref,
		ExpectedRevision: &activeSession.Revision,
		MutationGuard:    mutationGuard,
		ParticipantID:    strings.TrimSpace(req.ParticipantID),
	})
	if err != nil {
		return session.Session{}, err
	}
	return r.sessions.Session(ctx, ref)
}

func (r *Runtime) restoreDetachedParticipant(
	ctx context.Context,
	ref session.SessionRef,
	latest session.Session,
	previous session.ParticipantBinding,
	source string,
	mutationGuard session.MutationGuard,
) error {
	current, owned := participantBinding(latest, previous.ID)
	if !owned || strings.TrimSpace(current.DelegationID) != strings.TrimSpace(previous.DelegationID) ||
		strings.TrimSpace(current.AttachmentGeneration) != strings.TrimSpace(previous.AttachmentGeneration) {
		return nil
	}
	reattached, err := r.controllers.Attach(ctx, controller.AttachRequest{
		SessionRef: ref,
		Session:    latest,
		Binding:    previous,
		Agent:      acpParticipantAgentName(previous),
		Role:       previous.Role,
		Source:     strings.TrimSpace(source),
		Label:      previous.Label,
	})
	if err != nil {
		return err
	}
	reattached = normalizeRehydratedACPParticipantBinding(previous, reattached)
	if participantLifecycleJSONEqual(reattached, previous) {
		return nil
	}
	updated, persistErr := r.sessions.PutParticipant(ctx, session.PutParticipantRequest{
		SessionRef:           ref,
		ExpectedRevision:     &latest.Revision,
		MutationGuard:        mutationGuard,
		ExpectedDelegationID: stringPointer(previous.DelegationID),
		Binding:              reattached,
	})
	if persistErr == nil {
		if durable, ok := participantBinding(updated, reattached.ID); ok && participantLifecycleJSONEqual(durable, reattached) {
			return nil
		}
		persistErr = errors.New("agent-sdk/runtime: participant rollback binding result did not match the reattached endpoint")
	}
	confirmed, loadErr := r.sessions.Session(context.WithoutCancel(ctx), ref)
	if loadErr == nil {
		if durable, ok := participantBinding(confirmed, reattached.ID); ok && participantLifecycleJSONEqual(durable, reattached) {
			return nil
		}
	}
	detachErr := r.controllers.Detach(context.WithoutCancel(ctx), controller.DetachRequest{
		SessionRef: ref, Session: latest,
		ParticipantID: strings.TrimSpace(reattached.ID), DelegationID: strings.TrimSpace(reattached.DelegationID),
		AttachmentGeneration: strings.TrimSpace(reattached.AttachmentGeneration), Source: strings.TrimSpace(source),
	})
	return errors.Join(persistErr, loadErr, detachErr)
}

func (r *Runtime) confirmParticipantLifecycle(
	ctx context.Context,
	before session.Session,
	binding session.ParticipantBinding,
	action string,
	requestedEvent *session.Event,
	returned session.Session,
	returnedEvent *session.Event,
) (session.Session, participantLifecycleConfirmation, error) {
	if participantLifecycleResultMatches(before, binding, action, requestedEvent, returned, returnedEvent) {
		return session.CloneSession(returned), participantLifecycleConfirmed, nil
	}
	ref := session.NormalizeSessionRef(before.SessionRef)
	reloadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), participantLifecycleConfirmTimeout)
	defer cancel()
	durable, sessionErr := r.sessions.Session(reloadCtx, ref)
	events, eventErr := r.sessions.Events(reloadCtx, session.EventsRequest{SessionRef: ref})
	if sessionErr == nil && eventErr == nil && requestedEvent != nil {
		for _, candidate := range events {
			if candidate == nil || strings.TrimSpace(candidate.IdempotencyKey) != strings.TrimSpace(requestedEvent.IdempotencyKey) {
				continue
			}
			if participantLifecycleResultMatches(before, binding, action, requestedEvent, durable, candidate) {
				return session.CloneSession(durable), participantLifecycleConfirmed, nil
			}
		}
	}
	outcome := participantLifecycleUncertain
	if sessionErr == nil && participantLifecycleNotAppliedInSession(durable, binding, action) {
		outcome = participantLifecycleNotApplied
	}
	return session.CloneSession(durable), outcome, errors.Join(
		errors.New("agent-sdk/runtime: participant lifecycle outcome could not be confirmed"),
		sessionErr,
		eventErr,
	)
}

func participantLifecycleNotAppliedInSession(durable session.Session, binding session.ParticipantBinding, action string) bool {
	current, found := participantBinding(durable, binding.ID)
	switch strings.TrimSpace(action) {
	case "attached":
		return !found || !participantLifecycleJSONEqual(current, binding)
	case "detached":
		return found && participantLifecycleJSONEqual(current, binding)
	default:
		return false
	}
}

func participantLifecycleResultMatches(
	before session.Session,
	binding session.ParticipantBinding,
	action string,
	requestedEvent *session.Event,
	actual session.Session,
	actualEvent *session.Event,
) bool {
	if requestedEvent == nil || actualEvent == nil || strings.TrimSpace(actualEvent.ID) == "" || actualEvent.Seq == 0 {
		return false
	}
	expectedSession := session.CloneSession(before)
	switch strings.TrimSpace(action) {
	case "attached":
		session.PutParticipantBinding(&expectedSession, binding)
	case "detached":
		session.RemoveParticipantBinding(&expectedSession, binding.ID)
	default:
		return false
	}
	expectedSession.Revision = before.Revision + 1
	expectedSession.UpdatedAt = requestedEvent.Time
	if expectedSession.Title == "" {
		expectedSession.Title = strings.TrimSpace(requestedEvent.Text)
		if len(expectedSession.Title) > 80 {
			expectedSession.Title = expectedSession.Title[:80]
		}
	}
	if !participantLifecycleJSONEqual(session.CloneSession(actual), expectedSession) {
		return false
	}
	expectedEvent := session.CanonicalizeEvent(requestedEvent)
	if expectedEvent == nil {
		return false
	}
	expectedEvent.ID = strings.TrimSpace(actualEvent.ID)
	expectedEvent.SessionID = strings.TrimSpace(before.SessionID)
	expectedEvent.Seq = actualEvent.Seq
	if expectedEvent.Schema == 0 {
		expectedEvent.Schema = session.EventSchemaVersion
	}
	if expectedEvent.Visibility == "" {
		expectedEvent.Visibility = session.VisibilityCanonical
	}
	return actualEvent.Text == expectedEvent.Text && participantLifecycleJSONEqual(session.CloneEvent(actualEvent), expectedEvent)
}

func participantLifecycleJSONEqual(left, right any) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func (r *Runtime) PromptParticipant(ctx context.Context, req agent.PromptParticipantRequest) (agent.RunResult, error) {
	if r == nil || r.controllers == nil {
		return agent.RunResult{}, fmt.Errorf("agent-sdk/runtime: ACP controller backend is not configured")
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
	binding, ok := participantBinding(activeSession, strings.TrimSpace(req.ParticipantID))
	if !ok {
		return agent.RunResult{}, fmt.Errorf("agent-sdk/runtime: ACP participant %q not found", strings.TrimSpace(req.ParticipantID))
	}
	activeSession, binding, err = r.ensureACPParticipantRun(ctx, activeSession, ref, binding)
	if err != nil {
		return agent.RunResult{}, err
	}
	releasePrompt, claimed := r.tryClaimParticipantPrompt(ref, binding)
	if !claimed {
		return agent.RunResult{}, fmt.Errorf("agent-sdk/runtime: participant %q already has a prompt in progress", strings.TrimSpace(binding.ID))
	}
	contextPrelude, _, err := r.buildParticipantPromptContext(ctx, activeSession, ref, binding)
	if err != nil {
		releasePrompt()
		return agent.RunResult{}, err
	}
	turnID := r.nextID("participant-turn", nil)
	runID := r.nextID("participant-run", nil)
	runCtx, cancel := context.WithCancel(ctx)
	handle := newRunner(runID, cancel)
	go r.executeACPParticipantTurn(runCtx, activeSession, ref, req, binding, contextPrelude, runID, turnID, handle, releasePrompt)
	return agent.RunResult{
		Session: activeSession,
		Handle:  handle,
	}, nil
}

func (r *Runtime) executeACPParticipantTurn(
	ctx context.Context,
	activeSession session.Session,
	ref session.SessionRef,
	req agent.PromptParticipantRequest,
	binding session.ParticipantBinding,
	contextPrelude string,
	runID string,
	turnID string,
	handle *runner,
	releasePrompt func(),
) {
	defer handle.finish()
	defer releasePrompt()
	participantID := strings.TrimSpace(req.ParticipantID)
	if userEvent := participantPromptUserEvent(activeSession, binding, turnID, strings.TrimSpace(req.Source), req.Input, req.DisplayInput, req.DisplayTitle, req.ContentParts, r.now()); userEvent != nil {
		persisted, err := r.sessions.AppendEvent(ctx, session.AppendEventRequest{
			SessionRef:    ref,
			MutationGuard: session.RuntimeMutationGuard(ctx),
			Event:         userEvent,
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
		DisplayInput:   strings.TrimSpace(req.DisplayInput),
		DisplayTitle:   strings.TrimSpace(req.DisplayTitle),
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
	var toolFactOrdinal uint64
	handle.setCancelHook(func() error {
		return turnResult.Handle.Cancel().Err
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
		IsUserEcho: isACPParticipantUserEcho,
	}); err != nil {
		handle.publishError(err)
		return
	}
	if err := r.updateParticipantContextCheckpoint(ctx, ref, participantID); err != nil {
		handle.publishError(err)
		return
	}
}

func (r *Runtime) tryClaimParticipantPrompt(ref session.SessionRef, binding session.ParticipantBinding) (func(), bool) {
	if r == nil {
		return func() {}, false
	}
	key := participantPromptKey{
		sessionID:     strings.TrimSpace(ref.SessionID),
		participantID: strings.TrimSpace(binding.ID),
	}
	if key.sessionID == "" || key.participantID == "" {
		return func() {}, false
	}
	r.participantMu.Lock()
	if r.participantPromptClaims == nil {
		r.participantPromptClaims = map[participantPromptKey]struct{}{}
	}
	if _, exists := r.participantPromptClaims[key]; exists {
		r.participantMu.Unlock()
		return func() {}, false
	}
	r.participantPromptClaims[key] = struct{}{}
	r.participantMu.Unlock()

	return func() {
		r.participantMu.Lock()
		delete(r.participantPromptClaims, key)
		r.participantMu.Unlock()
	}, true
}

func (r *Runtime) ensureACPParticipantRun(
	ctx context.Context,
	activeSession session.Session,
	ref session.SessionRef,
	binding session.ParticipantBinding,
) (session.Session, session.ParticipantBinding, error) {
	binding = session.CloneParticipantBinding(binding)
	if binding.Kind == "" {
		binding.Kind = session.ParticipantKindACP
	}
	if binding.Kind != session.ParticipantKindACP {
		return session.Session{}, session.ParticipantBinding{}, fmt.Errorf("agent-sdk/runtime: participant %q is not ACP-backed", strings.TrimSpace(binding.ID))
	}
	agentName := acpParticipantAgentName(binding)
	if agentName == "" {
		return session.Session{}, session.ParticipantBinding{}, fmt.Errorf("agent-sdk/runtime: ACP participant %q has no agent name", strings.TrimSpace(binding.ID))
	}
	attached, err := r.controllers.Attach(ctx, controller.AttachRequest{
		SessionRef: ref,
		Session:    activeSession,
		Binding:    binding,
		Agent:      agentName,
		Role:       binding.Role,
		Label:      binding.Label,
	})
	if err != nil {
		return session.Session{}, session.ParticipantBinding{}, err
	}
	attached = normalizeRehydratedACPParticipantBinding(binding, attached)
	if attached == binding {
		return activeSession, attached, nil
	}
	updated, err := r.sessions.PutParticipant(ctx, session.PutParticipantRequest{
		SessionRef:           ref,
		ExpectedRevision:     &activeSession.Revision,
		MutationGuard:        session.RuntimeMutationGuard(ctx),
		ExpectedDelegationID: stringPointer(binding.DelegationID),
		Binding:              attached,
	})
	if err == nil {
		if durable, ok := participantBinding(updated, attached.ID); ok && participantLifecycleJSONEqual(durable, attached) {
			return updated, attached, nil
		}
		err = errors.New("agent-sdk/runtime: participant rehydrate binding result did not match the attached endpoint")
	}
	confirmed, loadErr := r.sessions.Session(context.WithoutCancel(ctx), ref)
	if loadErr == nil {
		if durable, ok := participantBinding(confirmed, attached.ID); ok && participantLifecycleJSONEqual(durable, attached) {
			return confirmed, attached, nil
		}
	}
	detachErr := r.controllers.Detach(context.WithoutCancel(ctx), controller.DetachRequest{
		SessionRef: ref, Session: activeSession,
		ParticipantID: strings.TrimSpace(attached.ID), DelegationID: strings.TrimSpace(attached.DelegationID),
		AttachmentGeneration: strings.TrimSpace(attached.AttachmentGeneration), Source: strings.TrimSpace(attached.Source),
	})
	return session.Session{}, session.ParticipantBinding{}, errors.Join(err, loadErr, detachErr)
}

func acpParticipantAgentName(binding session.ParticipantBinding) string {
	if agentName := strings.TrimSpace(binding.AgentName); agentName != "" {
		return agentName
	}
	return strings.TrimPrefix(strings.TrimSpace(binding.Label), "@")
}

func normalizeRehydratedACPParticipantBinding(original session.ParticipantBinding, attached session.ParticipantBinding) session.ParticipantBinding {
	out := session.CloneParticipantBinding(attached)
	if out.ID == "" {
		out.ID = strings.TrimSpace(original.ID)
	}
	if out.Kind == "" {
		out.Kind = session.ParticipantKindACP
	}
	if out.Role == "" {
		out.Role = original.Role
	}
	if out.Role == "" {
		out.Role = session.ParticipantRoleSidecar
	}
	if out.AgentName == "" {
		out.AgentName = acpParticipantAgentName(original)
	}
	if out.Label == "" {
		out.Label = firstNonEmpty(strings.TrimSpace(original.Label), strings.TrimSpace(out.AgentName), strings.TrimSpace(out.ID))
	}
	if out.Source == "" {
		out.Source = strings.TrimSpace(original.Source)
	}
	if out.ParentTurnID == "" {
		out.ParentTurnID = strings.TrimSpace(original.ParentTurnID)
	}
	if out.DelegationID == "" {
		out.DelegationID = strings.TrimSpace(original.DelegationID)
	}
	if out.AttachedAt.IsZero() {
		out.AttachedAt = original.AttachedAt
	}
	if out.ControllerRef == "" {
		out.ControllerRef = strings.TrimSpace(original.ControllerRef)
	}
	return out
}

func participantPromptUserEvent(
	activeSession session.Session,
	binding session.ParticipantBinding,
	turnID string,
	source string,
	input string,
	displayInput string,
	displayTitle string,
	parts []model.ContentPart,
	now time.Time,
) *session.Event {
	if strings.TrimSpace(input) == "" && len(parts) == 0 {
		return nil
	}
	label := participantBindingLabel(binding)
	meta := map[string]any{}
	if label != "" {
		meta["mention"] = label
		meta["handle"] = strings.TrimPrefix(label, "@")
	}
	if agent := strings.TrimSpace(binding.AgentName); agent != "" {
		meta["agent"] = agent
	}
	if displayTitle := strings.TrimSpace(displayTitle); displayTitle != "" {
		meta["display_title"] = displayTitle
	}
	message, displayText, meta := userdisplay.Resolve(input, displayInput, parts, meta)
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
		Text:    displayText,
		Protocol: &session.EventProtocol{
			Update: &session.ProtocolUpdate{
				SessionUpdate: string(session.ProtocolUpdateTypeUserMessage),
				Content:       session.ProtocolTextContent(displayText),
			},
		},
		Meta: meta,
	}
}
