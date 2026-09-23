package gatewayapp

import (
	"context"
	"fmt"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/control/application"
	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/control/modelprofile/builder"
	"github.com/caelis-labs/caelis/internal/kernel"
)

// applicationModelConfig uses the ordinary maintained capability matrix, but
// never the Host's current model/effort/speed selection. Empty application effort
// restores the selected provider model's default, not another Session's choice.
func applicationModelConfig(lookup *modelLookup, profile application.Profile) (ModelConfig, error) {
	configured, err := lookup.ResolveConfig(profile.Model)
	if err != nil {
		return ModelConfig{}, errorcode.Wrap(errorcode.InvalidArgument, "application model is unavailable", err)
	}
	capability, err := builder.FromProvider(configured)
	if err != nil {
		return ModelConfig{}, err
	}
	effort := profile.ReasoningEffort
	if effort == "" {
		effort = capability.Effort.DefaultEffort
	}
	if !capability.SupportsEffort(effort) {
		return ModelConfig{}, fmt.Errorf("%w: model %q does not support reasoning_effort %q", application.ErrUnsupported, profile.Model, effort)
	}
	configured.ReasoningEffort = effort
	switch profile.ServiceTier {
	case "":
	case string(model.ServiceTierPriority):
		if !modelconfig.SupportsSpeedMode(configured, "fast") {
			return ModelConfig{}, fmt.Errorf("%w: model %q does not support priority service tier", application.ErrUnsupported, profile.Model)
		}
	default:
		return ModelConfig{}, fmt.Errorf("%w: unknown service_tier %q", application.ErrUnsupported, profile.ServiceTier)
	}
	return configured, nil
}

func (s *runtimeComposition) validateApplicationProfile(ctx context.Context, profile application.Profile) error {
	if err := application.ValidateProfile(profile); err != nil {
		return err
	}
	configured, err := applicationModelConfig(s.lookup, profile)
	if err != nil {
		return err
	}
	// Building checks credentials and provider request support without issuing a
	// request. A rejected combination cannot become a saved but unusable choice.
	_, err = s.lookup.ResolveModelConfig(ctx, configured, s.activeRuntime.ContextWindow)
	return err
}

func (r *applicationTurnResolver) resolveProfileModel(ctx context.Context, profile application.Profile) (kernel.ModelResolution, error) {
	lookup, err := r.modelLookup(ctx)
	if err != nil {
		return kernel.ModelResolution{}, err
	}
	configured, err := applicationModelConfig(lookup, profile)
	if err != nil {
		return kernel.ModelResolution{}, err
	}
	return lookup.ResolveModelConfig(ctx, configured, r.composition.activeRuntime.ContextWindow)
}
