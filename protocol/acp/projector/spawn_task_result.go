package projector

import (
	"maps"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/display"
	"github.com/caelis-labs/caelis/agent-sdk/tool/identity"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

// SpawnTaskResult is one terminal child result observed through a canonical
// Task wait update. ParentCallID selects the original Spawn tool call; RawOutput
// carries the exact child result, including its FinalMessage when available.
type SpawnTaskResult struct {
	ParentCallID string
	Status       string
	RawOutput    map[string]any
}

// SpawnTaskResultsFromEnvelope returns terminal Spawn children observed by one
// final Task wait update. A singular wait uses the typed Envelope parent; a
// batch wait uses each canonical tasks[] parent relation because one Envelope
// cannot identify multiple parents. Running and non-subagent items are ignored.
func SpawnTaskResultsFromEnvelope(env eventstream.Envelope) []SpawnTaskResult {
	if env.Kind != eventstream.KindSessionUpdate || env.Update == nil ||
		(env.Scope != "" && env.Scope != eventstream.ScopeMain) {
		return nil
	}
	update, ok := eventstream.ToolCallUpdateFromEnvelope(env)
	if !ok || !spawnTaskObserverStatusFinal(update.Status) {
		return nil
	}
	rawInput := schema.NormalizeRawMap(update.RawInput)
	rawOutput := schema.NormalizeRawMap(update.RawOutput)
	if len(rawInput) == 0 || len(rawOutput) == 0 ||
		display.ToolTaskAction(rawInput, rawOutput, update.Meta) != "wait" {
		return nil
	}
	if tasks, batch := spawnBatchTaskOutputs(rawOutput["tasks"]); batch {
		out := make([]SpawnTaskResult, 0, len(tasks))
		seen := make(map[string]struct{}, len(tasks))
		for _, taskOutput := range tasks {
			parentCallID := strings.TrimSpace(display.MapString(taskOutput, "parent_call"))
			if parentCallID == "" || !spawnObservedTaskTerminal(taskOutput) {
				continue
			}
			if _, duplicate := seen[parentCallID]; duplicate {
				continue
			}
			seen[parentCallID] = struct{}{}
			out = append(out, SpawnTaskResult{
				ParentCallID: parentCallID,
				Status:       spawnObservedTaskStatus(update.Status, taskOutput),
				RawOutput:    maps.Clone(taskOutput),
			})
		}
		return out
	}
	if env.ParentTool == nil {
		return nil
	}
	parentCallID := strings.TrimSpace(env.ParentTool.ToolCallID)
	if parentCallID == "" || identity.CanonicalOrSelf(env.ParentTool.ToolName) != identity.Spawn ||
		strings.TrimSpace(update.ToolCallID) == parentCallID || !spawnObservedTaskTerminal(rawOutput) {
		return nil
	}
	if parentCall := strings.TrimSpace(display.MapString(rawOutput, "parent_call")); parentCall != "" && parentCall != parentCallID {
		return nil
	}
	return []SpawnTaskResult{{
		ParentCallID: parentCallID,
		Status:       spawnObservedTaskStatus(update.Status, rawOutput),
		RawOutput:    maps.Clone(rawOutput),
	}}
}

func spawnBatchTaskOutputs(value any) ([]map[string]any, bool) {
	switch tasks := value.(type) {
	case []any:
		out := make([]map[string]any, 0, len(tasks))
		for _, taskOutput := range tasks {
			if mapped, ok := taskOutput.(map[string]any); ok {
				out = append(out, mapped)
			}
		}
		return out, true
	case []map[string]any:
		return tasks, true
	default:
		return nil, false
	}
}

func spawnObservedTaskTerminal(rawOutput map[string]any) bool {
	return strings.EqualFold(display.ToolTaskTargetKind(nil, rawOutput, nil), "subagent") &&
		identity.CanonicalOrSelf(display.MapString(rawOutput, "parent_tool")) == identity.Spawn &&
		eventstream.IsTerminalLifecycleState(display.MapString(rawOutput, "state"))
}

func spawnTaskObserverStatusFinal(status *string) bool {
	if status == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(*status)) {
	case schema.ToolStatusCompleted, schema.ToolStatusFailed, "interrupted", "cancelled", "canceled", "terminated", "timed_out", "timeout":
		return true
	default:
		return false
	}
}

func spawnObservedTaskStatus(observerStatus *string, rawOutput map[string]any) string {
	switch strings.ToLower(strings.TrimSpace(display.MapString(rawOutput, "state"))) {
	case "completed", "complete", "succeeded", "success", "done":
		return schema.ToolStatusCompleted
	case "failed", "interrupted", "cancelled", "canceled", "terminated", "timed_out", "timeout":
		return schema.ToolStatusFailed
	}
	if observerStatus != nil && strings.EqualFold(strings.TrimSpace(*observerStatus), schema.ToolStatusFailed) {
		return schema.ToolStatusFailed
	}
	return schema.ToolStatusCompleted
}
