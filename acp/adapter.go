package acp

import "context"

// LoadSessionAdapter exposes the optional session/load surface.
type LoadSessionAdapter interface {
	LoadSession(context.Context, LoadSessionRequest, PromptCallbacks) (LoadSessionResponse, error)
}

// SessionModeAdapter exposes the optional session/set_mode surface.
type SessionModeAdapter interface {
	SetSessionMode(context.Context, SetSessionModeRequest) (SetSessionModeResponse, error)
}

// SessionConfigAdapter exposes the optional session/set_config_option surface.
type SessionConfigAdapter interface {
	SetSessionConfigOption(context.Context, SetSessionConfigOptionRequest) (SetSessionConfigOptionResponse, error)
}

func AsLoadSessionAdapter(agent Agent) (LoadSessionAdapter, bool) {
	adapter, ok := agent.(LoadSessionAdapter)
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

func AsTerminalAdapter(agent Agent) (TerminalAdapter, bool) {
	adapter, ok := agent.(TerminalAdapter)
	return adapter, ok
}
