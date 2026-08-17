package controladapter

import (
	"context"

	controlagents "github.com/caelis-labs/caelis/control/agents"
)

// DisconnectCandidates lists only user-configured external ACP Agents.
func (d *assembler) DisconnectCandidates(ctx context.Context) ([]controlagents.DisconnectCandidate, error) {
	if d == nil || d.deps == nil || d.deps.Agent.DisconnectCandidatesFn == nil {
		return nil, missingRuntimeDependency("ACP Agent disconnect candidates")
	}
	return d.deps.Agent.DisconnectCandidatesFn(ctx)
}
