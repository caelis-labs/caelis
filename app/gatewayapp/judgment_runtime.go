package gatewayapp

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/judgment"
	"github.com/caelis-labs/caelis/control/agentbinding"
	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/control/modelprofile"
)

// boundJudgment resolves only explicitly selected evaluation capabilities from
// the current activation snapshot. An absent binding performs no inference.
func (s *runtimeComposition) boundJudgment(ctx context.Context, handle agentbinding.Handle) (judgment.Evaluator, error) {
	snapshot, err := s.placementSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	binding, bound := agentbinding.Lookup(snapshot.placement.Bindings, handle)
	if !bound {
		return nil, nil
	}
	profile, ok := modelprofile.Lookup(snapshot.placement.Profiles, binding.ProfileID)
	if !ok {
		return nil, fmt.Errorf("gatewayapp: judgment binding references an unavailable profile")
	}
	if !profile.Judgment {
		return nil, nil
	}
	if !agentbinding.SupportsProfile(handle, profile) {
		return nil, fmt.Errorf("gatewayapp: profile cannot evaluate this capability")
	}
	cfg, ok := s.lookup.Config(profile.Backend.Provider.ModelConfigID)
	if !ok {
		return nil, fmt.Errorf("gatewayapp: judgment model configuration is unavailable")
	}
	cfg, err = resolveProviderCredentials(ctx, cfg, s.lookup.resolveHTTPClient, s.lookup.resolveTransportHTTPClient, s.lookup.resolveAPIKey)
	if err != nil {
		return nil, err
	}
	evaluator, err := modelconfig.BuildJudgment(cfg)
	if err != nil {
		return nil, err
	}
	return observedJudgment{Evaluator: evaluator, handle: handle, provider: cfg.Provider, diagnostics: s.authorities.diagnostics}, nil
}

// Evaluation diagnostics contain only request counts, timing and usage. They
// never retain source evidence, proposals, credentials or provider error bodies.
type observedJudgment struct {
	judgment.Evaluator
	handle      agentbinding.Handle
	provider    string
	diagnostics *slog.Logger
}

func (e observedJudgment) ProviderName() string { return e.provider }
func (e observedJudgment) Evaluate(ctx context.Context, request judgment.Request) (judgment.Response, error) {
	start := time.Now()
	response, err := e.Evaluator.Evaluate(ctx, request)
	if e.diagnostics != nil {
		outcome := "completed"
		if err != nil {
			outcome = "unavailable"
		}
		e.diagnostics.Info("Judgment evaluation", "handle", e.handle, "provider", e.provider, "model", firstNonEmpty(response.Model, e.Name()), "outcome", outcome, "elapsed_ms", time.Since(start).Milliseconds(), "input_tokens", response.Usage.InputTokens, "output_tokens", response.Usage.OutputTokens)
	}
	return response, err
}
