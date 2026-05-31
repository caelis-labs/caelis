package agent

import (
	"context"
	"iter"
	"maps"
	"time"

	coremodel "github.com/OnslaughtSnail/caelis/core/model"
	"github.com/OnslaughtSnail/caelis/ports/delegation"
	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/ports/tool"
)

// Events provides readonly indexed access to one event sequence.
type Events interface {
	All() iter.Seq[*session.Event]
	Len() int
	At(int) *session.Event
}

// ReadonlyState exposes immutable session state snapshots.
type ReadonlyState interface {
	Lookup(string) (any, bool)
	Snapshot() map[string]any
}

// SubmissionKind identifies one runtime submission path.
type SubmissionKind string

const (
	SubmissionKindConversation SubmissionKind = "conversation"
)

// Submission is one runtime continuation submission.
type Submission struct {
	Kind         SubmissionKind          `json:"kind,omitempty"`
	Text         string                  `json:"text,omitempty"`
	ContentParts []coremodel.ContentPart `json:"content_parts,omitempty"`
	Metadata     map[string]any          `json:"metadata,omitempty"`
}

// CancelStatus identifies the outcome of one cancellation request.
type CancelStatus string

const (
	CancelStatusCancelled        CancelStatus = "cancelled"
	CancelStatusAlreadyCancelled CancelStatus = "already_cancelled"
)

// CancelResult preserves the useful cancellation outcome without forcing
// callers to infer it from a lossy boolean.
type CancelResult struct {
	Status CancelStatus `json:"status,omitempty"`
	Err    error        `json:"-"`
}

func (r CancelResult) Cancelled() bool {
	return r.Status == CancelStatusCancelled
}

// RunLifecycleStatus identifies one runtime run lifecycle state.
type RunLifecycleStatus string

const (
	RunLifecycleStatusRunning         RunLifecycleStatus = "running"
	RunLifecycleStatusWaitingApproval RunLifecycleStatus = "waiting_approval"
	RunLifecycleStatusInterrupted     RunLifecycleStatus = "interrupted"
	RunLifecycleStatusFailed          RunLifecycleStatus = "failed"
	RunLifecycleStatusCompleted       RunLifecycleStatus = "completed"
)

// RunState is the runtime-visible state of one session run.
type RunState struct {
	Status          RunLifecycleStatus `json:"status,omitempty"`
	ActiveRunID     string             `json:"active_run_id,omitempty"`
	WaitingApproval bool               `json:"waiting_approval,omitempty"`
	LastError       string             `json:"last_error,omitempty"`
	UpdatedAt       time.Time          `json:"updated_at,omitempty"`
}

// ContextUsage reports context window usage for one session.
type ContextUsage struct {
	CurrentTokens int `json:"current_tokens,omitempty"`
	MaxTokens     int `json:"max_tokens,omitempty"`
}

// Runner is one active runtime run handle.
type Runner interface {
	RunID() string
	Events() iter.Seq2[*session.Event, error]
	Submit(Submission) error
	Cancel() CancelResult
	Close() error
}

// SubagentApprovalToolCall is the child tool call asking for approval.
type SubagentApprovalToolCall struct {
	ID       string         `json:"id,omitempty"`
	Name     string         `json:"name,omitempty"`
	Kind     string         `json:"kind,omitempty"`
	Title    string         `json:"title,omitempty"`
	Status   string         `json:"status,omitempty"`
	RawInput map[string]any `json:"raw_input,omitempty"`
}

// SubagentApprovalRequest is one runtime-owned approval bridge payload for a
// spawned child ACP agent.
type SubagentApprovalRequest struct {
	SessionRef   session.SessionRef       `json:"session_ref,omitempty"`
	Session      session.Session          `json:"session,omitempty"`
	TaskID       string                   `json:"task_id,omitempty"`
	ParentCallID string                   `json:"parent_call_id,omitempty"`
	Agent        string                   `json:"agent,omitempty"`
	Mode         string                   `json:"mode,omitempty"`
	ToolCall     SubagentApprovalToolCall `json:"tool_call,omitempty"`
	Options      []ApprovalOption         `json:"options,omitempty"`
}

