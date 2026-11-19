package agentsdk

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

// HookContext is detached execution evidence. It conveys no mutation or approval
// capability. A request assembly is distinct from an actual provider attempt.
type HookContext struct {
	SessionRef            session.SessionRef
	RunID, TurnID         string
	Purpose               HookPurpose
	OutputSource          HookOutputSource
	LogicalRequestID      string
	AssemblyRevision      uint64
	ConfigurationRevision string
	InvocationID          string
	AttemptOrdinal        uint64
	InputSource           session.ActorRef
	Tool                  *tool.Call
	ToolDefinition        *tool.Definition
	ContextSource         ContextSource
}

// HookOutputSource identifies a candidate's origin without claiming provider
// work for synthetic output. Synthetic candidates have no invocation identity.
type HookOutputSource string

const (
	HookOutputProvider   HookOutputSource = "provider"
	HookOutputCache      HookOutputSource = "cache"
	HookOutputInputEarly HookOutputSource = "input_early"
	HookOutputTurnEarly  HookOutputSource = "turn_start_early"
)

// HookPurpose separates the main loop from private auxiliary model calls.
type HookPurpose string

const (
	HookPurposeChat        HookPurpose = "chat"
	HookPurposeCompaction  HookPurpose = "compaction"
	HookPurposeSystemAgent HookPurpose = "system_agent"
)

// Hook registers one synchronous transformation. Functions can run concurrently
// for parallel tools and must synchronize shared state. Zero Timeout uses five
// seconds. Pure transformations receive private copies; late output is discarded.
type Hook[I, O any] struct {
	Name    string
	Timeout time.Duration
	Apply   func(context.Context, HookContext, I) (O, error)
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

// ToolInvokeHook wraps one admitted tool effect; the first entry is outermost.
type ToolInvokeHook struct {
	Name    string
	Timeout time.Duration
	Invoke  func(context.Context, HookContext, ToolNext) (tool.Result, error)
}

// ModelNext consumes one actual provider attempt at most once. It cannot alter
// the admitted request or accounting. ModelResult owns response transformation.
type ModelNext func(context.Context) error

// RetryRequest asks the existing transport retry owner to reconsider the exact
// failed attempt. Cause must be next's retryable provider error. It cannot
// retry admission failures, successful attempts, or irreversible output.
type RetryRequest struct{ Cause error }

func (r *RetryRequest) Error() string { return fmt.Sprintf("model retry requested: %v", r.Cause) }
func (r *RetryRequest) Unwrap() error { return r.Cause }

// ModelInvokeHook wraps an actual provider attempt, inside retry and accounting.
type ModelInvokeHook struct {
	Name    string
	Timeout time.Duration
	Invoke  func(context.Context, HookContext, *model.Request, ModelNext) error
}

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

// Hooks fixes the ordered phase configuration for a Run. Before/after phases
// run in registration order; around phases unwind in reverse order. Observers
// remain separate and have no authority over these values.
type Hooks struct {
	Input        []Hook[InputView, InputDecision]
	TurnStart    []Hook[struct{}, TurnStartDecision]
	ModelPrepare []Hook[ModelPreparation, ModelPreparation]
	ModelInvoke  []ModelInvokeHook
	ModelResult  []Hook[model.Response, model.Response]
	ToolPrepare  []Hook[ToolPreparation, ToolPreparation]
	ToolInvoke   []ToolInvokeHook
	ToolResult   []Hook[ToolOutcome, tool.Result]
	ModelView    []Hook[model.Message, model.Message]
	Completion   []Hook[model.Message, CompletionDecision]
	Stream       []Hook[struct{}, StreamTransformer]
	StreamMode   StreamMode
	// CompletionLimit is shared by all hooks. Zero uses two feedback rounds.
	CompletionLimit int
}

// HookError identifies a failed extension while preserving its original cause.
type HookError struct {
	Phase, Name string
	Cause       error
}

func (e *HookError) Error() string { return fmt.Sprintf("hook %s/%s: %v", e.Phase, e.Name, e.Cause) }
func (e *HookError) Unwrap() error { return e.Cause }
func (*HookError) Retryable() bool { return false }

// ContextSource pins the source prefix selected for a durable replacement.
type ContextSource struct {
	Stamp      string
	ThroughID  string
	ThroughSeq uint64
}
