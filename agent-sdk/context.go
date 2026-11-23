package agentsdk

import (
	"context"
	"errors"
	"iter"

	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/internal/jsonvalue"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/placement"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/agent-sdk/task/output"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/toolset"
)

// Events provides readonly indexed access to one event sequence.
type Events interface {
	All() iter.Seq[*session.Event]
	Len() int
	At(int) *session.Event
}

// SubmissionKind identifies one runtime submission path.
type SubmissionKind string

const (
	SubmissionKindConversation       SubmissionKind = "conversation"
	SubmissionKindAgentCommunication SubmissionKind = "agent_communication"
)

// Submission is one runtime continuation submission.
type Submission struct {
	Kind         SubmissionKind      `json:"kind,omitempty"`
	Text         string              `json:"text,omitempty"`
	DisplayInput string              `json:"display_input,omitempty"`
	ContentParts []model.ContentPart `json:"content_parts,omitempty"`
	Metadata     map[string]any      `json:"metadata,omitempty"`
	Actor        session.ActorRef    `json:"actor,omitempty"`
	// Inputs is one ordered Agent-communication admission at a safe model
	// boundary. It excludes all singular input fields and retains every source.
	Inputs []AgentCommunicationInput `json:"-"`
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

// RunLifecycleStatus describes a live handle and its active approval wait.
type RunLifecycleStatus string

const (
	RunLifecycleStatusRunning         RunLifecycleStatus = "running"
	RunLifecycleStatusWaitingApproval RunLifecycleStatus = "waiting_approval"
)

// RunState projects only a live handle in this Runtime. An empty value means
// no live handle; historical outcomes belong to their owning Session or Task facts.
type RunState struct {
	Status          RunLifecycleStatus `json:"status,omitempty"`
	ActiveRunID     string             `json:"active_run_id,omitempty"`
	WaitingApproval bool               `json:"waiting_approval,omitempty"`
}

// Runner is one active runtime run handle. Live source observation is installed
// on the Run request before execution starts; Runner deliberately exposes no
// pull stream or replay cache.
type Runner interface {
	RunID() string
	Submit(Submission) error
	Cancel() CancelResult
	// WaitCompletion waits for the producer and its final durable writes to
	// finish, then returns its terminal outcome. If the wait context ends
	// first, completion remains unproven.
	WaitCompletion(context.Context) error
	Close() error
}

// ContextSubmissionRunner accepts one live submission while preserving the
// caller's cancellation and deadline. Kernel prefers this optional capability
// when a Runner performs synchronous external work; ordinary model runners may
// continue to implement only Runner.Submit and drain their local queue.
type ContextSubmissionRunner interface {
	SubmitContext(context.Context, Submission) error
}

// BatchSubmissionRunner admits one ordered Agent-communication batch to the
// exact live Run. Callers must not fall back to per-message Submit calls.
type BatchSubmissionRunner interface {
	SubmitBatch(context.Context, []AgentCommunicationInput) error
}

// SubagentRunner starts delegated child runs from the current invocation.
// Concrete child-agent configuration is app-owned; runtime only sees the
// registry name and the resulting child instance ref.
type SubagentRunner interface {
	Spawn(context.Context, SubagentSpawnContext, delegation.Request) (delegation.Anchor, delegation.Result, error)
	Wait(context.Context, delegation.Anchor, int) (delegation.Result, error)
	Cancel(context.Context, delegation.Anchor) error
}

// ChildEndpointRef is the provider-neutral address of one exact delegated
// child endpoint. EndpointKey is an opaque runner-local identity copied from
// the durable participant binding; callers must not interpret it as Task
// lifecycle state.
type ChildEndpointRef struct {
	ParticipantID string                  `json:"participant_id,omitempty"`
	SessionID     string                  `json:"session_id,omitempty"`
	EndpointKey   string                  `json:"endpoint_key,omitempty"`
	Role          session.ParticipantRole `json:"role,omitempty"`
	Placement     placement.Placement     `json:"placement"`
}

// ErrChildInputNotReady proves that no input was admitted because the child is
// busy or finishing its current Turn. An embedding may keep the input queued
// and attempt admission later against the same participant instance. Unknown
// effect outcomes must never wrap this sentinel.
var ErrChildInputNotReady = errors.New("child input is not ready")

// ChildInputRequest submits Agent communication to one exact child.
// Source is assigned by the trusted Runtime topology boundary, never by a
// model-facing tool or external wire label.
// ActivityID, Output, and Completion bind a possible new activity. Active
// steering must retain the running producer's binding even when this request
// carries another candidate; the first output may not yet be observed by Task.
type ChildInputRequest struct {
	// UserInput distinguishes an authorized human prompt from Agent mail.
	// It requires a user Source and cannot be combined with Messages.
	UserInput bool `json:"user_input,omitempty"`
	// Messages is an ordered batch; when set, singular source/content fields must be empty.
	Messages     []AgentCommunicationInput `json:"messages,omitempty"`
	Target       ChildEndpointRef          `json:"target"`
	Source       session.ActorRef          `json:"source"`
	ActivityID   string                    `json:"activity_id,omitempty"`
	Input        string                    `json:"input,omitempty"`
	DisplayInput string                    `json:"display_input,omitempty"`
	ContentParts []model.ContentPart       `json:"content_parts,omitempty"`
	Output       output.Observer           `json:"-"`
	Completion   delegation.CompletionSink `json:"-"`
}

// ChildInputCommand addresses one child inside a parent Session topology.
// Runtime resolves Target to an exact ChildEndpointRef before crossing the
// runner boundary.
type ChildInputCommand struct {
	UserInput    bool                `json:"user_input,omitempty"`
	Target       string              `json:"target"`
	Source       session.ActorRef    `json:"source"`
	Input        string              `json:"input,omitempty"`
	DisplayInput string              `json:"display_input,omitempty"`
	ContentParts []model.ContentPart `json:"content_parts,omitempty"`
}

// ChildInputResult reports only local producer ownership. StartedActivity is
// true when idle input started a prompt activity; active steering
// remains part of the already running activity.
type ChildInputResult struct {
	StartedActivity bool `json:"started_activity,omitempty"`
}

// ChildInputRunner is the optional provider-neutral input capability for an
// exact delegated child endpoint.
type ChildInputRunner interface {
	SubmitChildInput(context.Context, ChildInputRequest) (ChildInputResult, error)
}

// ChildInputBatchRunner explicitly accepts all ordered Messages in one admission.
// A singular-only runner must never silently accept and discard a batch.
type ChildInputBatchRunner interface {
	SubmitChildInputBatch(context.Context, ChildInputRequest) (ChildInputResult, error)
}

// ChildEndpointBinder rehydrates process-local endpoint ownership from one
// Runtime-validated durable binding. It performs no remote operation; a later
// ChildInputRunner call may resume the exact remote Session.
// Existing live producers retain their binding. Idle recovery installs the new
// binding before reconnect setup can emit output.
type ChildEndpointBinder interface {
	BindChildEndpoint(context.Context, ChildEndpointRef, SubagentSpawnContext) error
}

// SubagentSpawnContext is the system-controlled parent context inherited by a
// child endpoint. None of these fields are model-controlled tool arguments.
type SubagentSpawnContext struct {
	// ContentParts preserves the user request's structured text and attachments.
	ContentParts      []model.ContentPart        `json:"content_parts,omitempty"`
	SessionRef        session.SessionRef         `json:"session_ref,omitempty"`
	Session           session.Session            `json:"session,omitempty"`
	CWD               string                     `json:"cwd,omitempty"`
	TaskID            string                     `json:"task_id,omitempty"`
	ActivityID        string                     `json:"activity_id,omitempty"`
	Handle            string                     `json:"handle,omitempty"`
	Role              session.ParticipantRole    `json:"role,omitempty"`
	ParentCallID      string                     `json:"parent_call_id,omitempty"`
	Mode              string                     `json:"mode,omitempty"`
	ApprovalMode      string                     `json:"approval_mode,omitempty"`
	ApprovalRequester approval.SubagentRequester `json:"-"`
	Output            output.Observer            `json:"-"`
	// Completion is the Runtime-owned terminal lifecycle path for this child
	// turn. Stream consumers and Task control calls remain observers.
	Completion delegation.CompletionSink `json:"-"`
}

// Context exposes the Session and event history for one Agent invocation.
type Context interface {
	context.Context
	Session() session.Session
	Events() Events
	DrainSubmissions() []Submission
}

// Agent is the runtime execution unit.
type Agent interface {
	Name() string
	Run(Context) iter.Seq2[*session.Event, error]
}

// AgentSpec describes execution capabilities assembled before invocation. Its
// optional resolver selects model-facing snapshots within that same authority.
type AgentSpec struct {
	// Models contains configured alternative bindings selectable by ModelPrepare.
	// Runtime wraps each model through the same capability and accounting owners.
	Models map[string]model.LLM `json:"-"`
	Name   string               `json:"name,omitempty"`
	Model  model.LLM            `json:"-"`
	Tools  []tool.Tool          `json:"-"`
	// DeferredTools supplies MCP tools as their background initialization finishes.
	// Runtime applies the same invocation, policy and lifecycle wrappers
	// as static tools. A run pins each accepted definition and callable together.
	DeferredTools toolset.Source `json:"-"`
	// ResolveModelRequest optionally replaces model-facing configuration at
	// every unsent request boundary. Runtime authority and policy remain fixed.
	ResolveModelRequest       ModelRequestResolver `json:"-"`
	Request                   ModelRequestOptions
	RequiredModelCapabilities model.Capabilities `json:"required_model_capabilities,omitempty"`
	Metadata                  map[string]any     `json:"metadata,omitempty"`

	// ResolveSessionInstructions supplies stable main-request guidance for the
	// current compaction window.
	// After ModelPrepare, Chat calls it with the compaction summary ID in the
	// assembled history, or an empty ID if that history has no summary.
	ResolveSessionInstructions SessionInstructionsResolver `json:"-"`
}

// SessionInstructionsResolver prepares text for a main request. An optional
// persist callback runs once after provider admission accepts that assembly.
type SessionInstructionsResolver func(ctx context.Context, summaryID string) (text string, persist func(context.Context) error, err error)

// ModelRequestOptions controls model request behavior independent of
// provider implementation.
type ModelRequestOptions struct {
	Stream      *bool             `json:"stream,omitempty"`
	Output      *model.OutputSpec `json:"output,omitempty"`
	ServiceTier model.ServiceTier `json:"service_tier,omitempty"`
}

func (o ModelRequestOptions) WithDefaults(defaults ModelRequestOptions) ModelRequestOptions {
	out := defaults
	if o.Stream != nil {
		value := *o.Stream
		out.Stream = &value
	}
	if o.Output != nil {
		out.Output = model.CloneOutputSpec(o.Output)
	}
	if o.ServiceTier != "" {
		out.ServiceTier = o.ServiceTier
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
	return model.CloneOutputSpec(o.Output)
}

// AgentFactory constructs one runnable agent instance.
type AgentFactory interface {
	NewAgent(context.Context, AgentSpec) (Agent, error)
}

// ContextSpec builds one immutable default runtime context.
type ContextSpec struct {
	Context          context.Context
	Session          session.Session
	Events           []*session.Event
	DrainSubmissions func() []Submission
	// InputReady optionally observes queued input without consuming it. The
	// callback must support concurrent calls; see InputReady for signal semantics.
	InputReady func() <-chan struct{}
}

// NewContext returns one immutable runtime context implementation suitable for
// tests and future default runtimes.
func NewContext(spec ContextSpec) Context {
	ctx := spec.Context
	if ctx == nil {
		ctx = context.Background()
	}
	// Do not inherit another invocation's queue observer through the parent.
	ctx = context.WithValue(ctx, inputReadyContextKey{}, spec.InputReady)
	return &contextSnapshot{
		Context:          ctx,
		session:          session.CloneSession(spec.Session),
		events:           NewEvents(spec.Events),
		drainSubmissions: spec.DrainSubmissions,
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

type contextSnapshot struct {
	context.Context
	session          session.Session
	events           Events
	drainSubmissions func() []Submission
}

func (c *contextSnapshot) Session() session.Session {
	return session.CloneSession(c.session)
}

func (c *contextSnapshot) Events() Events {
	return c.events
}

func (c *contextSnapshot) DrainSubmissions() []Submission {
	if c == nil || c.drainSubmissions == nil {
		return nil
	}
	return CloneSubmissions(c.drainSubmissions())
}

// CloneSubmission copies one submission before queueing or fan-out.
func CloneSubmission(sub Submission) Submission {
	out := Submission{
		Kind:         sub.Kind,
		Text:         sub.Text,
		DisplayInput: sub.DisplayInput,
		ContentParts: append([]model.ContentPart(nil), sub.ContentParts...),
		Metadata:     jsonvalue.CloneMap(sub.Metadata),
		Actor:        session.CloneActorRef(sub.Actor),
		Inputs:       CloneAgentCommunicationInputs(sub.Inputs),
	}
	return out
}

// CloneSubmissions copies one submission slice before queueing or fan-out.
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
