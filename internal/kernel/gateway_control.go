package kernel

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/OnslaughtSnail/caelis/ports/agent"
	"github.com/OnslaughtSnail/caelis/ports/session"
)

func (g *Gateway) HandoffController(ctx context.Context, req HandoffControllerRequest) (session.Session, error) {
	if g.control == nil {
		return session.Session{}, &Error{
			Kind:        KindUnsupported,
			Code:        CodeControlPlaneUnsupported,
			UserVisible: true,
			Message:     "gateway: control plane is not available",
		}
	}
	ref, err := g.sessionTarget(req.SessionRef, req.BindingKey)
	if err != nil {
		return session.Session{}, err
	}
	activeSession, err := g.control.HandoffController(ctx, agent.HandoffControllerRequest{
		SessionRef: ref,
		Kind:       req.Kind,
		Agent:      strings.TrimSpace(req.Agent),
		Source:     strings.TrimSpace(req.Source),
		Reason:     strings.TrimSpace(req.Reason),
	})
	if err != nil {
		return session.Session{}, err
	}
	g.bind(req.BindingKey, activeSession.SessionRef, BindingDescriptor{})
	return activeSession, nil
}

func (g *Gateway) AttachParticipant(ctx context.Context, req AttachParticipantRequest) (session.Session, error) {
	if g.control == nil {
		return session.Session{}, &Error{
			Kind:        KindUnsupported,
			Code:        CodeControlPlaneUnsupported,
			UserVisible: true,
			Message:     "gateway: control plane is not available",
		}
	}
	ref, err := g.sessionTarget(req.SessionRef, req.BindingKey)
	if err != nil {
		return session.Session{}, err
	}
	activeSession, err := g.control.AttachACPParticipant(ctx, agent.AttachACPParticipantRequest{
		SessionRef: ref,
		Agent:      strings.TrimSpace(req.Agent),
		Role:       req.Role,
		Source:     strings.TrimSpace(req.Source),
		Label:      strings.TrimSpace(req.Label),
	})
	if err != nil {
		return session.Session{}, err
	}
	g.bind(req.BindingKey, activeSession.SessionRef, BindingDescriptor{})
	return activeSession, nil
}

func (g *Gateway) DetachParticipant(ctx context.Context, req DetachParticipantRequest) (session.Session, error) {
	if g.control == nil {
		return session.Session{}, &Error{
			Kind:        KindUnsupported,
			Code:        CodeControlPlaneUnsupported,
			UserVisible: true,
			Message:     "gateway: control plane is not available",
		}
	}
	ref, err := g.sessionTarget(req.SessionRef, req.BindingKey)
	if err != nil {
		return session.Session{}, err
	}
	activeSession, err := g.control.DetachACPParticipant(ctx, agent.DetachACPParticipantRequest{
		SessionRef:    ref,
		ParticipantID: strings.TrimSpace(req.ParticipantID),
		Source:        strings.TrimSpace(req.Source),
	})
	if err != nil {
		return session.Session{}, err
	}
	g.bind(req.BindingKey, activeSession.SessionRef, BindingDescriptor{})
	return activeSession, nil
}

func (g *Gateway) PromptParticipant(ctx context.Context, req PromptParticipantRequest) (BeginTurnResult, error) {
	if g.control == nil {
		return BeginTurnResult{}, &Error{
			Kind:        KindUnsupported,
			Code:        CodeControlPlaneUnsupported,
			UserVisible: true,
			Message:     "gateway: control plane is not available",
		}
	}
	ref, err := g.sessionTarget(req.SessionRef, req.BindingKey)
	if err != nil {
		return BeginTurnResult{}, err
	}
	session, err := g.sessions.Session(ctx, ref)
	if err != nil {
		return BeginTurnResult{}, wrapSessionError(err)
	}
	g.bind(req.BindingKey, session.SessionRef, BindingDescriptor{})
	runCtx, cancel := context.WithCancel(ctx)
	cancelFn := sync.OnceValue(func() bool {
		cancel()
		return true
	})
	g.mu.Lock()
	if _, ok := g.active[session.SessionID]; ok {
		g.mu.Unlock()
		return BeginTurnResult{}, &Error{
			Kind:        KindConflict,
			Code:        CodeActiveRunConflict,
			UserVisible: true,
			Message:     "gateway: session already has an active run",
		}
	}
	handle := newTurnHandle(turnHandleConfig{
		handleID:                g.allocateID("handle"),
		runID:                   g.allocateID("participant-run"),
		turnID:                  g.allocateID("participant-turn"),
		activeKind:              ActiveTurnKindParticipant,
		sessionRef:              session.SessionRef,
		createdAt:               g.clock(),
		allowPendingSubmissions: true,
		cancel: func() bool {
			return cancelFn()
		},
	})
	g.active[session.SessionID] = handle
	g.noteActiveHandleLocked(session.SessionID, handle)
	g.mu.Unlock()

	go g.runParticipantTurn(runCtx, session, req, handle)

	return BeginTurnResult{
		Session: session,
		Handle:  handle,
	}, nil
}

func (g *Gateway) ControlPlaneState(ctx context.Context, req ControlPlaneStateRequest) (ControlPlaneState, error) {
	ref, err := g.sessionTarget(req.SessionRef, req.BindingKey)
	if err != nil {
		return ControlPlaneState{}, err
	}
	activeSession, err := g.sessions.Session(ctx, ref)
	if err != nil {
		return ControlPlaneState{}, wrapSessionError(err)
	}
	events, err := g.sessions.Events(ctx, session.EventsRequest{
		SessionRef: ref,
	})
	if err != nil {
		return ControlPlaneState{}, wrapSessionError(err)
	}
	runState, err := g.runtime.RunState(ctx, ref)
	if err != nil && !errors.Is(err, session.ErrSessionNotFound) {
		return ControlPlaneState{}, err
	}
	return buildControlPlaneState(activeSession, runState, events), nil
}
