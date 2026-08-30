package runtime

import (
	"context"
	"fmt"
	stdruntime "runtime"
	"strings"
	"sync"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/textstream"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
	"github.com/caelis-labs/caelis/agent-sdk/task/subagent"
)

const (
	defaultCommandYield        = 10 * time.Second
	taskWaitMaxYield           = time.Minute
	taskWriteOutputWait        = 2 * time.Second
	taskWriteOutputQuietPeriod = 100 * time.Millisecond
	taskCancelWait             = 10 * time.Millisecond
	// Command observation must comfortably bridge process startup, TaskStream
	// attachment, and ordinary TUI render stalls. The window stays bounded, but
	// is aligned with the semantic subagent recovery budget instead of evicting
	// a normal build log after only 64 KiB.
	commandLiveOutputBufferCapBytes = 4 * 1024 * 1024
	// Subagent observation deliberately keeps two bounded, process-local views:
	// a short exact delta window for lossless resume and a larger ingest-merged
	// semantic view for gap recovery. The semantic byte budget retains exact
	// completed Final Messages after lower-priority context, while the latest
	// Final is additionally protected by the Task result contract. The combined
	// bounded allocation is about 5 MiB per active subagent, plus that latest
	// Final. Neither view is durable or parent model context.
	subagentStreamFrameCap       = 128
	subagentExactStreamByteCap   = 1024 * 1024
	subagentStreamByteCap        = 4 * 1024 * 1024
	subagentOutputPreviewByteCap = 1600
)

type taskRuntime struct {
	runtime         *Runtime
	store           taskapi.Store
	activityChanged func(session.SessionRef)
	taskCommitted   func(*taskapi.Entry)

	mu         sync.RWMutex
	tasks      map[string]*commandTask
	subagents  map[string]*subagentTask
	pending    map[string][]stream.Frame
	order      map[string][]string
	backends   map[sandbox.Backend]sandbox.Runtime
	handles    map[string]map[string]struct{}
	operations map[string]struct{}
	// operationChanged broadcasts release of a session-scoped Task mutation
	// claim. Command waits observe process lifecycle without a claim, then use
	// this signal to serialize their short reconciliation phase.
	operationChanged map[string]chan struct{}
	// streamActivity is a TaskID-stable condition variable for subagent stream
	// activity. Concrete subagentTask values may be removed and rehydrated after
	// completion, so a cross-activity observer must never wait on one instance.
	streamActivity map[string]*taskStreamActivitySignal

	completions        map[string]*subagentCompletion
	completionApplying map[string]struct{}
}

type sandboxRuntimeBackends interface {
	SupportedBackends() []sandbox.Backend
}

type sandboxSessionRefOpener interface {
	OpenSessionRef(sandbox.SessionRef) (sandbox.Session, error)
}

// commandBackendCursor is the byte domain owned by one sandbox Session. It is
// deliberately separate from presentation/model cursors: stdout/stderr may
// advance past a complete UTF-8 prefix before that prefix is safe to publish.
type commandBackendCursor struct {
	stdout int64
	stderr int64
}

func (cursor commandBackendCursor) outputCursor() sandbox.OutputCursor {
	return sandbox.OutputCursor{
		Stdout: max(cursor.stdout, 0),
		Stderr: max(cursor.stderr, 0),
	}
}

func (cursor *commandBackendCursor) advance(next commandBackendCursor) {
	if cursor == nil {
		return
	}
	cursor.stdout = max(cursor.stdout, next.stdout)
	cursor.stderr = max(cursor.stderr, next.stderr)
}

// commandObservationFrontier is the model/presentation view of command output.
// base is the first retained byte and model is the complete boundary already
// exposed through a canonical Task observation.
type commandObservationFrontier struct {
	base  int64
	model int64
}

// commandOutputCheckpoint is one atomic durable recovery boundary. backend,
// output, and model belong to the same observation epoch; callers must advance
// the value as a unit rather than independently maxing its scalar fields.
type commandOutputCheckpoint struct {
	backend   commandBackendCursor
	output    int64
	model     int64
	available bool
	coherent  bool
	gap       bool
}

func (checkpoint commandOutputCheckpoint) resumable() bool {
	return checkpoint.available &&
		(checkpoint.coherent || checkpoint.gap) &&
		checkpoint.output >= 0 &&
		checkpoint.output == checkpoint.model
}

// advance keeps the newest complete boundary. A recovery gap is permanent for
// the output epoch, so an equal/newer checkpoint cannot silently restore
// coherence after bytes were lost.
func (checkpoint *commandOutputCheckpoint) advance(next commandOutputCheckpoint) {
	if checkpoint == nil || !next.resumable() {
		return
	}
	previousGap := checkpoint.gap
	switch {
	case !checkpoint.resumable(), next.output > checkpoint.output:
		*checkpoint = next
	case next.output == checkpoint.output:
		checkpoint.gap = checkpoint.gap || next.gap
		checkpoint.coherent = checkpoint.coherent && next.coherent
	}
	checkpoint.gap = checkpoint.gap || previousGap
	if checkpoint.gap {
		checkpoint.coherent = false
	}
}

