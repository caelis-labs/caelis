package runtime

import (
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
	defaultCommandYield             = 5 * time.Second
	taskWaitMaxYield                = time.Minute
	taskWriteOutputWait             = 2 * time.Second
	taskReadOutputWait              = 30 * time.Second
	taskOutputQuietPeriod           = 100 * time.Millisecond
	taskCancelWait                  = 10 * time.Millisecond
	commandLiveOutputBufferCapBytes = 64 * 1024
	subagentStreamFrameCap          = 1024
	subagentStreamByteCap           = 4 * 1024 * 1024
)

type taskRuntime struct {
	runtime *Runtime
	store   taskapi.Store

	mu         sync.RWMutex
	tasks      map[string]*commandTask
	subagents  map[string]*subagentTask
	pending    map[string][]stream.Frame
	order      map[string][]string
	backends   map[sandbox.Backend]sandbox.Runtime
	handles    map[string]map[string]struct{}
	operations map[string]struct{}
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
	ref        taskapi.Ref
	sessionRef session.SessionRef
	anchor     delegation.Anchor
	runner     subagent.Runner
	agent      string
	target     delegation.Target
	handle     string
	title      string
	prompt     string
	createdAt  time.Time
	revision   uint64
	lease      taskapi.Lease

	// streamMu preserves publication order across the pending-to-live handoff.
	// It must be acquired before publishing the task in taskRuntime.subagents.
	streamMu sync.Mutex
	mu       sync.Mutex
	state    taskapi.State
	running  bool
	result   map[string]any
	metadata map[string]any

	stdout           string
	stderr           string
	stdoutCursor     int64
	stderrCursor     int64
	turnSeq          int64
	streamFrames     []stream.Frame
	streamFrameSizes []int
	// Stream cursors are absolute for the Task lifetime and do not reset when
	// Continue starts another child turn.
	streamEventBase      int64
	streamOutputCursor   int64
	streamBytes          int
	streamTerminalFramed bool
	streamChanged        chan struct{}
}

type subagentContinuationCheckpoint struct {
	stdout         string
	stderr         string
	stdoutCursor   int64
	stderrCursor   int64
	turnSeq        int64
	terminalFramed bool
}

// beginContinuationTurn snapshots current-turn result state, advances turnSeq,
// and reopens the same absolute Task stream for the next child turn. Retained
// frames and absolute event/output cursors deliberately survive Continue.
func (task *subagentTask) beginContinuationTurn() subagentContinuationCheckpoint {
	task.mu.Lock()
	defer task.mu.Unlock()
	checkpoint := subagentContinuationCheckpoint{
		stdout: task.stdout, stderr: task.stderr,
		stdoutCursor: task.stdoutCursor, stderrCursor: task.stderrCursor,
		turnSeq: task.turnSeq, terminalFramed: task.streamTerminalFramed,
	}
	task.turnSeq++
	if task.turnSeq <= 0 {
		task.turnSeq = 1
	}
	task.stdout = ""
	task.stderr = ""
	task.stdoutCursor = 0
	task.stderrCursor = 0
	task.streamTerminalFramed = false
	task.notifyStreamChangeLocked()
	if task.metadata != nil {
		delete(task.metadata, "final_event_persisted")
	}
	return checkpoint
}

// restoreContinuationTurn reverts beginContinuationTurn when parent intent or
// remote continue fails. force restores even if stream output already arrived.
func (task *subagentTask) restoreContinuationTurn(checkpoint subagentContinuationCheckpoint, force bool) {
	task.mu.Lock()
	defer task.mu.Unlock()
	if !force && (task.stdout != "" || task.stderr != "") {
		return
	}
	task.stdout = checkpoint.stdout
	task.stderr = checkpoint.stderr
	task.stdoutCursor = checkpoint.stdoutCursor
	task.stderrCursor = checkpoint.stderrCursor
	task.turnSeq = checkpoint.turnSeq
	task.streamTerminalFramed = checkpoint.terminalFramed
	task.notifyStreamChangeLocked()
}

func newTaskRuntime(runtime *Runtime, store taskapi.Store) *taskRuntime {
	return &taskRuntime{
		runtime:    runtime,
		store:      store,
		tasks:      map[string]*commandTask{},
		subagents:  map[string]*subagentTask{},
		pending:    map[string][]stream.Frame{},
		order:      map[string][]string{},
		backends:   map[sandbox.Backend]sandbox.Runtime{},
		handles:    map[string]map[string]struct{}{},
		operations: map[string]struct{}{},
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
		delete(tm.operations, operationKey)
		tm.mu.Unlock()
	}, true
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
}

type StartSubagentOptions struct {
	ApprovalRequester agent.ApprovalRequester
	ApprovalMode      string
	// SpawnID preserves one user/Control initiated spawn identity across retry.
	// LLM-facing Spawn calls derive this from the stable tool-call ID.
	SpawnID string
}

func normalizeTaskWriteInput(input string) string {
	if input == "" || strings.HasSuffix(input, "\n") || strings.HasSuffix(input, "\r") {
		return input
	}
	return input + "\n"
}

func shouldDropInactiveSubagentTask(snapshot taskapi.Snapshot) bool {
	return !snapshot.Running && snapshot.State != taskapi.StateCompleted
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
