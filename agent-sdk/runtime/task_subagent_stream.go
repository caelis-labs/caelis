package runtime

import (
	"strings"

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
	tm.mu.RLock()
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
	tm.mu.RUnlock()
	if task == nil {
		if taskID != "" {
			tm.mu.Lock()
			tm.pending[taskID] = append(tm.pending[taskID], stream.CloneFrame(frame))
			tm.mu.Unlock()
		}
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
	if taskOutputHasNonBlankLine(text) && subagentFramesContainAssistantText(t.streamFrames) {
		return
	}
	if !taskOutputHasNonBlankLine(text) {
		if len(t.streamFrames) > 0 {
			return
		}
		text = result.OutputPreview
	}
	if !taskOutputHasNonBlankLine(text) {
		return
	}
	t.appendStreamLocked(text)
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
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, frame := range frames {
		text := subagentFrameAssistantText(frame)
		if text == "" {
			text = frame.Text
		}
		if frame.Event != nil || text != "" {
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
			t.streamFrames = append(t.streamFrames, cloned)
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
		t.appendStreamLocked(text)
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
	t.stdout += text
	t.stdoutCursor = int64(len([]byte(t.stdout)))
}
