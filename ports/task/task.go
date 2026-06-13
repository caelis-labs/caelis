package task

import (
	"context"
	"maps"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/ports/sandbox"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/ports/subagent"
)

// Kind identifies one durable task family.
type Kind string

const (
	KindCommand  Kind = "command"
	KindSubagent Kind = "subagent"
)

// State identifies one task lifecycle state.
type State string

const (
	StateRunning         State = "running"
	StateWaitingInput    State = "waiting_input"
	StateCompleted       State = "completed"
	StateFailed          State = "failed"
	StateCancelled       State = "cancelled"
	StateInterrupted     State = "interrupted"
	StateTerminated      State = "terminated"
	StateWaitingApproval State = "waiting_approval"
)

// Ref identifies one task in one owning session.
type Ref struct {
	TaskID     string `json:"task_id,omitempty"`
	SessionID  string `json:"session_id,omitempty"`
	TerminalID string `json:"terminal_id,omitempty"`
}

// Snapshot is one provider-neutral task status payload.
type Snapshot struct {
	Ref            Ref                 `json:"ref,omitempty"`
	Kind           Kind                `json:"kind,omitempty"`
	Title          string              `json:"title,omitempty"`
	State          State               `json:"state,omitempty"`
	Running        bool                `json:"running,omitempty"`
	SupportsInput  bool                `json:"supports_input,omitempty"`
	SupportsCancel bool                `json:"supports_cancel,omitempty"`
	CreatedAt      time.Time           `json:"created_at,omitempty"`
	UpdatedAt      time.Time           `json:"updated_at,omitempty"`
	StdoutCursor   int64               `json:"stdout_cursor,omitempty"`
	StderrCursor   int64               `json:"stderr_cursor,omitempty"`
	EventCursor    int64               `json:"event_cursor,omitempty"`
	Result         map[string]any      `json:"result,omitempty"`
	Metadata       map[string]any      `json:"metadata,omitempty"`
	Terminal       sandbox.TerminalRef `json:"terminal,omitempty"`
}

// Observer receives transient task lifecycle snapshots while a tool call is
// still running. Observed snapshots are adapter-facing and are not appended to
// model-visible tool history.
type Observer interface {
	ObserveTaskSnapshot(Snapshot)
}

// CommandStartRequest defines one yielded RUN_COMMAND launch request.
type CommandStartRequest struct {
	Command     string        `json:"command,omitempty"`
	Workdir     string        `json:"workdir,omitempty"`
	Yield       time.Duration `json:"yield,omitempty"`
	Timeout     time.Duration `json:"timeout,omitempty"`
	ParentCall  string        `json:"parent_call,omitempty"`
	ParentTool  string        `json:"parent_tool,omitempty"`
	Constraints any           `json:"-"`
	Observer    Observer      `json:"-"`
}

// SubagentStartRequest defines one yielded SPAWN launch request.
type SubagentStartRequest struct {
	Agent          string                     `json:"agent,omitempty"`
	Prompt         string                     `json:"prompt,omitempty"`
	ContextPrelude string                     `json:"context_prelude,omitempty"`
	ParentCall     string                     `json:"parent_call,omitempty"`
	ParentTool     string                     `json:"parent_tool,omitempty"`
	Source         string                     `json:"source,omitempty"`
	Mode           string                     `json:"mode,omitempty"`
	ApprovalMode   string                     `json:"approval_mode,omitempty"`
	Approval       subagent.ApprovalRequester `json:"-"`
}

// ControlRequest defines one task control request.
type ControlRequest struct {
	TaskID         string        `json:"task_id,omitempty"`
	Yield          time.Duration `json:"yield,omitempty"`
	Input          string        `json:"input,omitempty"`
	Source         string        `json:"source,omitempty"`
	ContextPrelude string        `json:"context_prelude,omitempty"`
}

// Entry is one durable task persistence record.
type Entry struct {
	TaskID         string              `json:"task_id,omitempty"`
	Kind           Kind                `json:"kind,omitempty"`
	Session        session.SessionRef  `json:"session,omitempty"`
	Title          string              `json:"title,omitempty"`
	State          State               `json:"state,omitempty"`
	Running        bool                `json:"running,omitempty"`
	SupportsInput  bool                `json:"supports_input,omitempty"`
	SupportsCancel bool                `json:"supports_cancel,omitempty"`
	CreatedAt      time.Time           `json:"created_at,omitempty"`
	UpdatedAt      time.Time           `json:"updated_at,omitempty"`
	HeartbeatAt    time.Time           `json:"heartbeat_at,omitempty"`
	StdoutCursor   int64               `json:"stdout_cursor,omitempty"`
	StderrCursor   int64               `json:"stderr_cursor,omitempty"`
	EventCursor    int64               `json:"event_cursor,omitempty"`
	Spec           map[string]any      `json:"spec,omitempty"`
	Result         map[string]any      `json:"result,omitempty"`
	Metadata       map[string]any      `json:"metadata,omitempty"`
	Terminal       sandbox.TerminalRef `json:"terminal,omitempty"`
}

// Store persists task records for one owning session.
type Store interface {
	Upsert(context.Context, *Entry) error
	Get(context.Context, string) (*Entry, error)
	ListSession(context.Context, session.SessionRef) ([]*Entry, error)
}

// Manager is the runtime-owned task control surface used by yielded tools.
type Manager interface {
	StartCommand(context.Context, CommandStartRequest) (Snapshot, error)
	Wait(context.Context, ControlRequest) (Snapshot, error)
	Write(context.Context, ControlRequest) (Snapshot, error)
	Cancel(context.Context, ControlRequest) (Snapshot, error)
}

// CloneRef returns one normalized task ref copy.
func CloneRef(in Ref) Ref {
	return Ref{
		TaskID:     strings.TrimSpace(in.TaskID),
		SessionID:  strings.TrimSpace(in.SessionID),
		TerminalID: strings.TrimSpace(in.TerminalID),
	}
}

// CloneSnapshot returns one normalized task snapshot copy.
func CloneSnapshot(in Snapshot) Snapshot {
	out := in
	out.Ref = CloneRef(in.Ref)
	out.Title = strings.TrimSpace(in.Title)
	out.Result = maps.Clone(in.Result)
	out.Metadata = maps.Clone(in.Metadata)
	out.Terminal = sandbox.CloneTerminalRef(in.Terminal)
	return out
}

// CloneEntry returns one normalized task entry copy.
func CloneEntry(in *Entry) *Entry {
	if in == nil {
		return nil
	}
	out := *in
	out.TaskID = strings.TrimSpace(in.TaskID)
	out.Kind = Kind(strings.TrimSpace(string(in.Kind)))
	out.Session = session.NormalizeSessionRef(in.Session)
	out.Title = strings.TrimSpace(in.Title)
	out.State = State(strings.TrimSpace(string(in.State)))
	out.Spec = maps.Clone(in.Spec)
	out.Result = maps.Clone(in.Result)
	out.Metadata = maps.Clone(in.Metadata)
	out.Terminal = sandbox.CloneTerminalRef(in.Terminal)
	return &out
}
