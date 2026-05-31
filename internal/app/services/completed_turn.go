package services

import (
	"context"
	"fmt"
	"strings"
	"time"

	coreruntime "github.com/OnslaughtSnail/caelis/core/runtime"
	"github.com/OnslaughtSnail/caelis/core/session"
)

type completedTurn struct {
	id        string
	runID     string
	ref       session.Ref
	startedAt time.Time
	events    chan coreruntime.EventEnvelope
}

type completedTurnEvent struct {
	cursor session.Cursor
	event  session.Event
}

func newCompletedTurnIdentity(kind string) (string, string, time.Time) {
	now := time.Now().UTC()
	kind = strings.TrimSpace(kind)
	if kind == "" {
		kind = "completed"
	}
	turnID := fmt.Sprintf("%s-turn-%d", kind, now.UnixNano())
	return turnID, kind + "-run-" + turnID, now
}

func newCompletedTurn(ref session.Ref, turnID string, runID string, startedAt time.Time, events []completedTurnEvent) coreruntime.Turn {
	turnID = strings.TrimSpace(turnID)
	if turnID == "" {
		turnID, runID, startedAt = newCompletedTurnIdentity("completed")
	}
	if strings.TrimSpace(runID) == "" {
		runID = "completed-run-" + turnID
	}
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	}
	out := &completedTurn{
		id:        turnID,
		runID:     strings.TrimSpace(runID),
		ref:       session.NormalizeRef(ref),
		startedAt: startedAt,
		events:    make(chan coreruntime.EventEnvelope, len(events)),
	}
	for _, item := range events {
		event := session.CloneEvent(item.event)
		cursor := session.Cursor(strings.TrimSpace(string(item.cursor)))
		if cursor == "" {
			cursor = session.Cursor(strings.TrimSpace(event.ID))
		}
		out.events <- coreruntime.EventEnvelope{
			Cursor: cursor,
			Event:  event,
		}
	}
	close(out.events)
	return out
}

func (t *completedTurn) ID() string {
	if t == nil {
		return ""
	}
	return t.id
}

func (t *completedTurn) RunID() string {
	if t == nil {
		return ""
	}
	return t.runID
}

func (t *completedTurn) SessionRef() session.Ref {
	if t == nil {
		return session.Ref{}
	}
	return session.NormalizeRef(t.ref)
}

func (t *completedTurn) StartedAt() time.Time {
	if t == nil {
		return time.Time{}
	}
	return t.startedAt
}

func (t *completedTurn) Events() <-chan coreruntime.EventEnvelope {
	if t == nil {
		ch := make(chan coreruntime.EventEnvelope)
		close(ch)
		return ch
	}
	return t.events
}

func (t *completedTurn) Submit(context.Context, coreruntime.Submission) error {
	return coreruntime.ErrNoActiveTurn
}

func (t *completedTurn) Cancel() coreruntime.CancelResult {
	return coreruntime.CancelResult{Status: coreruntime.CancelAlreadyCancelled}
}

func (t *completedTurn) Close() error {
	return nil
}

var _ coreruntime.Turn = (*completedTurn)(nil)
