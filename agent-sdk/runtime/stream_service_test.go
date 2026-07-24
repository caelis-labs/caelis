package runtime

import (
	"context"
	"errors"
	"math"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
)

func TestStreamReadCommandUsesCallbackAsSoleLiveIngestAuthority(t *testing.T) {
	t.Parallel()

	const chunk = "chunk\n"
	sess := &liveOutputRaceSession{stdout: chunk}
	task := &commandTask{
		ref:         taskapi.Ref{TaskID: "task-1", SessionID: "term-session", TerminalID: "term-1"},
		sessionRef:  session.SessionRef{SessionID: "session-1"},
		session:     sess,
		state:       taskapi.StateRunning,
		running:     true,
		createdAt:   time.Now(),
		outputState: commandOutputState{callback: true},
	}
	task.appendSandboxOutput(sandbox.OutputChunk{
		Stream: "stdout", Text: chunk, Cursor: int64(len([]byte(chunk))),
	})
	readCalled := false
	sess.onRead = func() { readCalled = true }

	snap, err := (&streamService{}).readCommand(context.Background(), task, stream.Cursor{})
	if err != nil {
		t.Fatalf("readCommand() error = %v", err)
	}
	if got := streamFrameText(snap.Frames); got != chunk {
		t.Fatalf("stream frame text = %q, want one live chunk", got)
	}
	task.mu.Lock()
	stored := task.output
	task.mu.Unlock()
	if stored != chunk {
		t.Fatalf("stored output = %q, want live chunk without fallback duplicate", stored)
	}
	if readCalled || task.outputState.backend.stdout != int64(len([]byte(chunk))) {
		t.Fatalf("live ingest = ReadOutput called %v cursor %d, want callback-only cursor %d", readCalled, task.outputState.backend.stdout, len([]byte(chunk)))
	}
}

func TestStreamReadCommandRejectsOutputAfterTerminalFrame(t *testing.T) {
	t.Parallel()

	task := &commandTask{
		ref:         taskapi.Ref{TaskID: "task-1", SessionID: "term-session", TerminalID: "term-1"},
		sessionRef:  session.SessionRef{SessionID: "session-1"},
		session:     &liveOutputRaceSession{},
		state:       taskapi.StateUnknownOutcome,
		createdAt:   time.Now(),
		outputState: commandOutputState{callback: true},
	}
	service := &streamService{}

	closed, err := service.readCommand(context.Background(), task, stream.Cursor{})
	if err != nil {
		t.Fatalf("readCommand() error = %v", err)
	}
	if len(closed.Frames) != 1 || !closed.Frames[0].Closed || closed.Frames[0].State != string(taskapi.StateUnknownOutcome) {
		t.Fatalf("initial frames = %#v, want one unknown_outcome close", closed.Frames)
	}

	task.appendOutput("late output must not reopen the stream\n")
	resumed, err := service.readCommand(context.Background(), task, closed.Cursor)
	if err != nil {
		t.Fatalf("second readCommand() error = %v", err)
	}
	if len(resumed.Frames) != 0 || resumed.Cursor != closed.Cursor {
		t.Fatalf("post-terminal read = %#v, want unchanged terminal cursor and no frames", resumed)
	}
	task.mu.Lock()
	output := task.output
	task.mu.Unlock()
	if output != "" {
		t.Fatalf("post-terminal aggregate output = %q, want rejected", output)
	}
}

func TestStreamReadRehydratedUnknownOutcomeWithoutSession(t *testing.T) {
	t.Parallel()

	entry := &taskapi.Entry{
		TaskID:    "task-1",
		Session:   session.SessionRef{SessionID: "session-1"},
		State:     taskapi.StateRunning,
		Running:   true,
		CreatedAt: time.Unix(100, 0),
		UpdatedAt: time.Unix(200, 0),
		Metadata:  map[string]any{"command_phase": commandPhaseEffectClaimed},
	}
	task, err := (&taskRuntime{}).rehydrateCommandTask(entry)
	if err != nil {
		t.Fatalf("rehydrateCommandTask() error = %v", err)
	}
	if task.session != nil {
		t.Fatalf("rehydrated session = %#v, want no observable sandbox handle", task.session)
	}

	snapshot, err := (&streamService{}).readCommand(context.Background(), task, stream.Cursor{})
	if err != nil {
		t.Fatalf("readCommand() error = %v", err)
	}
	if snapshot.State != string(taskapi.StateUnknownOutcome) || snapshot.Running || !snapshot.TerminalFramed {
		t.Fatalf("snapshot = %#v, want terminal unknown_outcome", snapshot)
	}
	if snapshot.ExitCode != nil {
		t.Fatalf("snapshot.ExitCode = %v, want unavailable exit status", *snapshot.ExitCode)
	}
	if len(snapshot.Frames) != 1 || !snapshot.Frames[0].Closed || snapshot.Frames[0].State != string(taskapi.StateUnknownOutcome) {
		t.Fatalf("frames = %#v, want one unknown_outcome close", snapshot.Frames)
	}
	if snapshot.Frames[0].ExitCode != nil {
		t.Fatalf("close frame ExitCode = %v, want unavailable exit status", *snapshot.Frames[0].ExitCode)
	}
}

func TestStreamReadRehydratedRunningCommandKeepsAbsoluteOutputBaseline(t *testing.T) {
	t.Parallel()

	const (
		alreadyObserved = "already observed\n"
		tail            = "new output\n"
	)
	baseline := int64(len([]byte(alreadyObserved)))
	entry := &taskapi.Entry{
		TaskID:    "task-1",
		Session:   session.SessionRef{SessionID: "session-1"},
		State:     taskapi.StateRunning,
		Running:   true,
		CreatedAt: time.Unix(100, 0),
		UpdatedAt: time.Unix(200, 0),
		Metadata: map[string]any{
			"command_phase":              commandPhaseUnknown,
			"output_cursor":              baseline,
			"model_output_cursor":        baseline,
			"output_checkpoint_coherent": true,
		},
	}
	task, err := (&taskRuntime{}).rehydrateCommandTask(entry)
	if err != nil {
		t.Fatalf("rehydrateCommandTask() error = %v", err)
	}
	if task.outputState.frontier.base != baseline || task.outputState.frontier.model != baseline {
		t.Fatalf("rehydrated cursors = base %d model %d, want %d", task.outputState.frontier.base, task.outputState.frontier.model, baseline)
	}

	task.appendOutput(tail)
	snapshot, err := (&streamService{}).readCommand(context.Background(), task, stream.Cursor{Output: baseline})
	if err != nil {
		t.Fatalf("readCommand() error = %v", err)
	}
	if got := streamFrameText(snapshot.Frames); got != tail {
		t.Fatalf("stream frame text = %q, want %q", got, tail)
	}
	if snapshot.Cursor.Output != baseline+int64(len([]byte(tail))) {
		t.Fatalf("stream output cursor = %d, want %d", snapshot.Cursor.Output, baseline+int64(len([]byte(tail))))
	}
}

func TestRehydrateRunningCommandIgnoresLegacyUnfencedOutputCheckpoint(t *testing.T) {
	t.Parallel()

	entry := &taskapi.Entry{
		TaskID:       "task-1",
		Session:      session.SessionRef{SessionID: "session-1"},
		State:        taskapi.StateRunning,
		Running:      true,
		CreatedAt:    time.Unix(100, 0),
		UpdatedAt:    time.Unix(200, 0),
		StdoutCursor: 12,
		Metadata: map[string]any{
			"command_phase":       commandPhaseUnknown,
			"output_cursor":       int64(12),
			"model_output_cursor": int64(12),
		},
	}
	task, err := (&taskRuntime{}).rehydrateCommandTask(entry)
	if err != nil {
		t.Fatalf("rehydrateCommandTask() error = %v", err)
	}
	if task.outputState.frontier.base != 0 || task.outputState.frontier.model != 0 || task.outputState.backend.stdout != 0 {
		t.Fatalf("legacy checkpoint restored as coherent: base=%d model=%d stdout=%d", task.outputState.frontier.base, task.outputState.frontier.model, task.outputState.backend.stdout)
	}
}

func TestRehydratedRunningCommandResumesFromAtomicCallbackCheckpoint(t *testing.T) {
	t.Parallel()

	const (
		shown = "shown\n"
		tail  = "tail\n"
	)
	live := &commandTask{
		ref:        taskapi.Ref{TaskID: "task-1", SessionID: "term-session", TerminalID: "term-1"},
		sessionRef: session.SessionRef{SessionID: "session-1"},
		state:      taskapi.StateRunning, running: true, createdAt: time.Now(),
		outputState: commandOutputState{
			callback:   true,
			exact:      true,
			checkpoint: commandOutputCheckpoint{coherent: true},
		},
		metadata: map[string]any{"command_phase": commandPhaseRunning},
	}
	live.appendSandboxOutput(sandbox.OutputChunk{
		Stream: "stdout", Text: shown, Cursor: int64(len([]byte(shown))),
	})
	live.mu.Lock()
	live.outputState.frontier.model = int64(len([]byte(shown)))
	live.commitOutputResumeCheckpointLocked()
	live.metadata["output_cursor"] = live.outputState.frontier.model
	live.metadata["model_output_cursor"] = live.outputState.frontier.model
	entry := live.entrySnapshot(time.Now())
	live.mu.Unlock()
	entry.Terminal = sandbox.TerminalRef{
		Backend: sandbox.BackendHost, SessionID: "term-session", TerminalID: "term-1",
	}
	coherent, _ := entry.Metadata["output_checkpoint_coherent"].(bool)
	if entry.StdoutCursor != int64(len([]byte(shown))) || !coherent {
		t.Fatalf("durable checkpoint = %#v, want callback text and stdout marker committed together", entry)
	}

	running := true
	sessionHandle := &yieldProbeSandboxSession{statusRunning: &running, stdout: shown + tail}
	backend := &yieldProbeSandboxRuntime{session: sessionHandle}
	tasks := newTaskRuntime(&Runtime{clock: time.Now}, nil)
	tasks.backends[sandbox.BackendHost] = backend
	rehydrated, err := tasks.rehydrateCommandTask(entry)
	if err != nil {
		t.Fatalf("rehydrateCommandTask() error = %v", err)
	}
	snapshot, err := (&streamService{}).readCommand(
		context.Background(),
		rehydrated,
		stream.Cursor{Output: int64(len([]byte(shown))), Events: 3},
	)
	if err != nil {
		t.Fatalf("readCommand() error = %v", err)
	}
	if got := streamFrameText(snapshot.Frames); got != tail {
		t.Fatalf("rehydrated stream = %q, want only unseen %q", got, tail)
	}
	if snapshot.Cursor.Output != int64(len([]byte(shown+tail))) {
		t.Fatalf("rehydrated cursor = %d, want %d", snapshot.Cursor.Output, len([]byte(shown+tail)))
	}
	if snapshot.Cursor.Events != 3 {
		t.Fatalf("rehydrated event cursor = %d, want non-regressing caller baseline 3", snapshot.Cursor.Events)
	}
	if len(snapshot.Frames) != 1 || snapshot.Frames[0].Cursor.Events != 3 {
		t.Fatalf("rehydrated frame cursors = %#v, want caller event baseline 3 on the delivery clone", snapshot.Frames)
	}
}

