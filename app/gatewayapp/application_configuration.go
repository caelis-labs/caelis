package gatewayapp

import (
	"context"
	"errors"
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
	configured, present, err := lookup.ResolveConfigIfPresent(profile.Model)
	if err != nil {
		if errors.Is(err, modelconfig.ErrAmbiguousSelector) {
			return ModelConfig{}, errorcode.Wrap(errorcode.InvalidArgument, fmt.Sprintf("application model %q is ambiguous; use a fully qualified model ID", profile.Model), err)
		}
		return ModelConfig{}, err
	}
	if !present {
		return ModelConfig{}, errorcode.New(errorcode.InvalidArgument, fmt.Sprintf("application model %q is not configured on this Host", profile.Model))
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
		return ModelConfig{}, errorcode.Wrap(errorcode.Unsupported, fmt.Sprintf("application model %q does not support reasoning_effort %q", profile.Model, effort), application.ErrUnsupported)
	}
	configured.ReasoningEffort = effort
	switch profile.ServiceTier {
	case "":
	case string(model.ServiceTierPriority):
		if !modelconfig.SupportsSpeedMode(configured, "fast") {
			return ModelConfig{}, errorcode.Wrap(errorcode.Unsupported, fmt.Sprintf("application model %q does not support service_tier %q", profile.Model, profile.ServiceTier), application.ErrUnsupported)
		}
	default:
		return ModelConfig{}, errorcode.Wrap(errorcode.Unsupported, fmt.Sprintf("application service_tier %q is unsupported; use an empty value for the provider default or priority with a supported model", profile.ServiceTier), application.ErrUnsupported)
	}
	return configured, nil
}

func (s *runtimeComposition) validateApplicationProfile(ctx context.Context, profile application.Profile) error {
	if err := application.ValidateProfile(profile); err != nil {
		return err
	}
	if _, err := applicationSkillCatalog(profile); err != nil {
		return errorcode.Wrap(errorcode.InvalidArgument, "application Skill selection is invalid", err)
	}
	configured, err := applicationModelConfig(s.lookup, profile)
	if err != nil {
		return err
	}
	// Building checks credentials and provider request support without issuing a
	// request. A rejected combination cannot become a saved but unusable choice.
	if _, err = s.lookup.ResolveModelConfig(ctx, configured, s.activeRuntime.ContextWindow); err != nil {
		return err
	}
	if profile.Reviewer != nil {
		_, err = resolveApplicationReviewer(ctx, s.lookup, profile, s.activeRuntime.ContextWindow)
	}
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

// applicationModelImageInput observes canonical model declarations even when a
// committed change's process-catalog refresh failed. The metadata-only lookup
// has no credential or HTTP resolvers and does not create an execution snapshot.
func (s *runtimeComposition) applicationModelImageInput(ctx context.Context, profile application.Profile) (*bool, error) {
	if s == nil || s.authorities.store == nil {
		return nil, errors.New("gatewayapp: app config store unavailable")
	}
	doc, err := s.authorities.store.LoadContext(ctx)
	if err != nil {
		return nil, err
	}
	lookup, err := newModelLookupFromDocument(doc, 0)
	if err != nil {
		return nil, err
	}
	configured, err := applicationModelConfig(lookup, profile)
	if err != nil {
		return nil, err
	}
	return modelconfig.ModelImageInput(configured), nil
}
