package agent

import (
	"context"

	coremodel "github.com/OnslaughtSnail/caelis/core/model"
	"github.com/OnslaughtSnail/caelis/ports/controller"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/ports/stream"
	"github.com/OnslaughtSnail/caelis/ports/tool"
)

// ApprovalOption is one user-selectable approval choice.
type ApprovalOption struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
	Kind string `json:"kind,omitempty"`
}

// ApprovalRequest is one runtime-owned approval request emitted before one
// sensitive tool execution continues.
type ApprovalRequest struct {
	SessionRef session.SessionRef        `json:"session_ref"`
	Session    session.Session           `json:"session"`
	RunID      string                    `json:"run_id,omitempty"`
	TurnID     string                    `json:"turn_id,omitempty"`
	Mode       string                    `json:"mode,omitempty"`
	Tool       tool.Definition           `json:"tool"`
	Call       tool.Call                 `json:"call"`
	Approval   *session.ProtocolApproval `json:"approval,omitempty"`
	Metadata   map[string]any            `json:"metadata,omitempty"`
}

// ApprovalResponse is one resolved approval outcome.
type ApprovalResponse struct {
	Outcome    string `json:"outcome,omitempty"`
	OptionID   string `json:"option_id,omitempty"`
	Approved   bool   `json:"approved,omitempty"`
	Reason     string `json:"reason,omitempty"`
	ReviewText string `json:"review_text,omitempty"`
}

// ApprovalRequester bridges runtime approval decisions to an interactive client
// such as ACP request_permission.
type ApprovalRequester interface {
	RequestApproval(context.Context, ApprovalRequest) (ApprovalResponse, error)
}

// RunRequest is the minimal runtime execution request.
type RunRequest struct {
	SessionRef        session.SessionRef      `json:"session_ref"`
	Input             string                  `json:"input,omitempty"`
	ContentParts      []coremodel.ContentPart `json:"content_parts,omitempty"`
	Request           ModelRequestOptions     `json:"request,omitempty"`
	ApprovalRequester ApprovalRequester       `json:"-"`
	Agent             Agent                   `json:"-"`
	AgentSpec         AgentSpec               `json:"-"`
}

// RunResult is one runtime execution result.
type RunResult struct {
	Session session.Session `json:"session"`
	Handle  Runner          `json:"-"`
}

// Runtime is the minimal runtime execution boundary for the new SDK.
type Runtime interface {
	Run(context.Context, RunRequest) (RunResult, error)
	RunState(context.Context, session.SessionRef) (RunState, error)
}

// AttachACPParticipantRequest attaches one ACP participant without replacing
// the active controller. This is the programmatic sidecar/delegation entrypoint
// for app and gateway layers.
type AttachACPParticipantRequest struct {
	SessionRef session.SessionRef      `json:"session_ref"`
	Agent      string                  `json:"agent,omitempty"`
	Role       session.ParticipantRole `json:"role,omitempty"`
	Source     string                  `json:"source,omitempty"`
	Label      string                  `json:"label,omitempty"`
}

// DetachACPParticipantRequest removes one attached ACP participant and releases
// any associated adapter-owned transport state.
type DetachACPParticipantRequest struct {
	SessionRef    session.SessionRef `json:"session_ref"`
	ParticipantID string             `json:"participant_id,omitempty"`
	Source        string             `json:"source,omitempty"`
}

type PromptACPParticipantRequest struct {
	SessionRef        session.SessionRef      `json:"session_ref"`
	ParticipantID     string                  `json:"participant_id,omitempty"`
	Input             string                  `json:"input,omitempty"`
	ContentParts      []coremodel.ContentPart `json:"content_parts,omitempty"`
	Source            string                  `json:"source,omitempty"`
	Stream            bool                    `json:"stream,omitempty"`
	ApprovalRequester ApprovalRequester       `json:"-"`
}

// HandoffControllerRequest switches the active controller for one session. The
// request is app-owned and not exposed on the LLM-facing tool surface.
type HandoffControllerRequest struct {
	SessionRef session.SessionRef     `json:"session_ref"`
	Kind       session.ControllerKind `json:"kind,omitempty"`
	Agent      string                 `json:"agent,omitempty"`
	Source     string                 `json:"source,omitempty"`
	Reason     string                 `json:"reason,omitempty"`
}

// SessionControlPlane exposes optional session orchestration capabilities such
// as ACP sidecar attachment and controller handoff.
type SessionControlPlane interface {
	AttachACPParticipant(context.Context, AttachACPParticipantRequest) (session.Session, error)
	PromptACPParticipant(context.Context, PromptACPParticipantRequest) (RunResult, error)
	DetachACPParticipant(context.Context, DetachACPParticipantRequest) (session.Session, error)
	HandoffController(context.Context, HandoffControllerRequest) (session.Session, error)
}

// StreamProvider is one optional runtime capability for unified task output
// reads and subscriptions.
type StreamProvider interface {
	Streams() stream.Service
}

// ControllerProvider exposes the optional controller-orchestration backend used
// by one runtime implementation. It is intended for advanced app wiring and
// tests rather than the LLM-facing execution surface.
type ControllerProvider interface {
	Controllers() controller.Backend
}
