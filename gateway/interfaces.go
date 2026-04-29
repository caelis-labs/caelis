package gateway

import (
	"context"

	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
)

type SessionService interface {
	StartSession(context.Context, StartSessionRequest) (sdksession.Session, error)
	LoadSession(context.Context, LoadSessionRequest) (sdksession.LoadedSession, error)
	ResumeSession(context.Context, ResumeSessionRequest) (sdksession.LoadedSession, error)
	ListSessions(context.Context, ListSessionsRequest) (sdksession.SessionList, error)
	ForkSession(context.Context, ForkSessionRequest) (sdksession.Session, error)
	BindSession(context.Context, BindSessionRequest) error
	LookupBinding(BindingStateRequest) (BindingState, error)
	ReplayEvents(context.Context, ReplayEventsRequest) (ReplayEventsResult, error)
}

type TurnService interface {
	BeginTurn(context.Context, BeginTurnRequest) (BeginTurnResult, error)
	Interrupt(context.Context, InterruptRequest) error
}

type ControlPlaneService interface {
	ControlPlaneState(context.Context, ControlPlaneStateRequest) (ControlPlaneState, error)
	HandoffController(context.Context, HandoffControllerRequest) (sdksession.Session, error)
	AttachParticipant(context.Context, AttachParticipantRequest) (sdksession.Session, error)
	DetachParticipant(context.Context, DetachParticipantRequest) (sdksession.Session, error)
}

type CoreService interface {
	SessionService
	TurnService
	ControlPlaneService
}

type HostService interface {
	Status() HostStatus
	Shutdown(context.Context) error
	EnsureRemoteSession(context.Context, RemoteSessionRequest) (sdksession.Session, error)
	BeginRemoteTurn(context.Context, RemoteTurnRequest) (BeginTurnResult, error)
}
