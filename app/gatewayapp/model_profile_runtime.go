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

func (s *runtimeComposition) resolveProviderProfileConfig(profileID, effort string) (ModelConfig, error) {
	if s == nil || s.authorities.store == nil {
		return ModelConfig{}, fmt.Errorf("gatewayapp: model profile store is unavailable")
	}
	snapshot, err := s.placementSnapshot(context.Background())
	if err != nil {
		return ModelConfig{}, err
	}
	return resolveRuntimeProviderProfile(snapshot.placement.Profiles, s.lookup, strings.TrimSpace(profileID), effort)
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
		// Completion is Host-live, while an activated Session Runtime keeps a
		// pinned snapshot. Consult canonical Control state for newly connected
		// profiles; the provider/ACP pin path validates and installs the exact
		// execution generation before the Session mutation commits.
		if s.authorities.store != nil {
			doc, loadErr := s.authorities.store.LoadContext(contextOrBackground(ctx))
			if loadErr != nil {
				return resolvedSessionModelProfile{}, false, loadErr
			}
			canonical := newPlacementSnapshot(doc)
			if validateErr := controlplacement.ValidateSnapshot(canonical.placement); validateErr != nil {
				return resolvedSessionModelProfile{}, false, validateErr
			}
			if candidate, found := modelprofile.Lookup(canonical.placement.Profiles, selector); found {
				profile = candidate
				ok = true
				snapshot = canonical
			}
		}
	}
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