// commandOutputState groups the live ingest path, model frontier, and durable
// checkpoints. The two checkpoints have distinct owners: checkpoint follows
// complete output ingestion, while resume advances only after the model
// frontier catches up to the same atomic boundary.
type commandOutputState struct {
	backend    commandBackendCursor
	frontier   commandObservationFrontier
	checkpoint commandOutputCheckpoint
	resume     commandOutputCheckpoint

	live     bool
	callback bool
	exact    bool

	recoveryStdout textstream.UTF8Decoder
	recoveryStderr textstream.UTF8Decoder
}

type commandTask struct {
	ref           taskapi.Ref
	handle        string
	sessionRef    session.SessionRef
	session       sandbox.Session
	command       string
	workdir       string
	tty           bool
	supportsInput bool
	parentCall    string
	requestDigest string
	title         string
	createdAt     time.Time
	revision      uint64
	lease         taskapi.Lease

	outputReadMu sync.Mutex
	mu           sync.Mutex
	state        taskapi.State
	running      bool
	output       string
	outputState  commandOutputState
	result       map[string]any
	metadata     map[string]any

	streamFrames         []stream.Frame
	streamEventBase      int64
	streamTerminalFramed bool
	streamChanged        chan struct{}
}

type subagentTask struct {
	ref          taskapi.Ref
	sessionRef   session.SessionRef
	anchor       delegation.Anchor
	runner       subagent.Runner
	agent        string
	target       delegation.Target
	handle       string
	title        string
	prompt       string
	mode         string
	approvalMode string
	createdAt    time.Time
	revision     uint64
	lease        taskapi.Lease

	// streamMu preserves publication order across the pending-to-live handoff.
	// It must be acquired before publishing the task in taskRuntime.subagents.
	streamMu sync.Mutex
	// activityApplyMu serializes process-local live frame application with the
	// matching durable journal callback without covering Task-store I/O.
	activityApplyMu sync.Mutex
	mu              sync.Mutex
	state           taskapi.State
	running         bool
	result          map[string]any
	metadata        map[string]any
	contextUsage    *taskapi.ContextUsageRecord

	stdout           string
	stderr           string
	stdoutCursor     int64
	stderrCursor     int64
	turnSeq          int64
	streamFrames     []stream.Frame
	streamFrameSizes []int
	// Stream cursors are absolute for the Task lifetime and do not reset when a
	// new observed child activity starts.
	streamEventBase    int64
	streamOutputCursor int64
	streamBytes        int
	semanticRetention  subagentSemanticRetention
	// assistantStream* tracks the producer's current ACP agent-message segment.
	// A MessageID change starts a new message; semantic retention preserves each
	// contiguous run in wire order.
	assistantStreamTurnID    string
	assistantStreamMessageID string
	latestFinalText          string
	latestFinalTurnSeq       int64
	latestFinalOrder         int64
	latestFinalAt            time.Time
	latestFinalActivityID    string
	// finalResponseCursor is the highest completed child Turn whose exact Final
	// Response has already been exposed by Spawn or an explicit Task read/wait.
	// It is an observation frontier, not a second output store; exact text stays
	// owned by latestFinalText and semanticRetention.
	finalResponseCursor   int64
	streamTerminalFramed  bool
	streamChanged         chan struct{}
	completionReady       bool
	activityID            string
	activityGeneration    int64
	activityCursor        uint64
	activityDurableCursor uint64
}

func newTaskRuntime(
	runtime *Runtime,
	store taskapi.Store,
	activityCallbacks ...func(session.SessionRef),
) *taskRuntime {
	var activityChanged func(session.SessionRef)
	if len(activityCallbacks) > 0 {
		activityChanged = activityCallbacks[0]
	}
	return &taskRuntime{
		runtime:            runtime,
		store:              store,
		activityChanged:    activityChanged,
		tasks:              map[string]*commandTask{},
		subagents:          map[string]*subagentTask{},
		pending:            map[string][]stream.Frame{},
		order:              map[string][]string{},
		backends:           map[sandbox.Backend]sandbox.Runtime{},
		handles:            map[string]map[string]struct{}{},
		operations:         map[string]struct{}{},
		operationChanged:   map[string]chan struct{}{},
		streamActivity:     map[string]*taskStreamActivitySignal{},
		completions:        map[string]*subagentCompletion{},
		completionApplying: map[string]struct{}{},
	}
}

func (tm *taskRuntime) tryClaimSubagentOperation(ref session.SessionRef, taskID string) (func(), bool) {
	if tm == nil {
		return nil, false
	}
	operationKey := taskOperationKey(ref, taskID)
	tm.mu.Lock()
	if tm.operations == nil {
		tm.operations = map[string]struct{}{}
	}
	if _, active := tm.operations[operationKey]; active {
		tm.mu.Unlock()
		return nil, false
	}
	tm.operations[operationKey] = struct{}{}
	tm.mu.Unlock()
	return func() {
		tm.mu.Lock()
		tm.releaseTaskOperationLocked(operationKey)
		completion, completionOperationKey := tm.startSubagentCompletionLocked(strings.TrimSpace(taskID))
		tm.mu.Unlock()
		if completion != nil {
			go tm.applySubagentCompletion(completion, completionOperationKey)
		}
	}, true
}

