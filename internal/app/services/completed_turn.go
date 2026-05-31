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

func newCompletedTurn(ref session.Ref, result AgentInvokeResult) coreruntime.Turn {
	now := time.Now().UTC()
	turnID := fmt.Sprintf("controller-turn-%d", now.UnixNano())
	out := &completedTurn{
		id:        turnID,
		runID:     "controller-run-" + turnID,
		ref:       session.NormalizeRef(ref),
		startedAt: now,
		events:    make(chan coreruntime.EventEnvelope, len(result.Events)),
	}
	for _, event := range result.Events {
		event = session.CloneEvent(event)
		cursor := session.Cursor(strings.TrimSpace(event.ID))
		if cursor == "" {
			cursor = result.Cursor
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
