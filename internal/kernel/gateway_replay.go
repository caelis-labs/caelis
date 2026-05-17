package kernel

import (
	"context"
	"errors"

	"github.com/OnslaughtSnail/caelis/ports/session"
)

func (g *Gateway) ReplayEvents(ctx context.Context, req ReplayEventsRequest) (ReplayEventsResult, error) {
	ref, err := g.sessionTarget(req.SessionRef, req.BindingKey)
	if err != nil {
		return ReplayEventsResult{}, err
	}
	activeSession, err := g.sessions.Session(ctx, ref)
	if err != nil {
		return ReplayEventsResult{}, wrapSessionError(err)
	}
	events, err := g.sessions.Events(ctx, session.EventsRequest{
		SessionRef:       ref,
		Limit:            0,
		IncludeTransient: true,
	})
	if err != nil {
		return ReplayEventsResult{}, wrapSessionError(err)
	}
	if err := validateReplaySessionEvents(events); err != nil {
		return ReplayEventsResult{}, err
	}
	controlEvents := replayControlPlaneEvents(events, req.IncludeTransient)
	runState, err := g.runtime.RunState(ctx, ref)
	if err != nil && !errors.Is(err, session.ErrSessionNotFound) {
		return ReplayEventsResult{}, err
	}
	hasLiveHandle := g.hasActiveHandle(ref.SessionID)
	replayEvents := replayTranscriptEvents(events, req.IncludeTransient)
	projected := projectSessionEvents(ref, replayEvents)
	projected, err = replayAfterCursor(projected, req.Cursor, req.Limit)
	if err != nil {
		return ReplayEventsResult{}, err
	}
	out := ReplayEventsResult{
		SessionRef:    ref,
		Events:        projected,
		NextCursor:    lastCursor(projected),
		Durable:       true,
		HasLiveHandle: hasLiveHandle,
		ControlPlane:  buildControlPlaneState(activeSession, runState, controlEvents),
	}
	return out, nil
}
