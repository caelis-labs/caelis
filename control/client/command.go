package controlclient

import (
	"context"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// Principal is trusted adapter context. It is never decoded from a command
// body or query parameter.
type Principal struct {
	ID    string   `json:"id"`
	Roles []string `json:"roles,omitempty"`
}

// HasRole reports whether the principal has role, ignoring surrounding
// whitespace and case.
func (p Principal) HasRole(role string) bool {
	role = strings.TrimSpace(role)
	if role == "" {
		return false
	}
	for _, assigned := range p.Roles {
		if strings.EqualFold(strings.TrimSpace(assigned), role) {
			return true
		}
	}
	return false
}

type Action string

const (
	ActionSessionCreate     Action = "session.create"
	ActionSessionClose      Action = "session.close"
	ActionPrompt            Action = "turn.prompt"
	ActionSteer             Action = "turn.steer"
	ActionCancel            Action = "turn.cancel"
	ActionApprovalResolve   Action = "approval.resolve"
	ActionParticipantAttach Action = "participant.attach"
	ActionParticipantPrompt Action = "participant.prompt"
	ActionParticipantCancel Action = "participant.cancel"
	ActionParticipantDetach Action = "participant.detach"
	ActionControllerHandoff Action = "controller.handoff"
	ActionSessionList       Action = "session.list"
	ActionSessionInspect    Action = "session.inspect"
)

type Outcome string

const (
	OutcomeAccepted   Outcome = "accepted"
	OutcomeCommitted  Outcome = "committed"
	OutcomeConflicted Outcome = "conflicted"
	OutcomeRejected   Outcome = "rejected"
	OutcomeUnknown    Outcome = "unknown"
)

// OutcomeError lets a backend classify recovery without transport coupling.
type OutcomeError struct {
	Outcome Outcome
	Err     error
}

func (e *OutcomeError) Error() string {
	if e == nil || e.Err == nil {
		return string(e.Outcome)
	}
	return e.Err.Error()
}

func (e *OutcomeError) Unwrap() error { return e.Err }

// WriteBase is required on every mutating request.
type WriteBase struct {
	OperationID             string  `json:"operation_id"`
	SessionID               string  `json:"session_id,omitempty"`
	ExpectedRevision        *uint64 `json:"expected_revision,omitempty"`
	ExpectedControllerEpoch string  `json:"expected_controller_epoch,omitempty"`
}

type TurnTarget struct {
	HandleID string `json:"handle_id,omitempty"`
	RunID    string `json:"run_id,omitempty"`
	TurnID   string `json:"turn_id,omitempty"`
}

type CreateSessionRequest struct {
	WriteBase
	PreferredSessionID string         `json:"preferred_session_id,omitempty"`
	WorkspaceKey       string         `json:"workspace_key,omitempty"`
	CWD                string         `json:"cwd,omitempty"`
	Title              string         `json:"title,omitempty"`
	Metadata           map[string]any `json:"metadata,omitempty"`
}

type CloseSessionRequest struct{ WriteBase }

type PromptRequest struct {
	WriteBase
	Input        string `json:"input"`
	DisplayInput string `json:"display_input,omitempty"`
}

type SteerRequest struct {
	WriteBase
	Target       TurnTarget `json:"target"`
	Input        string     `json:"input"`
	DisplayInput string     `json:"display_input,omitempty"`
}

type CancelRequest struct {
	WriteBase
	Target TurnTarget `json:"target"`
	Reason string     `json:"reason,omitempty"`
}

type ResolveApprovalRequest struct {
	WriteBase
	Target            TurnTarget `json:"target"`
	ApprovalRequestID string     `json:"approval_request_id"`
	Outcome           string     `json:"outcome"`
	OptionID          string     `json:"option_id,omitempty"`
	Approved          bool       `json:"approved"`
	Reason            string     `json:"reason,omitempty"`
	ReviewText        string     `json:"review_text,omitempty"`
}

type AttachParticipantRequest struct {
	WriteBase
	ProfileID string                  `json:"profile_id"`
	Effort    string                  `json:"effort"`
	Role      session.ParticipantRole `json:"role,omitempty"`
	Label     string                  `json:"label,omitempty"`
	Source    string                  `json:"source,omitempty"`
}

type PromptParticipantRequest struct {
	WriteBase
	ParticipantID string `json:"participant_id"`
	Input         string `json:"input"`
	DisplayInput  string `json:"display_input,omitempty"`
}

type CancelParticipantRequest struct {
	WriteBase
	ParticipantID string     `json:"participant_id"`
	Target        TurnTarget `json:"target"`
	Reason        string     `json:"reason,omitempty"`
}

type DetachParticipantRequest struct {
	WriteBase
	ParticipantID string `json:"participant_id"`
	Source        string `json:"source,omitempty"`
}

type HandoffRequest struct {
	WriteBase
	Kind   session.ControllerKind `json:"kind"`
	Agent  string                 `json:"agent,omitempty"`
	Source string                 `json:"source,omitempty"`
	Reason string                 `json:"reason,omitempty"`
}

// CommandResult is the typed recovery result persisted by the operation ledger.
type CommandResult struct {
	OperationID string     `json:"operation_id"`
	Outcome     Outcome    `json:"outcome"`
	SessionID   string     `json:"session_id,omitempty"`
	Revision    uint64     `json:"revision,omitempty"`
	Target      TurnTarget `json:"target,omitempty"`
	Detail      string     `json:"detail,omitempty"`
}

// CommandBackend executes already-authorized request-scoped commands.
type CommandBackend interface {
	ExecuteControlCommand(context.Context, Principal, Action, any) (CommandResult, error)
}

// CommandClient is the complete transport-neutral M2 write contract.
type CommandClient interface {
	CreateSession(context.Context, Principal, CreateSessionRequest) (CommandResult, error)
	CloseSession(context.Context, Principal, CloseSessionRequest) (CommandResult, error)
	Prompt(context.Context, Principal, PromptRequest) (CommandResult, error)
	Steer(context.Context, Principal, SteerRequest) (CommandResult, error)
	Cancel(context.Context, Principal, CancelRequest) (CommandResult, error)
	ResolveApproval(context.Context, Principal, ResolveApprovalRequest) (CommandResult, error)
	AttachParticipant(context.Context, Principal, AttachParticipantRequest) (CommandResult, error)
	PromptParticipant(context.Context, Principal, PromptParticipantRequest) (CommandResult, error)
	CancelParticipant(context.Context, Principal, CancelParticipantRequest) (CommandResult, error)
	DetachParticipant(context.Context, Principal, DetachParticipantRequest) (CommandResult, error)
	Handoff(context.Context, Principal, HandoffRequest) (CommandResult, error)
}

func (o Outcome) Valid() bool {
	switch o {
	case OutcomeAccepted, OutcomeCommitted, OutcomeConflicted, OutcomeRejected, OutcomeUnknown:
		return true
	default:
		return false
	}
}

func NewOutcomeError(outcome Outcome, err error) error {
	if !outcome.Valid() {
		return fmt.Errorf("controlclient: invalid outcome %q: %w", outcome, err)
	}
	return &OutcomeError{Outcome: outcome, Err: err}
}
