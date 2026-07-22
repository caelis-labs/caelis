package gatewayapp

import (
	"context"
	"fmt"

	sdkplacement "github.com/caelis-labs/caelis/agent-sdk/placement"
	"github.com/caelis-labs/caelis/control/agentbinding"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/control/modelconfig"
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

func (s *Stack) invalidatePlacementSnapshot() {
	if s == nil {
		return
	}
	s.placementCacheMu.Lock()
	s.placementCache = nil
	s.placementCacheGeneration++
	s.placementCacheMu.Unlock()
}

func (s *Stack) placementSnapshot(ctx context.Context) (*placementSnapshot, error) {
	if s == nil || s.store == nil {
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
		doc, err := s.store.Load()
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

func (s *Stack) resolveHandlePlacement(ctx context.Context, req controlplacement.HandleRequest) (sdkplacement.Placement, error) {
	snapshot, err := s.placementSnapshot(ctx)
	if err != nil {
		return sdkplacement.Placement{}, err
	}
	return controlplacement.ResolveHandle(snapshot.placement, req)
}

func (s *Stack) resolveParticipantPlacement(ctx context.Context, profileID, effort string) (sdkplacement.Placement, error) {
	snapshot, err := s.placementSnapshot(ctx)
	if err != nil {
		return sdkplacement.Placement{}, err
	}
	return controlplacement.ResolveParticipant(snapshot.placement, profileID, effort)
}

// ResolveHandlePlacement is the narrow adapter facet used by the transitional
// in-process control adapter.
func (s *Stack) ResolveHandlePlacement(ctx context.Context, handle agentbinding.Handle) (sdkplacement.Placement, error) {
	handle = agentbinding.NormalizeHandle(handle)
	purpose, err := controlplacement.PurposeForHandle(handle)
	if err != nil {
		return sdkplacement.Placement{}, err
	}
	return s.resolveHandlePlacement(ctx, controlplacement.HandleRequest{Handle: handle, Purpose: purpose})
}

func (s *Stack) resolveSystemAgentModel(
	ctx context.Context,
	handle agentbinding.Handle,
	contextWindow int,
) (kernelimpl.ModelResolution, bool, error) {
	if s == nil || s.store == nil {
		return kernelimpl.ModelResolution{}, false, nil
	}
	if s.lookup == nil {
		return kernelimpl.ModelResolution{}, false, fmt.Errorf("gatewayapp: resolve system Agent model: model lookup unavailable")
	}
	purpose, err := controlplacement.PurposeForHandle(handle)
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
