package acp

import "context"

// SessionListAdapter exposes the optional session/list surface.
type SessionListAdapter interface {
	ListSessions(context.Context, SessionListRequest) (SessionListResponse, error)
}

// ResumeSessionAdapter exposes the optional session/resume surface.
type ResumeSessionAdapter interface {
	ResumeSession(context.Context, ResumeSessionRequest) (ResumeSessionResponse, error)
}

// CloseSessionAdapter exposes the optional session/close surface.
type CloseSessionAdapter interface {
	CloseSession(context.Context, CloseSessionRequest) (CloseSessionResponse, error)
}

// SessionModeAdapter exposes the optional session/set_mode surface.
type SessionModeAdapter interface {
	SetSessionMode(context.Context, SetSessionModeRequest) (SetSessionModeResponse, error)
}

// SessionConfigAdapter exposes the optional session/set_config_option surface.
type SessionConfigAdapter interface {
	SetSessionConfigOption(context.Context, SetSessionConfigOptionRequest) (SetSessionConfigOptionResponse, error)
}

// SessionModelAdapter exposes the optional session/set_model surface.
type SessionModelAdapter interface {
	SetSessionModel(context.Context, SetSessionModelRequest) (SetSessionModelResponse, error)
}

// SessionSteeringAdapter accepts one steering request without owning a normal
// session/prompt lifecycle.
type SessionSteeringAdapter interface {
	SteerSession(context.Context, SessionSteeringRequest) (SessionSteeringResponse, error)
}
