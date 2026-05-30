// Package runtime defines the small orchestration contract shared by Caelis
// surfaces, app services, and protocol adapters.
package runtime

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/core/model"
	"github.com/OnslaughtSnail/caelis/core/session"
)

var ErrNoActiveTurn = errors.New("core/runtime: no active turn")

const (
	SessionModeAutoReview = "auto-review"
	SessionModeManual     = "manual"
)

func NormalizeSessionMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "manual":
		return SessionModeManual
	case "", "auto", "auto-review", "auto_review", "autoreview":
		return SessionModeAutoReview
	default:
		return ""
	}
}

type Engine interface {
	StartSession(context.Context, session.StartRequest) (session.Session, error)
	ListSessions(context.Context, session.ListQuery) (session.SessionPage, error)
	LoadSession(context.Context, session.Ref) (session.Snapshot, error)
	RecordEvents(context.Context, session.Ref, []session.Event) (session.Cursor, error)
	UpdateSessionState(context.Context, session.Ref, session.StatePatch) error
	BeginTurn(context.Context, TurnRequest) (Turn, error)
	Interrupt(context.Context, session.Ref) error
	Replay(context.Context, ReplayRequest) (<-chan EventEnvelope, error)
}

type TurnRequest struct {
	SessionRef   session.Ref           `json:"session_ref"`
	Input        string                `json:"input,omitempty"`
	ContentParts []model.ContentPart   `json:"content_parts,omitempty"`
	Instructions []string              `json:"instructions,omitempty"`
	Model        string                `json:"model,omitempty"`
	Reasoning    model.ReasoningConfig `json:"reasoning,omitempty"`
	Surface      string                `json:"surface,omitempty"`
	Mode         string                `json:"mode,omitempty"`
	Meta         map[string]any        `json:"meta,omitempty"`
}

type ReplayRequest struct {
	SessionRef       session.Ref    `json:"session_ref"`
	After            session.Cursor `json:"after,omitempty"`
	Limit            int            `json:"limit,omitempty"`
	IncludeTransient bool           `json:"include_transient,omitempty"`
}

type EventEnvelope struct {
	Cursor session.Cursor `json:"cursor,omitempty"`
	Event  session.Event  `json:"event,omitempty"`
	Err    string         `json:"err,omitempty"`
}

type SubmissionKind string

const (
	SubmissionConversation SubmissionKind = "conversation"
	SubmissionApproval     SubmissionKind = "approval"
)

type ApprovalDecision struct {
	Outcome  string `json:"outcome,omitempty"`
	OptionID string `json:"option_id,omitempty"`
	Approved bool   `json:"approved,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

type Submission struct {
	Kind         SubmissionKind      `json:"kind,omitempty"`
	Text         string              `json:"text,omitempty"`
	ContentParts []model.ContentPart `json:"content_parts,omitempty"`
	Approval     *ApprovalDecision   `json:"approval,omitempty"`
	Meta         map[string]any      `json:"meta,omitempty"`
}

type CancelStatus string

const (
	CancelCancelled        CancelStatus = "cancelled"
	CancelAlreadyCancelled CancelStatus = "already_cancelled"
)

type CancelResult struct {
	Status CancelStatus `json:"status,omitempty"`
	Err    error        `json:"-"`
}

func (r CancelResult) Cancelled() bool {
	return r.Status == CancelCancelled
}

type Turn interface {
	ID() string
	RunID() string
	SessionRef() session.Ref
	StartedAt() time.Time
	Events() <-chan EventEnvelope
	Submit(context.Context, Submission) error
	Cancel() CancelResult
	Close() error
}

type Agent interface {
	Name() string
	Run(Context) (<-chan session.Event, <-chan error)
}

type Context interface {
	context.Context
	Session() session.Session
	Events() []session.Event
	State() session.State
	DrainSubmissions() []Submission
}