func TestRunningEntryBeforeModelObservationKeepsPreviousResumeCheckpoint(t *testing.T) {
	t.Parallel()

	const (
		early = "early\n"
		later = "later\n"
	)
	live := &commandTask{
		ref:        taskapi.Ref{TaskID: "task-1", SessionID: "term-session", TerminalID: "term-1"},
		sessionRef: session.SessionRef{SessionID: "session-1"},
		state:      taskapi.StateRunning,
		running:    true,
		createdAt:  time.Now(),
		outputState: commandOutputState{
			callback: true,
			exact:    true,
			checkpoint: commandOutputCheckpoint{
				available: true,
				coherent:  true,
			},
			resume: commandOutputCheckpoint{
				available: true,
				coherent:  true,
			},
		},
		metadata: map[string]any{
			"command_phase": commandPhaseRunning,
		},
	}
	live.appendSandboxOutput(sandbox.OutputChunk{
		Stream: "stdout",
		Text:   early,
		Cursor: int64(len([]byte(early))),
	})

	live.mu.Lock()
	entry := live.entrySnapshot(time.Now())
	live.mu.Unlock()
	entry.Terminal = sandbox.TerminalRef{
		Backend: sandbox.BackendHost, SessionID: "term-session", TerminalID: "term-1",
	}
	outputCursor, outputKnown := taskInt64Value(entry.Metadata["output_cursor"])
	modelCursor, modelKnown := taskInt64Value(entry.Metadata["model_output_cursor"])
	if entry.StdoutCursor != 0 || !outputKnown || !modelKnown || outputCursor != 0 || modelCursor != 0 {
		t.Fatalf(
			"pre-observation durable checkpoint = stdout %d output %d/%v model %d/%v, want previous zero resume point",
			entry.StdoutCursor,
			outputCursor,
			outputKnown,
			modelCursor,
			modelKnown,
		)
	}

	running := true
	sessionHandle := &yieldProbeSandboxSession{
		statusRunning: &running,
		stdout:        early + later,
	}
	tasks := newTaskRuntime(&Runtime{clock: time.Now}, nil)
	tasks.backends[sandbox.BackendHost] = &yieldProbeSandboxRuntime{session: sessionHandle}
	rehydrated, err := tasks.rehydrateCommandTask(entry)
	if err != nil {
		t.Fatalf("rehydrateCommandTask() error = %v", err)
	}
	snapshot, err := (&streamService{}).readCommand(context.Background(), rehydrated, stream.Cursor{})
	if err != nil {
		t.Fatalf("readCommand() error = %v", err)
	}
	if got := streamFrameText(snapshot.Frames); got != early+later {
		t.Fatalf("rehydrated stream = %q, want unobserved early output plus later output %q", got, early+later)
	}
}

func TestRehydrateRunningCommandRejectsCheckpointAheadOfModelObservation(t *testing.T) {
	t.Parallel()

	const outputCursor = int64(6)
	entry := &taskapi.Entry{
		TaskID:       "task-1",
		Session:      session.SessionRef{SessionID: "session-1"},
		State:        taskapi.StateRunning,
		Running:      true,
		CreatedAt:    time.Unix(100, 0),
		UpdatedAt:    time.Unix(200, 0),
		StdoutCursor: outputCursor,
		Metadata: map[string]any{
			"command_phase":               commandPhaseUnknown,
			"output_checkpoint_available": true,
			"output_checkpoint_coherent":  true,
			"output_cursor":               outputCursor,
			"model_output_cursor":         int64(0),
		},
	}
	task, err := (&taskRuntime{}).rehydrateCommandTask(entry)
	if err != nil {
		t.Fatalf("rehydrateCommandTask() error = %v", err)
	}
	if task.outputState.backend.stdout != 0 || task.outputState.frontier.base != 0 || task.outputState.frontier.model != 0 || task.outputState.resume.available {
		t.Fatalf(
			"inconsistent checkpoint restored: stdout=%d base=%d model=%d resume=%v",
			task.outputState.backend.stdout,
			task.outputState.frontier.base,
			task.outputState.frontier.model,
			task.outputState.resume.available,
		)
	}
}

func TestRehydrateCommandRejectsExhaustedStreamEventCursor(t *testing.T) {
	t.Parallel()

	entry := &taskapi.Entry{
		TaskID:    "task-1",
		Session:   session.SessionRef{SessionID: "session-1"},
		State:     taskapi.StateCompleted,
		Running:   false,
		CreatedAt: time.Unix(100, 0),
		UpdatedAt: time.Unix(200, 0),
		Metadata: map[string]any{
			commandStreamEventCursorMeta: int64(math.MaxInt64),
		},
	}
	if _, err := (&taskRuntime{}).rehydrateCommandTask(entry); err == nil ||
		!strings.Contains(err.Error(), "event cursor is exhausted") {
		t.Fatalf("rehydrateCommandTask() error = %v, want exhausted event cursor rejection", err)
	}
}

func TestRehydratedCommandRecoveryDecodesSplitUTF8AcrossReads(t *testing.T) {
	t.Parallel()

	running := true
	raw := []byte("中")
	sessionHandle := &yieldProbeSandboxSession{
		statusRunning: &running,
		stdout:        "a" + string(raw[:1]),
	}
	task := &commandTask{
		ref:        taskapi.Ref{TaskID: "task-1", SessionID: sessionHandle.Ref().SessionID, TerminalID: sessionHandle.Terminal().TerminalID},
		sessionRef: session.SessionRef{SessionID: "session-1"},
		session:    sessionHandle,
		state:      taskapi.StateRunning,
		running:    true,
		createdAt:  time.Now(),
		metadata:   map[string]any{"command_phase": commandPhaseRunning},
	}
	status, err := sessionHandle.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	first, err := newTaskRuntime(&Runtime{clock: time.Now}, nil).reconcileCommandStatus(
		context.Background(),
		task,
		status,
	)
	if err != nil {
		t.Fatalf("reconcileCommandStatus() error = %v", err)
	}
	if got, _ := first.Result["latest_output"].(string); got != "a" {
		t.Fatalf("first recovery latest_output = %q, want complete prefix only", got)
	}
	task.mu.Lock()
	firstPending := task.outputState.recoveryStdout.PendingBytes()
	firstCoherent := task.outputState.checkpoint.coherent
	entry := task.entrySnapshot(time.Now())
	task.mu.Unlock()
	if firstPending != 1 || !firstCoherent {
		t.Fatalf("first recovery pending/coherent = %d/%v, want 1/true committed prefix", firstPending, firstCoherent)
	}
	if entry.StdoutCursor != 1 {
		t.Fatalf("incomplete recovery persisted stdout cursor %d, want complete-prefix marker 1", entry.StdoutCursor)
	}
	if cursor, ok := taskInt64Value(entry.Metadata["output_cursor"]); !ok || cursor != 1 {
		t.Fatalf("incomplete recovery output checkpoint = %d/%v, want 1/true", cursor, ok)
	}

	sessionHandle.stdout = "a" + string(raw)
	tasks := newTaskRuntime(&Runtime{clock: time.Now}, nil)
	tasks.backends[sandbox.BackendHost] = &yieldProbeSandboxRuntime{session: sessionHandle}
	rehydrated, err := tasks.rehydrateCommandTask(entry)
	if err != nil {
		t.Fatalf("rehydrateCommandTask() error = %v", err)
	}

	second, err := (&streamService{}).readCommand(context.Background(), rehydrated, stream.Cursor{Output: 1})
	if err != nil {
		t.Fatalf("second readCommand() error = %v", err)
	}
	if got := streamFrameText(second.Frames); got != "中" || !utf8.ValidString(got) {
		t.Fatalf("second recovery text = %q valid=%v, want one valid rune", got, utf8.ValidString(got))
	}
	rehydrated.mu.Lock()
	secondPending := rehydrated.outputState.recoveryStdout.PendingBytes()
	secondCoherent := rehydrated.outputState.checkpoint.coherent
	secondEntry := rehydrated.entrySnapshot(time.Now())
	rehydrated.mu.Unlock()
	if secondPending != 0 || !secondCoherent || secondEntry.StdoutCursor != 1 {
		t.Fatalf(
			"pre-observation recovery pending/coherent/resume cursor = %d/%v/%d, want 0/true/1",
			secondPending,
			secondCoherent,
			secondEntry.StdoutCursor,
		)
	}

	status, err = sessionHandle.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.reconcileCommandStatus(context.Background(), rehydrated, status); err != nil {
		t.Fatalf("reconcileCommandStatus() second observation error = %v", err)
	}
	rehydrated.mu.Lock()
	observedEntry := rehydrated.entrySnapshot(time.Now())
	rehydrated.mu.Unlock()
	if observedEntry.StdoutCursor != int64(1+len(raw)) {
		t.Fatalf(
			"post-observation durable stdout cursor = %d, want %d",
			observedEntry.StdoutCursor,
			1+len(raw),
		)
	}
}

