package task

import (
	"context"
	"fmt"
	"strings"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/internal/jsonvalue"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
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
	StatePrepared        State = "prepared"
	StateRunning         State = "running"
	StateWaitingInput    State = "waiting_input"
	StateCompleted       State = "completed"
	StateFailed          State = "failed"
	StateCancelled       State = "cancelled"
	StateInterrupted     State = "interrupted"
	StateTerminated      State = "terminated"
	StateWaitingApproval State = "waiting_approval"
	StateUnknownOutcome  State = "unknown_outcome"
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
	Revision       uint64              `json:"revision,omitempty"`
	Kind           Kind                `json:"kind,omitempty"`
	Title          string              `json:"title,omitempty"`
	State          State               `json:"state,omitempty"`
	Running        bool                `json:"running,omitempty"`
	SupportsInput  bool                `json:"supports_input,omitempty"`
	SupportsCancel bool                `json:"supports_cancel,omitempty"`
	CreatedAt      time.Time           `json:"created_at,omitempty"`
	UpdatedAt      time.Time           `json:"updated_at,omitempty"`
	Lease          Lease               `json:"lease,omitempty"`
	StdoutCursor   int64               `json:"stdout_cursor,omitempty"`
	StderrCursor   int64               `json:"stderr_cursor,omitempty"`
	EventCursor    int64               `json:"event_cursor,omitempty"`
	Result         map[string]any      `json:"result,omitempty"`
	Metadata       map[string]any      `json:"metadata,omitempty"`
	Terminal       sandbox.TerminalRef `json:"terminal,omitempty"`
}

// Lease is a neutral task-worker ownership record. Core stores enforce its CAS
// semantics; Control owns worker placement and renewal policy.
type Lease struct {
	ID          string    `json:"id,omitempty"`
	OwnerID     string    `json:"owner_id,omitempty"`
	Revision    uint64    `json:"revision,omitempty"`
	AcquiredAt  time.Time `json:"acquired_at,omitempty"`
	HeartbeatAt time.Time `json:"heartbeat_at,omitempty"`
	ExpiresAt   time.Time `json:"expires_at,omitempty"`
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

// SubagentStartRequest defines one yielded SPAWN launch request. SpawnID is the
// stable operation identity used for durable intent, retry, and restart
// recovery; callers that may retry must preserve it.
type SubagentStartRequest struct {
	SpawnID        string                          `json:"spawn_id,omitempty"`
	Agent          string                          `json:"agent,omitempty"`
	Prompt         string                          `json:"prompt,omitempty"`
	ContextPrelude string                          `json:"context_prelude,omitempty"`
	ParentCall     string                          `json:"parent_call,omitempty"`
	ParentTool     string                          `json:"parent_tool,omitempty"`
	Source         string                          `json:"source,omitempty"`
	Mode           string                          `json:"mode,omitempty"`
	ApprovalMode   string                          `json:"approval_mode,omitempty"`
	Approval       agent.SubagentApprovalRequester `json:"-"`
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
	Revision       uint64              `json:"revision,omitempty"`
	Kind           Kind                `json:"kind,omitempty"`
	Session        session.SessionRef  `json:"session,omitempty"`
	Title          string              `json:"title,omitempty"`
	State          State               `json:"state,omitempty"`
	Running        bool                `json:"running,omitempty"`
	SupportsInput  bool                `json:"supports_input,omitempty"`
	SupportsCancel bool                `json:"supports_cancel,omitempty"`
	CreatedAt      time.Time           `json:"created_at,omitempty"`
	UpdatedAt      time.Time           `json:"updated_at,omitempty"`
	Lease          Lease               `json:"lease,omitempty"`
	StdoutCursor   int64               `json:"stdout_cursor,omitempty"`
	StderrCursor   int64               `json:"stderr_cursor,omitempty"`
	EventCursor    int64               `json:"event_cursor,omitempty"`
	Spec           map[string]any      `json:"spec,omitempty"`
	Result         map[string]any      `json:"result,omitempty"`
	Metadata       map[string]any      `json:"metadata,omitempty"`
	Terminal       sandbox.TerminalRef `json:"terminal,omitempty"`
}

// Store persists task records for one owning session. Upsert callers that have
// not yet observed the canonical tool-result payload should sanitize entries
// with ResultPersistenceDeferred before persisting them; store implementations
// may still enforce ResultPersistenceCanonical to strip transient display data
// at the storage boundary.
type Store interface {
	Upsert(context.Context, *Entry) error
	Get(context.Context, string) (*Entry, error)
	ListSession(context.Context, session.SessionRef) ([]*Entry, error)
	GetSessionTaskByHandle(context.Context, session.SessionRef, Kind, string) (*Entry, error)
}

// PutRequest conditionally creates or replaces one task entry. Revision zero
// is the expected revision for a create.
type PutRequest struct {
	Entry            *Entry `json:"entry"`
	ExpectedRevision uint64 `json:"expected_revision"`
}

// CASStore is the optional lost-update-safe task persistence capability.
type CASStore interface {
	Put(context.Context, PutRequest) (*Entry, error)
}

// RevisionConflictError reports a stale task writer.
type RevisionConflictError struct {
	TaskID   string
	Expected uint64
	Actual   uint64
}

func (e *RevisionConflictError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("agent-sdk/task: task %q revision conflict: expected %d, actual %d", strings.TrimSpace(e.TaskID), e.Expected, e.Actual)
}

func (e *RevisionConflictError) ErrorCode() errorcode.Code { return errorcode.Conflict }

// AcquireLeaseRequest conditionally assigns one task worker lease.
type AcquireLeaseRequest struct {
	TaskID               string        `json:"task_id"`
	OwnerID              string        `json:"owner_id"`
	LeaseID              string        `json:"lease_id"`
	ExpectedTaskRevision uint64        `json:"expected_task_revision"`
	Now                  time.Time     `json:"now"`
	TTL                  time.Duration `json:"ttl"`
}

// HeartbeatLeaseRequest conditionally renews one task worker lease.
type HeartbeatLeaseRequest struct {
	TaskID                string        `json:"task_id"`
	OwnerID               string        `json:"owner_id"`
	LeaseID               string        `json:"lease_id"`
	ExpectedTaskRevision  uint64        `json:"expected_task_revision"`
	ExpectedLeaseRevision uint64        `json:"expected_lease_revision"`
	Now                   time.Time     `json:"now"`
	TTL                   time.Duration `json:"ttl"`
}

// ReleaseLeaseRequest conditionally releases one task worker lease.
type ReleaseLeaseRequest struct {
	TaskID                string `json:"task_id"`
	OwnerID               string `json:"owner_id"`
	LeaseID               string `json:"lease_id"`
	ExpectedTaskRevision  uint64 `json:"expected_task_revision"`
	ExpectedLeaseRevision uint64 `json:"expected_lease_revision"`
}

// LeaseStore is the optional task lease/heartbeat CAS capability.
type LeaseStore interface {
	AcquireLease(context.Context, AcquireLeaseRequest) (*Entry, error)
	HeartbeatLease(context.Context, HeartbeatLeaseRequest) (*Entry, error)
	ReleaseLease(context.Context, ReleaseLeaseRequest) (*Entry, error)
}

// LeaseConflictError reports a stale or non-owning lease mutation.
type LeaseConflictError struct {
	TaskID string
	Detail string
}

func (e *LeaseConflictError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("agent-sdk/task: task %q lease conflict: %s", strings.TrimSpace(e.TaskID), strings.TrimSpace(e.Detail))
}

func (e *LeaseConflictError) ErrorCode() errorcode.Code { return errorcode.Conflict }

// ResultPersistenceMode controls which task result fields are allowed in a
// durable task index entry.
type ResultPersistenceMode int

const (
	// ResultPersistenceCanonical preserves canonical final result fields while
	// removing transient display/stream fields.
	ResultPersistenceCanonical ResultPersistenceMode = iota
	// ResultPersistenceDeferred is used after task completion but before the
	// agent loop has synced the canonical tool-result payload into the index.
	ResultPersistenceDeferred
)

var (
	transientResultKeys     = []string{"stdout", "stderr", "output", "text", "latest_output", "output_preview"}
	deferredFinalResultKeys = []string{"result", "final_message"}
)

// TransientResultKeys returns task result keys that are display-only and must
// not be persisted in durable task indexes.
func TransientResultKeys() []string {
	return append([]string(nil), transientResultKeys...)
}

// NormalizeHandle returns the canonical comparable form of a task handle.
func NormalizeHandle(value string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(value), "@"))
}

