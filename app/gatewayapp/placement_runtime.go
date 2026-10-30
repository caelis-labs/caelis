package gatewayapp

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"sync"

	sdkplacement "github.com/caelis-labs/caelis/agent-sdk/placement"
	"github.com/caelis-labs/caelis/control/agentbinding"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/control/modelconfig"
	controlplacement "github.com/caelis-labs/caelis/control/placement"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
	kernelimpl "github.com/caelis-labs/caelis/internal/kernel"
)

type placementSnapshot struct {
	placement controlplacement.Snapshot
	revision  uint64
	lookup    *modelLookup
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
	return &placementSnapshot{revision: doc.ConfigurationRevision, placement: controlplacement.Snapshot{
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

// beginPinnedModelSelection stages only the Session main model and credentials.
// Team placement remains canonical and must never inherit a Session's pin or
// be rolled back by a conflicting Session revision CAS.
func (s *runtimeComposition) beginPinnedModelSelection(ctx context.Context, configured ModelConfig) (func(bool), error) {
	if s == nil || s.lookup == nil {
		return nil, fmt.Errorf("gatewayapp: Session Runtime model lookup is unavailable")
	}
	if s.activation == nil || s.activation.modelCatalog == nil {
		return nil, fmt.Errorf("gatewayapp: Host model catalog is unavailable")
	}
	finishCredential, err := s.lookup.beginPinAPIKeyCredential(ctx, configured, s.activation.modelCatalog)
	if err != nil {
		return nil, fmt.Errorf("gatewayapp: pin Session Runtime model credential: %w", err)
	}
	_, finishLookup, err := s.lookup.beginPinnedUpsert(configured)
	if err != nil {
		finishCredential(false)
		return nil, err
	}

	var once sync.Once
	return func(committed bool) {
		once.Do(func() {
			finishLookup(committed)
			finishCredential(committed)
		})
	}, nil
}

// beginPinnedACPSelection stages an independently selected main controller in
// the Session execution registry. Team configuration has no Session-local writer.
func (s *runtimeComposition) beginPinnedACPSelection(ctx context.Context, frozen sdkplacement.Placement) (func(bool), error) {
	if s == nil || s.authorities.store == nil {
		return nil, fmt.Errorf("gatewayapp: Session Runtime ACP selection is unavailable")
	}
	s.acpSelectionMu.Lock()
	unlockOnError := true
	defer func() {
		if unlockOnError {
			s.acpSelectionMu.Unlock()
		}
	}()
	frozen = sdkplacement.Normalize(frozen)
	if err := sdkplacement.ValidateSealed(frozen); err != nil {
		return nil, fmt.Errorf("gatewayapp: validate selected ACP placement: %w", err)
	}
	doc, err := s.authorities.store.LoadContext(contextOrBackground(ctx))
	if err != nil {
		return nil, err
	}
	canonical := newPlacementSnapshot(doc)
	if err := controlplacement.ValidateSnapshot(canonical.placement); err != nil {
		return nil, err
	}
	if err := controlplacement.ValidateFrozen(canonical.placement, frozen); err != nil {
		return nil, fmt.Errorf("gatewayapp: selected ACP placement changed: %w", err)
	}
	agent, connection, err := controlagents.ResolveAgent(canonical.placement.Agents, frozen.Agent)
	if err != nil {
		return nil, err
	}
	materialized, err := s.materializeExternalAgent(agent, connection)
	if err != nil {
		return nil, err
	}
	materialized.SessionOptions = controlagents.SessionOptions{
		ModelID:                 strings.TrimSpace(frozen.Model),
		ConfigValues:            maps.Clone(frozen.SessionConfigValues),
		ReasoningEffortConfigID: strings.TrimSpace(frozen.ReasoningEffortConfigID),
	}

	s.mu.Lock()
	beforeAssembly := assembly.CloneResolvedAssembly(s.activeRuntime.Assembly)
	nextAssembly := assembly.CloneResolvedAssembly(beforeAssembly)
	nextAssembly.Agents = upsertRuntimeACPAgent(nextAssembly.Agents, materialized)
	if s.acpControlPlane == nil {
		s.mu.Unlock()
		return nil, fmt.Errorf("gatewayapp: ACP control plane is unavailable")
	}
	if err := s.acpControlPlane.UpdateAgents(nextAssembly.Agents); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	s.activeRuntime.Assembly = nextAssembly
	s.mu.Unlock()

	var once sync.Once
	unlockOnError = false
	return func(committed bool) {
		once.Do(func() {
			if !committed {
				s.mu.Lock()
				if s.acpControlPlane != nil {
					_ = s.acpControlPlane.UpdateAgents(beforeAssembly.Agents)
				}
				s.activeRuntime.Assembly = beforeAssembly
				s.mu.Unlock()
			}
			s.acpSelectionMu.Unlock()
		})
	}, nil
}

func upsertRuntimeACPAgent(current []assembly.AgentConfig, configured assembly.AgentConfig) []assembly.AgentConfig {
	next := make([]assembly.AgentConfig, 0, len(current)+1)
	replaced := false
	for _, existing := range current {
		if strings.EqualFold(strings.TrimSpace(existing.Name), strings.TrimSpace(configured.Name)) {
			if !replaced {
				next = append(next, configured)
				replaced = true
			}
			continue
		}
		next = append(next, existing)
	}
	if !replaced {
		next = append(next, configured)
	}
	return next
}

func (s *runtimeComposition) placementSnapshot(ctx context.Context) (*placementSnapshot, error) {
	if captured, ok := contextOrBackground(ctx).Value(teamSnapshotKey{}).(capturedTeamSnapshot); ok && captured.owner == s {
		return captured.snapshot, nil
	}

	if s == nil || s.authorities.store == nil {
		return nil, fmt.Errorf("gatewayapp: placement is unavailable")
	}
	ctx = contextOrBackground(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for {
		doc, err := s.authorities.store.LoadContext(ctx)
		if err != nil {
			return nil, err
		}
		s.placementCacheMu.RLock()
		cached := s.placementCache
		generation := s.placementCacheGeneration
		s.placementCacheMu.RUnlock()
		if cached != nil && cached.revision == doc.ConfigurationRevision && cached.lookup != nil {
			return cached, nil
		}
		loaded := newPlacementSnapshot(doc)
		catalog := s.lookup
		if s.activation != nil && s.activation.modelCatalog != nil {
			catalog = s.activation.modelCatalog
		}
		if catalog == nil {
			loaded.lookup, err = newModelLookupFromDocument(doc, s.activeRuntime.ContextWindow)
		} else {
			loaded.lookup, err = cloneSessionModelLookup(catalog, doc)
		}
		if err != nil {
			return nil, err
		}
		if err := controlplacement.ValidateSnapshot(loaded.placement); err != nil {
			return nil, err
		}
		s.placementCacheMu.Lock()
		if s.placementCacheGeneration != generation {
			s.placementCacheMu.Unlock()
			continue
		}
		s.placementCache = loaded
		cached = loaded
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

func (s *runtimeComposition) resolveModelProfilePlacement(ctx context.Context, profileID, effort string) (sdkplacement.Placement, error) {
	snapshot, err := s.placementSnapshot(ctx)
	if err != nil {
		return sdkplacement.Placement{}, err
	}
	return controlplacement.ResolveProfile(snapshot.placement, profileID, effort)
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
	snapshot, err := s.placementSnapshot(ctx)
	if err != nil {
		return kernelimpl.ModelResolution{}, false, err
	}
	placement, err := controlplacement.ResolveHandle(snapshot.placement, controlplacement.HandleRequest{Handle: handle, Purpose: purpose})
	if err != nil {
		return kernelimpl.ModelResolution{}, false, err
	}
	hydrated, err := snapshot.lookup.ResolveConfig(placement.Model)
	if err != nil {
		return kernelimpl.ModelResolution{}, false, err
	}
	if placement.ReasoningEffort != "" {
		hydrated.ReasoningEffort = placement.ReasoningEffort
	}
	resolved, err := snapshot.lookup.ResolveModelConfig(ctx, hydrated, contextWindow)
	if err != nil {
		return kernelimpl.ModelResolution{}, false, err
	}
	if placement.ServiceTier != "" {
		resolved.FastMode = placement.ServiceTier == "priority"
	}
	return resolved, true, nil
}

// A call-local immutable projection, never another configuration authority.
// The canonical document and its existing revision remain the only publisher.
type teamSnapshotKey struct{}
type capturedTeamSnapshot struct {
	owner    *runtimeComposition
	snapshot *placementSnapshot
}

func (s *runtimeComposition) captureTeamSnapshot(ctx context.Context) (context.Context, error) {
	snapshot, err := s.placementSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	return context.WithValue(contextOrBackground(ctx), teamSnapshotKey{}, capturedTeamSnapshot{owner: s, snapshot: snapshot}), nil
}
