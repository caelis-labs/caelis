package controlclient

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// Principal is trusted adapter context. It is never decoded from a command
// body or query parameter.
type Principal struct {
	ID    string   `json:"id"`
	Roles []string `json:"roles,omitempty"`
}

const (
	// RoleSystemSessionRuntime allows a dedicated internal Turn-delivery client
	// to observe a system-managed Session without being its owner. Owner exact-
	// target Reconnect/Inspect remain available without this role so product ACP
	// and Agent-message delivery can follow managed children. Surface tokens must
	// not receive this role, and managed Sessions stay hidden from list/load/resume.
	RoleSystemSessionRuntime = "system-session-runtime"
)

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
	ActionSessionCreate             Action = "session.create"
	ActionSessionClose              Action = "session.close"
	ActionSessionCompact            Action = "session.compact"
	ActionPrompt                    Action = "turn.prompt"
	ActionAgentMessage              Action = "agent_message.deliver"
	ActionSteer                     Action = "turn.steer"
	ActionCancel                    Action = "turn.cancel"
	ActionApprovalResolve           Action = "approval.resolve"
	ActionParticipantAttach         Action = "participant.attach"
	ActionParticipantList           Action = "participant.list"
	ActionParticipantStart          Action = "participant.start"
	ActionParticipantPrompt         Action = "participant.prompt"
	ActionParticipantCancel         Action = "participant.cancel"
	ActionParticipantDetach         Action = "participant.detach"
	ActionControllerHandoff         Action = "controller.handoff"
	ActionSessionList               Action = "session.list"
	ActionSessionInspect            Action = "session.inspect"
	ActionSessionConfigure          Action = "session.configure"
	ActionSessionApprovalMode       Action = "configuration.session.approval-mode"
	ActionSessionModel              Action = "configuration.session.model"
	ActionSessionControllerMode     Action = "configuration.session.controller-mode"
	ActionSessionPresentationMode   Action = "configuration.session.presentation-mode"
	ActionSessionPresentationConfig Action = "configuration.session.presentation-config"
	ActionModelConnect              Action = "configuration.model.connect"
	ActionModelUse                  Action = "configuration.model.use"
	ActionModelDelete               Action = "configuration.model.delete"
	ActionSandboxBackend            Action = "configuration.sandbox.backend"
	ActionSandboxPrepare            Action = "configuration.sandbox.prepare"
	ActionSandboxRepair             Action = "configuration.sandbox.repair"
	ActionSandboxReset              Action = "configuration.sandbox.reset"
	ActionSandboxRefresh            Action = "configuration.sandbox.refresh"
	ActionAgentBindingBind          Action = "configuration.agent-binding.bind"
	ActionAgentBindingReset         Action = "configuration.agent-binding.reset"
	ActionAgentRoleCreate           Action = "configuration.agent-role.create"
	ActionAgentRoleDelete           Action = "configuration.agent-role.delete"
	ActionAgentBindingSetSave       Action = "configuration.agent-binding-set.save"
	ActionAgentBindingSetApply      Action = "configuration.agent-binding-set.apply"
	ActionAgentBindingSetDelete     Action = "configuration.agent-binding-set.delete"
	ActionACPAgentPrepare           Action = "configuration.agent.acp.prepare"
	ActionACPAgentPrepareAuth       Action = "configuration.agent.acp.prepare-auth"
	ActionACPAgentConnect           Action = "configuration.agent.acp.connect"
	ActionACPAgentDisconnect        Action = "configuration.agent.acp.disconnect"
	ActionPluginMarketplaceAdd      Action = "configuration.plugin.marketplace.add"
	ActionPluginMarketplaceUpdate   Action = "configuration.plugin.marketplace.update"
	ActionPluginMarketplaceRemove   Action = "configuration.plugin.marketplace.remove"
	ActionPluginAddPath             Action = "configuration.plugin.add-path"
	ActionPluginInstall             Action = "configuration.plugin.install"
	ActionPluginEnable              Action = "configuration.plugin.enable"
	ActionPluginDisable             Action = "configuration.plugin.disable"
	ActionPluginRemove              Action = "configuration.plugin.remove"
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

