package runtime

import (
	"math"
	"strings"
	"unicode/utf8"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
)

func (t *commandTask) appendSandboxOutput(chunk sandbox.OutputChunk) {
	if t == nil || chunk.Text == "" {
		return
	}
	t.mu.Lock()
	streamName := strings.ToLower(strings.TrimSpace(chunk.Stream))
	if chunk.Cursor > 0 {
		switch streamName {
		case "stderr":
			if chunk.Cursor <= t.outputState.backend.stderr && t.outputState.backend.stderr > 0 {
				t.mu.Unlock()
				return
			}
		case "stdout":
			if chunk.Cursor <= t.outputState.backend.stdout && t.outputState.backend.stdout > 0 {
				t.mu.Unlock()
				return
			}
		}
	}
	t.appendOutputLocked(chunk.Text)
	if chunk.Cursor <= 0 {
		t.outputState.checkpoint.coherent = false
		t.mu.Unlock()
		return
	}
	switch streamName {
	case "stderr":
		t.outputState.backend.stderr = chunk.Cursor
	case "stdout":
		t.outputState.backend.stdout = chunk.Cursor
	default:
		t.outputState.checkpoint.coherent = false
		t.mu.Unlock()
		return
	}
	t.commitOutputCheckpointLocked(t.outputState.backend.stdout, t.outputState.backend.stderr)
	t.mu.Unlock()
}

func (t *commandTask) ingestRecoveredOutputLocked(
	stdout []byte,
	stderr []byte,
	stdoutMarker int64,
	stderrMarker int64,
	nextStdout int64,
	nextStderr int64,
	terminal bool,
) error {
	if t == nil {
		return nil
	}
	if err := sandbox.ValidateOutputCursor(
		sandbox.OutputCursor{Stdout: stdoutMarker, Stderr: stderrMarker},
		sandbox.OutputCursor{Stdout: nextStdout, Stderr: nextStderr},
	); err != nil {
		t.outputState.checkpoint.coherent = false
		return err
	}
	_, stdoutGap := sandbox.OutputReadWindow(stdoutMarker, stdout, nextStdout)
	_, stderrGap := sandbox.OutputReadWindow(stderrMarker, stderr, nextStderr)
	if stdoutGap {
		t.outputState.recoveryStdout.Reset()
	}
	if stderrGap {
		t.outputState.recoveryStderr.Reset()
	}
	if stdoutGap || stderrGap {
		t.markRecoveredOutputGapLocked()
	}

	stdoutText := t.outputState.recoveryStdout.Decode(stdout)
	stderrText := t.outputState.recoveryStderr.Decode(stderr)
	if terminal {
		stdoutText += t.outputState.recoveryStdout.Flush()
		stderrText += t.outputState.recoveryStderr.Flush()
	}
	t.outputState.backend.stdout = nextStdout
	t.outputState.backend.stderr = nextStderr
	t.outputState.exact = false
	t.appendOutputLocked(terminalDeltaText(stdoutText, stderrText))
	committedStdout := max(nextStdout-int64(t.outputState.recoveryStdout.PendingBytes()), 0)
	committedStderr := max(nextStderr-int64(t.outputState.recoveryStderr.PendingBytes()), 0)
	t.commitOutputCheckpointLocked(committedStdout, committedStderr)
	return nil
}

func (t *commandTask) markRecoveredOutputGapLocked() {
	if t == nil {
		return
	}
	nextBase := max(t.outputCursorLocked(), t.outputState.frontier.model) + 1
	eventCursor := t.commandStreamEventCursorLocked()
	t.output = ""
	t.outputState.frontier.base = nextBase
	t.outputState.frontier.model = max(t.outputState.frontier.model, nextBase)
	t.streamFrames = nil
	t.streamEventBase = eventCursor
	t.outputState.checkpoint.gap = true
	t.outputState.checkpoint.coherent = false
	t.outputState.exact = false
	t.notifyCommandStreamChangeLocked()
}

