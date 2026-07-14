package controlclient

import (
	"context"

	"github.com/caelis-labs/caelis/agent-sdk/session"
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

// Service is the complete transport-neutral client boundary consumed by
// presentation and network adapters.
type Service interface {
	CommandClient
	ListSessions(context.Context, Principal, ListSessionsRequest) (session.SessionList, error)
	InspectSession(context.Context, Principal, StateRequest) (SessionState, error)
	Reconnect(context.Context, Principal, ReconnectRequest) (ReconnectResult, error)
	Events(context.Context, Principal, SubscribeRequest) (EventBatch, error)
	Subscribe(context.Context, Principal, SubscribeRequest) (SubscribeResult, error)
}
