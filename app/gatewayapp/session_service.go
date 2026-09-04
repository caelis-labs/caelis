package gatewayapp

import (
	"context"
	"fmt"
	"strings"

	sdkruntime "github.com/caelis-labs/caelis/agent-sdk/runtime"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/spawn"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
	"github.com/caelis-labs/caelis/internal/kernel"
)

// compactSession forces a model-backed checkpoint compaction for the given
// session.
func (s *runtimeComposition) compactSession(ctx context.Context, ref session.SessionRef, expectedRevision *uint64) (session.Session, error) {
	if s == nil {
		return session.Session{}, fmt.Errorf("gatewayapp: stack is unavailable")
	}
	s.mu.RLock()
	gw := s.gateway
	s.mu.RUnlock()
	if gw == nil || gw.Resolver() == nil {
		return session.Session{}, fmt.Errorf("gatewayapp: resolver is unavailable")
	}
	resolved, err := gw.Resolver().ResolveTurn(ctx, kernel.TurnIntent{SessionRef: ref})
	if err != nil {
		return session.Session{}, err
	}
	var compacted sdkruntime.CompactResult
	err = s.withPlaced(ctx, ref, func(runCtx context.Context, engine *sdkruntime.Runtime) error {
		var compactErr error
		compacted, compactErr = engine.Compact(runCtx, sdkruntime.CompactRequest{
			SessionRef:       ref,
			ExpectedRevision: expectedRevision,
			Model:            resolved.RunRequest.AgentSpec.Model,
			ServiceTier:      resolved.RunRequest.AgentSpec.Request.ServiceTier,
			Trigger:          "manual",
		})
		return compactErr
	})
	return compacted.Session, err
}

// withPlaced runs a synchronous Control operation inside the production
// placement envelope (durable execution fence, cancel-on-loss).
func (s *runtimeComposition) withPlaced(ctx context.Context, ref session.SessionRef, fn func(context.Context, *sdkruntime.Runtime) error) error {
	if s == nil {
		return fmt.Errorf("gatewayapp: stack is unavailable")
	}
	if fn == nil {
		return fmt.Errorf("gatewayapp: placed operation is required")
	}
	s.mu.RLock()
	engine := s.engine
	placement := s.placement
	s.mu.RUnlock()
	if engine == nil {
		return fmt.Errorf("gatewayapp: runtime is unavailable")
	}
	if placement == nil {
		return fmt.Errorf("gatewayapp: placement runtime is unavailable")
	}
	return placement.ExecutePlaced(ctx, ref, func(runCtx context.Context) error {
		return fn(runCtx, engine)
	})
}

func defaultCompactionConfig(contextWindow int) sdkruntime.CompactionConfig {
	return sdkruntime.CompactionConfig{
		Enabled:                    true,
		DefaultContextWindowTokens: contextWindow,
	}
}

func (s *runtimeComposition) SessionUsageSnapshot(ctx context.Context, ref session.SessionRef, modelAlias string) (compact.UsageSnapshot, error) {
	if s == nil || s.sessions == nil {
		return compact.UsageSnapshot{}, fmt.Errorf("gatewayapp: sessions service unavailable")
	}
	if strings.TrimSpace(ref.SessionID) == "" {
		return compact.UsageSnapshot{}, nil
	}
	events, err := s.sessions.Events(ctx, session.EventsRequest{SessionRef: ref})
	if err != nil {
		return compact.UsageSnapshot{}, err
	}
	alias := strings.TrimSpace(modelAlias)
	if alias == "" && s.lookup != nil {
		alias = strings.TrimSpace(s.lookup.DefaultAlias())
	}
	contextWindow := s.currentContextWindowTokensForAlias(alias)
	cfg := defaultCompactionConfig(contextWindow)
	cfg.EstimatedPromptPrefixTokens = s.estimatedPromptPrefixTokens(ctx, ref)
	modelCfg, _ := s.modelConfigForAlias(alias)
	return sdkruntime.ComputeUsageSnapshotForModel(
		events,
		nil,
		contextWindow,
		cfg,
		modelCfg.Provider,
		modelCfg.Model,
	), nil
}

func (s *runtimeComposition) estimatedPromptPrefixTokens(ctx context.Context, ref session.SessionRef) int {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	runtimeCfg := s.activeRuntime
	runtimeCfg.Assembly = assembly.CloneResolvedAssembly(runtimeCfg.Assembly)
	runtimeCfg.BaseMetadata = cloneMap(runtimeCfg.BaseMetadata)
	// This is an initial/static status estimate only. The Runtime model gate and
	// durable usage snapshots derive the authoritative prefix from each actual
	// model request, including the currently admitted deferred-tool projection.
	base := runtimeCfg.EstimatedPromptPrefixTokens
	s.mu.RUnlock()
	if base < 0 {
		base = 0
	}

	agents := s.delegationAgentsForSpawn()
	if len(agents) == 0 {
		return base
	}

	extra := 0
	baseSystemPrompt := stringFromMap(runtimeCfg.BaseMetadata, "system_prompt")
	withDelegation := systemPromptWithDelegationGuidance(baseSystemPrompt)
	if delta := estimatePromptTextTokens(withDelegation) - estimatePromptTextTokens(baseSystemPrompt); delta > 0 {
		extra += delta
	}
	extra += estimateToolPromptTokens(spawnTools(agents))
	return base + extra
}

func spawnTools(agents []delegation.Agent) []tool.Tool {
	if len(agents) == 0 {
		return nil
	}
	return []tool.Tool{spawn.New(agents)}
}

func (s *runtimeComposition) currentContextWindowTokensForAlias(alias string) int {
	alias = strings.TrimSpace(alias)
	if alias != "" {
		if cfg, ok := s.modelConfigForAlias(alias); ok && cfg.ContextWindowTokens > 0 {
			return cfg.ContextWindowTokens
		}
	}
	if s != nil && s.lookup != nil {
		s.lookup.mu.RLock()
		defer s.lookup.mu.RUnlock()
		if s.lookup.contextWindow > 0 {
			return s.lookup.contextWindow
		}
	}
	if s != nil {
		if contextWindow := s.runtimeProcessSnapshot().runtime.ContextWindow; contextWindow > 0 {
			return contextWindow
		}
	}
	return 0
}