// releaseTaskOperationLocked releases one operation owner and notifies every
// waiter before the slot may transfer to a queued producer completion. Woken
// waiters recheck ownership and keep waiting when such a transfer occurs.
// Callers must hold tm.mu.
func (tm *taskRuntime) releaseTaskOperationLocked(operationKey string) {
	delete(tm.operations, operationKey)
	if changed := tm.operationChanged[operationKey]; changed != nil {
		close(changed)
		delete(tm.operationChanged, operationKey)
	}
}

func (tm *taskRuntime) waitForTaskOperationClaim(ctx context.Context, ref session.SessionRef, taskID string) (func(), error) {
	if tm == nil {
		return nil, fmt.Errorf("task Runtime is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	operationKey := taskOperationKey(ref, taskID)
	for {
		if release, claimed := tm.tryClaimSubagentOperation(ref, taskID); claimed {
			return release, nil
		}
		tm.mu.Lock()
		if _, active := tm.operations[operationKey]; !active {
			tm.mu.Unlock()
			continue
		}
		if tm.operationChanged == nil {
			tm.operationChanged = map[string]chan struct{}{}
		}
		changed := tm.operationChanged[operationKey]
		if changed == nil {
			changed = make(chan struct{})
			tm.operationChanged[operationKey] = changed
		}
		tm.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-changed:
		}
	}
}

func (tm *taskRuntime) hasSubagentOperation(ref session.SessionRef, taskID string) bool {
	if tm == nil {
		return false
	}
	tm.mu.RLock()
	_, active := tm.operations[taskOperationKey(ref, taskID)]
	tm.mu.RUnlock()
	return active
}

func taskOperationKey(ref session.SessionRef, taskID string) string {
	return strings.TrimSpace(session.NormalizeSessionRef(ref).SessionID) + "\x00" + strings.TrimSpace(taskID)
}

type runtimeToolContext struct {
	mode              string
	approvalMode      string
	approvalRequester agent.ApprovalRequester
	runID             string
	turnID            string
	inputSender       agent.AgentInputSender
}

type StartSubagentOptions struct {
	ApprovalRequester agent.ApprovalRequester
	ApprovalMode      string
	// SpawnID preserves one user/Control initiated spawn identity across retry.
	// LLM-facing Spawn calls derive this from the stable tool-call ID.
	SpawnID string
}

func normalizeTaskWriteInput(input string, appendNewline *bool, backend sandbox.Backend) string {
	if appendNewline != nil && !*appendNewline {
		return input
	}
	if input == "" {
		return input
	}
	if sandbox.CanonicalBackend(backend) == sandbox.BackendWindows ||
		(sandbox.CanonicalBackend(backend) == sandbox.BackendHost && stdruntime.GOOS == "windows") {
		input = strings.TrimSuffix(input, "\n")
		if strings.HasSuffix(input, "\r") {
			return input
		}
		return input + "\r"
	}
	if strings.HasSuffix(input, "\n") || strings.HasSuffix(input, "\r") {
		return input
	}
	return input + "\n"
}

func subagentTaskStateCanStartTurn(state taskapi.State) bool {
	switch state {
	case taskapi.StateCompleted,
		taskapi.StateFailed,
		taskapi.StateCancelled,
		taskapi.StateInterrupted,
		taskapi.StateTerminated,
		taskapi.StateUnknownOutcome:
		return true
	default:
		return false
	}
}

func stateFromStatus(status sandbox.SessionStatus) taskapi.State {
	if status.Running {
		return taskapi.StateRunning
	}
	if status.ExitCode == 0 {
		return taskapi.StateCompleted
	}
	if status.ExitCode == -1 {
		return taskapi.StateCancelled
	}
	return taskapi.StateFailed
}

func taskStateFromDelegation(state delegation.State) taskapi.State {
	switch state {
	case delegation.StateCompleted:
		return taskapi.StateCompleted
	case delegation.StateCancelled:
		return taskapi.StateCancelled
	case delegation.StateInterrupted:
		return taskapi.StateInterrupted
	case delegation.StateUnknownOutcome:
		return taskapi.StateUnknownOutcome
	case delegation.StateWaitingApproval:
		return taskapi.StateWaitingApproval
	case delegation.StateFailed:
		return taskapi.StateFailed
	default:
		return taskapi.StateRunning
	}
}

func commandExitCodeAvailable(state taskapi.State, exitCode int, resultErr error) bool {
	if exitCode < 0 {
		return false
	}
	switch state {
	case taskapi.StateCompleted, taskapi.StateFailed:
	default:
		return false
	}
	if resultErr != nil && exitCode == 0 {
		return false
	}
	return true
}
