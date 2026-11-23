// Package hook defines the extension points of an Agent run.
package hook

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

// TurnContext identifies the Turn on which a hook runs.
type TurnContext struct {
	SessionRef    session.SessionRef
	RunID, TurnID string
	Purpose       Purpose
}

// InputContext identifies the source of an admitted input.
type InputContext struct {
	TurnContext
	InputSource session.ActorRef
}

// RequestContext identifies one logical model request across assemblies.
type RequestContext struct {
	TurnContext
	LogicalRequestID      string
	AssemblyRevision      uint64
	ConfigurationRevision string
}

// PrepareContext pins the history available for a durable context edit.
type PrepareContext struct {
	RequestContext
	ContextSource ContextSource
}

// AttemptContext identifies one provider invocation.
type AttemptContext struct {
	RequestContext
	InvocationID   string
	AttemptOrdinal uint64
}

// OutputContext describes a candidate. Synthetic output has no provider attempt.
type OutputContext struct {
	RequestContext
	InvocationID   string
	AttemptOrdinal uint64
	OutputSource   OutputSource
}

// ToolContext describes the bound occurrence. Call and definition are private
// copies. A nested Code Mode call also carries its parent occurrence.
type ToolContext struct {
	TurnContext
	ToolExecution  tool.InvocationContext
	ToolParent     *tool.ParentCall
	Tool           tool.Call
	ToolDefinition tool.Definition
}

// OutputSource identifies a candidate's origin without claiming provider
// work for synthetic output. Synthetic candidates have no invocation identity.
type OutputSource string

const (
	OutputProvider   OutputSource = "provider"
	OutputCache      OutputSource = "cache"
	OutputInputEarly OutputSource = "input_early"
	OutputTurnEarly  OutputSource = "turn_start_early"
)

// Purpose separates the main loop from private auxiliary model calls.
type Purpose string

const (
	PurposeChat        Purpose = "chat"
	PurposeCompaction  Purpose = "compaction"
	PurposeSystemAgent Purpose = "system_agent"
)

// BaseHook registers one named callback with the signature of its stage.
// Zero Timeout uses five seconds. Parallel tools may invoke a callback concurrently.
type BaseHook[F any] struct {
	Name    string
	Timeout time.Duration
	Apply   F
}

// InputView contains the effective input. Source and original authorization
// evidence live outside this editable value.
type InputView struct{ Message model.Message }

// InputDecision replaces the effective input and may add system context or
// answer early. An early answer follows ordinary completion and final commit.
type InputDecision struct {
	Message  model.Message
	Context  []model.Part
	Response *model.Message
}

// TurnStartDecision runs once per canonical Turn, including overflow recovery.
type TurnStartDecision struct {
	Context  []model.Part
	Response *model.Message
}

// ModelPreparation changes request parameters and presentation. Messages change
// only through ContextEdit. ModelName selects a configured snapshot candidate;
// it never supplies an unaccounted raw LLM.
type ModelPreparation struct {
	Request     *model.Request
	ModelName   string
	ContextEdit *ContextEdit
	Response    *model.Response
}

// ContextEdit changes the durable model view without changing source facts.
// ReplacePrefix requires exact source coverage and complete tool pairing.
type ContextEdit struct {
	Append        []model.Part
	ReplacePrefix []model.Message
	Source        ContextSource
}

// ToolPreparationKind selects exactly one preparation outcome.
type ToolPreparationKind string

const (
	ToolPrepareContinue ToolPreparationKind = "continue"
	ToolPrepareRespond  ToolPreparationKind = "respond"
	ToolPrepareBlock    ToolPreparationKind = "block"
)

// ToolPreparation can change only arguments. Identity, target and binding are
// fixed before this stage. Synthetic results still require Policy admission.
type ToolPreparation struct {
	Kind   ToolPreparationKind
	Input  json.RawMessage
	Result *tool.Result
}

// ToolOutcome carries the full business result and the original execution error.
// Error and system-owned result fields cannot be replaced by result hooks.
type ToolOutcome struct {
	Result tool.Result
	Error  error
}

// ToolNext consumes an admitted dispatch at most once and expires when its
// middleware returns. The owner drains any consumed producer before releasing
// execution authority, even if the middleware ignores cancellation.
type ToolNext func(context.Context) (tool.Result, error)

// ModelNext consumes one actual provider attempt at most once. It cannot alter
// the admitted request or accounting. ModelResult owns response transformation.
type ModelNext func(context.Context) error