func (t *commandTask) commitOutputCheckpointLocked(stdoutCursor int64, stderrCursor int64) {
	if t == nil {
		return
	}
	t.outputState.checkpoint.backend.stdout = max(stdoutCursor, 0)
	t.outputState.checkpoint.backend.stderr = max(stderrCursor, 0)
	t.outputState.checkpoint.output = t.outputCursorLocked()
	t.outputState.checkpoint.available = true
	t.outputState.checkpoint.coherent = !t.outputState.checkpoint.gap
}

func (t *commandTask) commitOutputResumeCheckpointLocked() {
	if t == nil || !t.outputState.checkpoint.available ||
		t.outputState.frontier.model != t.outputState.checkpoint.output {
		return
	}
	next := t.outputState.checkpoint
	next.model = t.outputState.frontier.model
	t.outputState.resume.advance(next)
}

func (t *commandTask) appendOutput(text string) {
	if t == nil || text == "" {
		return
	}
	t.mu.Lock()
	t.appendOutputLocked(text)
	t.mu.Unlock()
}

func (t *commandTask) appendOutputLocked(text string) {
	if t == nil || text == "" || t.streamTerminalFramed {
		return
	}
	raw := []byte(t.output)
	raw = append(raw, text...)
	if commandLiveOutputBufferCapBytes > 0 && len(raw) > commandLiveOutputBufferCapBytes {
		dropped := len(raw) - commandLiveOutputBufferCapBytes
		for dropped < len(raw) && !utf8.RuneStart(raw[dropped]) {
			dropped++
		}
		raw = raw[dropped:]
		t.outputState.frontier.base += int64(dropped)
		if t.outputState.frontier.model < t.outputState.frontier.base {
			t.outputState.frontier.model = t.outputState.frontier.base
		}
	}
	t.output = string(raw)
	t.outputState.live = true
	t.appendCommandStreamFrameLocked(stream.Frame{
		Text:    text,
		Running: t.running,
	})
	t.trimCommandStreamFramesLocked()
}

func (t *commandTask) appendCommandStreamFrameLocked(frame stream.Frame) {
	if t == nil || t.streamTerminalFramed {
		return
	}
	frame = stream.CloneFrame(frame)
	frame.Ref = stream.Ref{
		SessionID:  strings.TrimSpace(t.sessionRef.SessionID),
		TaskID:     strings.TrimSpace(t.ref.TaskID),
		TerminalID: strings.TrimSpace(t.ref.TerminalID),
	}
	frame.Cursor = stream.Cursor{
		Output: t.outputCursorLocked(),
		Events: nextCommandStreamEventCursor(t.commandStreamEventCursorLocked()),
	}
	t.streamFrames = append(t.streamFrames, frame)
	t.notifyCommandStreamChangeLocked()
}

func (t *commandTask) notifyCommandStreamChangeLocked() {
	if t == nil {
		return
	}
	if t.streamChanged != nil {
		close(t.streamChanged)
	}
	t.streamChanged = make(chan struct{})
}

func (t *commandTask) commandStreamEventCursorLocked() int64 {
	if t == nil {
		return 0
	}
	cursor := max(t.streamEventBase, int64(0))
	if count := len(t.streamFrames); count > 0 {
		cursor = max(cursor, t.streamFrames[count-1].Cursor.Events)
	}
	return cursor
}

func nextCommandStreamEventCursor(cursor int64) int64 {
	if cursor < 0 {
		return 1
	}
	if cursor == math.MaxInt64 {
		return math.MaxInt64
	}
	return cursor + 1
}

func (t *commandTask) commandStreamChangeWaiterLocked(cursor stream.Cursor) (<-chan struct{}, bool) {
	if t == nil || commandStreamAdvancedLocked(t, cursor) {
		return nil, true
	}
	if t.streamChanged == nil {
		t.streamChanged = make(chan struct{})
	}
	return t.streamChanged, false
}

func commandStreamAdvancedLocked(task *commandTask, cursor stream.Cursor) bool {
	if task == nil {
		return true
	}
	eventCursor := task.commandStreamEventCursorLocked()
	return task.outputCursorLocked() > cursor.Output || eventCursor > cursor.Events ||
		(!task.running && stream.IsTerminalState(string(task.state)))
}