// CommandResource identifies one durable product resource created or consumed
// by a command. Ref is opaque to clients; Digest lets a subsequent command
// bind to the exact resource content it observed.
type CommandResource struct {
	Kind   string `json:"kind,omitempty"`
	Ref    string `json:"ref,omitempty"`
	Digest string `json:"digest,omitempty"`
}

const (
	CommandResourceACPPreparation = "acp_preparation"
	CommandResourceModelProfile   = "model_profile"
	CommandResourcePlugin         = "plugin"
	CommandResourceMarketplace    = "plugin_marketplace"
)

type CreateSessionRequest struct {
	WriteBase
	PreferredSessionID string         `json:"preferred_session_id,omitempty"`
	WorkspaceKey       string         `json:"workspace_key,omitempty"`
	CWD                string         `json:"cwd,omitempty"`
	Title              string         `json:"title,omitempty"`
	Metadata           map[string]any `json:"metadata,omitempty"`
}

type CloseSessionRequest struct{ WriteBase }

// CompactSessionRequest starts one manual model-backed checkpoint for the
// addressed Session Runtime.
type CompactSessionRequest struct{ WriteBase }

type PromptRequest struct {
	WriteBase
	Input        string              `json:"input,omitempty"`
	DisplayInput string              `json:"display_input,omitempty"`
	ContentParts []model.ContentPart `json:"content_parts,omitempty"`
}

