package control

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/core/model"
	"github.com/OnslaughtSnail/caelis/core/session"
)

type ControllerRunner struct {
	Store session.Store
	Now   func() time.Time
}

type ControllerRequest struct {
	SessionRef   session.Ref
	Workspace    session.Workspace
	Controller   session.ControllerBinding
	Input        string
	ContentParts []model.ContentPart
	Agent        AgentSession
}

type ControllerResult struct {
	RemoteSessionID string
	Events          []session.Event
	Cursor          session.Cursor
}

func (r ControllerRunner) Invoke(ctx context.Context, req ControllerRequest) (ControllerResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if r.Store == nil {
		return ControllerResult{}, errors.New("engine/control: session store is required")
	}
	if req.Agent == nil {
		return ControllerResult{}, errors.New("engine/control: agent session is required")
	}
	ref := session.NormalizeRef(req.SessionRef)
	if strings.TrimSpace(ref.SessionID) == "" {
		return ControllerResult{}, errors.New("engine/control: session id is required")
	}
	snapshot, err := r.Store.Load(ctx, ref)
	if err != nil {
		return ControllerResult{}, err
	}
	workspace := req.Workspace
	if strings.TrimSpace(workspace.Key) == "" && strings.TrimSpace(workspace.CWD) == "" {
		workspace = snapshot.Session.Workspace
	}
	if err := req.Agent.Initialize(ctx); err != nil {
		return ControllerResult{}, err
	}
	remoteSessionID := strings.TrimSpace(req.Controller.RemoteSessionID)
	if remoteSessionID == "" {
		remoteSessionID, err = req.Agent.NewSession(ctx, workspace)
		if err != nil {
			return ControllerResult{}, err
		}
	}
	parts := model.CloneContentParts(req.ContentParts)
	if len(parts) == 0 && strings.TrimSpace(req.Input) != "" {
		parts = []model.ContentPart{{Type: model.ContentPartText, Text: req.Input}}
	}
	events, err := req.Agent.Prompt(ctx, remoteSessionID, parts)
	if err != nil {
		return ControllerResult{}, err
	}
	events = normalizeControllerEvents(snapshot.Session.Ref.SessionID, remoteSessionID, req.Controller, events, r.now())
	cursor, err := r.Store.Append(ctx, snapshot.Session.Ref, events)
	if err != nil {
		return ControllerResult{}, err
	}
	return ControllerResult{
		RemoteSessionID: remoteSessionID,
		Events:          events,
		Cursor:          cursor,
	}, nil
}

func (r ControllerRunner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now().UTC()
}

func normalizeControllerEvents(sessionID string, remoteSessionID string, controller session.ControllerBinding, events []session.Event, now time.Time) []session.Event {
	if len(events) == 0 {
		return nil
	}
	controller.RemoteSessionID = strings.TrimSpace(remoteSessionID)
	if strings.TrimSpace(controller.ID) == "" {
		controller.ID = firstNonEmpty(controller.AgentName, remoteSessionID, "external-acp")
	}
	if controller.Kind == "" {
		controller.Kind = session.ControllerACP
	}
	out := make([]session.Event, 0, len(events))
	for _, event := range events {
		next := session.CloneEvent(event)
		next.SessionID = strings.TrimSpace(sessionID)
		if next.Visibility == "" {
			next.Visibility = session.VisibilityCanonical
		}
		if next.Time.IsZero() {
			next.Time = now
		}
		if next.Actor.Kind == "" || next.Actor.Kind == session.ActorParticipant {
			next.Actor = session.ActorRef{
				Kind: session.ActorController,
				ID:   controller.ID,
				Name: firstNonEmpty(controller.Label, controller.AgentName, controller.ID),
			}
		}
		if next.Scope == nil {
			next.Scope = &session.EventScope{}
		}
		next.Scope.Source = firstNonEmpty(next.Scope.Source, "external_acp_controller")
		next.Scope.Controller = controller
		next.Scope.Participant = session.ParticipantBinding{}
		if next.Scope.ACP.SessionID == "" {
			next.Scope.ACP.SessionID = strings.TrimSpace(remoteSessionID)
		}
		out = append(out, next)
	}
	return out
}
