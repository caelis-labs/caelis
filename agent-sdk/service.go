package agentsdk

import (
	"context"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/placement"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

// ErrRunInputClosed reports that an exact live Run no longer accepts input.
// The input was not dispatched and callers may reselect a
// newer active Run or start a new idle Turn.
var ErrRunInputClosed = errorcode.New(errorcode.FailedPrecondition, "agent-sdk: run input is closed")

// RunConflictError reports that Core detected another active run for the same
// session. Control decides whether to queue, reject, or fork the new request.
type RunConflictError struct {
	SessionRef  session.SessionRef
	ActiveRunID string
}

func (e *RunConflictError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("agent-sdk: session %q already has active run %q", strings.TrimSpace(e.SessionRef.SessionID), strings.TrimSpace(e.ActiveRunID))
}

func (e *RunConflictError) ErrorCode() errorcode.Code { return errorcode.Conflict }

// ApprovalOption is one user-selectable approval choice.
type ApprovalOption struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
	Kind string `json:"kind,omitempty"`
}

// ApprovalRequest is one runtime-owned approval request emitted before one
// sensitive tool execution continues.
type ApprovalRequest struct {
	SessionRef session.SessionRef `json:"session_ref"`
	Session    session.Session    `json:"session"`
	RunID      string             `json:"run_id,omitempty"`
	TurnID     string             `json:"turn_id,omitempty"`
	// PauseTokenID identifies the SDK-owned durable pause, when this approval
	// was created by Runtime durable approval handling. Product Control may use
	// it as the request correlation value without adding it to ACP wire payloads.
	PauseTokenID string          `json:"pause_token_id,omitempty"`
	Tool         tool.Definition `json:"tool"`
	Call         tool.Call       `json:"call"`
	// ModelStep identifies the model-emitted tool-call batch. Concurrent batches
	// also carry a process-local admission barrier; neither is part of the ACP
	// permission payload.
	ModelStep *tool.ModelStepRef        `json:"model_step,omitempty"`
	Approval  *session.ProtocolApproval `json:"approval,omitempty"`
	Metadata  map[string]any            `json:"metadata,omitempty"`
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
	SessionRef session.SessionRef `json:"session_ref"`
	// InputKind distinguishes a real user turn from trusted Agent-to-Agent
	// communication. Empty retains ordinary conversation behavior.
	InputKind    SubmissionKind      `json:"input_kind,omitempty"`
	Input        string              `json:"input,omitempty"`
	DisplayInput string              `json:"display_input,omitempty"`
	ContentParts []model.ContentPart `json:"content_parts,omitempty"`
	// InputActor identifies who authored Input. Agent communication requires a
	// trusted source identity; ordinary conversation defaults to the real user.
	InputActor session.ActorRef `json:"-"`
	// InputCompaction preserves typed provenance if Runtime later summarizes the
	// input. It does not alter the provider-visible message.
	InputCompaction   *session.EventCompactionContext `json:"-"`
	Request           ModelRequestOptions             `json:"request,omitempty"`
	ApprovalRequester ApprovalRequester               `json:"-"`
	Agent             Agent                           `json:"-"`
	AgentSpec         AgentSpec                       `json:"-"`
}

// RunResult is one runtime execution result.
type RunResult struct {
	Session session.Session `json:"session"`
	Handle  Runner          `json:"-"`
}

// AttachLiveRunRequest identifies one execution that must still be live in the
// current Runtime process. It is not a durable continuation request.
type AttachLiveRunRequest struct {
	SessionRef session.SessionRef `json:"session_ref"`
	RunID      string             `json:"run_id"`
}

// ResolveApprovalRequest resolves one durable approval pause token.
type ResolveApprovalRequest struct {
	SessionRef session.SessionRef `json:"session_ref"`
	TokenID    string             `json:"token_id"`
	Decision   ApprovalResponse   `json:"decision"`
}

// RunNotAttachableError reports a run that has no live execution in this
// Runtime process. Its durable RunState remains available for recovery
// diagnostics, but callers must not interpret that state as a replay point.
type RunNotAttachableError struct {
	SessionRef session.SessionRef
	RunID      string
	Detail     string
}

func (e *RunNotAttachableError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("agent-sdk: run %q in session %q is not live-attachable: %s", strings.TrimSpace(e.RunID), strings.TrimSpace(e.SessionRef.SessionID), strings.TrimSpace(e.Detail))
}

func (e *RunNotAttachableError) ErrorCode() errorcode.Code { return errorcode.FailedPrecondition }