func TestRehydratedCommandRecoveryGapIsExplicitAndTerminalCursorDoesNotRegress(t *testing.T) {
	t.Parallel()

	const (
		baseline = int64(10)
		next     = int64(30)
		tail     = "tail"
	)
	running := true
	sessionHandle := &yieldProbeSandboxSession{
		statusRunning: &running,
		result: sandbox.CommandResult{
			Stdout: tail, ExitCode: 0, Backend: sandbox.BackendHost,
		},
		readOutput: func(stdoutCursor int64, stderrCursor int64) ([]byte, []byte, int64, int64, error) {
			if stdoutCursor < next {
				return []byte(tail), nil, next, stderrCursor, nil
			}
			return nil, nil, next, stderrCursor, nil
		},
	}
	task := &commandTask{
		ref:        taskapi.Ref{TaskID: "task-1", SessionID: sessionHandle.Ref().SessionID, TerminalID: sessionHandle.Terminal().TerminalID},
		sessionRef: session.SessionRef{SessionID: "session-1"},
		session:    sessionHandle,
		state:      taskapi.StateRunning,
		running:    true,
		createdAt:  time.Now(),
		outputState: commandOutputState{
			backend:  commandBackendCursor{stdout: baseline},
			frontier: commandObservationFrontier{base: baseline, model: baseline},
			checkpoint: commandOutputCheckpoint{
				backend:   commandBackendCursor{stdout: baseline},
				output:    baseline,
				model:     baseline,
				available: true,
				coherent:  true,
			},
		},
		metadata: map[string]any{
			"output_checkpoint_available": true,
			"command_phase":               commandPhaseRunning,
			"output_cursor":               baseline,
			"model_output_cursor":         baseline,
			"output_checkpoint_coherent":  true,
		},
	}
	status, err := sessionHandle.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	observed, err := newTaskRuntime(&Runtime{clock: time.Now}, nil).reconcileCommandStatus(
		context.Background(),
		task,
		status,
	)
	if err != nil {
		t.Fatalf("reconcileCommandStatus() running error = %v", err)
	}
	runningCursor, ok := taskInt64Value(observed.Metadata["output_cursor"])
	if !ok || runningCursor <= baseline {
		t.Fatalf("running output cursor = %d/%v, want > %d", runningCursor, ok, baseline)
	}
	streamed, err := (&streamService{}).readCommand(context.Background(), task, stream.Cursor{Output: baseline})
	if err != nil {
		t.Fatalf("readCommand() error = %v", err)
	}
	if streamed.TruncatedBefore <= baseline || streamFrameText(streamed.Frames) != tail {
		t.Fatalf("gap stream = %#v, want explicit gap plus retained %q", streamed, tail)
	}
	task.mu.Lock()
	coherent := task.outputState.checkpoint.coherent
	entry := task.entrySnapshot(time.Now())
	task.mu.Unlock()
	if coherent || entry.StdoutCursor != next {
		t.Fatalf("gap checkpoint = coherent %v cursor %d, want false/%d", coherent, entry.StdoutCursor, next)
	}
	if available, _ := entry.Metadata["output_checkpoint_available"].(bool); !available {
		t.Fatalf("gap checkpoint is not resumable: %#v", entry.Metadata)
	}
	if gap, _ := entry.Metadata["output_recovery_gap"].(bool); !gap {
		t.Fatalf("gap checkpoint lost gap epoch: %#v", entry.Metadata)
	}
	if cursor, ok := taskInt64Value(entry.Metadata["output_cursor"]); !ok || cursor != runningCursor {
		t.Fatalf("gap presentation checkpoint = %d/%v, want %d/true", cursor, ok, runningCursor)
	}

	tasks := newTaskRuntime(&Runtime{clock: time.Now}, nil)
	tasks.backends[sandbox.BackendHost] = &yieldProbeSandboxRuntime{session: sessionHandle}
	rehydrated, err := tasks.rehydrateCommandTask(entry)
	if err != nil {
		t.Fatalf("rehydrateCommandTask() error = %v", err)
	}
	if rehydrated.outputState.frontier.base != runningCursor || !rehydrated.outputState.checkpoint.gap {
		t.Fatalf("rehydrated gap baseline = %d/%v, want %d/true", rehydrated.outputState.frontier.base, rehydrated.outputState.checkpoint.gap, runningCursor)
	}
	noReplay, err := (&streamService{}).readCommand(
		context.Background(),
		rehydrated,
		stream.Cursor{Output: runningCursor},
	)
	if err != nil {
		t.Fatalf("rehydrated readCommand() error = %v", err)
	}
	if got := streamFrameText(noReplay.Frames); got != "" || noReplay.Cursor.Output != runningCursor {
		t.Fatalf("rehydrated gap replayed retained tail: %#v", noReplay)
	}

	const continued = "more"
	sessionHandle.readOutput = func(stdoutCursor int64, stderrCursor int64) ([]byte, []byte, int64, int64, error) {
		if stdoutCursor < next+int64(len(continued)) {
			return []byte(continued), nil, next + int64(len(continued)), stderrCursor, nil
		}
		return nil, nil, next + int64(len(continued)), stderrCursor, nil
	}
	continuedSnapshot, err := (&streamService{}).readCommand(
		context.Background(),
		rehydrated,
		stream.Cursor{Output: runningCursor},
	)
	if err != nil {
		t.Fatalf("continued readCommand() error = %v", err)
	}
	if got := streamFrameText(continuedSnapshot.Frames); got != continued || continuedSnapshot.Cursor.Output <= runningCursor {
		t.Fatalf("continued gap stream = %#v, want monotonic %q", continuedSnapshot, continued)
	}

	running = false
	status, err = sessionHandle.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	completed, err := tasks.reconcileCommandStatus(
		context.Background(),
		rehydrated,
		status,
	)
	if err != nil {
		t.Fatalf("reconcileCommandStatus() error = %v", err)
	}
	terminalCursor, ok := taskInt64Value(completed.Metadata["output_cursor"])
	if !ok || terminalCursor < continuedSnapshot.Cursor.Output {
		t.Fatalf("terminal output cursor = %d/%v, want >= continued cursor %d", terminalCursor, ok, continuedSnapshot.Cursor.Output)
	}
	if coherent, _ := completed.Metadata["output_checkpoint_coherent"].(bool); coherent {
		t.Fatalf("terminal recovery gap became coherent: %#v", completed.Metadata)
	}
}

func TestStreamAwaitCommandWakesForStateOnlyTerminalTransition(t *testing.T) {
	t.Parallel()

	sessionHandle := &liveOutputRaceSession{awaitStarted: make(chan struct{}, 1)}
	task := &commandTask{
		ref:        taskapi.Ref{TaskID: "task-1", SessionID: "term-session", TerminalID: "term-1"},
		sessionRef: session.SessionRef{SessionID: "session-1"},
		session:    sessionHandle,
		state:      taskapi.StateRunning,
		running:    true,
		createdAt:  time.Now(),
		result:     map[string]any{"state": string(taskapi.StateRunning)},
		metadata:   map[string]any{"state": string(taskapi.StateRunning), "running": true},
	}
	service := newStreamService(&taskRuntime{tasks: map[string]*commandTask{task.ref.TaskID: task}})
	result := make(chan stream.Snapshot, 1)
	errs := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() {
		snapshot, err := service.await(ctx, stream.ReadRequest{
			Ref: stream.Ref{SessionID: task.sessionRef.SessionID, TaskID: task.ref.TaskID},
		})
		if err != nil {
			errs <- err
			return
		}
		result <- snapshot
	}()

	select {
	case <-sessionHandle.awaitStarted:
	case <-ctx.Done():
		t.Fatal("command AwaitOutput did not block")
	}
	applyCommandEntry(task, &taskapi.Entry{
		TaskID:  task.ref.TaskID,
		Session: task.sessionRef,
		State:   taskapi.StateUnknownOutcome,
		Running: false,
		Result:  map[string]any{"state": string(taskapi.StateUnknownOutcome), "error": "cancel outcome unknown"},
		Metadata: map[string]any{
			"state": string(taskapi.StateUnknownOutcome), "running": false,
		},
		Terminal: task.session.Terminal(),
	})

	select {
	case err := <-errs:
		t.Fatalf("await() error = %v", err)
	case snapshot := <-result:
		if snapshot.Running || snapshot.State != string(taskapi.StateUnknownOutcome) || !snapshot.TerminalFramed {
			t.Fatalf("await() snapshot = %#v, want terminal state-only transition", snapshot)
		}
	case <-ctx.Done():
		t.Fatal("state-only terminal transition did not wake command stream")
	}
}

