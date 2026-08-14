package projector

import (
	"github.com/caelis-labs/caelis/agent-sdk/tool/identity"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
)

// CommandTaskResult identifies one terminal RunCommand observed through a
// canonical Task read or wait update. It is presentation repair input only;
// the Task result remains the lifecycle authority.
type CommandTaskResult struct {
	ParentCallID string
	Handle       string
}

// CommandTaskResultsFromEnvelope returns terminal RunCommand targets observed
// by one final Task read or wait update. Singular observations require the
// typed Envelope parent. Batch waits use each canonical tasks[] relation
// because one Envelope cannot identify multiple parents.
func CommandTaskResultsFromEnvelope(env eventstream.Envelope) []CommandTaskResult {
	return commandTaskResultsFromObservations(terminalTaskObservationsFromEnvelope(env))
}

func commandTaskResultsFromObservations(observations []terminalTaskObservation) []CommandTaskResult {
	out := make([]CommandTaskResult, 0, len(observations))
	for _, observation := range observations {
		if observation.ParentTool != identity.RunCommand ||
			(observation.TargetKind != "command" && observation.TargetKind != "terminal") {
			continue
		}
		out = append(out, CommandTaskResult{
			ParentCallID: observation.ParentCallID,
			Handle:       observation.Handle,
		})
	}
	return out
}
