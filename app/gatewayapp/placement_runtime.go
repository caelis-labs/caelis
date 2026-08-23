package gatewayapp

import (
	"context"
	"fmt"
	"sync"

	sdkplacement "github.com/caelis-labs/caelis/agent-sdk/placement"
	"github.com/caelis-labs/caelis/control/agentbinding"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/control/modelprofile"
	modelprofilebuilder "github.com/caelis-labs/caelis/control/modelprofile/builder"
	controlplacement "github.com/caelis-labs/caelis/control/placement"
	kernelimpl "github.com/caelis-labs/caelis/internal/kernel"
)

type placementSnapshot struct {
	placement controlplacement.Snapshot
}

func newPlacementSnapshot(doc AppConfig) *placementSnapshot {
	endpoints := make(map[string]modelconfig.ProviderEndpointConfig, len(doc.Models.ProviderEndpoints))
	for _, raw := range doc.Models.ProviderEndpoints {
		endpoint := modelconfig.NormalizeProviderEndpoint(raw)
		if endpoint.ID != "" {
			endpoints[endpoint.ID] = endpoint
		}
	}
	models := make([]modelconfig.Config, 0, len(doc.Models.Configs))
	for _, raw := range doc.Models.Configs {
		configured := modelconfig.NormalizeConfig(raw)
		if endpoint, ok := endpoints[configured.ProviderEndpointID]; ok {
			configured = modelconfig.MergeConfigProviderEndpoint(configured, endpoint)
		}
		models = append(models, configured)
	}
	return &placementSnapshot{placement: controlplacement.Snapshot{
		Profiles: doc.ModelProfiles,
		Bindings: doc.AgentBindings,
		Models:   models,
		Agents:   controlagents.NormalizeConfiguration(doc.ExternalAgents),
	}}
}

func (s *runtimeComposition) invalidateOwnPlacementSnapshot() {
	if s == nil {
		return
	}
	s.placementCacheMu.Lock()
	s.placementCache = nil
	s.placementCacheGeneration++
	s.placementCacheMu.Unlock()
}

// beginPinnedModelSelection stages one provider model, its product profile,
// and every model changed through the same shared provider endpoint as a
// single detached Runtime update. Both views remain unreadable until the
// matching durable Session revision CAS commits; rollback restores both.
func (s *runtimeComposition) beginPinnedModelSelection(ctx context.Context, configured ModelConfig) (func(bool), error) {
	if s == nil || s.lookup == nil {
		return nil, fmt.Errorf("gatewayapp: Session Runtime model lookup is unavailable")
	}
	profile, err := modelprofilebuilder.FromProvider(configured)
	if err != nil {
		return nil, fmt.Errorf("gatewayapp: build pinned Session model profile: %w", err)
	}
	if s.activation == nil || s.activation.modelCatalog == nil {
		return nil, fmt.Errorf("gatewayapp: Host model catalog is unavailable")
	}
	finishCredential, err := s.lookup.beginPinAPIKeyCredential(ctx, configured, s.activation.modelCatalog)
	if err != nil {
		return nil, fmt.Errorf("gatewayapp: pin Session Runtime model credential: %w", err)
	}
	affectedModels, finishLookup, err := s.lookup.beginPinnedUpsert(configured)
	if err != nil {
		finishCredential(false)
		return nil, err
	}

	s.placementCacheMu.Lock()
	before := s.placementCache
	if before == nil {
		s.placementCacheMu.Unlock()
		finishLookup(false)
		finishCredential(false)
		return nil, fmt.Errorf("gatewayapp: Session Runtime placement snapshot is unavailable")
	}
	next := &placementSnapshot{placement: before.placement}
	next.placement.Profiles, err = modelprofile.Upsert(next.placement.Profiles, profile)
	if err == nil {
		for _, affected := range affectedModels {
			next.placement.Models = upsertPlacementModel(next.placement.Models, affected)
		}
		err = controlplacement.ValidateSnapshot(next.placement)
	}
	if err != nil {
		s.placementCacheMu.Unlock()
		finishLookup(false)
		finishCredential(false)
		return nil, fmt.Errorf("gatewayapp: pin Session Runtime placement: %w", err)
	}
	s.placementCache = next

	var once sync.Once
	return func(committed bool) {
		once.Do(func() {
			if !committed {
				s.placementCache = before
			}
			finishLookup(committed)
			finishCredential(committed)
			s.placementCacheMu.Unlock()
		})
	}, nil
}