func TestTerminalDurableReloadContinuesEventCursorWithClosedFrame(t *testing.T) {
	t.Parallel()

	running := true
	sessionHandle := &yieldProbeSandboxSession{
		statusRunning: &running,
		result: sandbox.CommandResult{
			Stdout:   "one\ntwo\nthree\n",
			ExitCode: 0,
			Backend:  sandbox.BackendHost,
		},
	}
	task := &commandTask{
		ref:        taskapi.Ref{TaskID: "task-1", SessionID: sessionHandle.Ref().SessionID, TerminalID: sessionHandle.Terminal().TerminalID},
		sessionRef: session.SessionRef{SessionID: "session-1"},
		session:    sessionHandle,
		state:      taskapi.StateRunning,
		running:    true,
		createdAt:  time.Now(),
		outputState: commandOutputState{
			callback:   true,
			exact:      true,
			checkpoint: commandOutputCheckpoint{coherent: true},
		},
		metadata: map[string]any{"command_phase": commandPhaseRunning},
	}
	var stdoutCursor int64
	for _, text := range []string{"one\n", "two\n", "three\n"} {
		stdoutCursor += int64(len([]byte(text)))
		task.appendSandboxOutput(sandbox.OutputChunk{
			Stream: "stdout",
			Text:   text,
			Cursor: stdoutCursor,
		})
	}
	before, err := (&streamService{}).readCommand(context.Background(), task, stream.Cursor{})
	if err != nil {
		t.Fatalf("readCommand() running error = %v", err)
	}
	if before.Cursor.Events != 3 {
		t.Fatalf("running event cursor = %d, want three callback frames", before.Cursor.Events)
	}

	store := newFileTaskStoreForTest(t)
	tasks := newTaskRuntime(&Runtime{clock: time.Now}, store)
	running = false
	status, err := sessionHandle.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.reconcileCommandStatus(context.Background(), task, status); err != nil {
		t.Fatalf("reconcileCommandStatus() error = %v", err)
	}
	entry, err := store.Get(context.Background(), task.ref.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if eventCursor, ok := taskInt64Value(entry.Metadata[commandStreamEventCursorMeta]); !ok || eventCursor != before.Cursor.Events {
		t.Fatalf("durable event baseline = %d/%v, want %d", eventCursor, ok, before.Cursor.Events)
	}

	reloaded, err := newStreamService(tasks).Read(context.Background(), stream.ReadRequest{
		Ref: stream.Ref{SessionID: task.sessionRef.SessionID, TaskID: task.ref.TaskID},
		Cursor: stream.Cursor{
			Output: before.Cursor.Output,
			Events: before.Cursor.Events,
		},
	})
	if err != nil {
		t.Fatalf("stream Read() after durable reload error = %v", err)
	}
	if reloaded.Cursor.Events != before.Cursor.Events+1 {
		t.Fatalf("terminal event cursor = %d, want %d", reloaded.Cursor.Events, before.Cursor.Events+1)
	}
	if len(reloaded.Frames) != 1 || !reloaded.Frames[0].Closed ||
		reloaded.Frames[0].Cursor.Events != before.Cursor.Events+1 {
		t.Fatalf("terminal reload frames = %#v, want one monotonic closed frame", reloaded.Frames)
	}

	ahead, err := newStreamService(tasks).Read(context.Background(), stream.ReadRequest{
		Ref: stream.Ref{SessionID: task.sessionRef.SessionID, TaskID: task.ref.TaskID},
		Cursor: stream.Cursor{
			Output: before.Cursor.Output,
			Events: math.MaxInt64,
		},
	})
	if err != nil {
		t.Fatalf("stream Read() with saturated event cursor error = %v", err)
	}
	if ahead.Cursor.Events != math.MaxInt64 || !ahead.TerminalFramed {
		t.Fatalf("saturated event snapshot = %#v, want idempotent acknowledged event plane", ahead)
	}
	projected := stream.FramesForSnapshot(ahead)
	if len(projected) != 0 {
		t.Fatalf("saturated event projected frames = %#v, want no repeated close beyond acknowledged cursor", projected)
	}
	aheadAgain, err := newStreamService(tasks).Read(context.Background(), stream.ReadRequest{
		Ref: stream.Ref{SessionID: task.sessionRef.SessionID, TaskID: task.ref.TaskID},
		Cursor: stream.Cursor{
			Output: ahead.Cursor.Output,
			Events: ahead.Cursor.Events,
		},
	})
	if err != nil {
		t.Fatalf("stream Read() after saturated terminal snapshot error = %v", err)
	}
	if aheadAgain.Cursor != ahead.Cursor || len(stream.FramesForSnapshot(aheadAgain)) != 0 {
		t.Fatalf("saturated terminal read was not idempotent: first=%#v second=%#v", ahead, aheadAgain)
	}

	normalAgain, err := newStreamService(tasks).Read(context.Background(), stream.ReadRequest{
		Ref: stream.Ref{SessionID: task.sessionRef.SessionID, TaskID: task.ref.TaskID},
		Cursor: stream.Cursor{
			Output: before.Cursor.Output,
			Events: before.Cursor.Events,
		},
	})
	if err != nil {
		t.Fatalf("stream Read() after saturated reader error = %v", err)
	}
	if len(normalAgain.Frames) != 1 || !normalAgain.Frames[0].Closed ||
		normalAgain.Frames[0].Cursor.Events != before.Cursor.Events+1 {
		t.Fatalf("saturated reader poisoned shared stream state: %#v", normalAgain)
	}
}

func TestCompletedTaskSessionAwaitOutputRejectsCursorAhead(t *testing.T) {
	t.Parallel()

	sessionHandle := completedTaskSession{entry: &taskapi.Entry{
		State:   taskapi.StateCompleted,
		Running: false,
		Result:  map[string]any{"result": "done\n", "exit_code": 0},
	}}
	_, err := sessionHandle.AwaitOutput(context.Background(), sandbox.OutputCursor{Stdout: 6})
	var cursorErr *sandbox.OutputCursorAheadError
	if !errors.As(err, &cursorErr) {
		t.Fatalf("AwaitOutput() error = %v, want OutputCursorAheadError", err)
	}
}

func TestStreamSubscribeFollowsOnlyAReopenedSubagentContinue(t *testing.T) {
	t.Parallel()

	task := &subagentTask{
		ref:        taskapi.Ref{TaskID: "task-1", SessionID: "session-1", TerminalID: "task-1:1"},
		sessionRef: session.SessionRef{SessionID: "session-1"},
		state:      taskapi.StateCompleted,
		turnSeq:    1,
		createdAt:  time.Now(),
		result:     map[string]any{"result": "first turn"},
	}
	service := newStreamService(&taskRuntime{
		tasks:     map[string]*commandTask{},
		subagents: map[string]*subagentTask{task.ref.TaskID: task},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	frames := make(chan stream.Frame, 4)
	done := make(chan error, 1)
	go func() {
		for frame, err := range service.Subscribe(ctx, stream.SubscribeRequest{
			Ref:             stream.Ref{SessionID: task.sessionRef.SessionID, TaskID: task.ref.TaskID},
			FollowContinues: true,
		}) {
			if err != nil {
				done <- err
				return
			}
			if frame != nil {
				frames <- stream.CloneFrame(*frame)
			}
		}
		done <- nil
	}()

	first := receiveStreamFrame(t, frames)
	if !first.Closed || first.State != string(taskapi.StateCompleted) {
		t.Fatalf("first frame = %#v, want completed turn close", first)
	}
	task.applyStreamFrames([]stream.Frame{{
		Text: "late first-turn output", State: string(taskapi.StateRunning), Running: true,
	}})
	task.mu.Lock()
	lateState, lateRunning, lateResult := task.state, task.running, task.result["output_preview"]
	task.mu.Unlock()
	if lateState != taskapi.StateCompleted || lateRunning || lateResult != nil {
		t.Fatalf("late frame changed closed Task to state=%q running=%t result=%#v", lateState, lateRunning, lateResult)
	}
	select {
	case frame := <-frames:
		t.Fatalf("post-terminal frame was delivered before Continue: %#v", frame)
	case <-time.After(20 * time.Millisecond):
	}

	task.beginContinuationTurn()
	task.mu.Lock()
	task.state = taskapi.StateRunning
	task.running = true
	task.mu.Unlock()
	task.applyStreamFrames([]stream.Frame{{Text: "continued output", Running: true}})
	continued := receiveStreamFrame(t, frames)
	if continued.Closed || continued.Text != "continued output" || continued.Ref.TerminalID != "task-1:2" || continued.Cursor.Events <= first.Cursor.Events {
		t.Fatalf("continued frame = %#v, want reopened Task stream with a higher absolute cursor", continued)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("Subscribe() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Subscribe() did not stop after cancellation")
	}
}

func receiveStreamFrame(t *testing.T, frames <-chan stream.Frame) stream.Frame {
	t.Helper()
	select {
	case frame := <-frames:
		return frame
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Task stream frame")
		return stream.Frame{}
	}
}

func TestStreamReadCommandUsesReadOutputFallbackWithoutCallbackSource(t *testing.T) {
	t.Parallel()

	const chunk = "fallback\n"
	task := &commandTask{
		ref:        taskapi.Ref{TaskID: "task-1", SessionID: "term-session", TerminalID: "term-1"},
		sessionRef: session.SessionRef{SessionID: "session-1"},
		session:    &liveOutputRaceSession{stdout: chunk},
		state:      taskapi.StateRunning,
		running:    true,
		createdAt:  time.Now(),
	}

	snap, err := (&streamService{}).readCommand(context.Background(), task, stream.Cursor{})
	if err != nil {
		t.Fatalf("readCommand() error = %v", err)
	}
	if got := streamFrameText(snap.Frames); got != chunk {
		t.Fatalf("stream frame text = %q, want ReadOutput fallback chunk", got)
	}
}

func TestStreamReadCommandCompletedEmitsUndeliveredCallbackTail(t *testing.T) {
	t.Parallel()

	const shown = "live\n"
	const tail = "final\n"
	task := &commandTask{
		ref:         taskapi.Ref{TaskID: "task-1", SessionID: "term-session", TerminalID: "term-1"},
		sessionRef:  session.SessionRef{SessionID: "session-1"},
		session:     &liveOutputRaceSession{stdout: shown + tail, completed: true},
		state:       taskapi.StateRunning,
		running:     true,
		createdAt:   time.Now(),
		output:      shown,
		outputState: commandOutputState{callback: true},
	}

	snap, err := (&streamService{}).readCommand(context.Background(), task, stream.Cursor{Output: int64(len(shown))})
	if err != nil {
		t.Fatalf("readCommand() error = %v", err)
	}
	if got := streamFrameText(snap.Frames); got != tail {
		t.Fatalf("stream frame text = %q, want undelivered callback tail %q", got, tail)
	}
	if snap.Cursor.Output != int64(len(shown+tail)) {
		t.Fatalf("cursor.Output = %d, want complete delivered length %d", snap.Cursor.Output, len(shown+tail))
	}
	if snap.FinalText != shown+tail {
		t.Fatalf("FinalText = %q, want complete command result", snap.FinalText)
	}
}

func TestStreamSubscribeEmitsUndeliveredCommandTailBeforeClose(t *testing.T) {
	t.Parallel()

	const shown = "步骤 5/5: 处理中...\n"
	const tail = "✅ 任务完成！\n"
	task := &commandTask{
		ref:         taskapi.Ref{TaskID: "task-1", SessionID: "term-session", TerminalID: "term-1"},
		sessionRef:  session.SessionRef{SessionID: "session-1"},
		session:     &liveOutputRaceSession{stdout: shown + tail, completed: true},
		state:       taskapi.StateRunning,
		running:     true,
		createdAt:   time.Now(),
		output:      shown,
		outputState: commandOutputState{callback: true},
	}
	service := newStreamService(&taskRuntime{
		tasks: map[string]*commandTask{task.ref.TaskID: task},
	})

	var frames []stream.Frame
	for frame, err := range service.Subscribe(context.Background(), stream.SubscribeRequest{
		Ref: stream.Ref{
			SessionID: task.sessionRef.SessionID,
			TaskID:    task.ref.TaskID,
		},
		Cursor: stream.Cursor{Output: int64(len(shown))},
	}) {
		if err != nil {
			t.Fatalf("Subscribe() error = %v", err)
		}
		if frame != nil {
			frames = append(frames, stream.CloneFrame(*frame))
		}
	}
	if len(frames) != 2 {
		t.Fatalf("frames = %#v, want output tail then close", frames)
	}
	if frames[0].Closed || frames[0].Text != tail {
		t.Fatalf("first frame = %#v, want open tail %q", frames[0], tail)
	}
	if !frames[1].Closed || frames[1].Text != "" {
		t.Fatalf("second frame = %#v, want contentless close", frames[1])
	}
	if frames[1].ExitCode == nil || *frames[1].ExitCode != 0 {
		t.Fatalf("second frame exit code = %#v, want command exit code 0", frames[1].ExitCode)
	}
	if frames[0].Cursor.Output != int64(len(shown+tail)) || frames[1].Cursor.Output != frames[0].Cursor.Output {
		t.Fatalf("frame cursors = %#v, want delivered cursor %d preserved through close", frames, len(shown+tail))
	}
}

func TestResolvedStreamReaderKeepsLiveTaskAcrossDeferredDurableReplacement(t *testing.T) {
	t.Parallel()

	const shown = "步骤 5/5: 处理中...\n"
	const tail = "✅ 任务完成！\n"
	sess := &liveOutputRaceSession{stdout: shown + tail}
	live := &commandTask{
		ref:         taskapi.Ref{TaskID: "task-1", SessionID: "term-session", TerminalID: "term-1"},
		sessionRef:  session.SessionRef{SessionID: "session-1"},
		session:     sess,
		state:       taskapi.StateRunning,
		running:     true,
		createdAt:   time.Now(),
		output:      shown,
		outputState: commandOutputState{callback: true},
	}
	tasks := &taskRuntime{tasks: map[string]*commandTask{live.ref.TaskID: live}}
	service := newStreamService(tasks)
	read, _, err := service.resolveReader(context.Background(), stream.Ref{
		SessionID: live.sessionRef.SessionID,
		TaskID:    live.ref.TaskID,
	})
	if err != nil {
		t.Fatalf("resolveReader() error = %v", err)
	}

	// TASK wait removes the live task after persisting a terminal entry whose
	// final result remains deferred until the canonical tool result is synced.
	// A subscription must not switch to that empty reconstruction mid-stream.
	deferredEntry := &taskapi.Entry{
		TaskID:   live.ref.TaskID,
		Session:  live.sessionRef,
		State:    taskapi.StateCompleted,
		Terminal: live.session.Terminal(),
	}
	deferred, err := tasks.rehydrateCommandTask(deferredEntry)
	if err != nil {
		t.Fatalf("rehydrateCommandTask() error = %v", err)
	}
	tasks.tasks[live.ref.TaskID] = deferred
	sess.completed = true

	snap, err := read(context.Background(), stream.Cursor{Output: int64(len([]byte(shown)))})
	if err != nil {
		t.Fatalf("resolved reader error = %v", err)
	}
	if got := streamFrameText(snap.Frames); got != tail {
		t.Fatalf("stream frame text = %q, want live task completion tail %q", got, tail)
	}
	if snap.Cursor.Output != int64(len([]byte(shown+tail))) {
		t.Fatalf("cursor.Output = %d, want %d", snap.Cursor.Output, len([]byte(shown+tail)))
	}
}

func TestCompleteCommandTaskReconcilesCallbackTailForConcurrentSubscribers(t *testing.T) {
	t.Parallel()

	const shown = "步骤 5/5: 处理中...\n"
	const tail = "✅ 任务完成！\n"
	sess := &liveOutputRaceSession{stdout: shown + tail, completed: true}
	task := &commandTask{
		ref:        taskapi.Ref{TaskID: "task-1", SessionID: "term-session", TerminalID: "term-1"},
		sessionRef: session.SessionRef{SessionID: "session-1"},
		session:    sess,
		state:      taskapi.StateRunning,
		running:    true,
		createdAt:  time.Now(),
		output:     shown,
		outputState: commandOutputState{
			backend:  commandBackendCursor{stdout: int64(len([]byte(shown)))},
			callback: true,
		},
		result:   map[string]any{"state": string(taskapi.StateRunning)},
		metadata: map[string]any{"state": string(taskapi.StateRunning), "running": true},
	}
	tasks := newTaskRuntime(&Runtime{clock: time.Now}, nil)
	tasks.installCommandTask(task)
	status, err := sess.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}

	snapshot, err := tasks.reconcileCommandStatus(context.Background(), task, status)
	if err != nil {
		t.Fatalf("reconcileCommandStatus() error = %v", err)
	}
	if snapshot.Running || snapshot.State != taskapi.StateCompleted {
		t.Fatalf("snapshot = %#v, want completed", snapshot)
	}
	task.mu.Lock()
	output := task.output
	cursor := task.outputCursorLocked()
	task.mu.Unlock()
	if output != shown+tail || cursor != int64(len([]byte(shown+tail))) {
		t.Fatalf("live task output/cursor = %q/%d, want complete callback result %q/%d", output, cursor, shown+tail, len([]byte(shown+tail)))
	}
}

func TestCompleteCommandTaskDoesNotHoldTaskLockAcrossResult(t *testing.T) {
	t.Parallel()

	var task *commandTask
	sess := &liveOutputRaceSession{
		stdout:    "tail\n",
		completed: true,
		onResult: func() {
			task.appendOutput("tail\n")
		},
	}
	task = &commandTask{
		ref:         taskapi.Ref{TaskID: "task-1", SessionID: "term-session", TerminalID: "term-1"},
		sessionRef:  session.SessionRef{SessionID: "session-1"},
		session:     sess,
		state:       taskapi.StateRunning,
		running:     true,
		createdAt:   time.Now(),
		outputState: commandOutputState{callback: true},
		result:      map[string]any{"state": string(taskapi.StateRunning)},
		metadata:    map[string]any{"state": string(taskapi.StateRunning), "running": true},
	}
	tasks := newTaskRuntime(&Runtime{clock: time.Now}, nil)
	status, err := sess.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, completeErr := tasks.reconcileCommandStatus(context.Background(), task, status)
		done <- completeErr
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("reconcileCommandStatus() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("reconcileCommandStatus() deadlocked when Result re-entered the output callback")
	}
}

func TestAwaitCommandWakesWhenCallbackAppendsFrame(t *testing.T) {
	t.Parallel()

	awaitStarted := make(chan struct{}, 1)
	sess := &liveOutputRaceSession{awaitStarted: awaitStarted}
	task := &commandTask{
		ref:         taskapi.Ref{TaskID: "task-1", SessionID: "term-session", TerminalID: "term-1"},
		sessionRef:  session.SessionRef{SessionID: "session-1"},
		session:     sess,
		state:       taskapi.StateRunning,
		running:     true,
		createdAt:   time.Now(),
		outputState: commandOutputState{callback: true},
		result:      map[string]any{"state": string(taskapi.StateRunning)},
		metadata:    map[string]any{"state": string(taskapi.StateRunning), "running": true},
	}
	service := &streamService{}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan stream.Snapshot, 1)
	errs := make(chan error, 1)
	go func() {
		snapshot, err := service.awaitCommand(ctx, task, stream.Cursor{}, false)
		if err != nil {
			errs <- err
			return
		}
		done <- snapshot
	}()
	select {
	case <-awaitStarted:
	case <-time.After(time.Second):
		t.Fatal("AwaitOutput() did not start")
	}

	task.appendOutput("live\n")
	select {
	case snapshot := <-done:
		if got := streamFrameText(snapshot.Frames); got != "live\n" {
			t.Fatalf("frame text = %q, want callback output", got)
		}
	case err := <-errs:
		t.Fatalf("awaitCommand() error = %v", err)
	case <-time.After(time.Second):
		t.Fatal("awaitCommand() did not wake for an appended callback frame")
	}
}

func TestAwaitCommandRetainsOneBackendObserverAcrossStreamWake(t *testing.T) {
	t.Parallel()

	awaitStarted := make(chan struct{}, 1)
	sess := &liveOutputRaceSession{awaitStarted: awaitStarted}
	task := &commandTask{
		ref:         taskapi.Ref{TaskID: "task-1", SessionID: "term-session", TerminalID: "term-1"},
		sessionRef:  session.SessionRef{SessionID: "session-1"},
		session:     sess,
		state:       taskapi.StateRunning,
		running:     true,
		createdAt:   time.Now(),
		outputState: commandOutputState{callback: true},
		result:      map[string]any{"state": string(taskapi.StateRunning)},
		metadata:    map[string]any{"state": string(taskapi.StateRunning), "running": true},
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := (&streamService{}).awaitCommand(ctx, task, stream.Cursor{}, false)
		done <- err
	}()
	select {
	case <-awaitStarted:
	case <-ctx.Done():
		t.Fatal("AwaitOutput() did not start")
	}

	statusBeforeWake := sess.statusCalls.Load()
	task.mu.Lock()
	task.notifyCommandStreamChangeLocked()
	task.mu.Unlock()
	deadline := time.Now().Add(time.Second)
	for sess.statusCalls.Load() <= statusBeforeWake && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := sess.statusCalls.Load(); got <= statusBeforeWake {
		t.Fatalf("Status() calls = %d, want stream wake to trigger another read after %d", got, statusBeforeWake)
	}
	if got := sess.awaitCalls.Load(); got != 1 {
		t.Fatalf("AwaitOutput() calls = %d, want one retained observer across a stream-only wake", got)
	}

	task.appendOutput("live\n")
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("awaitCommand() error = %v", err)
		}
	case <-ctx.Done():
		t.Fatal("awaitCommand() did not return after callback output")
	}
}

