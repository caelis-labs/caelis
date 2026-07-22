package controlclient

import (
	"context"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	controlclientapi "github.com/caelis-labs/caelis/control/client"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
)

type ListSessionsRequest struct {
	WorkspaceKey string `json:"workspace_key,omitempty"`
	Cursor       string `json:"cursor,omitempty"`
	Limit        int    `json:"limit,omitempty"`
}

type EventBatch struct {
	Events         []eventstream.Envelope `json:"events,omitempty"`
	ResumeMode     ResumeMode             `json:"resume_mode"`
	TransientGap   bool                   `json:"transient_gap,omitempty"`
	BoundaryCursor string                 `json:"boundary_cursor,omitempty"`
}

// Service composes the Control-owned command boundary with the transitional
// Session state and feed contracts consumed by presentation and network
// adapters.
type Service interface {
	controlclientapi.CommandClient
	ListSessions(context.Context, controlclientapi.Principal, ListSessionsRequest) (session.SessionList, error)
	InspectSession(context.Context, controlclientapi.Principal, StateRequest) (SessionState, error)
	Reconnect(context.Context, controlclientapi.Principal, ReconnectRequest) (ReconnectResult, error)
	Events(context.Context, controlclientapi.Principal, SubscribeRequest) (EventBatch, error)
	Subscribe(context.Context, controlclientapi.Principal, SubscribeRequest) (SubscribeResult, error)
}
