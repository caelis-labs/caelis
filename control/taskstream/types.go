package taskstream

import (
	"context"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/task"
)

// Principal is trusted Control-host context. Transport adapters construct it
// from their authenticated principal; it is never decoded from a request body.
type Principal struct {
	ID    string
	Roles []string
}

type SourceClass string

const (
	SourceExact       SourceClass = "exact"
	SourceReplacement SourceClass = "replacement"
	SourceStatus      SourceClass = "status"
)

type DeliveryKind string

const (
	DeliveryReplaceBegin DeliveryKind = "replace_begin"
	DeliveryReplacePage  DeliveryKind = "replace_page"
	DeliveryReplaceEnd   DeliveryKind = "replace_end"
	DeliveryAppendPage   DeliveryKind = "append_page"
	DeliveryStatus       DeliveryKind = "status"
)

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
	ActivityID     string     `json:"activity_id,omitempty"`
	CurrentTurnID  string     `json:"current_turn_id,omitempty"`
	UpdatedAt      time.Time  `json:"updated_at,omitempty"`
}

type ListRequest struct {
	SessionID string `json:"session_id"`
}

type ListResult struct {
	Tasks []TaskDescriptor `json:"tasks,omitempty"`
}

// DirectoryWatchRequest selects the lightweight current-state Task directory
// for one Session. It has no Task content cursor or replay semantics.
type DirectoryWatchRequest struct {
	SessionID string `json:"session_id"`
}

// DirectorySnapshot is the latest complete Task status index for one Session.
// Revision is process-local and monotonic only while at least one observer is
// attached; reconnecting observers always receive a fresh complete snapshot.
type DirectorySnapshot struct {
	Revision uint64           `json:"revision"`
	Tasks    []TaskDescriptor `json:"tasks,omitempty"`
}

// DirectorySubscription delivers replaceable current-state snapshots. A slow
// observer may skip intermediate revisions but always converges on the newest
// complete directory without affecting Task execution or content streams.
type DirectorySubscription interface {
	Snapshots() <-chan DirectorySnapshot
	Close() error
	Err() error
}

type DirectoryWatchResult struct {
	Subscription DirectorySubscription `json:"-"`
}

type ReadRequest struct {
	SessionID          string `json:"session_id"`
	TaskID             string `json:"task_id"`
	Cursor             string `json:"cursor,omitempty"`
	ExpectedActivityID string `json:"expected_activity_id,omitempty"`
}

type SubscribeRequest struct {
	SessionID string `json:"session_id"`
	TaskID    string `json:"task_id"`
	Cursor    string `json:"cursor,omitempty"`
	// Follow keeps a subagent Task timeline attached across activity periods
	// until the observer closes it. It never changes Task lifecycle.
	Follow bool `json:"follow,omitempty"`
}

type Delivery struct {
	Kind       DeliveryKind `json:"kind"`
	Source     SourceClass  `json:"source"`
	SnapshotID string       `json:"snapshot_id,omitempty"`
	Page       uint32       `json:"page,omitempty"`
	Records    []Record     `json:"records,omitempty"`
	NextCursor string       `json:"next_cursor,omitempty"`
	ActivityID string       `json:"activity_id,omitempty"`
}

// Record is one cursor-stamped Task frame or descriptor. ACP-shaped Surface projection
// belongs to control/appserver/taskstream; Control owns authorization, cursor
// binding, and fan-out.
type Record struct {
	Cursor     string         `json:"cursor"`
	Generation string         `json:"generation"`
	Sequence   uint64         `json:"sequence"`
	Task       TaskDescriptor `json:"task"`
	Frame      *Frame         `json:"frame,omitempty"`
}

type ReadResult struct {
	Deliveries []Delivery `json:"deliveries,omitempty"`
	ActivityID string     `json:"activity_id,omitempty"`
}

// Subscription owns only client delivery; closing it never cancels the Task.
// Consumers commit Delivery.NextCursor after successfully applying the page.
type Subscription interface {
	Deliveries() <-chan Delivery
	Close() error
	Err() error
}

type SubscribeResult struct {
	Subscription Subscription `json:"-"`
}

// Service is the coherent Control boundary consumed by Task-aware Surfaces.
type Service interface {
	List(context.Context, Principal, ListRequest) (ListResult, error)
	Events(context.Context, Principal, ReadRequest) (ReadResult, error)
	Subscribe(context.Context, Principal, SubscribeRequest) (SubscribeResult, error)
}

// DirectoryService is the optional realtime Task status capability. It stays
// separate from Service so content observers and compatibility adapters do not
// acquire a status subscription implicitly.
type DirectoryService interface {
	WatchDirectory(context.Context, Principal, DirectoryWatchRequest) (DirectoryWatchResult, error)
}