func TestStreamReadCommandCompletedEmitsUndeliveredTailFrame(t *testing.T) {
	t.Parallel()

	const shown = "already shown\n"
	const tail = "final tail\n"
	task := &commandTask{
		ref:        taskapi.Ref{TaskID: "task-1", SessionID: "term-session", TerminalID: "term-1"},
		sessionRef: session.SessionRef{SessionID: "session-1"},
		session:    &liveOutputRaceSession{stdout: shown + tail, completed: true},
		state:      taskapi.StateRunning,
		running:    true,
		createdAt:  time.Now(),
		output:     shown,
		outputState: commandOutputState{
			backend: commandBackendCursor{stdout: int64(len([]byte(shown)))},
		},
	}

	snap, err := (&streamService{}).readCommand(context.Background(), task, stream.Cursor{Output: int64(len([]byte(shown)))})
	if err != nil {
		t.Fatalf("readCommand() error = %v", err)
	}
	if got := streamFrameText(snap.Frames); got != tail {
		t.Fatalf("stream frame text = %q, want undelivered final tail", got)
	}
	if snap.Running {
		t.Fatal("snapshot.Running = true, want completed snapshot")
	}
	if snap.FinalText != shown+tail {
		t.Fatalf("FinalText = %q, want complete command result", snap.FinalText)
	}
}

