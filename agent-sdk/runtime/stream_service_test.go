package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
)

func TestStreamReadCommandUsesCallbackOutputWithoutReadFallback(t *testing.T) {
	t.Parallel()

	const chunk = "chunk\n"
	sess := &liveOutputRaceSession{stdout: chunk}
	task := &commandTask{
		ref:            taskapi.Ref{TaskID: "task-1", SessionID: "term-session", TerminalID: "term-1"},
		sessionRef:     session.SessionRef{SessionID: "session-1"},
		session:        sess,
		state:          taskapi.StateRunning,
		running:        true,
		createdAt:      time.Now(),
		outputCallback: true,
	}
	task.appendOutput(chunk)
	sess.onRead = func() { t.Fatal("ReadOutput fallback should not be used for callback-backed live output") }

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

func TestStreamReadCommandCompletedUsesFinalTextWithoutFrame(t *testing.T) {
	t.Parallel()

	task := &commandTask{
		ref:            taskapi.Ref{TaskID: "task-1", SessionID: "term-session", TerminalID: "term-1"},
		sessionRef:     session.SessionRef{SessionID: "session-1"},
		session:        &liveOutputRaceSession{stdout: "final\n", completed: true},
		state:          taskapi.StateRunning,
		running:        true,
		createdAt:      time.Now(),
		output:         "live\n",
		outputCallback: true,
	}

	snap, err := (&streamService{}).readCommand(context.Background(), task, stream.Cursor{Output: int64(len("live\n"))})
	if err != nil {
		t.Fatalf("readCommand() error = %v", err)
	}
	if len(snap.Frames) != 0 {
		t.Fatalf("frames = %#v, want no duplicate final terminal frame", snap.Frames)
	}
	if snap.FinalText != "final\n" {
		t.Fatalf("FinalText = %q, want complete command result", snap.FinalText)
	}
}

func TestStreamReadCommandCompletedEmitsUndeliveredTailFrame(t *testing.T) {
	t.Parallel()

	const shown = "already shown\n"
	const tail = "final tail\n"
	task := &commandTask{
		ref:          taskapi.Ref{TaskID: "task-1", SessionID: "term-session", TerminalID: "term-1"},
		sessionRef:   session.SessionRef{SessionID: "session-1"},
		session:      &liveOutputRaceSession{stdout: shown + tail, completed: true},
		state:        taskapi.StateRunning,
		running:      true,
		createdAt:    time.Now(),
		output:       shown,
		stdoutCursor: int64(len([]byte(shown))),
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
	snap, err := tm.completeCommandTaskWithStatus(context.Background(), task, status)
	if err != nil {
		t.Fatalf("completeCommandTaskWithStatus() error = %v", err)
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
	snap, err := tm.completeCommandTaskWithStatus(context.Background(), task, status)
	if err != nil {
		t.Fatalf("completeCommandTaskWithStatus() error = %v", err)
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
	snap, err := tm.completeCommandTaskWithStatus(context.Background(), task, status)
	if err != nil {
		t.Fatalf("completeCommandTaskWithStatus() error = %v", err)
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
	if task.outputBase != 10 {
		t.Fatalf("outputBase = %d, want dropped byte count 10", task.outputBase)
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
	if got := terminalFinalText("", "stdout", "stderr\n", errors.New("exit status 1")); got != "stdout\nstderr\n" {
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

	if got := terminalFinalText("", "", "", errors.New("process exited with code 1")); got != "(no output)" {
		t.Fatalf("terminalFinalText(process exited) = %q, want no-output placeholder", got)
	}
	if got := terminalFinalText("", "", "Write-Error: raw failure\n", errors.New("process exited with code 1")); got != "Write-Error: raw failure\n" {
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
	if got := streamClosedState(stream.Snapshot{ExitCode: &status.ExitCode}); got != "cancelled" {
		t.Fatalf("streamClosedState() = %q, want cancelled", got)
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
	if got := streamClosedState(snap); got != string(taskapi.StateInterrupted) {
		t.Fatalf("streamClosedState() = %q, want interrupted", got)
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
	stdout    string
	stderr    string
	completed bool
	exitCode  int
	onRead    func()
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

func (s *liveOutputRaceSession) Status(context.Context) (sandbox.SessionStatus, error) {
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
	return sandbox.CommandResult{Stdout: s.stdout, Stderr: s.stderr, ExitCode: s.exitCode}, nil
}

func (s *liveOutputRaceSession) Terminate(context.Context) error { return nil }
