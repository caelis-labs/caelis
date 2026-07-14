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

func AsSessionListAdapter(agent Agent) (SessionListAdapter, bool) {
	adapter, ok := agent.(SessionListAdapter)
	return adapter, ok
}

func AsResumeSessionAdapter(agent Agent) (ResumeSessionAdapter, bool) {
	adapter, ok := agent.(ResumeSessionAdapter)
	return adapter, ok
}

func AsCloseSessionAdapter(agent Agent) (CloseSessionAdapter, bool) {
	adapter, ok := agent.(CloseSessionAdapter)
	return adapter, ok
}

func AsSessionModeAdapter(agent Agent) (SessionModeAdapter, bool) {
	adapter, ok := agent.(SessionModeAdapter)
	return adapter, ok
}

func AsSessionConfigAdapter(agent Agent) (SessionConfigAdapter, bool) {
	adapter, ok := agent.(SessionConfigAdapter)
	return adapter, ok
}

func AsSessionModelAdapter(agent Agent) (SessionModelAdapter, bool) {
	adapter, ok := agent.(SessionModelAdapter)
	return adapter, ok
}

func AsTerminalAdapter(agent Agent) (TerminalAdapter, bool) {
	adapter, ok := agent.(TerminalAdapter)
	return adapter, ok
}
