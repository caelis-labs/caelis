package gateway

import (
	"context"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
)

type Service interface {
	SessionService
	TurnService
	ControlPlaneService
}

type SessionService interface {
	StartSession(context.Context, StartSessionRequest) (session.Session, error)
	LoadSession(context.Context, LoadSessionRequest) (session.LoadedSession, error)
	ResumeSession(context.Context, ResumeSessionRequest) (session.LoadedSession, error)
	ForkSession(context.Context, ForkSessionRequest) (session.Session, error)
	ListSessions(context.Context, ListSessionsRequest) (session.SessionList, error)
	BindSession(context.Context, BindSessionRequest) error
	LookupBinding(BindingStateRequest) (BindingState, error)
	ReplayEvents(context.Context, ReplayEventsRequest) (ReplayEventsResult, error)
}

type TurnService interface {
	BeginTurn(context.Context, BeginTurnRequest) (BeginTurnResult, error)
	SubmitActiveTurn(context.Context, SubmitActiveTurnRequest) error
	Interrupt(context.Context, InterruptRequest) error
	ActiveTurns() []ActiveTurnState
}

type ControlPlaneService interface {
	ControlPlaneState(context.Context, ControlPlaneStateRequest) (ControlPlaneState, error)
	HandoffController(context.Context, HandoffControllerRequest) (session.Session, error)
	AttachParticipant(context.Context, AttachParticipantRequest) (session.Session, error)
	PromptParticipant(context.Context, PromptParticipantRequest) (BeginTurnResult, error)
	StartParticipant(context.Context, StartParticipantRequest) (BeginTurnResult, error)
	DetachParticipant(context.Context, DetachParticipantRequest) (session.Session, error)
}

type StreamProvider interface {
	Streams() stream.Service
}

type TurnResolver interface {
	ResolveTurn(context.Context, TurnIntent) (ResolvedTurn, error)
}

type ControllerTurnResolver interface {
	ResolveControllerTurn(context.Context, TurnIntent) (ResolvedTurn, error)
}

type RuntimeResolver interface {
	TurnResolver
	approval.ModelResolver
}

type RequestPolicy interface {
	ResolveTurnRequest(BeginTurnRequest) agent.ModelRequestOptions
}
