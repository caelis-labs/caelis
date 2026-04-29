package runtime

import (
	"context"

	sdkcontroller "github.com/OnslaughtSnail/caelis/sdk/controller"
	sdkmodel "github.com/OnslaughtSnail/caelis/sdk/model"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
	sdkstream "github.com/OnslaughtSnail/caelis/sdk/stream"
	sdktool "github.com/OnslaughtSnail/caelis/sdk/tool"
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
	SessionRef sdksession.SessionRef        `json:"session_ref"`
	Session    sdksession.Session           `json:"session"`
	RunID      string                       `json:"run_id,omitempty"`
	TurnID     string                       `json:"turn_id,omitempty"`
	Mode       string                       `json:"mode,omitempty"`
	Tool       sdktool.Definition           `json:"tool"`
	Call       sdktool.Call                 `json:"call"`
	Approval   *sdksession.ProtocolApproval `json:"approval,omitempty"`
	Metadata   map[string]any               `json:"metadata,omitempty"`
}

// ApprovalResponse is one resolved approval outcome.
type ApprovalResponse struct {
	Outcome  string `json:"outcome,omitempty"`
	OptionID string `json:"option_id,omitempty"`
	Approved bool   `json:"approved,omitempty"`
}

// ApprovalRequester bridges runtime approval decisions to an interactive client
// such as ACP request_permission.
type ApprovalRequester interface {
	RequestApproval(context.Context, ApprovalRequest) (ApprovalResponse, error)
}

// RunRequest is the minimal runtime execution request.
type RunRequest struct {
	SessionRef        sdksession.SessionRef  `json:"session_ref"`
	Input             string                 `json:"input,omitempty"`
	ContentParts      []sdkmodel.ContentPart `json:"content_parts,omitempty"`
	Request           ModelRequestOptions    `json:"request,omitempty"`
	ApprovalRequester ApprovalRequester      `json:"-"`
	Agent             Agent                  `json:"-"`
	AgentSpec         AgentSpec              `json:"-"`
}

// RunResult is one runtime execution result.
type RunResult struct {
	Session sdksession.Session `json:"session"`
	Handle  Runner             `json:"-"`
}

// Runtime is the minimal runtime execution boundary for the new SDK.
type Runtime interface {
	Run(context.Context, RunRequest) (RunResult, error)
	RunState(context.Context, sdksession.SessionRef) (RunState, error)
}

// AttachACPParticipantRequest attaches one ACP participant without replacing
// the active controller. This is the programmatic sidecar/delegation entrypoint
// for app and gateway layers.
type AttachACPParticipantRequest struct {
	SessionRef sdksession.SessionRef      `json:"session_ref"`
	Agent      string                     `json:"agent,omitempty"`
	Role       sdksession.ParticipantRole `json:"role,omitempty"`
	Source     string                     `json:"source,omitempty"`
	Label      string                     `json:"label,omitempty"`
}

// DetachACPParticipantRequest removes one attached ACP participant and releases
// any associated adapter-owned transport state.
type DetachACPParticipantRequest struct {
	SessionRef    sdksession.SessionRef `json:"session_ref"`
	ParticipantID string                `json:"participant_id,omitempty"`
	Source        string                `json:"source,omitempty"`
}

type PromptACPParticipantRequest struct {
	SessionRef    sdksession.SessionRef  `json:"session_ref"`
	ParticipantID string                 `json:"participant_id,omitempty"`
	Input         string                 `json:"input,omitempty"`
	ContentParts  []sdkmodel.ContentPart `json:"content_parts,omitempty"`
	Source        string                 `json:"source,omitempty"`
}

// HandoffControllerRequest switches the active controller for one session. The
// request is app-owned and not exposed on the LLM-facing tool surface.
type HandoffControllerRequest struct {
	SessionRef sdksession.SessionRef     `json:"session_ref"`
	Kind       sdksession.ControllerKind `json:"kind,omitempty"`
	Agent      string                    `json:"agent,omitempty"`
	Source     string                    `json:"source,omitempty"`
	Reason     string                    `json:"reason,omitempty"`
}

// SessionControlPlane exposes optional session orchestration capabilities such
// as ACP sidecar attachment and controller handoff.
type SessionControlPlane interface {
	AttachACPParticipant(context.Context, AttachACPParticipantRequest) (sdksession.Session, error)
	PromptACPParticipant(context.Context, PromptACPParticipantRequest) (sdksession.Session, error)
	DetachACPParticipant(context.Context, DetachACPParticipantRequest) (sdksession.Session, error)
	HandoffController(context.Context, HandoffControllerRequest) (sdksession.Session, error)
}

// StreamProvider is one optional runtime capability for unified task output
// reads and subscriptions.
type StreamProvider interface {
	Streams() sdkstream.Service
}

// ControllerProvider exposes the optional controller-orchestration backend used
// by one runtime implementation. It is intended for advanced app wiring and
// tests rather than the LLM-facing execution surface.
type ControllerProvider interface {
	Controllers() sdkcontroller.Backend
}