func (t *commandTask) trimCommandStreamFramesLocked() {
	if t == nil || len(t.streamFrames) == 0 {
		return
	}
	for len(t.streamFrames) > 0 {
		first := &t.streamFrames[0]
		end := first.Cursor.Output
		start := end - int64(len([]byte(first.Text)))
		if end <= t.outputState.frontier.base && first.Text != "" {
			t.streamEventBase = max(t.streamEventBase, first.Cursor.Events)
			t.streamFrames[0] = stream.Frame{}
			t.streamFrames = t.streamFrames[1:]
			continue
		}
		if first.Text != "" && start < t.outputState.frontier.base {
			first.Text = sliceStringFromByteCursor(first.Text, t.outputState.frontier.base-start)
		}
		break
	}
}

func (t *commandTask) ensureCommandOutputSeedLocked() {
	if t == nil || len(t.streamFrames) != 0 || t.output == "" {
		return
	}
	t.appendCommandStreamFrameLocked(stream.Frame{Text: t.output, Running: t.running})
}

func (t *commandTask) ensureCommandTerminalFrameLocked(state taskapi.State, status sandbox.SessionStatus, includeExitCode bool) {
	if t == nil || !stream.IsTerminalState(string(state)) || t.streamTerminalFramed {
		return
	}
	frame := stream.Frame{
		State:     string(state),
		Running:   false,
		Closed:    true,
		UpdatedAt: status.UpdatedAt,
	}
	if !status.Running && includeExitCode {
		exitCode := status.ExitCode
		frame.ExitCode = &exitCode
	}
	t.appendCommandStreamFrameLocked(frame)
	t.streamTerminalFramed = true
}

func (t *commandTask) outputCursorLocked() int64 {
	if t == nil {
		return 0
	}
	return t.outputState.frontier.base + int64(len([]byte(t.output)))
}

func (t *commandTask) outputFromCursorLocked(cursor int64) string {
	if t == nil || t.output == "" {
		return ""
	}
	if cursor < t.outputState.frontier.base {
		cursor = t.outputState.frontier.base
	}
	return sliceStringFromByteCursor(t.output, cursor-t.outputState.frontier.base)
}

// reconcileFinalOutputLocked appends only the canonical result suffix that is
// not yet present in the callback-backed stream. A mismatch is left untouched:
// stdout/stderr result grouping is not guaranteed to preserve live interleave
// order, so replacing or appending an unaligned result would duplicate bytes.
func (t *commandTask) reconcileFinalOutputLocked(finalOutput string) bool {
	if t == nil {
		return false
	}
	if t.output == "" && t.outputState.frontier.base == 0 && t.outputState.backend.stdout == 0 && t.outputState.backend.stderr == 0 && strings.TrimSpace(finalOutput) == noOutputPlaceholder {
		return true
	}
	base := t.outputState.frontier.base
	cursor := t.outputCursorLocked()
	finalCursor := int64(len([]byte(finalOutput)))
	if base < 0 || cursor < base || base > finalCursor || cursor > finalCursor {
		return false
	}
	if finalOutput[base:cursor] != t.output {
		return false
	}
	if cursor < finalCursor {
		t.appendOutputLocked(finalOutput[cursor:])
	}
	return true
}

func commandTaskStreamRef(task *commandTask) stream.Ref {
	if task == nil {
		return stream.Ref{}
	}
	task.mu.Lock()
	defer task.mu.Unlock()
	return stream.Ref{
		SessionID:  task.sessionRef.SessionID,
		TaskID:     task.ref.TaskID,
		TerminalID: task.ref.TerminalID,
	}
}

func commandTaskStreamCursor(task *commandTask) (stream.Cursor, bool) {
	if task == nil {
		return stream.Cursor{}, false
	}
	task.mu.Lock()
	defer task.mu.Unlock()
	eventCursor := task.commandStreamEventCursorLocked()
	return stream.Cursor{
		Output: task.outputCursorLocked(),
		Events: eventCursor,
	}, task.outputState.frontier.model < task.outputCursorLocked()
}
