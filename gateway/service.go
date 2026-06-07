package gateway

import (
	"context"
	"iter"

	"github.com/OnslaughtSnail/caelis/session"
)

// Service is the surface-facing gateway contract. TUI, headless, and
// ACP server all consume this interface.
type Service interface {
	// CreateSession creates a new session.
	CreateSession(context.Context, CreateSessionRequest) (session.Session, error)

	// ListSessions returns sessions matching the filter.
	ListSessions(context.Context, ListSessionsRequest) (ListSessionsResponse, error)

	// DeleteSession removes a session.
	DeleteSession(context.Context, DeleteSessionRequest) error

	// BeginTurn starts a new turn in a session. Returns a Turn handle.
	BeginTurn(context.Context, TurnRequest) (Turn, error)

	// Submit submits user input for the active turn.
	Submit(context.Context, SubmitRequest) error

	// Cancel cancels the active turn.
	Cancel(context.Context, CancelRequest) error

	// Replay replays session events from a cursor.
	Replay(context.Context, ReplayRequest) (ReplayResponse, error)

	// Subscribe streams gateway events for a session.
	Subscribe(context.Context, SubscribeRequest) iter.Seq2[EventEnvelope, error]
}
