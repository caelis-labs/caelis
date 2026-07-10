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

	mu        sync.RWMutex
	tasks     map[string]*commandTask
	subagents map[string]*subagentTask
	pending   map[string][]stream.Frame
	order     map[string][]string
	backends  map[sandbox.Backend]sandbox.Runtime
	handles   map[string]map[string]struct{}
}

type sandboxRuntimeBackends interface {
	SupportedBackends() []sandbox.Backend
}

type sandboxSessionRefOpener interface {
	OpenSessionRef(sandbox.SessionRef) (sandbox.Session, error)
}

type commandTask struct {
	ref        taskapi.Ref
	sessionRef session.SessionRef
	session    sandbox.Session
	command    string
	workdir    string
	title      string
	createdAt  time.Time
	revision   uint64
	lease      taskapi.Lease

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

func newTaskRuntime(runtime *Runtime, store taskapi.Store) *taskRuntime {
	return &taskRuntime{
		runtime:   runtime,
		store:     store,
		tasks:     map[string]*commandTask{},
		subagents: map[string]*subagentTask{},
		pending:   map[string][]stream.Frame{},
		order:     map[string][]string{},
		backends:  map[sandbox.Backend]sandbox.Runtime{},
		handles:   map[string]map[string]struct{}{},
	}
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
	// LLM-facing SPAWN calls derive this from the stable tool-call ID.
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
