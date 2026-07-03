package kernel

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/caelis-labs/caelis/ports/session"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	acpprojector "github.com/caelis-labs/caelis/protocol/acp/projector"
)

const acpProjectionCursorPrefix = "acp-projection:"

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
	cursorEvents, cursorState, err := sessionEventsForACPReplayCursor(events, req.Cursor)
	if err != nil {
		return ReplayEventsResult{}, err
	}
	replayEvents := replayTranscriptEvents(cursorEvents, req.IncludeTransient)
	projected, err := projectSessionACPReplayEvents(ref, replayEvents, cursorState, req.Limit)
	if err != nil {
		return ReplayEventsResult{}, err
	}
	taskPanelTargetHistory, err := projectSessionACPReplayTaskPanelHistory(ref, events, cursorState)
	if err != nil {
		return ReplayEventsResult{}, err
	}
	// Replay-only task panel augmentation must not advance the durable cursor:
	// it is derived from the task store to make resumed async panels look like
	// their live final state, not a new persisted session event.
	nextCursor := lastACPEventCursor(projected)
	projected = g.augmentReplayTaskPanelEvents(ctx, ref, projected, taskPanelTargetHistory)
	out := ReplayEventsResult{
		SessionRef:    ref,
		Events:        projected,
		NextCursor:    nextCursor,
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

func projectSessionACPReplayEvents(ref session.SessionRef, events []*session.Event, cursor acpReplayCursorState, limit int) ([]eventstream.Envelope, error) {
	if len(events) == 0 {
		return nil, nil
	}
	out := make([]eventstream.Envelope, 0, len(events))
	policy := acpReplayPolicy{cursor: cursor, sourceLimit: limit}
	for _, event := range events {
		if event == nil {
			continue
		}
		projected := projectSessionACPEvent(ref, event, "", "", "")
		var (
			keep bool
			err  error
		)
		projected, keep, err = policy.apply(event, projected)
		if err != nil {
			return nil, err
		}
		if !keep {
			break
		}
		if len(projected) == 0 {
			continue
		}
		out = append(out, projected...)
	}
	return out, nil
}

func projectSessionACPReplayTaskPanelHistory(ref session.SessionRef, events []*session.Event, cursor acpReplayCursorState) ([]eventstream.Envelope, error) {
	if strings.TrimSpace(cursor.raw) == "" {
		return nil, nil
	}
	historyEvents := sessionEventsThroughACPReplayCursor(events, cursor)
	if len(historyEvents) == 0 {
		return nil, nil
	}
	projected := projectSessionACPEvents(ref, historyEvents)
	if len(projected) == 0 {
		return nil, nil
	}
	if !cursor.projection {
		return projected, nil
	}
	return trimACPReplayThroughCursor(projected, cursor)
}

func sessionEventsThroughACPReplayCursor(events []*session.Event, cursor acpReplayCursorState) []*session.Event {
	eventID := strings.TrimSpace(cursor.eventID)
	if eventID == "" {
		eventID = strings.TrimSpace(cursor.raw)
	}
	if eventID == "" {
		return nil
	}
	for i, event := range events {
		if event == nil {
			continue
		}
		if strings.TrimSpace(event.ID) == eventID {
			return events[:i+1]
		}
	}
	return nil
}

func projectSessionACPEvent(ref session.SessionRef, event *session.Event, handleID string, runID string, turnID string) []eventstream.Envelope {
	base := acpprojector.EnvelopeBaseFromSessionEvent(ref, event, acpprojector.SessionEventTransport{
		HandleID: handleID,
		RunID:    runID,
		TurnID:   turnID,
	})
	base.Meta = sessionACPEventMeta(event)
	out := acpprojector.ProjectSessionEventEnvelope(base, event)
	return stampSessionACPProjectionIDs(strings.TrimSpace(event.ID), out)
}

func sessionACPEventMeta(event *session.Event) map[string]any {
	var meta map[string]any
	if event != nil {
		meta = event.Meta
	}
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

type acpReplayCursorState struct {
	raw        string
	eventID    string
	projection bool
}

type acpReplayPolicy struct {
	cursor      acpReplayCursorState
	sourceLimit int
	sourceCount int
}

func (p *acpReplayPolicy) apply(event *session.Event, projected []eventstream.Envelope) ([]eventstream.Envelope, bool, error) {
	if p == nil {
		return projected, true, nil
	}
	if p.cursor.projection && event != nil && strings.TrimSpace(event.ID) == p.cursor.eventID {
		var err error
		projected, err = trimACPReplayAfterCursor(projected, p.cursor)
		if err != nil {
			return nil, false, err
		}
	}
	if len(projected) == 0 {
		return nil, true, nil
	}
	if p.sourceLimit > 0 && p.sourceCount >= p.sourceLimit {
		return nil, false, nil
	}
	p.sourceCount++
	return projected, true, nil
}

func sessionEventsForACPReplayCursor(events []*session.Event, cursor string) ([]*session.Event, acpReplayCursorState, error) {
	cursor = strings.TrimSpace(cursor)
	if cursor == "" {
		return events, acpReplayCursorState{}, nil
	}
	if eventID, _, ok := parseACPProjectionCursor(cursor); ok {
		for i, event := range events {
			if event == nil {
				continue
			}
			if strings.TrimSpace(event.ID) == eventID {
				return events[i:], acpReplayCursorState{raw: cursor, eventID: eventID, projection: true}, nil
			}
		}
	}
	for i, event := range events {
		if event == nil {
			continue
		}
		if strings.TrimSpace(event.ID) == cursor {
			return events[i+1:], acpReplayCursorState{raw: cursor}, nil
		}
	}
	return nil, acpReplayCursorState{}, cursorNotFoundError(cursor)
}

func parseACPProjectionCursor(cursor string) (string, int, bool) {
	cursor = strings.TrimSpace(cursor)
	if !strings.HasPrefix(cursor, acpProjectionCursorPrefix) {
		return "", 0, false
	}
	payload := strings.TrimPrefix(cursor, acpProjectionCursorPrefix)
	sep := strings.LastIndex(payload, ":")
	if sep <= 0 || sep == len(payload)-1 {
		return "", 0, false
	}
	index, err := strconv.Atoi(payload[sep+1:])
	if err != nil || index < 0 {
		return "", 0, false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(payload[:sep])
	if err != nil {
		return "", 0, false
	}
	eventID := strings.TrimSpace(string(decoded))
	if eventID == "" {
		return "", 0, false
	}
	return eventID, index, true
}

func trimACPReplayAfterCursor(events []eventstream.Envelope, cursor acpReplayCursorState) ([]eventstream.Envelope, error) {
	if !cursor.projection {
		return events, nil
	}
	sawSource := false
	for i, env := range events {
		if strings.TrimSpace(env.EventID) == cursor.eventID {
			sawSource = true
		}
		if strings.TrimSpace(env.Cursor) == cursor.raw || strings.TrimSpace(env.ProjectionID) == cursor.raw {
			return events[i+1:], nil
		}
	}
	if sawSource {
		return nil, cursorNotFoundError(cursor.raw)
	}
	return events, nil
}

func trimACPReplayThroughCursor(events []eventstream.Envelope, cursor acpReplayCursorState) ([]eventstream.Envelope, error) {
	if !cursor.projection {
		return events, nil
	}
	sawSource := false
	for i, env := range events {
		if strings.TrimSpace(env.EventID) == cursor.eventID {
			sawSource = true
		}
		if strings.TrimSpace(env.Cursor) == cursor.raw || strings.TrimSpace(env.ProjectionID) == cursor.raw {
			return events[:i+1], nil
		}
	}
	if sawSource {
		return nil, cursorNotFoundError(cursor.raw)
	}
	return events, nil
}

func stampSessionACPProjectionIDs(eventID string, events []eventstream.Envelope) []eventstream.Envelope {
	eventID = strings.TrimSpace(eventID)
	if eventID == "" || len(events) == 0 {
		return events
	}
	out := make([]eventstream.Envelope, len(events))
	for i, env := range events {
		env.EventID = eventID
		env.ProjectionID = formatACPProjectionCursor(eventID, i)
		env.Cursor = env.ProjectionID
		out[i] = env
	}
	return out
}

func formatACPProjectionCursor(eventID string, index int) string {
	return fmt.Sprintf("%s%s:%d", acpProjectionCursorPrefix, base64.RawURLEncoding.EncodeToString([]byte(strings.TrimSpace(eventID))), index)
}
