package runtime

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/output"
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
	t.output = ""
	t.outputState.frontier.base = nextBase
	t.outputState.frontier.model = max(t.outputState.frontier.model, nextBase)
	t.outputState.checkpoint.gap = true
	t.outputState.checkpoint.coherent = false
	t.outputState.exact = false
	t.notifyCommandOutputChangeLocked()
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
	if t == nil || text == "" || t.outputTerminal {
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
	if t.outputObserver != nil {
		_ = t.outputObserver.ObserveTaskOutput(context.Background(), output.Event{
			OccurredAt: time.Now(), Text: text, Running: t.running,
		})
	}
	t.notifyCommandOutputChangeLocked()
}

func (t *commandTask) emitOutputTerminalLocked(state taskapi.State, status sandbox.SessionStatus, includeExitCode bool) {
	if t == nil || t.outputTerminal {
		return
	}
	t.outputTerminal = true
	event := output.Event{OccurredAt: status.UpdatedAt, State: string(state), Closed: true}
	if event.OccurredAt.IsZero() {
		event.OccurredAt = time.Now()
	}
	if includeExitCode {
		code := status.ExitCode
		event.ExitCode = &code
	}
	if t.outputObserver != nil {
		_ = t.outputObserver.ObserveTaskOutput(context.Background(), event)
	}
	t.notifyCommandOutputChangeLocked()
}

// notifyCommandOutputChangeLocked wakes Task-control callers waiting for a
// post-input observation. It retains no output; Control's spool is the sole
// transient delivery buffer.
func (t *commandTask) notifyCommandOutputChangeLocked() {
	if t == nil {
		return
	}
	if t.outputChanged != nil {
		close(t.outputChanged)
	}
	t.outputChanged = make(chan struct{})
}

func (t *commandTask) commandOutputChangeWaiterLocked(cursor int64) (<-chan struct{}, bool) {
	if t == nil || t.outputCursorLocked() > cursor || !t.running {
		return nil, true
	}
	if t.outputChanged == nil {
		t.outputChanged = make(chan struct{})
	}
	return t.outputChanged, false
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
// not yet present in the callback-backed output. A mismatch is left untouched:
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

func sliceStringFromByteCursor(text string, cursor int64) string {
	if cursor < 0 {
		cursor = 0
	}
	raw := []byte(text)
	if cursor >= int64(len(raw)) {
		return ""
	}
	for cursor < int64(len(raw)) && !utf8.RuneStart(raw[cursor]) {
		cursor++
	}
	return string(raw[cursor:])
}