type SteerRequest struct {
	WriteBase
	Target       TurnTarget          `json:"target"`
	Input        string              `json:"input,omitempty"`
	DisplayInput string              `json:"display_input,omitempty"`
	ContentParts []model.ContentPart `json:"content_parts,omitempty"`
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

// StartParticipantRequest atomically attaches one handle-selected participant
// and starts its first Turn. Handle resolution occurs inside the addressed
// Session Runtime so an active workspace composition cannot observe later Host
// configuration changes.
type StartParticipantRequest struct {
	WriteBase
	Handle         string                  `json:"handle"`
	Role           session.ParticipantRole `json:"role,omitempty"`
	Label          string                  `json:"label,omitempty"`
	Source         string                  `json:"source,omitempty"`
	Input          string                  `json:"input,omitempty"`
	DisplayInput   string                  `json:"display_input,omitempty"`
	DisplayAddress string                  `json:"display_address,omitempty"`
	DisplayTitle   string                  `json:"display_title,omitempty"`
	ContentParts   []model.ContentPart     `json:"content_parts,omitempty"`
	Transient      bool                    `json:"transient,omitempty"`
	DetachSource   string                  `json:"detach_source,omitempty"`
}

type PromptParticipantRequest struct {
	WriteBase
	ParticipantID  string              `json:"participant_id"`
	Input          string              `json:"input,omitempty"`
	DisplayInput   string              `json:"display_input,omitempty"`
	DisplayAddress string              `json:"display_address,omitempty"`
	DisplayTitle   string              `json:"display_title,omitempty"`
	ContentParts   []model.ContentPart `json:"content_parts,omitempty"`
	Source         string              `json:"source,omitempty"`
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
	OperationID   string           `json:"operation_id"`
	Outcome       Outcome          `json:"outcome"`
	SessionID     string           `json:"session_id,omitempty"`
	Revision      uint64           `json:"revision,omitempty"`
	Target        TurnTarget       `json:"target,omitempty"`
	ParticipantID string           `json:"participant_id,omitempty"`
	Resource      *CommandResource `json:"resource,omitempty"`
	Detail        string           `json:"detail,omitempty"`
	ErrorCode     errorcode.Code   `json:"error_code,omitempty"`
	ErrorKind     ErrorKind        `json:"error_kind,omitempty"`
}

// CommandReceiptError preserves a typed mutation receipt when a caller cannot
// safely report success, including a non-terminal outcome or a failed
// post-commit observation. Callers can inspect Receipt and must not retry a
// committed operation merely because the observation failed.
type CommandReceiptError struct {
	Receipt CommandResult
	Err     error
}

func (e *CommandReceiptError) Error() string {
	if e == nil || e.Err == nil {
		return "controlclient: command receipt error"
	}
	return e.Err.Error()
}

func (e *CommandReceiptError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// CommandBackend executes already-authorized request-scoped commands.
type CommandBackend interface {
	ExecuteControlCommand(context.Context, Principal, Action, any) (CommandResult, error)
}

// CommandRecoveryBackend may prove a result from domain-owned durable evidence
// after the operation ledger contains only its intent. Recovery must be
// observational: it must never repeat an external or durable effect. The
// capability allowlist is evaluated from Control's canonical Action before
// the operation intent is created; unknown actions must return false.
type CommandRecoveryBackend interface {
	CanRecoverControlCommand(Action) bool
	RecoverControlCommand(context.Context, Principal, OperationIntent, any) (CommandResult, bool, error)
}

// CommandClient is the complete transport-neutral Control write contract.
type CommandClient interface {
	CreateSession(context.Context, Principal, CreateSessionRequest) (CommandResult, error)
	CloseSession(context.Context, Principal, CloseSessionRequest) (CommandResult, error)
	CompactSession(context.Context, Principal, CompactSessionRequest) (CommandResult, error)
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

// ParticipantStarter is the principal-aware server-side capability used to
// assemble the focused ParticipantClient exposed by AppServer transports.
type ParticipantStarter interface {
	StartParticipant(context.Context, Principal, StartParticipantRequest) (CommandResult, error)
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

func commandOutcomeUnknown(result CommandResult, err error) bool {
	if result.Outcome == OutcomeUnknown {
		return true
	}
	var outcomeErr *OutcomeError
	if errors.As(err, &outcomeErr) && outcomeErr.Outcome == OutcomeUnknown {
		return true
	}
	return errorcode.CodeOf(err) == errorcode.UnknownOutcome
}

func commandReceiptOutcome(result CommandResult, err error) Outcome {
	if result.Outcome.Valid() {
		return result.Outcome
	}
	var outcomeErr *OutcomeError
	if errors.As(err, &outcomeErr) && outcomeErr.Outcome.Valid() {
		return outcomeErr.Outcome
	}
	if errorcode.CodeOf(err) == errorcode.UnknownOutcome {
		return OutcomeUnknown
	}
	return ""
}

// commandMutationError normalizes transport and in-process command behavior.
// The in-process service may replay a persisted rejected result with a nil Go
// error, while HTTP clients return an OutcomeError for the same receipt.
func commandMutationError(result CommandResult, err error) error {
	outcome := commandReceiptOutcome(result, err)
	if err == nil && (outcome == OutcomeAccepted || outcome == OutcomeCommitted) {
		return nil
	}
	if err == nil {
		detail := strings.TrimSpace(result.Detail)
		if detail == "" {
			if outcome == "" {
				detail = "command returned no terminal outcome"
			} else {
				detail = string(outcome)
			}
		}
		if outcome == "" {
			outcome = OutcomeUnknown
			result.Outcome = outcome
		}
		err = NewOutcomeError(outcome, errors.New(detail))
	}
	return &CommandReceiptError{Receipt: result, Err: err}
}

func unknownTurnAdmissionError(result CommandResult, err error) error {
	if err == nil {
		err = errorcode.New(errorcode.UnknownOutcome, "controlclient: turn admission outcome cannot be proven")
	}
	if result.Outcome == "" {
		result.Outcome = OutcomeUnknown
	}
	return &CommandReceiptError{Receipt: result, Err: err}
}
