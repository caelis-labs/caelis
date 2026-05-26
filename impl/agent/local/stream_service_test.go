package local

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/ports/sandbox"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/ports/stream"
	taskapi "github.com/OnslaughtSnail/caelis/ports/task"
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

func TestCompletedTaskSessionReadsLegacyStdoutStderr(t *testing.T) {
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
	if string(stdout) != "out\n" || string(stderr) != "err\n" {
		t.Fatalf("ReadOutput() stdout/stderr = %q/%q, want legacy output", stdout, stderr)
	}
	if nextStdout != int64(len("out\n")) || nextStderr != int64(len("err\n")) {
		t.Fatalf("ReadOutput() cursors = %d/%d, want legacy output lengths", nextStdout, nextStderr)
	}
	result, err := sess.Result(context.Background())
	if err != nil {
		t.Fatalf("Result() error = %v", err)
	}
	if result.Stdout != "out\n" || result.Stderr != "err\n" || result.ExitCode != 7 {
		t.Fatalf("Result() = %#v, want legacy stdout/stderr/exit code", result)
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
	if stdoutMarker < 0 {
		stdoutMarker = 0
	}
	if stdoutMarker > int64(len(stdout)) {
		stdoutMarker = int64(len(stdout))
	}
	if stderrMarker < 0 {
		stderrMarker = 0
	}
	return append([]byte(nil), stdout[stdoutMarker:]...), nil, int64(len(stdout)), stderrMarker, nil
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
	return sandbox.CommandResult{Stdout: s.stdout}, nil
}

func (s *liveOutputRaceSession) Terminate(context.Context) error { return nil }
