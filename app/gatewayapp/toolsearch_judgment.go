package gatewayapp

import (
	"context"
	"fmt"

	"github.com/caelis-labs/caelis/agent-sdk/judgment"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolsearch"
	"github.com/caelis-labs/caelis/control/agentbinding"
	"github.com/caelis-labs/caelis/control/modelprofile"
)

// boundToolSearchRanker resolves the explicit ToolSearch binding for each call.
// Without a binding the SDK selector receives the complete main-turn model
// object and settings; neither tools nor conversation history accompany it.
type boundToolSearchRanker struct {
	resolve      func(context.Context) (judgment.Evaluator, error)
	resolveAgent func(context.Context) (model.LLM, error)
}

func newBoundToolSearchRanker(s *runtimeComposition) boundToolSearchRanker {
	return boundToolSearchRanker{
		resolve: func(ctx context.Context) (judgment.Evaluator, error) {
			return s.boundJudgment(ctx, agentbinding.HandleToolSearch)
		},
		resolveAgent: s.boundToolSearchAgentModel,
	}
}

func (r boundToolSearchRanker) Rank(ctx context.Context, query string, candidates []tool.Definition, limit int, selected toolsearch.SearchModel) ([]string, error) {
	if r.resolve != nil {
		evaluator, err := r.resolve(ctx)
		if err != nil {
			return nil, err
		}
		if evaluator != nil {
			return toolsearch.NewSemanticRanker(evaluator).Rank(ctx, query, candidates, limit, selected)
		}
	}
	if r.resolveAgent != nil {
		llm, err := r.resolveAgent(ctx)
		if err != nil {
			return nil, err
		}
		if llm != nil {
			selected.Model = llm
			selected.Reasoning = model.ReasoningConfig{}
			selected.ServiceTier = ""
		}
	}
	return toolsearch.NewAgentRanker().Rank(ctx, query, candidates, limit, selected)
}

// boundToolSearchAgentModel reuses Host model lookup and credential resolution.
// The binding is optional; a Jev binding is handled by boundJudgment instead.
func (s *runtimeComposition) boundToolSearchAgentModel(ctx context.Context) (model.LLM, error) {
	snapshot, err := s.placementSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	binding, bound := agentbinding.Lookup(snapshot.placement.Bindings, agentbinding.HandleToolSearch)
	if !bound {
		return nil, nil
	}
	profile, ok := modelprofile.Lookup(snapshot.placement.Profiles, binding.ProfileID)
	if !ok {
		return nil, fmt.Errorf("gatewayapp: ToolSearch binding references an unavailable profile")
	}
	if profile.Judgment {
		return nil, nil
	}
	if !agentbinding.SupportsProfile(agentbinding.HandleToolSearch, profile) {
		return nil, fmt.Errorf("gatewayapp: ToolSearch binding requires a provider model")
	}
	cfg, ok := s.lookup.Config(profile.Backend.Provider.ModelConfigID)
	if !ok {
		return nil, fmt.Errorf("gatewayapp: ToolSearch model configuration is unavailable")
	}
	cfg.ReasoningEffort = binding.Effort
	resolved, err := s.lookup.ResolveModelConfig(ctx, cfg, s.activeRuntime.ContextWindow)
	if err != nil {
		return nil, err
	}
	return withSystemAgentReasoningEffort(resolved), nil
}