func upsertPlacementModel(current []modelconfig.Config, configured modelconfig.Config) []modelconfig.Config {
	configured = placementModelConfig(configured)
	next := make([]modelconfig.Config, 0, len(current)+1)
	replaced := false
	for _, raw := range current {
		model := modelconfig.NormalizeConfig(raw)
		if model.ID == configured.ID {
			if !replaced {
				next = append(next, configured)
				replaced = true
			}
			continue
		}
		next = append(next, model)
	}
	if !replaced {
		next = append(next, configured)
	}
	return next
}

func placementModelConfig(configured modelconfig.Config) modelconfig.Config {
	configured = modelconfig.NormalizeConfig(configured)
	// Placement needs stable execution semantics for validation and sealing,
	// not the Runtime-only credential material retained by model lookup.
	configured.HTTPClient = nil
	configured.Token = ""
	configured.CredentialPath = ""
	return configured
}

func (s *runtimeComposition) placementSnapshot(ctx context.Context) (*placementSnapshot, error) {
	if s == nil || s.authorities.store == nil {
		return nil, fmt.Errorf("gatewayapp: placement is unavailable")
	}
	ctx = contextOrBackground(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for {
		s.placementCacheMu.RLock()
		cached := s.placementCache
		generation := s.placementCacheGeneration
		s.placementCacheMu.RUnlock()
		if cached != nil {
			return cached, nil
		}
		doc, err := s.authorities.store.Load()
		if err != nil {
			return nil, err
		}
		loaded := newPlacementSnapshot(doc)
		if err := controlplacement.ValidateSnapshot(loaded.placement); err != nil {
			return nil, err
		}
		s.placementCacheMu.Lock()
		if s.placementCacheGeneration != generation {
			s.placementCacheMu.Unlock()
			continue
		}
		if s.placementCache == nil {
			s.placementCache = loaded
		}
		cached = s.placementCache
		s.placementCacheMu.Unlock()
		return cached, nil
	}
}

func (s *runtimeComposition) resolveHandlePlacement(ctx context.Context, req controlplacement.HandleRequest) (sdkplacement.Placement, error) {
	snapshot, err := s.placementSnapshot(ctx)
	if err != nil {
		return sdkplacement.Placement{}, err
	}
	return controlplacement.ResolveHandle(snapshot.placement, req)
}

func (s *runtimeComposition) resolveParticipantPlacement(ctx context.Context, profileID, effort string) (sdkplacement.Placement, error) {
	snapshot, err := s.placementSnapshot(ctx)
	if err != nil {
		return sdkplacement.Placement{}, err
	}
	return controlplacement.ResolveParticipant(snapshot.placement, profileID, effort)
}

func (s *runtimeComposition) resolveSystemAgentModel(
	ctx context.Context,
	handle agentbinding.Handle,
	contextWindow int,
) (kernelimpl.ModelResolution, bool, error) {
	if s == nil || s.authorities.store == nil {
		return kernelimpl.ModelResolution{}, false, nil
	}
	if s.lookup == nil {
		return kernelimpl.ModelResolution{}, false, fmt.Errorf("gatewayapp: resolve system Agent model: model lookup unavailable")
	}
	purpose, err := controlplacement.PurposeForFixedHandle(handle)
	if err != nil {
		return kernelimpl.ModelResolution{}, false, err
	}
	placement, err := s.resolveHandlePlacement(ctx, controlplacement.HandleRequest{Handle: handle, Purpose: purpose})
	if err != nil {
		return kernelimpl.ModelResolution{}, false, err
	}
	hydrated, err := s.lookup.ResolveConfig(placement.Model)
	if err != nil {
		return kernelimpl.ModelResolution{}, false, err
	}
	if placement.ReasoningEffort != "" {
		hydrated.ReasoningEffort = placement.ReasoningEffort
	}
	resolved, err := s.lookup.ResolveModelConfig(ctx, hydrated, contextWindow)
	if err != nil {
		return kernelimpl.ModelResolution{}, false, err
	}
	return resolved, true, nil
}
