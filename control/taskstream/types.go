package taskstream

import (
	"context"
	"errors"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
)

// Principal is trusted Control-host context. Transport adapters construct it
// from their authenticated principal; it is never decoded from a request body.
type Principal struct {
	ID    string
	Roles []string
}

// ResumeMode describes whether a Task subscription retained the requested
// process-local observation boundary.
type ResumeMode string

const (
	ResumeModeExact        ResumeMode = "exact"
	ResumeModeCurrentState ResumeMode = "current_state"
)

var ErrSlowConsumer = errors.New("taskstream: slow consumer")

// ParentTool identifies the canonical parent tool call for one Task.
type ParentTool struct {
	ToolCallID string `json:"tool_call_id,omitempty"`
	ToolName   string `json:"tool_name,omitempty"`
}

// TaskDescriptor is the durable discovery and current-state view of a
// streamable Task. It intentionally excludes transient output bodies.
type TaskDescriptor struct {
	SessionID      string     `json:"session_id"`
	TaskID         string     `json:"task_id"`
	Handle         string     `json:"handle"`
	AgentHandle    string     `json:"agent_handle,omitempty"`
	Kind           task.Kind  `json:"kind"`
	Title          string     `json:"title,omitempty"`
	State          task.State `json:"state"`
	Running        bool       `json:"running"`
	SupportsInput  bool       `json:"supports_input,omitempty"`
	SupportsCancel bool       `json:"supports_cancel,omitempty"`
	ParentTool     ParentTool `json:"parent_tool,omitempty"`
	ParticipantID  string     `json:"participant_id,omitempty"`
	CurrentTurnID  string     `json:"current_turn_id,omitempty"`
	UpdatedAt      time.Time  `json:"updated_at,omitempty"`
}

type ListRequest struct {
	SessionID string `json:"session_id"`
}

type ListResult struct {
	Tasks []TaskDescriptor `json:"tasks,omitempty"`
}

type ReadRequest struct {
	SessionID string `json:"session_id"`
	TaskID    string `json:"task_id"`
	Cursor    string `json:"cursor,omitempty"`
}

type SubscribeRequest = ReadRequest

type Batch struct {
	Records        []Record   `json:"records,omitempty"`
	ResumeMode     ResumeMode `json:"resume_mode"`
	TransientGap   bool       `json:"transient_gap,omitempty"`
	BoundaryCursor string     `json:"boundary_cursor,omitempty"`
}

// Gap marks transient Task output that is no longer observable.
type Gap struct {
	SessionID string     `json:"session_id"`
	TaskID    string     `json:"task_id"`
	Kind      task.Kind  `json:"kind"`
	State     task.State `json:"state"`
}

// Record is one cursor-stamped Task frame or gap. ACP projection belongs to
// protocol/acp; Control owns authorization, cursor binding, and fan-out.
type Record struct {
	Cursor     string         `json:"cursor"`
	Generation string         `json:"generation"`
	Sequence   uint64         `json:"sequence"`
	Task       TaskDescriptor `json:"task"`
	Frame      *stream.Frame  `json:"frame,omitempty"`
	Gap        *Gap           `json:"gap,omitempty"`
}

// Subscription owns only client delivery; closing it never cancels the Task.
type Subscription interface {
	Records() <-chan Record
	Close() error
	Err() error
	LastCursor() string
}

type SubscribeResult struct {
	Subscription   Subscription `json:"-"`
	ResumeMode     ResumeMode   `json:"resume_mode"`
	TransientGap   bool         `json:"transient_gap,omitempty"`
	BoundaryCursor string       `json:"boundary_cursor,omitempty"`
}

// Service is the coherent Control boundary consumed by Task-aware Surfaces.
type Service interface {
	List(context.Context, Principal, ListRequest) (ListResult, error)
	Events(context.Context, Principal, ReadRequest) (Batch, error)
	Subscribe(context.Context, Principal, SubscribeRequest) (SubscribeResult, error)
}
