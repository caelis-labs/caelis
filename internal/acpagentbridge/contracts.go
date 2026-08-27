package acpagentbridge

import (
	"context"
	"errors"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

// ErrCapabilityUnsupported marks an optional ACP Agent method that the
// private bridge cannot serve. The Surface maps it to JSON-RPC method-not-found.
var ErrCapabilityUnsupported = errors.New("internal/acpagentbridge: capability unsupported")

// PromptCallbacks routes one product ACP prompt's updates and approval
// requests back to the exact client connection that submitted it.
type PromptCallbacks interface {
	SessionUpdate(context.Context, schema.SessionNotification) error
	RequestPermission(context.Context, schema.RequestPermissionRequest) (schema.RequestPermissionResponse, error)
}

// SessionLoader supplies the lower-level direct-runtime bridge with durable
// ACP replay. Product AppServer assembly uses its typed Session client instead.
type SessionLoader interface {
	LoadSession(context.Context, schema.LoadSessionRequest, PromptCallbacks) (schema.LoadSessionResponse, error)
}

// CommandProvider supplies direct-runtime conformance with ACP command
// discovery. Product assembly reads commands from AppServer presentation.
type CommandProvider interface {
	AvailableCommands(context.Context, string) ([]schema.AvailableCommand, error)
}

// SessionModeReader supplies the bridge with the current ACP mode projection.
type SessionModeReader interface {
	SessionModes(context.Context, session.Session) (*schema.SessionModeState, error)
}

// SessionModeWriter applies one ACP mode mutation for direct-runtime
// conformance. Product assembly sends configuration through AppServer instead.
type SessionModeWriter interface {
	SetSessionMode(context.Context, schema.SetSessionModeRequest) (schema.SetSessionModeResponse, error)
}

// SessionConfigReader supplies the bridge with ACP configuration projection.
type SessionConfigReader interface {
	SessionConfigOptions(context.Context, session.Session) ([]schema.SessionConfigOption, error)
}

// SessionConfigWriter applies one ACP configuration mutation for direct-runtime
// conformance. Product assembly sends configuration through AppServer instead.
type SessionConfigWriter interface {
	SetSessionConfigOption(context.Context, schema.SetSessionConfigOptionRequest) (schema.SetSessionConfigOptionResponse, error)
}
