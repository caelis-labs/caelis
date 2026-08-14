package gatewayapp

import (
	"context"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/control/modelcatalog"
	"github.com/caelis-labs/caelis/control/modelprofile"
)

// resolveRuntimeProviderProfile resolves a Control-owned profile without
// mutating the model catalog, provider endpoints, or credential store.
func resolveRuntimeProviderProfile(
	profiles modelprofile.Configuration,
	lookup *modelLookup,
	profileID string,
	effort string,
) (ModelConfig, error) {
	profileID = modelprofile.NormalizeID(profileID)
	if profileID == "" {
		return ModelConfig{}, nil
	}
	profile, ok := modelprofile.Lookup(profiles, profileID)
	if !ok {
		return ModelConfig{}, fmt.Errorf("gatewayapp: model profile %q is not configured; use /connect", profileID)
	}
	if profile.Kind() != modelprofile.BackendProvider || profile.Backend.Provider == nil {
		return ModelConfig{}, fmt.Errorf("gatewayapp: model profile %q is not provider-backed", profileID)
	}
	if lookup == nil {
		return ModelConfig{}, fmt.Errorf("gatewayapp: model lookup is unavailable")
	}
	configured, err := lookup.ResolveConfig(profile.Backend.Provider.ModelConfigID)
	if err != nil {
		return ModelConfig{}, fmt.Errorf("gatewayapp: resolve model profile %q: %w", profileID, err)
	}
	effort = modelcatalog.NormalizeReasoningEffort(effort)
	if effort == "" {
		effort = modelcatalog.NormalizeReasoningEffort(profile.Effort.DefaultEffort)
	}
	if _, ok := profile.WireEffort(effort); !ok {
		return ModelConfig{}, fmt.Errorf("gatewayapp: model profile %q does not support reasoning effort %q", profileID, effort)
	}
	configured.ReasoningEffort = effort
	return configured, nil
}

func (s *Stack) resolveProviderProfileConfig(profileID, effort string) (ModelConfig, error) {
	if s == nil || s.store == nil {
		return ModelConfig{}, fmt.Errorf("gatewayapp: model profile store is unavailable")
	}
	snapshot, err := s.placementSnapshot(context.Background())
	if err != nil {
		return ModelConfig{}, err
	}
	return resolveRuntimeProviderProfile(snapshot.placement.Profiles, s.lookup, strings.TrimSpace(profileID), effort)
}
