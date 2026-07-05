package gatewayapp

import (
	"context"
	"fmt"
	"strings"
	"time"

	sdkruntime "github.com/caelis-labs/caelis/agent-sdk/runtime"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/assembly"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/spawn"
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
	if s == nil {
		return task.Snapshot{}, fmt.Errorf("gatewayapp: stack is unavailable")
	}
	s.mu.RLock()
	engine := s.engine
	s.mu.RUnlock()
	if engine == nil {
		return task.Snapshot{}, fmt.Errorf("gatewayapp: runtime is unavailable")
	}
	return engine.StartSubagentWithOptions(ctx, ref, agent, prompt, source, sdkruntime.StartSubagentOptions{
		ApprovalRequester: opts.ApprovalRequester,
		ApprovalMode:      opts.ApprovalMode,
	})
}

func (s *Stack) ContinueSubagentByHandle(
	ctx context.Context,
	ref session.SessionRef,
	handle string,
	prompt string,
	yield time.Duration,
) (task.Snapshot, error) {
	if s == nil {
		return task.Snapshot{}, fmt.Errorf("gatewayapp: stack is unavailable")
	}
	s.mu.RLock()
	engine := s.engine
	s.mu.RUnlock()
	if engine == nil {
		return task.Snapshot{}, fmt.Errorf("gatewayapp: runtime is unavailable")
	}
	return engine.ContinueSubagentByHandle(ctx, ref, handle, prompt, yield)
}

func (s *Stack) WaitSubagentTask(
	ctx context.Context,
	ref session.SessionRef,
	taskID string,
	yield time.Duration,
) (task.Snapshot, error) {
	if s == nil {
		return task.Snapshot{}, fmt.Errorf("gatewayapp: stack is unavailable")
	}
	s.mu.RLock()
	engine := s.engine
	s.mu.RUnlock()
	if engine == nil {
		return task.Snapshot{}, fmt.Errorf("gatewayapp: runtime is unavailable")
	}
	return engine.WaitSubagentTask(ctx, ref, taskID, yield)
}

// CompactSession forces a model-backed checkpoint compaction for the given
// session.
func (s *Stack) CompactSession(ctx context.Context, ref session.SessionRef) error {
	if s == nil {
		return fmt.Errorf("gatewayapp: stack is unavailable")
	}
	s.mu.RLock()
	engine := s.engine
	gw := s.gateway
	s.mu.RUnlock()
	if engine == nil {
		return fmt.Errorf("gatewayapp: runtime is unavailable")
	}
	if gw == nil || gw.Resolver() == nil {
		return fmt.Errorf("gatewayapp: resolver is unavailable")
	}
	resolved, err := gw.Resolver().ResolveTurn(ctx, gateway.TurnIntent{SessionRef: ref})
	if err != nil {
		return err
	}
	_, err = engine.Compact(ctx, sdkruntime.CompactRequest{
		SessionRef: ref,
		Model:      resolved.RunRequest.AgentSpec.Model,
		Trigger:    "manual",
	})
	return err
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

	var participants []session.ParticipantBinding
	if s.Sessions != nil && strings.TrimSpace(ref.SessionID) != "" {
		if session, err := s.Sessions.Session(ctx, ref); err == nil {
			participants = session.Participants
		}
	}
	agents := delegationAgentsForSpawn(runtimeCfg.Assembly, participants)
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