func TestStreamReadRehydratedCompletedResultDoesNotDuplicateOutput(t *testing.T) {
	t.Parallel()

	entry := &taskapi.Entry{
		TaskID:    "task-1",
		State:     taskapi.StateCompleted,
		Running:   false,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Result: map[string]any{
			"result":    "done\n",
			"exit_code": 0,
		},
		Terminal: sandbox.TerminalRef{Backend: sandbox.BackendHost, SessionID: "term-session", TerminalID: "term-1"},
	}
	task, err := (&taskRuntime{}).rehydrateCommandTask(entry)
	if err != nil {
		t.Fatalf("rehydrateCommandTask() error = %v", err)
	}
	snap, err := (&streamService{}).readCommand(context.Background(), task, stream.Cursor{})
	if err != nil {
		t.Fatalf("readCommand() error = %v", err)
	}
	if got := streamFrameText(snap.Frames); got != "done\n" {
		t.Fatalf("stream frame text = %q, want one copy of completed output", got)
	}
	task.mu.Lock()
	stored := task.output
	task.mu.Unlock()
	if stored != "done\n" {
		t.Fatalf("stored output = %q, want one copy of completed output", stored)
	}
}

func TestStreamReadRehydratedNoOutputPlaceholderDoesNotBecomeTerminalFrame(t *testing.T) {
	t.Parallel()

	entry := &taskapi.Entry{
		TaskID:    "task-1",
		State:     taskapi.StateCompleted,
		Running:   false,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Result: map[string]any{
			"result":    noOutputPlaceholder,
			"exit_code": 0,
		},
		Terminal: sandbox.TerminalRef{Backend: sandbox.BackendHost, SessionID: "term-session", TerminalID: "term-1"},
	}
	task, err := (&taskRuntime{}).rehydrateCommandTask(entry)
	if err != nil {
		t.Fatalf("rehydrateCommandTask() error = %v", err)
	}
	snap, err := (&streamService{}).readCommand(context.Background(), task, stream.Cursor{})
	if err != nil {
		t.Fatalf("readCommand() error = %v", err)
	}
	if got := streamFrameText(snap.Frames); got != "" {
		t.Fatalf("stream frame text = %q, want no terminal output for placeholder", got)
	}
	if snap.FinalText != noOutputPlaceholder {
		t.Fatalf("FinalText = %q, want display placeholder", snap.FinalText)
	}
}

func TestStreamReadRehydratedLiteralNoOutputTextKeepsTerminalFrame(t *testing.T) {
	t.Parallel()

	entry := &taskapi.Entry{
		TaskID:       "task-1",
		State:        taskapi.StateCompleted,
		Running:      false,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
		StdoutCursor: int64(len(noOutputPlaceholder)),
		Result: map[string]any{
			"result":    noOutputPlaceholder,
			"exit_code": 0,
		},
		Terminal: sandbox.TerminalRef{Backend: sandbox.BackendHost, SessionID: "term-session", TerminalID: "term-1"},
	}
	task, err := (&taskRuntime{}).rehydrateCommandTask(entry)
	if err != nil {
		t.Fatalf("rehydrateCommandTask() error = %v", err)
	}
	snap, err := (&streamService{}).readCommand(context.Background(), task, stream.Cursor{})
	if err != nil {
		t.Fatalf("readCommand() error = %v", err)
	}
	if got := streamFrameText(snap.Frames); got != noOutputPlaceholder {
		t.Fatalf("stream frame text = %q, want literal terminal output", got)
	}
}

func TestCompleteCommandTaskReturnsMergedResultOnly(t *testing.T) {
	t.Parallel()

	sess := &liveOutputRaceSession{stdout: "out\n", stderr: "err\n", completed: true}
	status, err := sess.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	task := &commandTask{
		ref:        taskapi.Ref{TaskID: "task-1", SessionID: "term-session", TerminalID: "term-1"},
		sessionRef: session.SessionRef{SessionID: "session-1"},
		session:    sess,
		state:      taskapi.StateRunning,
		running:    true,
		createdAt:  time.Now(),
	}
	tm := newTaskRuntime(&Runtime{clock: time.Now}, nil)
	snap, err := tm.reconcileCommandStatus(context.Background(), task, status)
	if err != nil {
		t.Fatalf("reconcileCommandStatus() error = %v", err)
	}
	if _, exists := snap.Result["stdout"]; exists {
		t.Fatalf("snapshot result unexpectedly contains stdout: %#v", snap.Result)
	}
	if _, exists := snap.Result["stderr"]; exists {
		t.Fatalf("snapshot result unexpectedly contains stderr: %#v", snap.Result)
	}
	if got, _ := snap.Result["result"].(string); got != "out\nerr\n" {
		t.Fatalf("snapshot result = %q, want merged terminal summary", got)
	}
	if got := snap.Metadata["output_cursor"]; got != int64(len("out\nerr\n")) {
		t.Fatalf("metadata output_cursor = %#v, want terminal byte length", got)
	}
}

func TestCompleteCommandTaskDoesNotPersistNoOutputPlaceholder(t *testing.T) {
	t.Parallel()

	sess := &liveOutputRaceSession{completed: true}
	status, err := sess.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	task := &commandTask{
		ref:        taskapi.Ref{TaskID: "task-1", SessionID: "term-session", TerminalID: "term-1"},
		sessionRef: session.SessionRef{SessionID: "session-1"},
		session:    sess,
		state:      taskapi.StateRunning,
		running:    true,
		createdAt:  time.Now(),
	}
	tm := newTaskRuntime(&Runtime{clock: time.Now}, nil)
	snap, err := tm.reconcileCommandStatus(context.Background(), task, status)
	if err != nil {
		t.Fatalf("reconcileCommandStatus() error = %v", err)
	}
	if got, exists := snap.Result["result"]; exists {
		t.Fatalf("snapshot result = %#v, want no durable no-output placeholder", got)
	}
	if got := snap.Metadata["output_cursor"]; got != int64(0) {
		t.Fatalf("metadata output_cursor = %#v, want zero terminal bytes", got)
	}
	streamSnap, err := (&streamService{}).readCommand(context.Background(), task, stream.Cursor{})
	if err != nil {
		t.Fatalf("readCommand() error = %v", err)
	}
	if streamSnap.FinalText != noOutputPlaceholder {
		t.Fatalf("stream FinalText = %q, want display placeholder", streamSnap.FinalText)
	}
}

func TestCompleteCommandTaskKeepsBlankOnlyCursorWithoutResult(t *testing.T) {
	t.Parallel()

	sess := &liveOutputRaceSession{stdout: "\n", completed: true}
	status, err := sess.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	task := &commandTask{
		ref:        taskapi.Ref{TaskID: "task-1", SessionID: "term-session", TerminalID: "term-1"},
		sessionRef: session.SessionRef{SessionID: "session-1"},
		session:    sess,
		state:      taskapi.StateRunning,
		running:    true,
		createdAt:  time.Now(),
	}
	tm := newTaskRuntime(&Runtime{clock: time.Now}, nil)
	snap, err := tm.reconcileCommandStatus(context.Background(), task, status)
	if err != nil {
		t.Fatalf("reconcileCommandStatus() error = %v", err)
	}
	if got, exists := snap.Result["result"]; exists {
		t.Fatalf("snapshot result = %#v, want no durable blank-only result", got)
	}
	if _, exists := snap.Result["stdout"]; exists {
		t.Fatalf("snapshot result unexpectedly contains stdout: %#v", snap.Result)
	}
	if _, exists := snap.Result["stderr"]; exists {
		t.Fatalf("snapshot result unexpectedly contains stderr: %#v", snap.Result)
	}
	if got := snap.Metadata["output_cursor"]; got != int64(1) {
		t.Fatalf("metadata output_cursor = %#v, want blank output byte length", got)
	}
}

func TestCompletedTaskSessionReadsCanonicalResult(t *testing.T) {
	t.Parallel()

	sess := completedTaskSession{entry: &taskapi.Entry{
		State: taskapi.StateCompleted,
		Result: map[string]any{
			"stdout": "out\n",
			"stderr": "err\n",
			"result": "out\nerr\n",
		},
		Terminal: sandbox.TerminalRef{Backend: sandbox.BackendHost, SessionID: "term-session", TerminalID: "term-1"},
	}}
	result, err := sess.Result(context.Background())
	if err != nil {
		t.Fatalf("Result() error = %v", err)
	}
	if result.Stdout != "out\nerr\n" || result.Stderr != "" {
		t.Fatalf("Result() streams = %q/%q, want canonical result as stdout", result.Stdout, result.Stderr)
	}
}

func TestCommandLiveOutputBufferIsBoundedAndCursorStable(t *testing.T) {
	t.Parallel()

	task := &commandTask{}
	task.appendOutput(strings.Repeat("a", commandLiveOutputBufferCapBytes+10))

	task.mu.Lock()
	if got := len([]byte(task.output)); got != commandLiveOutputBufferCapBytes {
		t.Fatalf("retained output bytes = %d, want %d", got, commandLiveOutputBufferCapBytes)
	}
	if task.outputState.frontier.base != 10 {
		t.Fatalf("outputBase = %d, want dropped byte count 10", task.outputState.frontier.base)
	}
	cursor := task.outputCursorLocked()
	task.mu.Unlock()

	task.appendOutput("tail")
	task.mu.Lock()
	if got := task.outputFromCursorLocked(cursor); got != "tail" {
		t.Fatalf("outputFromCursorLocked(previous cursor) = %q, want appended tail", got)
	}
	task.mu.Unlock()
}

