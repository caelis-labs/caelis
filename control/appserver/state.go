package appserver

import (
	"context"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

const (
	EnvelopeVersion = "caelis.control.envelope/v1"
	HTTPAPIVersion  = "v1"
	ServerIdentity  = "caelis-control-host"

	CapabilityAppServerClients        = "appserver-clients-v2"
	CapabilityTaskStreams             = "task-streams-v2"
	CapabilitySessionFeed             = "session-feed-v2"
	CapabilityMultiWorkspace          = "multi-workspace-sessions-v1"
	CapabilityWorkspaceCWDList        = "workspace-cwd-session-list-v1"
	CapabilityWorkspaceTrust          = "workspace-trust-v1"
	CapabilityWorkspaceTrustPreflight = "workspace-trust-preflight-v1"
	CapabilityHostReadiness           = "host-readiness-v1"
)

var ErrStateRevisionConflict = errorcode.New(errorcode.Conflict, "controlclient: session state changed during bootstrap")

// ServerInfo is the minimal compatibility handshake available before a
// client has selected or created a Session.
type ServerInfo struct {
	ProtocolVersion     int      `json:"protocol_version"`
	EnvelopeVersion     string   `json:"envelope_version"`
	APIVersion          string   `json:"api_version"`
	DistributionVersion string   `json:"distribution_version,omitempty"`
	BuildID             string   `json:"build_id,omitempty"`
	BuildKind           string   `json:"build_kind,omitempty"`
	ServerID            string   `json:"server_id,omitempty"`
	InstanceID          string   `json:"instance_id,omitempty"`
	Capabilities        []string `json:"capabilities,omitempty"`
	Transports          []string `json:"transports,omitempty"`
}

// HostStatus is the safely scoped process-level health/readiness response. It
// contains no principal, Session, workspace, configuration, or credential data.
type HostStatus struct {
	ServerID   string `json:"server_id"`
	InstanceID string `json:"instance_id"`
	Ready      bool   `json:"ready"`
}

// RequiredManagedHostCapabilities returns the capabilities required by
// transparent local start-or-attach clients.
func RequiredManagedHostCapabilities() []string {
	return []string{
		CapabilityAppServerClients,
		CapabilityTaskStreams,
		CapabilitySessionFeed,
		CapabilityMultiWorkspace,
		CapabilityWorkspaceCWDList,
		CapabilityWorkspaceTrust,
		CapabilityWorkspaceTrustPreflight,
		CapabilityHostReadiness,
	}
}

// ClientCapabilities declares presentation ownership and reserved bootstrap
// capability slots without implying Runtime support.
type ClientCapabilities struct {
	ClientManagedTerminal        bool `json:"client_managed_terminal"`
	CaelisTerminalStream         bool `json:"caelis_terminal_stream"`
	GoalBootstrapSupported       bool `json:"goal_bootstrap_supported"`
	ManageLoopBootstrapSupported bool `json:"manage_loop_bootstrap_supported"`
}

// RunKind identifies which Control-owned execution currently occupies a
// Session. Kernel is the main conversation Turn; participant is a delegated
// execution and must not be mistaken for a steerable main Turn.
type RunKind string

const (
	RunKindKernel      RunKind = "kernel"
	RunKindParticipant RunKind = "participant"
)

// RunState is the live Control-owned identity of one Session Turn.
type RunState struct {
	Status          string    `json:"status,omitempty"`
	WaitingApproval bool      `json:"waiting_approval,omitempty"`
	Active          bool      `json:"active,omitempty"`
	Kind            RunKind   `json:"kind,omitempty"`
	HandleID        string    `json:"handle_id,omitempty"`
	RunID           string    `json:"run_id,omitempty"`
	TurnID          string    `json:"turn_id,omitempty"`
	StartedAt       time.Time `json:"started_at,omitempty"`
}

// ActiveApproval is the one resolvable Control FIFO head.
type ActiveApproval struct {
	RequestID     eventstream.ApprovalRequestID   `json:"request_id"`
	Scope         eventstream.Scope               `json:"scope,omitempty"`
	ScopeID       string                          `json:"scope_id,omitempty"`
	ParticipantID string                          `json:"participant_id,omitempty"`
	ParentTool    *eventstream.ParentToolRelation `json:"parent_tool,omitempty"`
	Permission    *session.ProtocolApproval       `json:"permission"`
}

// ApprovalState exposes only the active request details and queued count.
type ApprovalState struct {
	Active      *ActiveApproval `json:"active,omitempty"`
	QueuedCount int             `json:"queued_count,omitempty"`
}

// RuntimeState is assembled by Control from the live handle and approval FIFO.
type RuntimeState struct {
	Run      RunState      `json:"run"`
	Approval ApprovalState `json:"approval"`
}

// RuntimeStateReader supplies the live portion of SessionState.
type RuntimeStateReader interface {
	ControlClientRuntimeState(context.Context, session.SessionRef) (RuntimeState, error)
}

// SessionState is the complete reconnect bootstrap for one explicit Session.
type SessionState struct {
	ProtocolVersion  int                          `json:"protocol_version"`
	EnvelopeVersion  string                       `json:"envelope_version"`
	APIVersion       string                       `json:"api_version"`
	SessionID        string                       `json:"session_id"`
	Revision         uint64                       `json:"revision"`
	WorkspaceKey     string                       `json:"workspace_key,omitempty"`
	CWD              string                       `json:"cwd,omitempty"`
	Title            string                       `json:"title,omitempty"`
	Metadata         map[string]any               `json:"metadata,omitempty"`
	BoundaryCursor   string                       `json:"boundary_cursor,omitempty"`
	BoundaryPosition *eventstream.FeedPosition    `json:"boundary_position,omitempty"`
	Run              RunState                     `json:"run"`
	Controller       session.ControllerBinding    `json:"controller"`
	Participants     []session.ParticipantBinding `json:"participants,omitempty"`
	Approval         ApprovalState                `json:"approval"`
	Capabilities     ClientCapabilities           `json:"capabilities"`
}

// StateRequest addresses exactly one Session ID.
type StateRequest struct {
	SessionID string `json:"session_id"`
}

// StateReader returns consistent reconnect bootstrap state.
type StateReader interface {
	State(context.Context, StateRequest) (SessionState, error)
}

// ReconnectRequest atomically bootstraps one explicit Session from Cursor.
type ReconnectRequest struct {
	SessionID string `json:"session_id"`
	Cursor    string `json:"cursor,omitempty"`
}

// ReconnectResult couples typed state to the exact feed cut and continuation
// that produced its boundary metadata.
type ReconnectResult struct {
	State        SessionState     `json:"state"`
	Subscription FeedSubscription `json:"-"`
}

// ReconnectReader owns the Control-layer bootstrap/splice transaction.
type ReconnectReader interface {
	Reconnect(context.Context, ReconnectRequest) (ReconnectResult, error)
}
