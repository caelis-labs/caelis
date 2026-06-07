package gateway

import (
	"github.com/OnslaughtSnail/caelis/model"
	"github.com/OnslaughtSnail/caelis/session"
)

// CreateSessionRequest is the input to Service.CreateSession.
type CreateSessionRequest struct {
	AppName      string
	UserID       string
	WorkspaceKey string
	Title        string
	Workspace    session.Workspace
	Controller   session.ControllerBinding
	Participants []session.ParticipantBinding
}

// ListSessionsRequest is the input to Service.ListSessions.
type ListSessionsRequest struct {
	AppName      string
	UserID       string
	WorkspaceKey string
	Cursor       string
	Limit        int
}

// ListSessionsResponse is the output of Service.ListSessions.
type ListSessionsResponse struct {
	Sessions []session.Session
	Cursor   string
}

// DeleteSessionRequest is the input to Service.DeleteSession.
type DeleteSessionRequest struct {
	SessionRef session.Ref
}

// TurnRequest is the input to Service.BeginTurn.
type TurnRequest struct {
	SessionRef session.Ref
	Branch     string
}

// Turn is a handle for an active turn.
type Turn struct {
	TurnID    string
	SessionID string
}

// SubmitRequest is the input to Service.Submit.
type SubmitRequest struct {
	TurnID      string
	UserMessage model.Message
	Metadata    map[string]any
}

// CancelRequest is the input to Service.Cancel.
type CancelRequest struct {
	TurnID string
	Reason string
}

// ReplayRequest is the input to Service.Replay.
type ReplayRequest struct {
	SessionRef session.Ref
	AfterID    string
	Limit      int
}

// ReplayResponse is the output of Service.Replay.
type ReplayResponse struct {
	Events []EventEnvelope
	Cursor string
}

// SubscribeRequest is the input to Service.Subscribe.
type SubscribeRequest struct {
	SessionRef session.Ref
	AfterID    string
}
