package controller

import (
	"context"
	"iter"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/ports/model"
	"github.com/caelis-labs/caelis/ports/session"
)

// ApprovalOption is one controller-side approval choice surfaced by a remote
// ACP controller.
type ApprovalOption struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
	Kind string `json:"kind,omitempty"`
}

// ApprovalToolCall describes the remote tool invocation asking for approval.
type ApprovalToolCall struct {
	ID       string         `json:"id,omitempty"`
	Name     string         `json:"name,omitempty"`
	Kind     string         `json:"kind,omitempty"`
	Title    string         `json:"title,omitempty"`
	Status   string         `json:"status,omitempty"`
	RawInput map[string]any `json:"raw_input,omitempty"`
}

// ApprovalRequest is the runtime-owned approval bridge payload used by remote
// ACP controllers. It is system-controlled and never exposed to the model.
type ApprovalRequest struct {
	SessionRef session.SessionRef `json:"session_ref,omitempty"`
	Session    session.Session    `json:"session,omitempty"`
	Agent      string             `json:"agent,omitempty"`
	Mode       string             `json:"mode,omitempty"`
	ToolCall   ApprovalToolCall   `json:"tool_call,omitempty"`
	Options    []ApprovalOption   `json:"options,omitempty"`
}

// ApprovalResponse is one bridged controller approval outcome.
type ApprovalResponse struct {
	Outcome  string `json:"outcome,omitempty"`
	OptionID string `json:"option_id,omitempty"`
	Approved bool   `json:"approved,omitempty"`
}

// ApprovalRequester bridges a remote controller approval request into the
// parent runtime's approval surface.
type ApprovalRequester interface {
	RequestControllerApproval(context.Context, ApprovalRequest) (ApprovalResponse, error)
}

// AttachRequest creates one ACP-backed participant attachment.
type AttachRequest struct {
	SessionRef session.SessionRef         `json:"session_ref,omitempty"`
	Session    session.Session            `json:"session,omitempty"`
	Binding    session.ParticipantBinding `json:"binding,omitempty"`
	Agent      string                     `json:"agent,omitempty"`
	Role       session.ParticipantRole    `json:"role,omitempty"`
	Source     string                     `json:"source,omitempty"`
	Label      string                     `json:"label,omitempty"`
}

// DetachRequest removes one ACP-backed participant attachment.
type DetachRequest struct {
	SessionRef    session.SessionRef `json:"session_ref,omitempty"`
	Session       session.Session    `json:"session,omitempty"`
	ParticipantID string             `json:"participant_id,omitempty"`
	Source        string             `json:"source,omitempty"`
}

// HandoffRequest activates one ACP controller for a session.
type HandoffRequest struct {
	SessionRef     session.SessionRef `json:"session_ref,omitempty"`
	Session        session.Session    `json:"session,omitempty"`
	Agent          string             `json:"agent,omitempty"`
	Source         string             `json:"source,omitempty"`
	Reason         string             `json:"reason,omitempty"`
	ContextPrelude string             `json:"context_prelude,omitempty"`
	ContextSyncSeq int                `json:"context_sync_seq,omitempty"`
}

// TurnRequest runs one turn through the active ACP controller.
type TurnRequest struct {
	SessionRef        session.SessionRef  `json:"session_ref,omitempty"`
	Session           session.Session     `json:"session,omitempty"`
	TurnID            string              `json:"turn_id,omitempty"`
	Input             string              `json:"input,omitempty"`
	ContentParts      []model.ContentPart `json:"content_parts,omitempty"`
	ContextPrelude    string              `json:"context_prelude,omitempty"`
	ContextSyncSeq    int                 `json:"context_sync_seq,omitempty"`
	Stream            bool                `json:"stream,omitempty"`
	Mode              string              `json:"mode,omitempty"`
	ApprovalRequester ApprovalRequester   `json:"-"`
}

