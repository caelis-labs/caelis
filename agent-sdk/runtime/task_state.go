package runtime

import (
	"strings"
	"sync"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
	"github.com/caelis-labs/caelis/agent-sdk/task/subagent"
)

const (
	defaultCommandYield             = 7 * time.Second
	defaultTaskWaitUntilDoneYield   = 5 * time.Minute
	taskCancelWait                  = 10 * time.Millisecond
	commandLiveOutputBufferCapBytes = 64 * 1024
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

type commandTask struct {
	ref           taskapi.Ref
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

	mu             sync.Mutex
	state          taskapi.State
	running        bool
	stdoutCursor   int64
	stderrCursor   int64
	modelCursor    int64
	output         string
	outputBase     int64
	outputLive     bool
	outputCallback bool
	result         map[string]any
	metadata       map[string]any
}

type subagentTask struct {
	ref        taskapi.Ref
	sessionRef session.SessionRef
	anchor     delegation.Anchor
	runner     subagent.Runner
	agent      string
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

	stdout       string
	stderr       string
	stdoutCursor int64
	stderrCursor int64
	turnSeq      int64
	streamFrames []stream.Frame
}

type subagentContinuationCheckpoint struct {
	stdout       string
	stderr       string
	stdoutCursor int64
	stderrCursor int64
	streamFrames []stream.Frame
	turnSeq      int64
}

// beginContinuationTurn snapshots local stream state, advances turnSeq, and
// clears in-memory stream buffers for the next child turn.
func (task *subagentTask) beginContinuationTurn() subagentContinuationCheckpoint {
	task.mu.Lock()
	defer task.mu.Unlock()
	checkpoint := subagentContinuationCheckpoint{
		stdout: task.stdout, stderr: task.stderr,
		stdoutCursor: task.stdoutCursor, stderrCursor: task.stderrCursor,
		streamFrames: append([]stream.Frame(nil), task.streamFrames...),
		turnSeq:      task.turnSeq,
	}
	task.turnSeq++
	if task.turnSeq <= 0 {
		task.turnSeq = 1
	}
	task.stdout = ""
	task.stderr = ""
	task.stdoutCursor = 0
	task.stderrCursor = 0
	task.streamFrames = nil
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
	task.streamFrames = checkpoint.streamFrames
	task.turnSeq = checkpoint.turnSeq
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
