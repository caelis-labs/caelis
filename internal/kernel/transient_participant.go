package kernel

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/placement"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

type startParticipantRequest struct {
	SessionRef   session.SessionRef
	BindingKey   string
	Agent        string
	Role         session.ParticipantRole
	Label        string
	Placement    placement.Placement
	Input        string
	DisplayInput string
	DisplayTitle string
	ContentParts []model.ContentPart
	Source       string
}

func (g *Gateway) StartParticipant(ctx context.Context, req StartParticipantRequest) (BeginTurnResult, error) {
	if err := g.waitForTurnStart(ctx); err != nil {
		return BeginTurnResult{}, err
	}
	lifecycle, err := normalizeParticipantLifecycle(req.Lifecycle)
	if err != nil {
		return BeginTurnResult{}, err
	}
	result, attached, participantID, err := g.startParticipant(ctx, startParticipantRequest{
		SessionRef:   req.SessionRef,
		BindingKey:   req.BindingKey,
		Agent:        req.Agent,
		Role:         req.Role,
		Label:        req.Label,
		Placement:    req.Placement,
		Input:        req.Input,
		DisplayInput: req.DisplayInput,
		DisplayTitle: req.DisplayTitle,
		ContentParts: req.ContentParts,
		Source:       req.Source,
	})
	if err != nil {
		return BeginTurnResult{}, err
	}
	if lifecycle != ParticipantLifecycleTransient {
		return result, nil
	}
	return g.attachTransientDetach(ctx, req, result, attached, participantID)
}

func (g *Gateway) startParticipant(ctx context.Context, req startParticipantRequest) (BeginTurnResult, session.Session, string, error) {
	req.Role = defaultParticipantRole(req.Role)
	beforeParticipantIDs := g.participantIDsBeforeAttach(ctx, req.SessionRef, req.BindingKey)
	attached, err := g.AttachParticipant(ctx, AttachParticipantRequest{
		SessionRef: req.SessionRef,
		BindingKey: req.BindingKey,
		Agent:      req.Agent,
		Role:       req.Role,
		Source:     strings.TrimSpace(req.Source),
		Label:      strings.TrimSpace(req.Label),
		Placement:  req.Placement,
	})
	if err != nil {
		return BeginTurnResult{}, session.Session{}, "", err
	}
	participantID, err := participantIDForAttachedSession(attached, req.Agent, req.Label, req.Role)
	if err != nil {
		if rollbackID := participantRollbackID(attached, beforeParticipantIDs, req.Role); rollbackID != "" {
			if rollbackErr := g.detachTransientParticipant(ctx, attached.SessionRef, req.BindingKey, rollbackID, "side_agent_prompt_rollback"); rollbackErr != nil {
				return BeginTurnResult{}, session.Session{}, "", errors.Join(err, rollbackErr)
			}
		}
		return BeginTurnResult{}, session.Session{}, "", err
	}
	result, err := g.PromptParticipant(ctx, PromptParticipantRequest{
		SessionRef:    attached.SessionRef,
		BindingKey:    req.BindingKey,
		ParticipantID: participantID,
		Input:         req.Input,
		DisplayInput:  req.DisplayInput,
		DisplayTitle:  req.DisplayTitle,
		ContentParts:  req.ContentParts,
		Source:        strings.TrimSpace(req.Source),
	})
	if err != nil {
		if rollbackErr := g.detachTransientParticipant(ctx, attached.SessionRef, req.BindingKey, participantID, "side_agent_prompt_rollback"); rollbackErr != nil {
			return BeginTurnResult{}, session.Session{}, "", errors.Join(err, rollbackErr)
		}
		return BeginTurnResult{}, session.Session{}, "", err
	}
	if result.Session.SessionID == "" {
		result.Session = attached
	}
	return result, attached, participantID, nil
}

