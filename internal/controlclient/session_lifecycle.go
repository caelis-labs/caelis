package controlclient

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

const sessionLifecycleStateKey = "control.session.lifecycle.v1"

var ErrSessionClosed = errors.New("controlclient: session is closed")

type closeSessionStore interface {
	session.Reader
	session.StateReader
	session.EventBatchStateService
}

// CloseSession atomically records the durable lifecycle event and the state
// gate used to reject later Control mutations. Repeated closes are idempotent.
func CloseSession(ctx context.Context, store session.Service, active session.Session, reason string) (session.Session, error) {
	compound, ok := store.(closeSessionStore)
	if !ok {
		return session.Session{}, errors.New("controlclient: session lifecycle persistence is unavailable")
	}
	closed, err := IsSessionClosed(ctx, compound, active.SessionRef)
	if err != nil {
		return session.Session{}, err
	}
	if closed {
		return compound.Session(ctx, active.SessionRef)
	}

	reason = strings.TrimSpace(reason)
	closedAt := time.Now().UTC()
	expectedRevision := active.Revision
	eventID := "control-session-closed:" + active.SessionID
	_, err = compound.AppendEventsAndUpdateState(ctx, session.AppendEventsAndUpdateStateRequest{
		SessionRef:       active.SessionRef,
		ExpectedRevision: &expectedRevision,
		MutationGuard:    session.ControlMutationGuard(session.ControlMutationPurposeLifecycle),
		TransactionID:    eventID,
		MutationDigest:   "control-session-close-v1",
		Events: []*session.Event{{
			ID: eventID, IdempotencyKey: eventID,
			Type: session.EventTypeLifecycle, Visibility: session.VisibilityMirror,
			Actor: session.ActorRef{Kind: session.ActorKindSystem, Name: "control"},
			Scope: &session.EventScope{Source: "control-client"},
			Lifecycle: &session.EventLifecycle{
				Status: "closed", Reason: reason,
				Meta: map[string]any{"closed_at": closedAt.Format(time.RFC3339Nano)},
			},
		}},
		UpdateState: func(_ []*session.Event, state map[string]any) (map[string]any, error) {
			if state == nil {
				state = map[string]any{}
			}
			state[sessionLifecycleStateKey] = map[string]any{
				"closed": true, "closed_at": closedAt.Format(time.RFC3339Nano), "reason": reason,
			}
			return state, nil
		},
	})
	updated, readErr := compound.Session(context.WithoutCancel(ctx), active.SessionRef)
	if readErr != nil {
		if err != nil {
			return session.Session{}, errors.Join(err, readErr)
		}
		return session.Session{}, readErr
	}
	if err != nil {
		return updated, fmt.Errorf("controlclient: close session: %w", err)
	}
	return updated, nil
}

// IsSessionClosed reads the durable Control lifecycle gate.
func IsSessionClosed(ctx context.Context, reader session.StateReader, ref session.SessionRef) (bool, error) {
	if reader == nil {
		return false, errors.New("controlclient: session lifecycle reader is unavailable")
	}
	state, err := reader.SnapshotState(ctx, ref)
	if err != nil {
		return false, err
	}
	lifecycle, ok := state[sessionLifecycleStateKey].(map[string]any)
	if !ok {
		return false, nil
	}
	closed, _ := lifecycle["closed"].(bool)
	return closed, nil
}
