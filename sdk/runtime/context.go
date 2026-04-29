package runtime

import (
	"context"
	"iter"
	"maps"
	"time"

	sdkdelegation "github.com/OnslaughtSnail/caelis/sdk/delegation"
	sdkmodel "github.com/OnslaughtSnail/caelis/sdk/model"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
	sdksubagent "github.com/OnslaughtSnail/caelis/sdk/subagent"
	sdktool "github.com/OnslaughtSnail/caelis/sdk/tool"
)

// Events provides readonly indexed access to one event sequence.
type Events interface {
	All() iter.Seq[*sdksession.Event]
	Len() int
	At(int) *sdksession.Event
}

// ReadonlyState exposes immutable session state snapshots.
type ReadonlyState interface {
	Lookup(string) (any, bool)
	Snapshot() map[string]any
}

// Submission is one runtime continuation or approval submission.
type Submission struct {
	Kind     string         `json:"kind,omitempty"`
	Text     string         `json:"text,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
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
	Events() iter.Seq2[*sdksession.Event, error]
	Submit(Submission) error
	Cancel() bool
	Close() error
}

// SubagentRunner starts delegated child runs from the current invocation.
// Concrete child-agent configuration is app-owned; runtime only sees the
// registry name and the resulting child instance ref.
type SubagentRunner = sdksubagent.Runner

// ReadonlyContext exposes immutable invocation state derived from persisted
// events and runtime overlays.
type Context interface {
	context.Context
	Session() sdksession.Session
	Events() Events
	ReadonlyState() ReadonlyState
	Overlay() bool
}

// Agent is the runtime execution unit.
type Agent interface {
	Name() string
	Run(Context) iter.Seq2[*sdksession.Event, error]
}

// AgentSpec describes the concrete execution capabilities assembled into one
// agent instance before invocation begins.
type AgentSpec struct {
	Name           string         `json:"name,omitempty"`
	Model          sdkmodel.LLM   `json:"-"`
	Tools          []sdktool.Tool `json:"-"`
	SubagentRunner SubagentRunner `json:"-"`
	Request        ModelRequestOptions
	Metadata       map[string]any `json:"metadata,omitempty"`
}

// ModelRequestOptions controls per-turn model request behavior independent of
// provider implementation.
type ModelRequestOptions struct {
	Stream *bool `json:"stream,omitempty"`
}

func (o ModelRequestOptions) WithDefaults(defaults ModelRequestOptions) ModelRequestOptions {
	out := defaults
	if o.Stream != nil {
		value := *o.Stream
		out.Stream = &value
	}
	return out
}

func (o ModelRequestOptions) StreamEnabled(defaultValue bool) bool {
	if o.Stream == nil {
		return defaultValue
	}
	return *o.Stream
}

type SubagentRunRequest = sdkdelegation.Request
type SubagentRunResult = sdkdelegation.Result
type SubagentAnchor = sdkdelegation.Anchor

// AgentFactory constructs one runnable agent instance.
type AgentFactory interface {
	NewAgent(context.Context, AgentSpec) (Agent, error)
}

// ContextSpec builds one immutable default runtime context.
type ContextSpec struct {
	Context context.Context
	Session sdksession.Session
	Events  []*sdksession.Event
	State   map[string]any
	Overlay bool
}

// NewContext returns one immutable runtime context implementation suitable for
// tests and future default runtimes.
func NewContext(spec ContextSpec) Context {
	ctx := spec.Context
	if ctx == nil {
		ctx = context.Background()
	}
	return &contextSnapshot{
		Context: ctx,
		session: sdksession.CloneSession(spec.Session),
		events:  NewEvents(spec.Events),
		state:   NewReadonlyState(spec.State),
		overlay: spec.Overlay,
	}
}

// NewEvents wraps one event slice as one readonly view. Events are deep-copied
// so callers cannot mutate the underlying snapshot.
func NewEvents(events []*sdksession.Event) Events {
	if len(events) == 0 {
		return eventSlice{}
	}
	return eventSlice{events: sdksession.CloneEvents(events)}
}

// NewReadonlyState wraps one state snapshot as one readonly view.
func NewReadonlyState(values map[string]any) ReadonlyState {
	return readonlyStateSnapshot{values: maps.Clone(values)}
}

type eventSlice struct {
	events []*sdksession.Event
}

func (e eventSlice) All() iter.Seq[*sdksession.Event] {
	return func(yield func(*sdksession.Event) bool) {
		for _, event := range e.events {
			if !yield(sdksession.CloneEvent(event)) {
				return
			}
		}
	}
}

func (e eventSlice) Len() int {
	return len(e.events)
}

func (e eventSlice) At(i int) *sdksession.Event {
	if i < 0 || i >= len(e.events) || e.events[i] == nil {
		return nil
	}
	return sdksession.CloneEvent(e.events[i])
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
	session sdksession.Session
	events  Events
	state   ReadonlyState
	overlay bool
}

func (c *contextSnapshot) Session() sdksession.Session {
	return sdksession.CloneSession(c.session)
}

func (c *contextSnapshot) Events() Events {
	return c.events
}

func (c *contextSnapshot) ReadonlyState() ReadonlyState {
	return c.state
}

func (c *contextSnapshot) Overlay() bool {
	return c != nil && c.overlay
}
