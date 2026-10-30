package gatewayapp

import (
	"context"
	"fmt"
	"strings"

	sdkplacement "github.com/caelis-labs/caelis/agent-sdk/placement"
	"github.com/caelis-labs/caelis/control/modelcatalog"
	"github.com/caelis-labs/caelis/control/modelprofile"
	modelprofilebuilder "github.com/caelis-labs/caelis/control/modelprofile/builder"
	controlplacement "github.com/caelis-labs/caelis/control/placement"
)

type resolvedSessionModelProfile struct {
	Profile   modelprofile.ModelProfile
	Placement sdkplacement.Placement
	Config    ModelConfig
	Effort    string
}

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

// resolveRuntimeProfile returns the local provider configuration needed by a
// selected profile. ACP-backed profiles intentionally return an empty model:
// their Turns execute through the external controller Runtime.
func resolveRuntimeProfile(
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
	if profile.Kind() == modelprofile.BackendACP {
		effort = modelcatalog.NormalizeReasoningEffort(effort)
		if effort == "" {
			effort = profile.Effort.DefaultEffort
		}
		if !profile.SupportsEffort(effort) {
			return ModelConfig{}, fmt.Errorf("gatewayapp: model profile %q does not support reasoning effort %q", profileID, effort)
		}
		return ModelConfig{}, nil
	}
	return resolveRuntimeProviderProfile(profiles, lookup, profileID, effort)
}

// Materialize credentials while the placement's lookup still owns selection.
// Only the process-local child pin receives the key; ACP and durable profiles do not.
func materializeProviderProfileConfig(ctx context.Context, snapshot *placementSnapshot, profileID, effort string) (ModelConfig, error) {
	configured, err := resolveRuntimeProviderProfile(snapshot.placement.Profiles, snapshot.lookup, profileID, effort)
	if err != nil {
		return ModelConfig{}, err
	}
	return snapshot.lookup.materializeAPIKeyCredential(ctx, configured)
}

// A native self participant inherits the activated Session main model, including
// capabilities. Runtime Team revisions must not validate it against a different
// provider generation. Explicit deletion still revokes the model through the
// existing catalog and child pin admission fences.
func (s *runtimeComposition) selfPlacementSnapshot(ctx context.Context, profileID string) (*placementSnapshot, error) {
	if s.activation == nil {
		return s.placementSnapshot(ctx)
	}
	configured, err := s.lookup.ResolveConfig(strings.TrimPrefix(modelprofile.NormalizeID(profileID), "provider:"))
	if err != nil {
		return nil, err
	}
	if s.activation.modelCatalog == nil || !s.activation.modelCatalog.HasAlias(configured.ID) {
		return nil, fmt.Errorf("gatewayapp: self model %q is no longer configured", configured.ID)
	}
	profile, err := modelprofilebuilder.FromProvider(configured)
	if err != nil {
		return nil, err
	}
	doc := AppConfig{Models: persistedModelConfig{Configs: []ModelConfig{configured}}, ModelProfiles: modelprofile.Configuration{Profiles: []modelprofile.ModelProfile{profile}}}
	snapshot := newPlacementSnapshot(doc)
	snapshot.lookup, err = cloneSessionModelLookup(s.lookup, doc)
	if err != nil {
		return nil, err
	}
	return snapshot, nil
}

func (s *runtimeComposition) resolveSessionModelProfile(
	ctx context.Context,
	selector string,
	effort string,
) (resolvedSessionModelProfile, bool, error) {
	if s == nil {
		return resolvedSessionModelProfile{}, false, fmt.Errorf("gatewayapp: Runtime model selection is unavailable")
	}
	snapshot, err := s.placementSnapshot(ctx)
	if err != nil {
		return resolvedSessionModelProfile{}, false, err
	}
	profile, ok := modelprofile.Lookup(snapshot.placement.Profiles, selector)
	configured := ModelConfig{}
	if !ok {
		catalog := s.lookup
		if s.activation != nil && s.activation.modelCatalog != nil {
			catalog = s.activation.modelCatalog
		}
		if catalog == nil {
			return resolvedSessionModelProfile{}, false, nil
		}
		configured, ok, err = catalog.ResolveConfigIfPresent(selector)
		if err != nil {
			return resolvedSessionModelProfile{}, false, err
		}
		if !ok {
			return resolvedSessionModelProfile{}, false, nil
		}
		profile, ok = modelprofile.Lookup(snapshot.placement.Profiles, modelprofile.BuildProviderID(configured.ID))
		if !ok {
			profile, err = modelprofilebuilder.FromProvider(configured)
			if err != nil {
				return resolvedSessionModelProfile{}, false, err
			}
		}
	}
	if configured.ID == "" && profile.Kind() == modelprofile.BackendProvider {
		catalog := s.lookup
		if s.activation != nil && s.activation.modelCatalog != nil {
			catalog = s.activation.modelCatalog
		}
		if catalog == nil {
			return resolvedSessionModelProfile{}, false, fmt.Errorf("gatewayapp: model catalog is unavailable")
		}
		configured, err = catalog.ResolveConfig(profile.Backend.Provider.ModelConfigID)
		if err != nil {
			return resolvedSessionModelProfile{}, false, err
		}
	}
	effort = modelcatalog.NormalizeReasoningEffort(effort)
	if effort == "" {
		effort = profile.Effort.DefaultEffort
	}
	if profile.Judgment {
		return resolvedSessionModelProfile{}, false, fmt.Errorf("gatewayapp: judgment models cannot control a Session")
	}
	if !profile.SupportsEffort(effort) {
		return resolvedSessionModelProfile{}, false, fmt.Errorf("gatewayapp: model profile %q does not support reasoning effort %q", profile.ID, effort)
	}
	if profile.Kind() == modelprofile.BackendProvider {
		return resolvedSessionModelProfile{Profile: profile, Config: configured, Effort: effort}, true, nil
	}
	frozen, err := controlplacement.ResolveProfile(snapshot.placement, profile.ID, effort)
	if err != nil {
		return resolvedSessionModelProfile{}, false, err
	}
	return resolvedSessionModelProfile{Profile: profile, Placement: frozen, Config: configured, Effort: effort}, true, nil
}
