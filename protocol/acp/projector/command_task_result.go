package projector

import (
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/shell"
)

// CommandTaskResult identifies one terminal RunCommand observed through a
// canonical Task read or wait update. It is presentation repair input only;
// the Task result remains the lifecycle authority.
type CommandTaskResult struct {
	ParentCallID string
	Handle       string
}

func commandTaskResultsFromObservations(observations []terminalTaskObservation) []CommandTaskResult {
	out := make([]CommandTaskResult, 0, len(observations))
	for _, observation := range observations {
		if observation.ParentTool != shell.RunCommandToolName ||
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
