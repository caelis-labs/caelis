package kernel

import (
	"context"
	"errors"
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/protocol/acp/eventstream"
	"github.com/OnslaughtSnail/caelis/protocol/acp/metautil"
	acpprojector "github.com/OnslaughtSnail/caelis/protocol/acp/projector"
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
	cursorEvents, err := sessionEventsAfterCursor(events, req.Cursor)
	if err != nil {
		return ReplayEventsResult{}, err
	}
	replayEvents := replayTranscriptEvents(cursorEvents, req.IncludeTransient)
	if req.Limit > 0 && len(replayEvents) > req.Limit {
		replayEvents = replayEvents[:req.Limit]
	}
	projected := projectSessionACPEvents(ref, replayEvents)
	out := ReplayEventsResult{
		SessionRef:    ref,
		Events:        projected,
		NextCursor:    lastACPEventCursor(projected),
		Durable:       true,
		HasLiveHandle: hasLiveHandle,
		ControlPlane:  buildControlPlaneState(activeSession, runState, controlEvents),
	}
	return out, nil
}

func projectSessionACPEvents(ref session.SessionRef, events []*session.Event) []eventstream.Envelope {
	if len(events) == 0 {
		return nil
	}
	out := make([]eventstream.Envelope, 0, len(events))
	for _, event := range events {
		if event == nil {
			continue
		}
		out = append(out, projectSessionACPEvent(ref, event, "", "", "")...)
	}
	return out
}

func projectSessionACPEvent(ref session.SessionRef, event *session.Event, handleID string, runID string, turnID string) []eventstream.Envelope {
	base := sessionACPEventBase(ref, event)
	base.HandleID = strings.TrimSpace(handleID)
	base.RunID = strings.TrimSpace(runID)
	base.TurnID = firstNonEmpty(base.TurnID, strings.TrimSpace(turnID))
	out := acpprojector.ProjectSessionEventEnvelope(base, event)
	if usage := usageSnapshotFromSessionEvent(event); usage != nil {
		out = append(out, sessionACPUsageEnvelope(base, usage))
	}
	return out
}

func sessionACPEventBase(ref session.SessionRef, event *session.Event) eventstream.Envelope {
	base := eventstream.Envelope{
		Cursor:     strings.TrimSpace(event.ID),
		SessionID:  strings.TrimSpace(ref.SessionID),
		TurnID:     turnIDFromSessionEvent(event),
		OccurredAt: event.Time,
		Final:      sessionACPEventFinal(event),
		Meta:       sessionACPEventMeta(event),
	}
	if origin := canonicalOriginFromSessionEvent(ref, event); origin != nil {
		base.Scope = eventstream.Scope(origin.Scope)
		base.ScopeID = strings.TrimSpace(origin.ScopeID)
		base.Actor = strings.TrimSpace(origin.Actor)
		base.ParticipantID = strings.TrimSpace(origin.ParticipantID)
	}
	return base
}

func sessionACPUsageEnvelope(base eventstream.Envelope, usage *UsageSnapshot) eventstream.Envelope {
	if usage == nil {
		return eventstream.Envelope{}
	}
	return eventstream.Envelope{
		Kind:       eventstream.KindUsage,
		Cursor:     base.Cursor,
		SessionID:  base.SessionID,
		HandleID:   base.HandleID,
		RunID:      base.RunID,
		TurnID:     base.TurnID,
		OccurredAt: base.OccurredAt,
		Scope:      base.Scope,
		ScopeID:    base.ScopeID,
		Actor:      base.Actor,
		Usage: &eventstream.UsageSnapshot{
			PromptTokens:      usage.PromptTokens,
			CachedInputTokens: usage.CachedInputTokens,
			CompletionTokens:  usage.CompletionTokens,
			ReasoningTokens:   usage.ReasoningTokens,
			TotalTokens:       usage.TotalTokens,
		},
		Meta: base.Meta,
	}
}

func sessionACPEventFinal(event *session.Event) bool {
	if narrative := canonicalNarrativePayload(event); narrative != nil {
		return narrative.Final
	}
	return event != nil && event.Visibility != session.VisibilityUIOnly
}

func sessionACPEventMeta(event *session.Event) map[string]any {
	meta := canonicalEventMeta(event)
	if event == nil || event.Invocation == nil {
		return meta
	}
	invocation := session.CloneEventInvocation(*event.Invocation)
	if strings.TrimSpace(invocation.Provider) == "" && strings.TrimSpace(invocation.Model) == "" {
		return meta
	}
	return metautil.Merge(meta, map[string]any{
		metautil.Root: map[string]any{
			metautil.Version: 1,
			"invocation": map[string]any{
				"provider": strings.TrimSpace(invocation.Provider),
				"model":    strings.TrimSpace(invocation.Model),
			},
		},
	})
}

func lastACPEventCursor(events []eventstream.Envelope) string {
	if len(events) == 0 {
		return ""
	}
	return strings.TrimSpace(events[len(events)-1].Cursor)
}
