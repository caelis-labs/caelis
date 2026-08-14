package runtime

import (
	"strconv"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
)

const (
	subagentFinalResponsesResultKey     = "final_responses"
	subagentUnreadFinalResponsesMetaKey = "unread_final_responses"
)

type subagentFinalResponse struct {
	TurnID       string `json:"turn_id"`
	TurnSeq      int64  `json:"turn_seq"`
	FinalMessage string `json:"final_message"`
}

// consumeSubagentFinalResponses advances the single parent-model observation
// frontier and decorates one Task read/wait snapshot with every exact retained
// Final Response that has not previously crossed that frontier. Final text
// remains owned by the Task's existing latest/semantic retention path.
func (tm *taskRuntime) consumeSubagentFinalResponses(snapshot taskapi.Snapshot) taskapi.Snapshot {
	if tm == nil || snapshot.Kind != taskapi.KindSubagent {
		return snapshot
	}
	tm.mu.RLock()
	task := tm.subagents[strings.TrimSpace(snapshot.Ref.TaskID)]
	tm.mu.RUnlock()
	if task == nil {
		return snapshot
	}

	task.mu.Lock()
	if task.latestFinalTurnSeq == 0 && snapshot.State == taskapi.StateCompleted {
		if finalMessage := firstNonBlankTaskOutput(
			taskRawStringValue(snapshot.Result["final_message"]),
			taskRawStringValue(snapshot.Result["result"]),
		); taskOutputHasNonBlankLine(finalMessage) {
			turnSeq := max(task.turnSeq, 1)
			task.latestFinalText = finalMessage
			task.latestFinalTurnSeq = turnSeq
			task.latestFinalOrder = task.streamEventBase + int64(len(task.streamFrames))
			task.semanticRetention.protectLatestFinal(subagentTurnID(task.ref.TaskID, turnSeq), task.latestFinalOrder)
		}
	}
	responses := task.unreadFinalResponsesLocked()
	if len(responses) > 0 {
		task.finalResponseCursor = max(task.finalResponseCursor, responses[len(responses)-1].TurnSeq)
		if task.metadata == nil {
			task.metadata = map[string]any{}
		}
		task.metadata[subagentFinalResponseCursorMeta] = task.finalResponseCursor
	}
	task.mu.Unlock()

	result := session.CloneState(snapshot.Result)
	if result == nil {
		result = map[string]any{}
	}
	// The durable Task result protects the latest Final, but an explicit Task
	// read/wait exposes it only through the unread response frontier. Removing
	// the canonical fallback here prevents a second observation from replaying
	// an already delivered Final.
	delete(result, "result")
	delete(result, "final_message")
	if len(responses) > 0 {
		items := make([]any, 0, len(responses))
		for _, response := range responses {
			items = append(items, map[string]any{
				"turn_id": response.TurnID, "turn_seq": response.TurnSeq,
				"final_message": response.FinalMessage,
			})
		}
		result[subagentFinalResponsesResultKey] = items
	}
	snapshot.Result = result
	metadata := session.CloneState(snapshot.Metadata)
	delete(metadata, subagentUnreadFinalResponsesMetaKey)
	snapshot.Metadata = metadata
	return snapshot
}

func snapshotHasUnreadSubagentFinalResponses(snapshot taskapi.Snapshot) bool {
	value, _ := snapshot.Metadata[subagentUnreadFinalResponsesMetaKey].(bool)
	return snapshot.Kind == taskapi.KindSubagent && value
}

// markSubagentFinalResponseObserved records that Spawn itself already returned
// the initial synchronous Final Response. A later Task read must not echo it.
func (tm *taskRuntime) markSubagentFinalResponseObserved(snapshot taskapi.Snapshot) {
	if tm == nil || snapshot.Kind != taskapi.KindSubagent || snapshot.Running {
		return
	}
	if !taskOutputHasNonBlankLine(firstNonBlankTaskOutput(
		taskRawStringValue(snapshot.Result["final_message"]),
		taskRawStringValue(snapshot.Result["result"]),
	)) {
		return
	}
	tm.mu.RLock()
	task := tm.subagents[strings.TrimSpace(snapshot.Ref.TaskID)]
	tm.mu.RUnlock()
	if task == nil {
		return
	}
	task.mu.Lock()
	task.finalResponseCursor = max(task.finalResponseCursor, task.latestFinalTurnSeq)
	if task.metadata == nil {
		task.metadata = map[string]any{}
	}
	task.metadata[subagentFinalResponseCursorMeta] = task.finalResponseCursor
	task.mu.Unlock()
}

func (t *subagentTask) hasUnreadFinalResponsesLocked() bool {
	return t != nil && t.latestFinalTurnSeq > t.finalResponseCursor
}

func (t *subagentTask) unreadFinalResponsesLocked() []subagentFinalResponse {
	if t == nil || !t.hasUnreadFinalResponsesLocked() {
		return nil
	}
	responses := make([]subagentFinalResponse, 0, 2)
	if t.semanticRetention.order != nil {
		for element := t.semanticRetention.order.Front(); element != nil; element = element.Next() {
			unit, _ := element.Value.(*subagentSemanticUnit)
			if unit == nil || unit.latestFinal || unit.priority != subagentSemanticFinal {
				continue
			}
			turnSeq, ok := subagentTurnSeq(t.ref.TaskID, unit.turnID)
			if !ok || turnSeq <= t.finalResponseCursor {
				continue
			}
			text := subagentFrameAssistantText(unit.materialize())
			if !taskOutputHasNonBlankLine(text) {
				continue
			}
			responses = append(responses, subagentFinalResponse{
				TurnID: unit.turnID, TurnSeq: turnSeq, FinalMessage: text,
			})
		}
	}
	if t.latestFinalTurnSeq > t.finalResponseCursor && taskOutputHasNonBlankLine(t.latestFinalText) {
		responses = append(responses, subagentFinalResponse{
			TurnID:       subagentTurnID(t.ref.TaskID, t.latestFinalTurnSeq),
			TurnSeq:      t.latestFinalTurnSeq,
			FinalMessage: t.latestFinalText,
		})
	}
	return responses
}

func subagentTurnSeq(taskID string, turnID string) (int64, bool) {
	prefix := strings.TrimSpace(taskID) + ":"
	turnID = strings.TrimSpace(turnID)
	if prefix == ":" || !strings.HasPrefix(turnID, prefix) {
		return 0, false
	}
	seq, err := strconv.ParseInt(strings.TrimPrefix(turnID, prefix), 10, 64)
	return seq, err == nil && seq > 0
}
