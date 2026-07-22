package runtime

import (
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
)

func (tm *taskRuntime) PublishStream(frame stream.Frame) {
	if tm == nil {
		return
	}
	taskID := strings.TrimSpace(frame.Ref.TaskID)
	sessionID := strings.TrimSpace(frame.Ref.SessionID)
	// Resolve-or-enqueue is one atomic decision with task installation. If a
	// publish raced the install between separate read/write locks, it could be
	// queued after the install had already drained pending frames.
	tm.mu.Lock()
	var task *subagentTask
	if taskID != "" {
		task = tm.subagents[taskID]
	}
	if task == nil && taskID == "" && sessionID != "" {
		var matched *subagentTask
		ambiguous := false
		for _, candidate := range tm.subagents {
			if candidate == nil {
				continue
			}
			if strings.TrimSpace(candidate.anchor.SessionID) == sessionID {
				if matched != nil {
					ambiguous = true
					break
				}
				matched = candidate
			}
		}
		if !ambiguous {
			task = matched
		}
	}
	if task == nil && taskID != "" {
		tm.pending[taskID] = append(tm.pending[taskID], stream.CloneFrame(frame))
	}
	tm.mu.Unlock()
	if task == nil {
		return
	}
	task.applyStreamFrames([]stream.Frame{frame})
}

func (t *subagentTask) seedStreamFromResult(result delegation.Result) {
	if t == nil {
		return
	}
	if taskOutputHasNonBlankLine(t.stdout) || taskOutputHasNonBlankLine(t.stderr) {
		return
	}
	text := result.Result
	turnID := subagentTurnID(t.ref.TaskID, t.turnSeq)
	if taskOutputHasNonBlankLine(text) && subagentFramesContainAssistantTextForTurn(t.streamFrames, turnID) {
		return
	}
	if !taskOutputHasNonBlankLine(text) {
		if len(t.streamFrames) > 0 {
			return
		}
		text = result.OutputPreview
		if taskOutputHasNonBlankLine(text) {
			// A preview is useful to a transient reader but is not proof of a
			// canonical assistant result. Keep it out of the structured frame
			// set used by side-agent dialogue persistence.
			t.appendStreamLocked(text)
		}
		return
	}
	if !taskOutputHasNonBlankLine(text) {
		return
	}
	t.appendStreamFrameLocked(stream.Frame{Text: text, Running: false})
}

func subagentFramesContainAssistantTextForTurn(frames []stream.Frame, turnID string) bool {
	turnID = strings.TrimSpace(turnID)
	for _, frame := range frames {
		if strings.TrimSpace(frame.Ref.TerminalID) != turnID {
			continue
		}
		if strings.TrimSpace(subagentFrameAssistantText(frame)) != "" {
			return true
		}
	}
	return false
}

func subagentFramesContainAssistantText(frames []stream.Frame) bool {
	for _, frame := range frames {
		if strings.TrimSpace(subagentFrameAssistantText(frame)) != "" {
			return true
		}
	}
	return false
}

func (t *subagentTask) applyStreamFrames(frames []stream.Frame) {
	if t == nil || len(frames) == 0 {
		return
	}
	t.streamMu.Lock()
	defer t.streamMu.Unlock()
	t.applyStreamFramesLocked(frames)
}

func (t *subagentTask) applyStreamFramesLocked(frames []stream.Frame) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, frame := range frames {
		if t.streamTerminalFramed {
			continue
		}
		text := subagentFrameAssistantText(frame)
		if text == "" {
			text = frame.Text
		}
		if frame.Event != nil || text != "" || frame.Closed {
			cloned := stream.CloneFrame(frame)
			if cloned.Text == "" {
				cloned.Text = text
			}
			cloned.Ref.TaskID = firstNonEmpty(strings.TrimSpace(cloned.Ref.TaskID), strings.TrimSpace(t.ref.TaskID))
			cloned.Ref.SessionID = firstNonEmpty(strings.TrimSpace(cloned.Ref.SessionID), strings.TrimSpace(t.sessionRef.SessionID))
			cloned.Ref.TerminalID = firstNonEmpty(strings.TrimSpace(cloned.Ref.TerminalID), subagentTurnID(t.ref.TaskID, t.turnSeq))
			if cloned.Event != nil {
				if cloned.Event.Scope == nil {
					cloned.Event.Scope = &session.EventScope{}
				}
				cloned.Event.Scope.TurnID = firstNonEmpty(strings.TrimSpace(cloned.Event.Scope.TurnID), subagentTurnID(t.ref.TaskID, t.turnSeq))
			}
			t.appendStreamFrameLocked(cloned)
			t.streamTerminalFramed = t.streamTerminalFramed || cloned.Closed
		}
		if text == "" {
			if frame.State != "" {
				t.state = taskStateFromDelegation(delegation.State(frame.State))
				t.running = frame.Running
			} else if frame.Running {
				t.running = true
			}
			continue
		}
		if t.result == nil {
			t.result = map[string]any{}
		}
		t.result["output_preview"] = compactFinalOutput(t.stdout, t.stderr)
		if frame.State != "" {
			t.state = taskStateFromDelegation(delegation.State(frame.State))
		}
		t.running = frame.Running
	}
}

