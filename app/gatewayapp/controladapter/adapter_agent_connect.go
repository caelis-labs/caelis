package controladapter

import (
	"context"

	controlagents "github.com/caelis-labs/caelis/control/agents"
)

// DisconnectCandidates lists only user-configured external ACP Agents.
func (d *assembler) DisconnectCandidates(ctx context.Context) ([]controlagents.DisconnectCandidate, error) {
	if d == nil || d.stack == nil || d.stack.Agent.DisconnectCandidatesFn == nil {
		return nil, missingRuntimeDependency("ACP Agent disconnect candidates")
	}
	return d.stack.Agent.DisconnectCandidatesFn(ctx)
}
