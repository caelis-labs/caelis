package gatewayapp

import (
	"context"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/control/agentbinding"
)

// An unbound Guardian leaves selection to the AssemblyResolver's current
// Session model, which can differ from the Host's default model.
func (s *runtimeComposition) resolveGuardianAgentModel(ctx context.Context, contextWindow int) (model.LLM, bool, error) {
	snapshot, err := s.placementSnapshot(ctx)
	if err != nil {
		return nil, false, err
	}
	if _, bound := agentbinding.Lookup(snapshot.placement.Bindings, agentbinding.HandleGuardian); !bound {
		return nil, false, nil
	}
	resolved, bound, err := s.resolveSystemAgentModel(ctx, agentbinding.HandleGuardian, contextWindow)
	if err != nil {
		return nil, false, err
	}
	return withSystemAgentReasoningEffort(resolved), bound, nil
}
