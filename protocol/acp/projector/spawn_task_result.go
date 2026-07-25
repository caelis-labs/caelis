package projector

import (
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/display"
	"github.com/caelis-labs/caelis/agent-sdk/tool/identity"
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
		if observation.ParentTool != identity.Spawn || observation.TargetKind != "subagent" {
			continue
		}
		out = append(out, SpawnTaskResult{
			ParentCallID: observation.ParentCallID,
			Status:       spawnObservedTaskStatus(observation.ObserverStatus, observation.RawOutput),
			RawOutput:    observation.RawOutput,
		})
	}
	return out
}

func spawnObservedTaskStatus(observerStatus string, rawOutput map[string]any) string {
	switch strings.ToLower(strings.TrimSpace(display.MapString(rawOutput, "state"))) {
	case "completed", "complete", "succeeded", "success", "done":
		return schema.ToolStatusCompleted
	case "failed", "interrupted", "cancelled", "canceled", "terminated", "timed_out", "timeout":
		return schema.ToolStatusFailed
	}
	if strings.EqualFold(strings.TrimSpace(observerStatus), schema.ToolStatusFailed) {
		return schema.ToolStatusFailed
	}
	return schema.ToolStatusCompleted
}
