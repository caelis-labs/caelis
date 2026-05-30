// Package control coordinates external participants against canonical sessions.
package control

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/core/model"
	"github.com/OnslaughtSnail/caelis/core/session"
)

type AgentSession interface {
	Initialize(context.Context) error
	NewSession(context.Context, session.Workspace) (string, error)
	Prompt(context.Context, string, []model.ContentPart) ([]session.Event, error)
	Close() error
}

type ParticipantRunner struct {
	Store session.Store
	Now   func() time.Time
}

type ParticipantRequest struct {
	SessionRef   session.Ref
	Workspace    session.Workspace
	Participant  session.ParticipantBinding
	Input        string
	ContentParts []model.ContentPart
	Agent        AgentSession
}

type ParticipantResult struct {
	RemoteSessionID string
	Events          []session.Event
	Cursor          session.Cursor
}

func (r ParticipantRunner) Invoke(ctx context.Context, req ParticipantRequest) (ParticipantResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if r.Store == nil {
		return ParticipantResult{}, errors.New("engine/control: session store is required")
	}
	if req.Agent == nil {
		return ParticipantResult{}, errors.New("engine/control: agent session is required")
	}
	ref := session.NormalizeRef(req.SessionRef)
	if strings.TrimSpace(ref.SessionID) == "" {
		return ParticipantResult{}, errors.New("engine/control: session id is required")
	}
	snapshot, err := r.Store.Load(ctx, ref)
	if err != nil {
		return ParticipantResult{}, err
	}
	workspace := req.Workspace
	if strings.TrimSpace(workspace.Key) == "" && strings.TrimSpace(workspace.CWD) == "" {
		workspace = snapshot.Session.Workspace
	}
	if err := req.Agent.Initialize(ctx); err != nil {
		return ParticipantResult{}, err
	}
	remoteSessionID := strings.TrimSpace(req.Participant.SessionID)
	if remoteSessionID == "" {
		remoteSessionID, err = req.Agent.NewSession(ctx, workspace)
		if err != nil {
			return ParticipantResult{}, err
		}
	}
	parts := model.CloneContentParts(req.ContentParts)
	if len(parts) == 0 && strings.TrimSpace(req.Input) != "" {
		parts = []model.ContentPart{{Type: model.ContentPartText, Text: req.Input}}
	}
	events, err := req.Agent.Prompt(ctx, remoteSessionID, parts)
	if err != nil {
		return ParticipantResult{}, err
	}
	events = normalizeParticipantEvents(snapshot.Session.Ref.SessionID, remoteSessionID, req.Participant, events, r.now())
	cursor, err := r.Store.Append(ctx, snapshot.Session.Ref, events)
	if err != nil {
		return ParticipantResult{}, err
	}
	return ParticipantResult{
		RemoteSessionID: remoteSessionID,
		Events:          events,
		Cursor:          cursor,
	}, nil
}

func (r ParticipantRunner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now().UTC()
}

func normalizeParticipantEvents(sessionID string, remoteSessionID string, participant session.ParticipantBinding, events []session.Event, now time.Time) []session.Event {
	if len(events) == 0 {
		return nil
	}
	participant.SessionID = strings.TrimSpace(remoteSessionID)
	if strings.TrimSpace(participant.ID) == "" {
		participant.ID = firstNonEmpty(participant.AgentName, remoteSessionID, "external-acp")
	}
	if participant.Kind == "" {
		participant.Kind = session.ParticipantACP
	}
	if participant.Role == "" {
		participant.Role = session.ParticipantDelegated
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
		if next.Actor.Kind == "" {
			next.Actor = session.ActorRef{
				Kind: session.ActorParticipant,
				ID:   participant.ID,
				Name: firstNonEmpty(participant.Label, participant.AgentName, participant.ID),
			}
		}
		if next.Scope == nil {
			next.Scope = &session.EventScope{}
		}
		next.Scope.Source = firstNonEmpty(next.Scope.Source, "external_acp")
		next.Scope.Participant = participant
		if next.Scope.ACP.SessionID == "" {
			next.Scope.ACP.SessionID = strings.TrimSpace(remoteSessionID)
		}
		out = append(out, next)
	}
	return out
}

// NormalizeParticipantEvents projects remote participant events into the parent
// canonical session without recording them.
func NormalizeParticipantEvents(sessionID string, remoteSessionID string, participant session.ParticipantBinding, events []session.Event, now time.Time) []session.Event {
	return normalizeParticipantEvents(sessionID, remoteSessionID, participant, events, now)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