// RetryRequest asks the existing transport retry owner to reconsider the exact
// failed attempt. Cause must be next's retryable provider error. It cannot
// retry admission failures, successful attempts, or irreversible output.
type RetryRequest struct{ Cause error }

func (r *RetryRequest) Error() string { return fmt.Sprintf("model retry requested: %v", r.Cause) }
func (r *RetryRequest) Unwrap() error { return r.Cause }

// CompletionKind controls a candidate before final-input closure and commit.
type CompletionKind string

const (
	CompletionAccept   CompletionKind = "accept"
	CompletionReplace  CompletionKind = "replace"
	CompletionContinue CompletionKind = "continue"
	CompletionReject   CompletionKind = "reject"
)

// CompletionDecision is a one-of: Replace requires Message, Continue requires
// Feedback, and Accept/Reject carry neither. All hooks share one feedback budget.
type CompletionDecision struct {
	Kind     CompletionKind
	Message  *model.Message
	Feedback []model.Part
}

// StreamMode selects irreversible incremental publication or buffered final
// publication. Buffered is the default and supports arbitrary final replacement.
type StreamMode string

const (
	StreamBuffered    StreamMode = "buffered"
	StreamIncremental StreamMode = "incremental"
)

// StreamTransformer owns text state for one actual attempt or synthetic candidate. Transform
// and Finish run in order; they never see media, signatures, identity or usage.
type StreamTransformer interface {
	Transform(context.Context, string) (string, error)
	Finish(context.Context) (string, error)
}

// Input transforms one admitted message before persistence.
type Input = BaseHook[func(context.Context, InputContext, InputView) (InputDecision, error)]

// TurnStart runs once for the initial input of a Turn.
type TurnStart = BaseHook[func(context.Context, TurnContext) (TurnStartDecision, error)]

// ModelPrepare edits a logical model request before provider admission.
type ModelPrepare = BaseHook[func(context.Context, PrepareContext, ModelPreparation) (ModelPreparation, error)]

// ModelInvoke wraps one actual provider attempt.
type ModelInvoke = BaseHook[func(context.Context, AttemptContext, *model.Request, ModelNext) error]

// ModelResult transforms a model response.
type ModelResult = BaseHook[func(context.Context, OutputContext, model.Response) (model.Response, error)]

// ToolPrepare prepares the effective arguments before admission.
type ToolPrepare = BaseHook[func(context.Context, ToolContext, ToolPreparation) (ToolPreparation, error)]

// ToolInvoke wraps one admitted tool dispatch.
type ToolInvoke = BaseHook[func(context.Context, ToolContext, ToolNext) (tool.Result, error)]

// ToolResult transforms the full business result.
type ToolResult = BaseHook[func(context.Context, ToolContext, ToolOutcome) (tool.Result, error)]

// ModelView transforms the reply projected to the model.
type ModelView = BaseHook[func(context.Context, ToolContext, model.Message) (model.Message, error)]

// Completion reviews a final candidate before commit.
type Completion = BaseHook[func(context.Context, OutputContext, model.Message) (CompletionDecision, error)]

// Stream creates a text transformer for one output candidate.
type Stream = BaseHook[func(context.Context, OutputContext) (StreamTransformer, error)]

// Set fixes the ordered phase configuration for a Run. Before/after phases
// run in registration order; around phases unwind in reverse order. Observers
// remain separate and have no authority over these values.
type Set struct {
	Input        []Input
	TurnStart    []TurnStart
	ModelPrepare []ModelPrepare
	ModelInvoke  []ModelInvoke
	ModelResult  []ModelResult
	ToolPrepare  []ToolPrepare
	ToolInvoke   []ToolInvoke
	ToolResult   []ToolResult
	ModelView    []ModelView
	Completion   []Completion
	Stream       []Stream
	StreamMode   StreamMode
	// CompletionLimit is shared by all hooks. Zero uses two feedback rounds.
	CompletionLimit int
}

// Error identifies a failed extension while preserving its original cause.
type Error struct {
	Phase, Name string
	Cause       error
}

func (e *Error) Error() string { return fmt.Sprintf("hook %s/%s: %v", e.Phase, e.Name, e.Cause) }
func (e *Error) Unwrap() error { return e.Cause }
func (*Error) Retryable() bool { return false }

// ContextSource pins the source prefix selected for a durable replacement.
type ContextSource struct {
	Stamp      string
	ThroughID  string
	ThroughSeq uint64
}