// SubagentApprovalResponse is one bridged child approval outcome.
type SubagentApprovalResponse struct {
	Outcome  string `json:"outcome,omitempty"`
	OptionID string `json:"option_id,omitempty"`
	Approved bool   `json:"approved,omitempty"`
}

// SubagentApprovalRequester bridges child ACP permission requests into the
// parent runtime's approval surface.
type SubagentApprovalRequester interface {
	RequestSubagentApproval(context.Context, SubagentApprovalRequest) (SubagentApprovalResponse, error)
}

// SpawnContext is the system-controlled parent session context inherited by
// one child ACP agent. None of these fields are exposed on the LLM-facing SPAWN
// tool surface.
type SpawnContext struct {
	SessionRef        session.SessionRef        `json:"session_ref,omitempty"`
	Session           session.Session           `json:"session,omitempty"`
	CWD               string                    `json:"cwd,omitempty"`
	TaskID            string                    `json:"task_id,omitempty"`
	ParentCallID      string                    `json:"parent_call_id,omitempty"`
	Mode              string                    `json:"mode,omitempty"`
	ApprovalRequester SubagentApprovalRequester `json:"-"`
}

// SubagentRunner starts delegated child runs from the current invocation.
// Concrete child-agent configuration is app-owned; runtime only sees the
// registry name and the resulting child instance ref.
type SubagentRunner interface {
	Spawn(context.Context, SpawnContext, delegation.Request) (delegation.Anchor, delegation.Result, error)
	Continue(context.Context, delegation.Anchor, delegation.ContinueRequest) (delegation.Result, error)
	Wait(context.Context, delegation.Anchor, int) (delegation.Result, error)
	Cancel(context.Context, delegation.Anchor) error
}

// ReadonlyContext exposes immutable invocation state derived from persisted
// events and runtime overlays.
type Context interface {
	context.Context
	Session() session.Session
	Events() Events
	ReadonlyState() ReadonlyState
	DrainSubmissions() []Submission
	Overlay() bool
}

// Agent is the runtime execution unit.
type Agent interface {
	Name() string
	Run(Context) iter.Seq2[*session.Event, error]
}

// AgentSpec describes the concrete execution capabilities assembled into one
// agent instance before invocation begins.
type AgentSpec struct {
	Name           string         `json:"name,omitempty"`
	Model          model.LLM      `json:"-"`
	Tools          []tool.Tool    `json:"-"`
	SubagentRunner SubagentRunner `json:"-"`
	Request        ModelRequestOptions
	Metadata       map[string]any `json:"metadata,omitempty"`
}

// ModelRequestOptions controls per-turn model request behavior independent of
// provider implementation.
type ModelRequestOptions struct {
	Stream *bool             `json:"stream,omitempty"`
	Output *model.OutputSpec `json:"output,omitempty"`
}

func (o ModelRequestOptions) WithDefaults(defaults ModelRequestOptions) ModelRequestOptions {
	out := defaults
	if o.Stream != nil {
		value := *o.Stream
		out.Stream = &value
	}
	if o.Output != nil {
		out.Output = cloneOutputSpec(o.Output)
	}
	return out
}

func (o ModelRequestOptions) StreamEnabled(defaultValue bool) bool {
	if o.Stream == nil {
		return defaultValue
	}
	return *o.Stream
}

func (o ModelRequestOptions) OutputSpec() *model.OutputSpec {
	return cloneOutputSpec(o.Output)
}

func cloneOutputSpec(in *model.OutputSpec) *model.OutputSpec {
	if in == nil {
		return nil
	}
	out := *in
	if in.JSONSchema != nil {
		out.JSONSchema = cloneOutputJSONMap(in.JSONSchema)
	}
	return &out
}

func cloneOutputJSONMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = cloneOutputJSONValue(value)
	}
	return out
}

func cloneOutputJSONValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneOutputJSONMap(typed)
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = cloneOutputJSONValue(item)
		}
		return out
	case []string:
		return append([]string(nil), typed...)
	default:
		return typed
	}
}

type SubagentRunRequest = delegation.Request
type SubagentRunResult = delegation.Result
type SubagentAnchor = delegation.Anchor

// AgentFactory constructs one runnable agent instance.
type AgentFactory interface {
	NewAgent(context.Context, AgentSpec) (Agent, error)
}

// ContextSpec builds one immutable default runtime context.
type ContextSpec struct {
	Context          context.Context
	Session          session.Session
	Events           []*session.Event
	State            map[string]any
	DrainSubmissions func() []Submission
	Overlay          bool
}

// NewContext returns one immutable runtime context implementation suitable for
// tests and future default runtimes.
func NewContext(spec ContextSpec) Context {
	ctx := spec.Context
	if ctx == nil {
		ctx = context.Background()
	}
	return &contextSnapshot{
		Context:          ctx,
		session:          session.CloneSession(spec.Session),
		events:           NewEvents(spec.Events),
		state:            NewReadonlyState(spec.State),
		drainSubmissions: spec.DrainSubmissions,
		overlay:          spec.Overlay,
	}
}

// NewEvents wraps one event slice as one readonly view. Events are deep-copied
// so callers cannot mutate the underlying snapshot.
func NewEvents(events []*session.Event) Events {
	if len(events) == 0 {
		return eventSlice{}
	}
	return eventSlice{events: session.CloneEvents(events)}
}

// NewReadonlyState wraps one state snapshot as one readonly view.
func NewReadonlyState(values map[string]any) ReadonlyState {
	return readonlyStateSnapshot{values: maps.Clone(values)}
}

type eventSlice struct {
	events []*session.Event
}

func (e eventSlice) All() iter.Seq[*session.Event] {
	return func(yield func(*session.Event) bool) {
		for _, event := range e.events {
			if !yield(session.CloneEvent(event)) {
				return
			}
		}
	}
}

func (e eventSlice) Len() int {
	return len(e.events)
}

func (e eventSlice) At(i int) *session.Event {
	if i < 0 || i >= len(e.events) || e.events[i] == nil {
		return nil
	}
	return session.CloneEvent(e.events[i])
}

type readonlyStateSnapshot struct {
	values map[string]any
}

func (s readonlyStateSnapshot) Lookup(key string) (any, bool) {
	value, ok := s.values[key]
	return value, ok
}

func (s readonlyStateSnapshot) Snapshot() map[string]any {
	return maps.Clone(s.values)
}

type contextSnapshot struct {
	context.Context
	session          session.Session
	events           Events
	state            ReadonlyState
	drainSubmissions func() []Submission
	overlay          bool
}

func (c *contextSnapshot) Session() session.Session {
	return session.CloneSession(c.session)
}

func (c *contextSnapshot) Events() Events {
	return c.events
}

func (c *contextSnapshot) ReadonlyState() ReadonlyState {
	return c.state
}

func (c *contextSnapshot) DrainSubmissions() []Submission {
	if c == nil || c.drainSubmissions == nil {
		return nil
	}
	return CloneSubmissions(c.drainSubmissions())
}

func (c *contextSnapshot) Overlay() bool {
	return c != nil && c.overlay
}

func CloneSubmission(sub Submission) Submission {
	return Submission{
		Kind:         sub.Kind,
		Text:         sub.Text,
		ContentParts: coremodel.CloneContentParts(sub.ContentParts),
		Metadata:     maps.Clone(sub.Metadata),
	}
}

func CloneSubmissions(items []Submission) []Submission {
	if len(items) == 0 {
		return nil
	}
	out := make([]Submission, 0, len(items))
	for _, item := range items {
		out = append(out, CloneSubmission(item))
	}
	return out
}