func (g *Gateway) attachTransientDetach(
	ctx context.Context,
	req StartParticipantRequest,
	result BeginTurnResult,
	attached session.Session,
	participantID string,
) (BeginTurnResult, error) {
	detachSource := strings.TrimSpace(req.DetachSource)
	if detachSource == "" {
		detachSource = "side_agent_complete"
	}
	detach := func(failed bool) error {
		source := detachSource
		if failed {
			source = "side_agent_prompt_failed"
		}
		return g.detachTransientParticipant(ctx, attached.SessionRef, req.BindingKey, participantID, source)
	}
	if result.Handle == nil {
		if err := detach(false); err != nil {
			return BeginTurnResult{}, err
		}
		return result, nil
	}
	handle, ok := result.Handle.(*turnHandle)
	if !ok {
		_ = result.Handle.Close()
		detachErr := detach(false)
		err := fmt.Errorf("gateway: transient participant handle %T does not support completion hooks", result.Handle)
		if detachErr != nil {
			return BeginTurnResult{}, errors.Join(err, detachErr)
		}
		return BeginTurnResult{}, err
	}
	handle.onFinish(func() {
		// Detach must not block finish forever: a stuck backend would otherwise
		// keep the event stream open indefinitely (P1-5 liveness residual).
		detachCtx, cancel := context.WithTimeout(context.Background(), transientParticipantDetachTimeout)
		defer cancel()
		if err := g.detachTransientParticipant(detachCtx, attached.SessionRef, req.BindingKey, participantID, func() string {
			if handle.didFail() {
				return "side_agent_prompt_failed"
			}
			return detachSource
		}()); err != nil {
			handle.publishError(fmt.Errorf("gateway: transient participant detach failed: %w", err))
		}
	})
	return result, nil
}

func normalizeParticipantLifecycle(lifecycle ParticipantLifecycle) (ParticipantLifecycle, error) {
	switch lifecycle {
	case "", ParticipantLifecyclePersistent:
		return ParticipantLifecyclePersistent, nil
	case ParticipantLifecycleTransient:
		return ParticipantLifecycleTransient, nil
	default:
		return "", fmt.Errorf("gateway: unsupported participant lifecycle %q", lifecycle)
	}
}

func defaultParticipantRole(role session.ParticipantRole) session.ParticipantRole {
	if role == "" {
		return session.ParticipantRoleSidecar
	}
	return role
}

const transientParticipantDetachTimeout = 5 * time.Second

func (g *Gateway) detachTransientParticipant(ctx context.Context, ref session.SessionRef, bindingKey string, participantID string, source string) error {
	participantID = strings.TrimSpace(participantID)
	if participantID == "" {
		return nil
	}
	// Honor caller deadlines (finish-hook timeout). Only synthesize a bound when
	// the caller did not provide one; never strip an existing deadline via
	// context.WithoutCancel, which would allow unbounded detach blocking.
	detachCtx := ctx
	cancel := func() {}
	if ctx == nil {
		detachCtx, cancel = context.WithTimeout(context.Background(), transientParticipantDetachTimeout)
	} else if _, ok := ctx.Deadline(); !ok {
		detachCtx, cancel = context.WithTimeout(context.WithoutCancel(ctx), transientParticipantDetachTimeout)
	}
	defer cancel()
	_, err := g.DetachParticipant(detachCtx, DetachParticipantRequest{
		SessionRef:    ref,
		BindingKey:    bindingKey,
		ParticipantID: participantID,
		Source:        strings.TrimSpace(source),
	})
	return err
}

func (g *Gateway) participantIDsBeforeAttach(ctx context.Context, ref session.SessionRef, bindingKey string) map[string]bool {
	if g == nil || g.sessions == nil {
		return nil
	}
	ref, err := g.sessionTarget(ref, bindingKey)
	if err != nil {
		return nil
	}
	activeSession, err := g.sessions.Session(ctx, ref)
	if err != nil {
		return nil
	}
	out := map[string]bool{}
	for _, participant := range activeSession.Participants {
		if id := strings.TrimSpace(participant.ID); id != "" {
			out[id] = true
		}
	}
	return out
}

func participantIDForAttachedSession(activeSession session.Session, agentName string, label string, role session.ParticipantRole) (string, error) {
	agentName = strings.TrimSpace(agentName)
	label = strings.TrimSpace(label)
	for i := len(activeSession.Participants) - 1; i >= 0; i-- {
		participant := activeSession.Participants[i]
		if role != "" && participant.Role != role {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(participant.AgentName), agentName) {
			continue
		}
		if label != "" && !strings.EqualFold(strings.TrimSpace(participant.Label), label) {
			continue
		}
		if id := strings.TrimSpace(participant.ID); id != "" {
			return id, nil
		}
	}
	return "", &Error{
		Kind:        KindInternal,
		Code:        "participant_missing_after_attach",
		UserVisible: false,
		Message:     "gateway: participant was not attached",
	}
}

func participantRollbackID(activeSession session.Session, before map[string]bool, role session.ParticipantRole) string {
	for i := len(activeSession.Participants) - 1; i >= 0; i-- {
		participant := activeSession.Participants[i]
		id := strings.TrimSpace(participant.ID)
		if id == "" || before[id] {
			continue
		}
		if role != "" && participant.Role != role {
			continue
		}
		return id
	}
	return ""
}
