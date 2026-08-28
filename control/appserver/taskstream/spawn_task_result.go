package taskstream

import (
	"maps"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/display"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/spawn"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

// SpawnTaskResult is one terminal child result observed through a canonical
// Task read or wait update. ParentCallID selects the original Spawn tool call;
// RawOutput carries the exact child result, including FinalMessage when
// available.
type SpawnTaskResult struct {
	ParentCallID string
	Status       string
	RawOutput    map[string]any
}

// SpawnTaskResultsFromEnvelope returns terminal Spawn children observed by one
// final Task read or wait update. Singular observations use the typed Envelope
// parent; batch waits use each canonical tasks[] relation because one Envelope
// cannot identify multiple parents. Running and non-subagent items are ignored.
func SpawnTaskResultsFromEnvelope(env eventstream.Envelope) []SpawnTaskResult {
	return spawnTaskResultsFromObservations(terminalTaskObservationsFromEnvelope(env))
}

func spawnTaskResultsFromObservations(observations []terminalTaskObservation) []SpawnTaskResult {
	out := make([]SpawnTaskResult, 0, len(observations))
	for _, observation := range observations {
		if observation.ParentTool != spawn.ToolName || observation.TargetKind != "subagent" {
			continue
		}
		expandedTurns := map[string]struct{}{}
		if finals, ok := taskBatchOutputs(observation.RawOutput["final_responses"]); ok && len(finals) > 0 {
			for _, final := range finals {
				finalMessage, _ := final["final_message"].(string)
				if strings.TrimSpace(finalMessage) == "" {
					continue
				}
				rawOutput := maps.Clone(observation.RawOutput)
				delete(rawOutput, "final_responses")
				rawOutput["state"] = "completed"
				rawOutput["final_message"] = finalMessage
				if turnID := strings.TrimSpace(display.MapString(final, "turn_id")); turnID != "" {
					rawOutput["turn_id"] = turnID
					expandedTurns[turnID] = struct{}{}
				}
				if turnSeq, exists := final["turn_seq"]; exists {
					rawOutput["turn_seq"] = turnSeq
				}
				out = append(out, SpawnTaskResult{
					ParentCallID: observation.ParentCallID,
					Status:       schema.ToolStatusCompleted,
					RawOutput:    rawOutput,
				})
			}
		}
		state := strings.TrimSpace(display.MapString(observation.RawOutput, "state"))
		currentTurnID := strings.TrimSpace(display.MapString(observation.RawOutput, "turn_id"))
		_, currentExpanded := expandedTurns[currentTurnID]
		if len(expandedTurns) > 0 && (!eventstream.IsTerminalLifecycleState(state) || currentExpanded) {
			continue
		}
		rawOutput := observation.RawOutput
		if len(expandedTurns) > 0 {
			rawOutput = maps.Clone(observation.RawOutput)
			delete(rawOutput, "final_responses")
			// The singular alias belongs to the newest unread response, not to a
			// distinct failed/cancelled or output-free current Turn.
			delete(rawOutput, "final_message")
		}
		out = append(out, SpawnTaskResult{
			ParentCallID: observation.ParentCallID,
			Status:       spawnObservedTaskStatus(observation.ObserverStatus, rawOutput),
			RawOutput:    rawOutput,
		})
	}
	return out
}

func spawnObservedTaskStatus(observerStatus string, rawOutput map[string]any) string {
	switch strings.ToLower(strings.TrimSpace(display.MapString(rawOutput, "state"))) {
	case "completed", "complete", "succeeded", "success", "done":
		return schema.ToolStatusCompleted
	case "failed", "interrupted", "cancelled", "canceled", "terminated", "timed_out", "timeout", "unknown_outcome":
		return schema.ToolStatusFailed
	}
	if strings.EqualFold(strings.TrimSpace(observerStatus), schema.ToolStatusFailed) {
		return schema.ToolStatusFailed
	}
	return schema.ToolStatusCompleted
}