// Runtime is the minimal runtime execution boundary for the new SDK.
type Runtime interface {
	Run(context.Context, RunRequest) (RunResult, error)
	RunState(context.Context, session.SessionRef) (RunState, error)
}

// RunnerCompletionRuntime guarantees that every successful producer entry
// point returning a non-nil Runner, including optional participant prompts,
// also returns a RunnerCompletionWaiter. Control fencing decorators require
// this capability before starting a producer.
type RunnerCompletionRuntime interface {
	Runtime
	RunnerCompletionWaiterGuaranteed()
}

// LiveRunAttacher exposes process-local observation of an execution that is
// still registered in the same Runtime instance. It never reconstructs or
// continues a durable run after restart.
type LiveRunAttacher interface {
	AttachLiveRun(context.Context, AttachLiveRunRequest) (RunResult, error)
}

// ApprovalResolver resolves one durable approval pause. Resolution can wake a
// matching live waiter, but does not make a non-live run resumable.
type ApprovalResolver interface {
	ResolveApproval(context.Context, ResolveApprovalRequest) error
}

// AttachParticipantRequest attaches one external participant without replacing
// the active controller. Concrete runtimes may back the participant with ACP,
// a built-in subagent, or another controller protocol.
type AttachParticipantRequest struct {
	SessionRef session.SessionRef      `json:"session_ref"`
	Agent      string                  `json:"agent,omitempty"`
	Role       session.ParticipantRole `json:"role,omitempty"`
	Source     string                  `json:"source,omitempty"`
	Label      string                  `json:"label,omitempty"`
	// Placement is resolved and sealed by Control before attach, then preserved
	// verbatim for durable reattachment.
	Placement placement.Placement `json:"placement"`
}

// DetachParticipantRequest removes one attached participant and releases
// any associated adapter-owned transport state.
type DetachParticipantRequest struct {
	SessionRef    session.SessionRef `json:"session_ref"`
	ParticipantID string             `json:"participant_id,omitempty"`
	Source        string             `json:"source,omitempty"`
}

// PromptParticipantRequest prompts one attached participant.
type PromptParticipantRequest struct {
	SessionRef    session.SessionRef `json:"session_ref"`
	ParticipantID string             `json:"participant_id,omitempty"`
	// TurnID is the Control-assigned semantic participant turn identity.
	// Runtime allocates one only when this field is empty.
	TurnID       string `json:"turn_id,omitempty"`
	Input        string `json:"input,omitempty"`
	DisplayInput string `json:"display_input,omitempty"`
	// DisplayAddress is the canonical user-visible route (for example,
	// /zenith(remy)). It is display-only and must not be used as participant
	// identity.
	DisplayAddress    string              `json:"display_address,omitempty"`
	DisplayTitle      string              `json:"display_title,omitempty"`
	ContentParts      []model.ContentPart `json:"content_parts,omitempty"`
	Source            string              `json:"source,omitempty"`
	Stream            bool                `json:"stream,omitempty"`
	ApprovalRequester ApprovalRequester   `json:"-"`
}

// HandoffControllerRequest switches the active controller for one session. The
// request is app-owned and not exposed on the LLM-facing tool surface.
type HandoffControllerRequest struct {
	SessionRef              session.SessionRef        `json:"session_ref"`
	ExpectedRevision        *uint64                   `json:"expected_revision,omitempty"`
	ExpectedControllerEpoch string                    `json:"expected_controller_epoch,omitempty"`
	Kind                    session.ControllerKind    `json:"kind,omitempty"`
	Agent                   string                    `json:"agent,omitempty"`
	Placement               placement.Placement       `json:"placement,omitzero"`
	Source                  string                    `json:"source,omitempty"`
	Reason                  string                    `json:"reason,omitempty"`
	StateUpdate             session.AppendStateUpdate `json:"-"`
}

// ParticipantControlPlane exposes neutral participant execution capabilities.
// Host Control owns selection and lifecycle policy around these operations.
type ParticipantControlPlane interface {
	AttachParticipant(context.Context, AttachParticipantRequest) (session.Session, error)
	PromptParticipant(context.Context, PromptParticipantRequest) (RunResult, error)
	DetachParticipant(context.Context, DetachParticipantRequest) (session.Session, error)
}

// SessionControlPlane is the host-facing composition of neutral participant
// execution and the Control-owned controller handoff operation.
type SessionControlPlane interface {
	ParticipantControlPlane
	HandoffController(context.Context, HandoffControllerRequest) (session.Session, error)
}

// StreamProvider is one optional runtime capability for unified task output
// reads and subscriptions.
type StreamProvider interface {
	Streams() stream.Service
}
