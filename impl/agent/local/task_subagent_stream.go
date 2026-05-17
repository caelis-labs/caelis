package local

import (
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/delegation"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/ports/stream"
)

func (tm *taskRuntime) PublishStream(frame stream.Frame) {
	if tm == nil {
		return
	}
	taskID := strings.TrimSpace(frame.Ref.TaskID)
	sessionID := strings.TrimSpace(frame.Ref.SessionID)
	tm.mu.RLock()
	task := tm.subagents[taskID]
	if task == nil && sessionID != "" {
		for _, candidate := range tm.subagents {
			if candidate == nil {
				continue
			}
			if strings.TrimSpace(candidate.anchor.SessionID) == sessionID {
				task = candidate
				break
			}
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
	if strings.TrimSpace(t.stdout) != "" || strings.TrimSpace(t.stderr) != "" {
		return
	}
	text := strings.TrimSpace(result.Result)
	if text != "" && subagentFramesContainAssistantText(t.streamFrames) {
		return
	}
	if text == "" {
		if len(t.streamFrames) > 0 {
			return
		}
		text = strings.TrimSpace(result.OutputPreview)
	}
	if text == "" {
		return
	}
	t.appendStreamLocked(text)
}

func subagentFramesContainAssistantText(frames []stream.Frame) bool {
	for _, frame := range frames {
		if strings.TrimSpace(frame.Text) != "" {
			return true
		}
		event := frame.Event
		if event == nil || session.EventTypeOf(event) != session.EventTypeAssistant {
			continue
		}
		if event.Message != nil && strings.TrimSpace(event.Message.TextContent()) != "" {
			return true
		}
		updateType := ""
		if event.Protocol != nil {
			updateType = strings.TrimSpace(event.Protocol.UpdateType)
		}
		if updateType == string(session.ProtocolUpdateTypeAgentThought) {
			continue
		}
		if strings.TrimSpace(event.Text) != "" {
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
		if frame.Event != nil || frame.Text != "" {
			cloned := stream.CloneFrame(frame)
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
		text := frame.Text
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

func (t *subagentTask) appendStreamLocked(text string) {
	if t == nil || text == "" {
		return
	}
	t.stdout += text
	t.stdoutCursor = int64(len([]byte(t.stdout)))
}