// ParticipantPromptRequest sends one bounded prompt to an attached ACP
// participant without changing the main controller.
type ParticipantPromptRequest struct {
	SessionRef        session.SessionRef  `json:"session_ref,omitempty"`
	Session           session.Session     `json:"session,omitempty"`
	TurnID            string              `json:"turn_id,omitempty"`
	ParticipantID     string              `json:"participant_id,omitempty"`
	Input             string              `json:"input,omitempty"`
	DisplayInput      string              `json:"display_input,omitempty"`
	DisplayTitle      string              `json:"display_title,omitempty"`
	ContentParts      []model.ContentPart `json:"content_parts,omitempty"`
	ContextPrelude    string              `json:"context_prelude,omitempty"`
	Stream            bool                `json:"stream,omitempty"`
	Mode              string              `json:"mode,omitempty"`
	ApprovalRequester ApprovalRequester   `json:"-"`
}

type CancelStatus string

const (
	CancelStatusCancelled        CancelStatus = "cancelled"
	CancelStatusAlreadyCancelled CancelStatus = "already_cancelled"
)

type CancelResult struct {
	Status CancelStatus `json:"status,omitempty"`
	Err    error        `json:"-"`
}

func (r CancelResult) Cancelled() bool {
	return r.Status == CancelStatusCancelled
}

type TurnHandle interface {
	Events() iter.Seq2[*session.Event, error]
	Cancel() CancelResult
	Close() error
}

// TurnResult is one normalized ACP-controller turn result.
type TurnResult struct {
	Handle    TurnHandle `json:"-"`
	UpdatedAt time.Time  `json:"updated_at,omitempty"`
}

// ControllerCommand is one slash command declared by a remote ACP controller.
type ControllerCommand struct {
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
}

// ControllerConfigChoice is one selectable value declared by a remote ACP
// session config option.
type ControllerConfigChoice struct {
	Value       string `json:"value,omitempty"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
}

// ControllerConfigOption is a normalized view of one remote ACP session config
// option. The runtime keeps this generic so the TUI does not depend on ACP
// schema structs directly.
type ControllerConfigOption struct {
	ID           string                   `json:"id,omitempty"`
	Name         string                   `json:"name,omitempty"`
	Type         string                   `json:"type,omitempty"`
	Category     string                   `json:"category,omitempty"`
	Description  string                   `json:"description,omitempty"`
	CurrentValue string                   `json:"current_value,omitempty"`
	Options      []ControllerConfigChoice `json:"options,omitempty"`
}

// ControllerMode is one remote ACP session mode declared by the controller.
type ControllerMode struct {
	ID          string `json:"id,omitempty"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
}

// ControllerStatus summarizes the live remote ACP controller surface for
// local UI state such as slash commands and remote model selection.
type ControllerStatus struct {
	SessionRef      session.SessionRef       `json:"session_ref,omitempty"`
	Agent           string                   `json:"agent,omitempty"`
	RemoteSessionID string                   `json:"remote_session_id,omitempty"`
	RemoteTitle     string                   `json:"remote_title,omitempty"`
	Model           string                   `json:"model,omitempty"`
	ModelOptions    []ControllerConfigChoice `json:"model_options,omitempty"`
	ReasoningEffort string                   `json:"reasoning_effort,omitempty"`
	EffortOptions   []ControllerConfigChoice `json:"effort_options,omitempty"`
	// EffortOptionsByModel is populated for ACP servers that expose model/effort
	// combinations only through models.availableModels instead of configOptions.
	EffortOptionsByModel map[string][]ControllerConfigChoice `json:"effort_options_by_model,omitempty"`
	Commands             []ControllerCommand                 `json:"commands,omitempty"`
	ConfigOptions        []ControllerConfigOption            `json:"config_options,omitempty"`
	Mode                 string                              `json:"mode,omitempty"`
	ModeOptions          []ControllerMode                    `json:"mode_options,omitempty"`
	UpdatedAt            time.Time                           `json:"updated_at,omitempty"`
}

// SetControllerModelRequest changes the active remote ACP controller model
// using the server-declared config option surface.
type SetControllerModelRequest struct {
	SessionRef      session.SessionRef `json:"session_ref,omitempty"`
	Model           string             `json:"model,omitempty"`
	ReasoningEffort string             `json:"reasoning_effort,omitempty"`
}

