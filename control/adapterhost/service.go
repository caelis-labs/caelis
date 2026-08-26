package adapterhost

import (
	"context"
	"io"
	"time"
)

const (
	// CodexAdapterID is the stable built-in Codex adapter identity.
	CodexAdapterID = "codex"
	// ChannelSubprotocol identifies Caelis private ACP-over-WebSocket framing.
	ChannelSubprotocol = "caelis.adapter-acp.v1"
)

// Descriptor is the stable adapter discovery and compatibility projection.
type Descriptor struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Available    bool   `json:"available"`
	BackendState string `json:"backend_state"`
	Diagnostic   string `json:"diagnostic,omitempty"`
}

// GrantRequest asks the Host to authorize exactly one adapter channel.
type GrantRequest struct {
	ConnectionID  string   `json:"connection_id"`
	CWD           string   `json:"cwd"`
	AllowedRoots  []string `json:"allowed_roots"`
	WritableRoots []string `json:"writable_roots,omitempty"`
}

// Grant is a short-lived, single-use channel credential.
type Grant struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// ChannelContext is the fully authorized Host projection supplied to a
// provider-neutral backend hook.
type ChannelContext struct {
	PrincipalID   string
	ConnectionID  string
	AllowedRoots  []string
	WritableRoots []string
}

// Service is the focused Host capability exposed by the private adapter API.
type Service interface {
	Inspect(context.Context, string) (Descriptor, error)
	IssueGrant(context.Context, string, string, GrantRequest) (Grant, error)
	ServeChannel(context.Context, string, string, io.Reader, io.Writer) error
}