func subagentFrameAssistantText(frame stream.Frame) string {
	if strings.TrimSpace(frame.Text) != "" {
		return frame.Text
	}
	event := frame.Event
	if event == nil || session.EventTypeOf(event) != session.EventTypeAssistant {
		return ""
	}
	if update := session.ProtocolUpdateOf(event); update != nil &&
		strings.TrimSpace(update.SessionUpdate) == string(session.ProtocolUpdateTypeAgentThought) {
		return ""
	}
	if event.Message != nil {
		return event.Message.TextContent()
	}
	return event.Text
}

func (t *subagentTask) appendStreamLocked(text string) {
	if t == nil || text == "" {
		return
	}
	t.streamOutputCursor += int64(len([]byte(text)))
	t.appendRetainedSubagentTextLocked(text)
}

func (t *subagentTask) appendRetainedSubagentTextLocked(text string) {
	if t == nil || text == "" {
		return
	}
	raw := append([]byte(t.stdout), []byte(text)...)
	if len(raw) > subagentStreamByteCap {
		dropped := len(raw) - subagentStreamByteCap
		for dropped < len(raw) && !utf8.RuneStart(raw[dropped]) {
			dropped++
		}
		raw = raw[dropped:]
	}
	t.stdout = string(raw)
	t.stdoutCursor = int64(len([]byte(t.stdout)))
}

func (t *subagentTask) appendStreamFrameLocked(frame stream.Frame) {
	if t == nil || t.streamTerminalFramed {
		return
	}
	frame = stream.CloneFrame(frame)
	text := subagentFrameAssistantText(frame)
	if text == "" {
		text = frame.Text
	}
	if frame.Text == "" {
		frame.Text = text
	}
	frame.Ref.TaskID = firstNonEmpty(strings.TrimSpace(frame.Ref.TaskID), strings.TrimSpace(t.ref.TaskID))
	frame.Ref.SessionID = firstNonEmpty(strings.TrimSpace(frame.Ref.SessionID), strings.TrimSpace(t.sessionRef.SessionID))
	frame.Ref.TerminalID = firstNonEmpty(strings.TrimSpace(frame.Ref.TerminalID), subagentTurnID(t.ref.TaskID, t.turnSeq))
	frame.Cursor = stream.Cursor{
		Output: t.streamOutputCursor + int64(len([]byte(text))),
		Events: t.streamEventBase + int64(len(t.streamFrames)) + 1,
	}
	frameBytes := subagentStreamFrameSize(frame)
	if frameBytes > subagentStreamByteCap {
		t.streamOutputCursor = frame.Cursor.Output
		frame.Text = ""
		frame.Event = nil
		// Preserve the absolute event position while making the missing body
		// explicit to readers. Control projects this boundary as a transient gap.
		frame.EventsTruncatedBefore = frame.Cursor.Events
		frameBytes = subagentStreamFrameSize(frame)
	} else if text != "" {
		t.streamOutputCursor = frame.Cursor.Output
		t.appendRetainedSubagentTextLocked(text)
	}
	t.streamFrames = append(t.streamFrames, frame)
	t.streamFrameSizes = append(t.streamFrameSizes, frameBytes)
	t.streamBytes += frameBytes
	for len(t.streamFrames) > 0 && (len(t.streamFrames) > subagentStreamFrameCap || t.streamBytes > subagentStreamByteCap) {
		evictedBytes := subagentStreamFrameSize(t.streamFrames[0])
		if len(t.streamFrameSizes) > 0 {
			evictedBytes = t.streamFrameSizes[0]
			t.streamFrameSizes[0] = 0
			t.streamFrameSizes = t.streamFrameSizes[1:]
		}
		t.streamBytes -= evictedBytes
		t.streamFrames[0] = stream.Frame{}
		t.streamFrames = t.streamFrames[1:]
		t.streamEventBase++
	}
}

func (t *subagentTask) ensureTerminalStreamFrameLocked() {
	if t == nil || t.running || !stream.IsTerminalState(string(t.state)) || t.streamTerminalFramed {
		return
	}
	t.appendStreamFrameLocked(stream.Frame{
		Ref: stream.Ref{
			SessionID:  strings.TrimSpace(t.sessionRef.SessionID),
			TaskID:     strings.TrimSpace(t.ref.TaskID),
			TerminalID: subagentTurnID(t.ref.TaskID, t.turnSeq),
		},
		State:     string(t.state),
		Running:   false,
		Closed:    true,
		UpdatedAt: time.Now(),
	})
	t.streamTerminalFramed = true
}

func subagentStreamFrameSize(frame stream.Frame) int {
	data, err := json.Marshal(frame)
	if err != nil {
		return len(frame.Text)
	}
	return len(data)
}