// SetControllerModeRequest changes the active remote ACP controller mode using
// the server-declared config option surface when available, with session/set_mode
// retained as the compatibility fallback.
type SetControllerModeRequest struct {
	SessionRef session.SessionRef `json:"session_ref,omitempty"`
	Mode       string             `json:"mode,omitempty"`
}

// ControllerStatusProvider is optionally implemented by controller backends
// that expose remote session state to local UI surfaces.
type ControllerStatusProvider interface {
	ControllerStatus(context.Context, session.SessionRef) (ControllerStatus, bool, error)
}

// ControllerConfigurator is optionally implemented by controller backends that
// can update remote session config options.
type ControllerConfigurator interface {
	SetControllerModel(context.Context, SetControllerModelRequest) (ControllerStatus, error)
	SetControllerMode(context.Context, SetControllerModeRequest) (ControllerStatus, error)
}

// Backend is the runtime-facing control-plane contract for ACP-backed main
// controllers and sidecar participants.
type Backend interface {
	Activate(context.Context, HandoffRequest) (session.ControllerBinding, error)
	Deactivate(context.Context, session.SessionRef) error
	RunTurn(context.Context, TurnRequest) (TurnResult, error)
	Attach(context.Context, AttachRequest) (session.ParticipantBinding, error)
	PromptParticipant(context.Context, ParticipantPromptRequest) (TurnResult, error)
	Detach(context.Context, DetachRequest) error
}

func NormalizeAttachRequest(in AttachRequest) AttachRequest {
	out := in
	out.SessionRef = session.NormalizeSessionRef(in.SessionRef)
	out.Session = session.CloneSession(in.Session)
	out.Binding = session.CloneParticipantBinding(in.Binding)
	out.Agent = strings.TrimSpace(in.Agent)
	out.Source = strings.TrimSpace(in.Source)
	out.Label = strings.TrimSpace(in.Label)
	return out
}

func NormalizeDetachRequest(in DetachRequest) DetachRequest {
	out := in
	out.SessionRef = session.NormalizeSessionRef(in.SessionRef)
	out.Session = session.CloneSession(in.Session)
	out.ParticipantID = strings.TrimSpace(in.ParticipantID)
	out.Source = strings.TrimSpace(in.Source)
	return out
}

func NormalizeHandoffRequest(in HandoffRequest) HandoffRequest {
	out := in
	out.SessionRef = session.NormalizeSessionRef(in.SessionRef)
	out.Session = session.CloneSession(in.Session)
	out.Agent = strings.TrimSpace(in.Agent)
	out.Source = strings.TrimSpace(in.Source)
	out.Reason = strings.TrimSpace(in.Reason)
	out.ContextPrelude = strings.TrimSpace(in.ContextPrelude)
	out.ContextSyncSeq = in.ContextSyncSeq
	return out
}

func NormalizeTurnRequest(in TurnRequest) TurnRequest {
	out := in
	out.SessionRef = session.NormalizeSessionRef(in.SessionRef)
	out.Session = session.CloneSession(in.Session)
	out.TurnID = strings.TrimSpace(in.TurnID)
	out.Input = strings.TrimSpace(in.Input)
	if len(in.ContentParts) > 0 {
		out.ContentParts = append([]model.ContentPart(nil), in.ContentParts...)
	}
	out.ContextPrelude = strings.TrimSpace(in.ContextPrelude)
	out.ContextSyncSeq = in.ContextSyncSeq
	out.Mode = strings.TrimSpace(in.Mode)
	return out
}

func NormalizeParticipantPromptRequest(in ParticipantPromptRequest) ParticipantPromptRequest {
	out := in
	out.SessionRef = session.NormalizeSessionRef(in.SessionRef)
	out.Session = session.CloneSession(in.Session)
	out.TurnID = strings.TrimSpace(in.TurnID)
	out.ParticipantID = strings.TrimSpace(in.ParticipantID)
	out.Input = strings.TrimSpace(in.Input)
	out.ContentParts = append([]model.ContentPart(nil), in.ContentParts...)
	out.ContextPrelude = strings.TrimSpace(in.ContextPrelude)
	out.Stream = in.Stream
	out.Mode = strings.TrimSpace(in.Mode)
	return out
}
