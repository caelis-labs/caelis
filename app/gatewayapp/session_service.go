package gatewayapp

import (
	"context"
	"fmt"
	"strings"
	"time"

	sdkruntime "github.com/caelis-labs/caelis/agent-sdk/runtime"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/spawn"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
	"github.com/caelis-labs/caelis/ports/gateway"
)

func (s *Stack) StartSession(ctx context.Context, preferredSessionID string, bindingKey string) (session.Session, error) {
	if s == nil {
		return session.Session{}, fmt.Errorf("gatewayapp: stack is unavailable")
	}
	gw := s.KernelSessions()
	if gw == nil {
		return session.Session{}, fmt.Errorf("gatewayapp: gateway is unavailable")
	}
	return gw.StartSession(ctx, gateway.StartSessionRequest{
		AppName:            s.AppName,
		UserID:             s.UserID,
		Workspace:          s.Workspace,
		PreferredSessionID: strings.TrimSpace(preferredSessionID),
		BindingKey:         strings.TrimSpace(bindingKey),
		Binding: gateway.BindingDescriptor{
			Surface: strings.TrimSpace(bindingKey),
			Owner:   s.AppName,
		},
	})
}

func (s *Stack) StartSubagent(
	ctx context.Context,
	ref session.SessionRef,
	agent string,
	prompt string,
	source string,
) (task.Snapshot, error) {
	return s.StartSubagentWithOptions(ctx, ref, agent, prompt, source, StartSubagentOptions{})
}

func (s *Stack) StartSubagentWithOptions(
	ctx context.Context,
	ref session.SessionRef,
	agent string,
	prompt string,
	source string,
	opts StartSubagentOptions,
) (task.Snapshot, error) {
	var snapshot task.Snapshot
	err := s.withPlaced(ctx, ref, func(runCtx context.Context, engine *sdkruntime.Runtime) error {
		var err error
		snapshot, err = engine.StartSubagentWithOptions(runCtx, ref, agent, prompt, source, sdkruntime.StartSubagentOptions{
			ApprovalRequester: opts.ApprovalRequester,
			ApprovalMode:      opts.ApprovalMode,
		})
		return err
	})
	return snapshot, err
}

func (s *Stack) ContinueSubagentByHandle(
	ctx context.Context,
	ref session.SessionRef,
	handle string,
	prompt string,
	yield time.Duration,
) (task.Snapshot, error) {
	var snapshot task.Snapshot
	err := s.withPlaced(ctx, ref, func(runCtx context.Context, engine *sdkruntime.Runtime) error {
		var err error
		snapshot, err = engine.ContinueSubagentByHandle(runCtx, ref, handle, prompt, yield)
		return err
	})
	return snapshot, err
}

func (s *Stack) WaitSubagentTask(
	ctx context.Context,
	ref session.SessionRef,
	taskID string,
	yield time.Duration,
) (task.Snapshot, error) {
	var snapshot task.Snapshot
	err := s.withPlaced(ctx, ref, func(runCtx context.Context, engine *sdkruntime.Runtime) error {
		var err error
		snapshot, err = engine.WaitSubagentTask(runCtx, ref, taskID, yield)
		return err
	})
	return snapshot, err
}

// CompactSession forces a model-backed checkpoint compaction for the given
// session.
func (s *Stack) CompactSession(ctx context.Context, ref session.SessionRef) error {
	if s == nil {
		return fmt.Errorf("gatewayapp: stack is unavailable")
	}
	s.mu.RLock()
	gw := s.gateway
	s.mu.RUnlock()
	if gw == nil || gw.Resolver() == nil {
		return fmt.Errorf("gatewayapp: resolver is unavailable")
	}
	resolved, err := gw.Resolver().ResolveTurn(ctx, gateway.TurnIntent{SessionRef: ref})
	if err != nil {
		return err
	}
	return s.withPlaced(ctx, ref, func(runCtx context.Context, engine *sdkruntime.Runtime) error {
		_, compactErr := engine.Compact(runCtx, sdkruntime.CompactRequest{
			SessionRef: ref,
			Model:      resolved.RunRequest.AgentSpec.Model,
			Trigger:    "manual",
		})
		return compactErr
	})
}

// withPlaced runs a synchronous Control operation inside the production
// placement envelope (leased heartbeat, cancel-on-loss).
func (s *Stack) withPlaced(ctx context.Context, ref session.SessionRef, fn func(context.Context, *sdkruntime.Runtime) error) error {
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

func (s *Stack) SessionUsageSnapshot(ctx context.Context, ref session.SessionRef, modelAlias string) (compact.UsageSnapshot, error) {
	if s == nil || s.Sessions == nil {
		return compact.UsageSnapshot{}, fmt.Errorf("gatewayapp: sessions service unavailable")
	}
	if strings.TrimSpace(ref.SessionID) == "" {
		return compact.UsageSnapshot{}, nil
	}
	events, err := s.Sessions.Events(ctx, session.EventsRequest{SessionRef: ref})
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
	return sdkruntime.ComputeUsageSnapshot(events, nil, contextWindow, cfg), nil
}

func (s *Stack) estimatedPromptPrefixTokens(ctx context.Context, ref session.SessionRef) int {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	runtimeCfg := s.runtime
	runtimeCfg.Assembly = assembly.CloneResolvedAssembly(runtimeCfg.Assembly)
	runtimeCfg.BaseMetadata = cloneMap(runtimeCfg.BaseMetadata)
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

func (s *Stack) currentContextWindowTokensForAlias(alias string) int {
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
	if s != nil && s.runtime.ContextWindow > 0 {
		return s.runtime.ContextWindow
	}
	return 0
}