// SanitizeResultForPersistence returns a copy of result with non-durable fields
// removed for the requested persistence mode.
func SanitizeResultForPersistence(result map[string]any, mode ResultPersistenceMode) map[string]any {
	if result == nil {
		return nil
	}
	out := jsonvalue.CloneMap(result)
	for _, key := range transientResultKeys {
		delete(out, key)
	}
	if mode == ResultPersistenceDeferred {
		for _, key := range deferredFinalResultKeys {
			delete(out, key)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// SanitizeEntryForPersistence returns a cloned entry with a sanitized Result.
func SanitizeEntryForPersistence(entry *Entry, mode ResultPersistenceMode) *Entry {
	out := CloneEntry(entry)
	if out == nil {
		return nil
	}
	out.Result = SanitizeResultForPersistence(out.Result, mode)
	return out
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
	out.Result = jsonvalue.CloneMap(in.Result)
	out.Metadata = jsonvalue.CloneMap(in.Metadata)
	out.Lease = CloneLease(in.Lease)
	out.Terminal = sandbox.CloneTerminalRef(in.Terminal)
	return out
}

// CloneLease returns one normalized lease copy.
func CloneLease(in Lease) Lease {
	in.ID = strings.TrimSpace(in.ID)
	in.OwnerID = strings.TrimSpace(in.OwnerID)
	return in
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
	out.Lease = CloneLease(in.Lease)
	out.Spec = jsonvalue.CloneMap(in.Spec)
	out.Result = jsonvalue.CloneMap(in.Result)
	out.Metadata = jsonvalue.CloneMap(in.Metadata)
	out.Terminal = sandbox.CloneTerminalRef(in.Terminal)
	return &out
}