func TestCommandStreamAssignsOneMonotonicEventCursorPerOutputFrame(t *testing.T) {
	t.Parallel()

	task := &commandTask{
		ref:         taskapi.Ref{TaskID: "task-1", TerminalID: "term-1"},
		sessionRef:  session.SessionRef{SessionID: "session-1"},
		session:     &liveOutputRaceSession{},
		state:       taskapi.StateRunning,
		running:     true,
		createdAt:   time.Now(),
		outputState: commandOutputState{callback: true},
	}
	task.appendOutput("first")
	task.appendOutput("second")

	first, err := (&streamService{}).readCommand(context.Background(), task, stream.Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Frames) != 2 || first.Frames[0].Cursor.Events != 1 || first.Frames[1].Cursor.Events != 2 || first.Cursor.Events != 2 {
		t.Fatalf("first command stream = %#v, want two independently sequenced frames", first)
	}
	second, err := (&streamService{}).readCommand(context.Background(), task, stream.Cursor{Output: int64(len("first")), Events: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Frames) != 1 || second.Frames[0].Text != "second" || second.Frames[0].Cursor.Events != 2 {
		t.Fatalf("resumed command stream = %#v, want only second frame", second)
	}
}

func TestCommandUnknownOutcomeIsExplicitTerminalFrame(t *testing.T) {
	t.Parallel()

	task := &commandTask{
		ref:        taskapi.Ref{TaskID: "task-1", TerminalID: "term-1"},
		sessionRef: session.SessionRef{SessionID: "session-1"},
		session:    &liveOutputRaceSession{},
		state:      taskapi.StateUnknownOutcome,
		createdAt:  time.Now(),
	}
	snapshot, err := (&streamService{}).readCommand(context.Background(), task, stream.Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Running || snapshot.State != string(taskapi.StateUnknownOutcome) || len(snapshot.Frames) != 1 || !snapshot.Frames[0].Closed || snapshot.Frames[0].ExitCode != nil {
		t.Fatalf("unknown command stream = %#v, want one terminal unknown-outcome frame without inferred exit", snapshot)
	}
}

func TestCommandLiveOutputTruncationKeepsUTF8BoundaryAndSignalsGap(t *testing.T) {
	t.Parallel()

	task := &commandTask{
		ref:         taskapi.Ref{TaskID: "task-1", SessionID: "term-session", TerminalID: "term-1"},
		sessionRef:  session.SessionRef{SessionID: "session-1"},
		session:     &liveOutputRaceSession{},
		state:       taskapi.StateRunning,
		running:     true,
		createdAt:   time.Now(),
		outputState: commandOutputState{callback: true},
	}
	task.appendOutput("中" + strings.Repeat("a", commandLiveOutputBufferCapBytes-2))

	snap, err := (&streamService{}).readCommand(context.Background(), task, stream.Cursor{})
	if err != nil {
		t.Fatalf("readCommand() error = %v", err)
	}
	if snap.TruncatedBefore != int64(len([]byte("中"))) || len(snap.Frames) != 1 || snap.Frames[0].TruncatedBefore != snap.TruncatedBefore {
		t.Fatalf("truncated snapshot = %#v, want typed gap at first complete rune", snap)
	}
	if !utf8.ValidString(snap.Frames[0].Text) || len([]byte(snap.Frames[0].Text)) != commandLiveOutputBufferCapBytes-2 {
		t.Fatalf("retained frame bytes = %d valid=%v, want valid UTF-8 suffix", len([]byte(snap.Frames[0].Text)), utf8.ValidString(snap.Frames[0].Text))
	}
}

func TestTerminalFinalTextPreservesNonBlankWhitespaceAndDropsBlankOnlyOutput(t *testing.T) {
	t.Parallel()

	if got := terminalFinalText("  x\n", "", "", nil); got != "  x\n" {
		t.Fatalf("terminalFinalText(live output) = %q, want exact whitespace", got)
	}
	if got := terminalFinalText("   ", "", "", nil); got != "(no output)" {
		t.Fatalf("terminalFinalText(whitespace output) = %q, want no-output placeholder", got)
	}
	if got := terminalFinalText("", "  stdout\n", "", nil); got != "  stdout\n" {
		t.Fatalf("terminalFinalText(stdout) = %q, want exact stdout", got)
	}
	if got := terminalFinalText("", "stdout", "stderr\n", sandbox.MarkCommandExit(errors.New("exit status 1"))); got != "stdout\nstderr\n" {
		t.Fatalf("terminalFinalText(stdout+stderr) = %q, want separated streams without trimming", got)
	}
}

func TestTerminalOutputTextPreservesBlankOnlyTerminalBytes(t *testing.T) {
	t.Parallel()

	if got := terminalOutputText("   ", "", ""); got != "   " {
		t.Fatalf("terminalOutputText(blank live output) = %q, want exact output", got)
	}
	if got := terminalOutputText("", "\n", ""); got != "\n" {
		t.Fatalf("terminalOutputText(blank stdout) = %q, want exact stdout", got)
	}
}

func TestTerminalFinalTextSuppressesSyntheticWindowsExitSummary(t *testing.T) {
	t.Parallel()

	if got := terminalFinalText("", "", "", sandbox.MarkCommandExit(errors.New("process exited with code 1"))); got != "(no output)" {
		t.Fatalf("terminalFinalText(process exited) = %q, want no-output placeholder", got)
	}
	if got := terminalFinalText("", "", "Write-Error: raw failure\n", sandbox.MarkCommandExit(errors.New("process exited with code 1"))); got != "Write-Error: raw failure\n" {
		t.Fatalf("terminalFinalText(stderr) = %q, want raw stderr", got)
	}
}

func TestStreamReadSubagentCursorUsesStableRawOutput(t *testing.T) {
	t.Parallel()

	task := &subagentTask{
		ref:        taskapi.Ref{TaskID: "task-1", TerminalID: "term-1"},
		sessionRef: session.SessionRef{SessionID: "session-1"},
		createdAt:  time.Now(),
		state:      taskapi.StateRunning,
		running:    true,
		stdout:     "abc",
	}
	service := &streamService{}

	first, err := service.readSubagent(context.Background(), task, stream.Cursor{})
	if err != nil {
		t.Fatalf("first readSubagent() error = %v", err)
	}
	if got := streamFrameText(first.Frames); got != "abc" {
		t.Fatalf("first frame text = %q, want raw output without synthetic newline", got)
	}
	if got := first.Cursor.Output; got != int64(len("abc")) {
		t.Fatalf("first cursor output = %d, want %d", got, len("abc"))
	}

	task.mu.Lock()
	task.appendStreamLocked("def")
	task.mu.Unlock()
	second, err := service.readSubagent(context.Background(), task, first.Cursor)
	if err != nil {
		t.Fatalf("second readSubagent() error = %v", err)
	}
	if got := streamFrameText(second.Frames); got != "def" {
		t.Fatalf("second frame text = %q, want exact appended chunk", got)
	}
}

func TestSubagentOversizedFrameAdvancesCursorAndMarksTransientGap(t *testing.T) {
	t.Parallel()

	task := &subagentTask{
		ref:        taskapi.Ref{TaskID: "task-1"},
		sessionRef: session.SessionRef{SessionID: "session-1"},
		createdAt:  time.Now(),
		state:      taskapi.StateRunning,
		running:    true,
	}
	task.mu.Lock()
	task.appendStreamFrameLocked(stream.Frame{Text: strings.Repeat("x", subagentStreamByteCap+1), Running: true})
	retainedOutput := task.stdout
	task.mu.Unlock()

	snapshot, err := (&streamService{}).readSubagent(context.Background(), task, stream.Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	if retainedOutput != "" || snapshot.Cursor.Events != 1 || snapshot.Cursor.Output != subagentStreamByteCap+1 || len(snapshot.Frames) != 1 {
		t.Fatalf("oversized subagent stream = %#v retained=%d, want cursor-only marker", snapshot, len(retainedOutput))
	}
	if frame := snapshot.Frames[0]; frame.Text != "" || frame.Event != nil || frame.EventsTruncatedBefore != 1 {
		t.Fatalf("oversized frame marker = %#v, want explicit event gap", frame)
	}
}

func TestSubagentFrameRingEvictionReportsEventLowerBound(t *testing.T) {
	t.Parallel()

	task := &subagentTask{
		ref:        taskapi.Ref{TaskID: "task-1"},
		sessionRef: session.SessionRef{SessionID: "session-1"},
		createdAt:  time.Now(),
		state:      taskapi.StateRunning,
		running:    true,
	}
	task.mu.Lock()
	for i := 0; i < subagentStreamFrameCap+1; i++ {
		task.appendStreamFrameLocked(stream.Frame{Text: "x", Running: true})
	}
	task.mu.Unlock()

	snapshot, err := (&streamService{}).readSubagent(context.Background(), task, stream.Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Frames) != subagentStreamFrameCap || snapshot.EventsTruncatedBefore != 1 || snapshot.Cursor.Events != subagentStreamFrameCap+1 {
		t.Fatalf("evicted subagent stream = frames:%d lower:%d cursor:%d", len(snapshot.Frames), snapshot.EventsTruncatedBefore, snapshot.Cursor.Events)
	}
}

func TestPublishStreamDoesNotRouteAmbiguousSharedChildSessionWithoutTaskID(t *testing.T) {
	t.Parallel()

	tm := newTaskRuntime(nil, nil)
	tm.mu.Lock()
	tm.subagents["task-a"] = &subagentTask{
		ref:        taskapi.Ref{TaskID: "task-a"},
		sessionRef: session.SessionRef{SessionID: "parent-session"},
		anchor:     delegation.Anchor{SessionID: "shared-child-session"},
		createdAt:  time.Now(),
		state:      taskapi.StateRunning,
		running:    true,
	}
	tm.subagents["task-b"] = &subagentTask{
		ref:        taskapi.Ref{TaskID: "task-b"},
		sessionRef: session.SessionRef{SessionID: "parent-session"},
		anchor:     delegation.Anchor{SessionID: "shared-child-session"},
		createdAt:  time.Now(),
		state:      taskapi.StateRunning,
		running:    true,
	}
	tm.mu.Unlock()

	tm.PublishStream(stream.Frame{
		Ref:     stream.Ref{SessionID: "shared-child-session"},
		Text:    "unscoped child output\n",
		Running: true,
	})

	taskA := tm.subagents["task-a"]
	taskB := tm.subagents["task-b"]
	taskA.mu.Lock()
	taskAOut := taskA.stdout
	taskA.mu.Unlock()
	taskB.mu.Lock()
	taskBOut := taskB.stdout
	taskB.mu.Unlock()
	if taskAOut != "" || taskBOut != "" {
		t.Fatalf("ambiguous shared-session frame routed to task output: task-a=%q task-b=%q", taskAOut, taskBOut)
	}
}

func TestPublishStreamKeepsSiblingChildEventsIsolatedWhenInterleaved(t *testing.T) {
	t.Parallel()

	tm := newTaskRuntime(nil, nil)
	for _, taskID := range []string{"task-a", "task-b"} {
		tm.subagents[taskID] = &subagentTask{
			ref:        taskapi.Ref{TaskID: taskID},
			sessionRef: session.SessionRef{SessionID: "parent-session"},
			anchor:     delegation.Anchor{SessionID: "shared-child-session"},
			createdAt:  time.Now(),
			state:      taskapi.StateRunning,
			running:    true,
		}
	}

	publish := func(taskID, eventID, messageID, text string) {
		t.Helper()
		tm.PublishStream(stream.Frame{
			Ref:     stream.Ref{TaskID: taskID, SessionID: "shared-child-session"},
			Running: true,
			Event: &session.Event{
				ID:   eventID,
				Type: session.EventTypeAssistant,
				Protocol: &session.EventProtocol{Method: session.ProtocolMethodSessionUpdate, Update: &session.ProtocolUpdate{
					SessionUpdate: string(session.ProtocolUpdateTypeAgentMessage),
					MessageID:     messageID,
					Content:       session.ProtocolTextContent(text),
				}},
			},
		})
	}
	publish("task-a", "a-1", "a-message", "A1")
	publish("task-b", "b-1", "b-message", "B1")
	publish("task-a", "a-2", "a-message", "A2")
	publish("task-b", "b-2", "b-message", "B2")

	service := newStreamService(tm)
	for taskID, wantIDs := range map[string][]string{
		"task-a": {"a-1", "a-2"},
		"task-b": {"b-1", "b-2"},
	} {
		snapshot, err := service.Read(context.Background(), stream.ReadRequest{
			Ref: stream.Ref{SessionID: "parent-session", TaskID: taskID},
		})
		if err != nil {
			t.Fatalf("Read(%s) error = %v", taskID, err)
		}
		gotIDs := make([]string, 0, len(snapshot.Frames))
		for _, frame := range snapshot.Frames {
			if frame.Event != nil {
				gotIDs = append(gotIDs, frame.Event.ID)
			}
		}
		if !reflect.DeepEqual(gotIDs, wantIDs) {
			t.Fatalf("Read(%s) event ids = %v, want %v", taskID, gotIDs, wantIDs)
		}
	}
}

func TestSubagentStreamPendingHandoffPreservesPublicationOrder(t *testing.T) {
	t.Parallel()

	task := &subagentTask{
		ref:        taskapi.Ref{TaskID: "task-1"},
		sessionRef: session.SessionRef{SessionID: "parent-session"},
		createdAt:  time.Now(),
		state:      taskapi.StateRunning,
		running:    true,
	}
	frame := func(id, text string) stream.Frame {
		return stream.Frame{
			Ref:     stream.Ref{TaskID: task.ref.TaskID, SessionID: "child-session"},
			Text:    text,
			Running: true,
			Event:   &session.Event{ID: id, Type: session.EventTypeAssistant},
		}
	}

	// This is the task-install handoff: the task becomes discoverable while the
	// installer still owns streamMu and has an older pending frame to apply.
	task.streamMu.Lock()
	publishStarted := make(chan struct{})
	publishDone := make(chan struct{})
	go func() {
		close(publishStarted)
		task.applyStreamFrames([]stream.Frame{frame("live", "B")})
		close(publishDone)
	}()
	<-publishStarted
	task.applyStreamFramesLocked([]stream.Frame{frame("pending", "A")})
	task.streamMu.Unlock()
	<-publishDone

	task.mu.Lock()
	gotIDs := make([]string, 0, len(task.streamFrames))
	for _, streamed := range task.streamFrames {
		gotIDs = append(gotIDs, streamed.Event.ID)
	}
	gotOutput := task.stdout
	task.mu.Unlock()
	if !reflect.DeepEqual(gotIDs, []string{"pending", "live"}) {
		t.Fatalf("stream event ids = %v, want pending frame before live frame", gotIDs)
	}
	if gotOutput != "AB" {
		t.Fatalf("stream output = %q, want publication order %q", gotOutput, "AB")
	}
}

func TestCompletedTaskSessionIgnoresLegacyStdoutStderr(t *testing.T) {
	t.Parallel()

	sess := completedTaskSession{entry: &taskapi.Entry{
		State: taskapi.StateFailed,
		Result: map[string]any{
			"stdout":    "out\n",
			"stderr":    "err\n",
			"exit_code": 7,
		},
		Terminal: sandbox.TerminalRef{Backend: sandbox.BackendHost, SessionID: "term-session", TerminalID: "term-1"},
	}}
	stdout, stderr, nextStdout, nextStderr, err := sess.ReadOutput(context.Background(), 0, 0)
	if err != nil {
		t.Fatalf("ReadOutput() error = %v", err)
	}
	if string(stdout) != "" || string(stderr) != "" {
		t.Fatalf("ReadOutput() stdout/stderr = %q/%q, want legacy streams ignored", stdout, stderr)
	}
	if nextStdout != 0 || nextStderr != 0 {
		t.Fatalf("ReadOutput() cursors = %d/%d, want zero", nextStdout, nextStderr)
	}
	result, err := sess.Result(context.Background())
	if err != nil {
		t.Fatalf("Result() error = %v", err)
	}
	if result.Stdout != "" || result.Stderr != "" || result.ExitCode != 7 {
		t.Fatalf("Result() = %#v, want legacy streams ignored with exit code", result)
	}
}

func TestCompletedTaskSessionInfersCancelledExitCode(t *testing.T) {
	t.Parallel()

	sess := completedTaskSession{entry: &taskapi.Entry{
		State: taskapi.StateCancelled,
		Result: map[string]any{
			"state": string(taskapi.StateCancelled),
		},
		Terminal: sandbox.TerminalRef{Backend: sandbox.BackendHost, SessionID: "term-session", TerminalID: "term-1"},
	}}
	status, err := sess.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.ExitCode != -1 {
		t.Fatalf("Status().ExitCode = %d, want -1 for cancelled task", status.ExitCode)
	}
	frames := stream.FramesForSnapshot(stream.Snapshot{ExitCode: &status.ExitCode})
	if got := frames[len(frames)-1].State; got != "cancelled" {
		t.Fatalf("terminal close state = %q, want cancelled", got)
	}
}

func TestStreamReadSubagentPreservesInterruptedStateWithoutExitCode(t *testing.T) {
	t.Parallel()

	task := &subagentTask{
		ref:        taskapi.Ref{TaskID: "task-1", TerminalID: "term-1"},
		sessionRef: session.SessionRef{SessionID: "session-1"},
		createdAt:  time.Now(),
		state:      taskapi.StateInterrupted,
		running:    false,
		result:     map[string]any{"result": "child session interrupted"},
	}

	snap, err := (&streamService{}).readSubagent(context.Background(), task, stream.Cursor{})
	if err != nil {
		t.Fatalf("readSubagent() error = %v", err)
	}
	if snap.State != string(taskapi.StateInterrupted) {
		t.Fatalf("snapshot state = %q, want interrupted", snap.State)
	}
	if snap.ExitCode != nil {
		t.Fatalf("snapshot ExitCode = %#v, want nil for subagent lifecycle state", snap.ExitCode)
	}
	frames := stream.FramesForSnapshot(snap)
	if got := frames[len(frames)-1].State; got != string(taskapi.StateInterrupted) {
		t.Fatalf("terminal close state = %q, want interrupted", got)
	}
}

func streamFrameText(frames []stream.Frame) string {
	out := ""
	for _, frame := range frames {
		out += frame.Text
	}
	return out
}

type liveOutputRaceSession struct {
	stdout       string
	stderr       string
	completed    bool
	exitCode     int
	onRead       func()
	onResult     func()
	awaitStarted chan struct{}
	awaitCalls   atomic.Int64
	statusCalls  atomic.Int64
}

func (s *liveOutputRaceSession) Ref() sandbox.SessionRef {
	return sandbox.SessionRef{Backend: sandbox.BackendHost, SessionID: "term-session"}
}

func (s *liveOutputRaceSession) Terminal() sandbox.TerminalRef {
	return sandbox.TerminalRef{Backend: sandbox.BackendHost, SessionID: "term-session", TerminalID: "term-1"}
}

func (s *liveOutputRaceSession) WriteInput(context.Context, []byte) error { return nil }

func (s *liveOutputRaceSession) ReadOutput(_ context.Context, stdoutMarker, stderrMarker int64) ([]byte, []byte, int64, int64, error) {
	if s.onRead != nil {
		s.onRead()
	}
	stdout := []byte(s.stdout)
	stderr := []byte(s.stderr)
	if stdoutMarker < 0 {
		stdoutMarker = 0
	}
	if stdoutMarker > int64(len(stdout)) {
		stdoutMarker = int64(len(stdout))
	}
	if stderrMarker < 0 {
		stderrMarker = 0
	}
	if stderrMarker > int64(len(stderr)) {
		stderrMarker = int64(len(stderr))
	}
	return append([]byte(nil), stdout[stdoutMarker:]...), append([]byte(nil), stderr[stderrMarker:]...), int64(len(stdout)), int64(len(stderr)), nil
}

func (s *liveOutputRaceSession) AwaitOutput(ctx context.Context, cursor sandbox.OutputCursor) (sandbox.OutputObservation, error) {
	s.awaitCalls.Add(1)
	status, err := s.Status(ctx)
	if err != nil {
		return sandbox.OutputObservation{}, err
	}
	next := sandbox.OutputCursor{Stdout: int64(len([]byte(s.stdout))), Stderr: int64(len([]byte(s.stderr)))}
	cursor = sandbox.NormalizeOutputCursor(cursor)
	if err := sandbox.ValidateOutputCursor(cursor, next); err != nil {
		return sandbox.OutputObservation{}, err
	}
	if next.Stdout > cursor.Stdout || next.Stderr > cursor.Stderr || !status.Running {
		return sandbox.OutputObservation{Cursor: next, Status: status}, nil
	}
	if s.awaitStarted != nil {
		select {
		case s.awaitStarted <- struct{}{}:
		default:
		}
	}
	<-ctx.Done()
	return sandbox.OutputObservation{}, ctx.Err()
}

func (s *liveOutputRaceSession) Status(context.Context) (sandbox.SessionStatus, error) {
	s.statusCalls.Add(1)
	running := !s.completed
	return sandbox.SessionStatus{
		SessionRef:    s.Ref(),
		Terminal:      s.Terminal(),
		Running:       running,
		SupportsInput: true,
		ExitCode:      s.exitCode,
		UpdatedAt:     time.Now(),
	}, nil
}

func (s *liveOutputRaceSession) Wait(context.Context, time.Duration) (sandbox.SessionStatus, error) {
	return s.Status(context.Background())
}

func (s *liveOutputRaceSession) Result(context.Context) (sandbox.CommandResult, error) {
	if s.onResult != nil {
		s.onResult()
	}
	return sandbox.CommandResult{Stdout: s.stdout, Stderr: s.stderr, ExitCode: s.exitCode}, nil
}

func (s *liveOutputRaceSession) Terminate(context.Context) error { return nil }
