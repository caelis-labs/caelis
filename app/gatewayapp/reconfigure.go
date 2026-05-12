package gatewayapp

import (
	"context"
	"fmt"
	"strings"

	"github.com/OnslaughtSnail/caelis/impl/agent/local"
	"github.com/OnslaughtSnail/caelis/impl/agent/local/chat"
	"github.com/OnslaughtSnail/caelis/impl/approval/agentreview"
	"github.com/OnslaughtSnail/caelis/impl/policy/presets"
	"github.com/OnslaughtSnail/caelis/impl/tool/builtin"
	"github.com/OnslaughtSnail/caelis/kernel"
	"github.com/OnslaughtSnail/caelis/ports/sandbox"
	"github.com/OnslaughtSnail/caelis/ports/session"
)

func (s *Stack) saveModelConfigs() error {
	if s == nil || s.store == nil || s.lookup == nil {
		return nil
	}
	doc, err := s.store.Load()
	if err != nil {
		return err
	}
	doc.Models = s.lookup.Snapshot()
	return s.store.Save(doc)
}

func (s *Stack) saveSandboxConfig() error {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	cfg := s.sandbox
	s.mu.RUnlock()
	return s.saveSandboxConfigValue(cfg)
}

func (s *Stack) saveSandboxConfigValue(cfg SandboxConfig) error {
	if s == nil || s.store == nil {
		return nil
	}
	doc, err := s.store.Load()
	if err != nil {
		return err
	}
	doc.Sandbox = cfg
	return s.store.Save(doc)
}

func (s *Stack) rebuildGateway() error {
	if s == nil {
		return fmt.Errorf("gatewayapp: stack is unavailable")
	}
	s.mu.RLock()
	oldGateway := s.Gateway
	sandboxCfg := effectiveSandboxConfig(s.sandbox, s.Workspace.CWD)
	runtimeCfg := s.runtime
	s.mu.RUnlock()
	if err := rejectReconfigureWithActiveTurns(oldGateway, "rebuild gateway"); err != nil {
		return err
	}
	sandboxRuntime, err := sandbox.New(sandbox.Config{
		CWD:              s.Workspace.CWD,
		RequestedBackend: sandbox.Backend(sandboxCfg.RequestedType),
		HelperPath:       sandboxCfg.HelperPath,
		ReadableRoots:    append([]string(nil), sandboxCfg.ReadableRoots...),
		WritableRoots:    append([]string(nil), sandboxCfg.WritableRoots...),
		ReadOnlySubpaths: append([]string(nil), sandboxCfg.ReadOnlySubpaths...),
	})
	if err != nil {
		return err
	}
	effectivePolicyMode := policyMode(runtimeCfg.PermissionMode)
	effectiveBaseMetadata := cloneMap(runtimeCfg.BaseMetadata)
	sandboxStatus := sandboxRuntime.Status()
	if sandboxStatus.FallbackToHost {
		if effectiveBaseMetadata == nil {
			effectiveBaseMetadata = map[string]any{}
		}
		effectiveBaseMetadata["sandbox_auto_review_disabled"] = true
		if hint := strings.TrimSpace(sandboxStatus.FallbackInstallHint); hint != "" {
			effectiveBaseMetadata["sandbox_install_hint"] = hint
		}
		if reason := strings.TrimSpace(sandboxStatus.FallbackReason); reason != "" {
			effectiveBaseMetadata["sandbox_fallback_reason"] = reason
		}
		if effectivePolicyMode == presets.ModeAutoReview {
			effectivePolicyMode = presets.ModeManual
			effectiveBaseMetadata["policy_mode"] = presets.ModeManual
		}
	}
	tools, err := builtin.BuildCoreTools(builtin.CoreToolsConfig{Runtime: sandboxRuntime})
	if err != nil {
		_ = sandboxRuntime.Close()
		return err
	}
	estimatedPrefixTokens := estimateModelPromptPrefixTokens(effectiveBaseMetadata, tools)
	compactionCfg := defaultCompactionConfig(runtimeCfg.ContextWindow)
	compactionCfg.EstimatedPromptPrefixTokens = estimatedPrefixTokens
	rt, err := local.New(local.Config{
		Sessions:          s.Sessions,
		AgentFactory:      chat.Factory{},
		DefaultPolicyMode: effectivePolicyMode,
		Compaction:        compactionCfg,
		Assembly:          runtimeCfg.Assembly,
		TaskStore:         s.taskStore,
	})
	if err != nil {
		_ = sandboxRuntime.Close()
		return err
	}
	resolver, err := kernel.NewAssemblyResolver(kernel.AssemblyResolverConfig{
		Sessions:          s.Sessions,
		Assembly:          runtimeCfg.Assembly,
		DefaultModelAlias: s.lookup.DefaultID(),
		ContextWindow:     runtimeCfg.ContextWindow,
		ModelLookup:       s.lookup,
		Tools:             tools,
		BaseMetadata:      cloneMap(effectiveBaseMetadata),
		ToolAugmenter: func(ctx context.Context, req kernel.ToolAugmentContext) (kernel.ToolAugmentation, error) {
			s.mu.RLock()
			runtimeCfg := s.runtime
			s.mu.RUnlock()
			var participants []session.ParticipantBinding
			if strings.TrimSpace(req.SessionRef.SessionID) != "" {
				session, err := s.Sessions.Session(ctx, req.SessionRef)
				if err != nil {
					return kernel.ToolAugmentation{}, err
				}
				participants = session.Participants
			}
			agents := delegationAgentsForSpawn(runtimeCfg.Assembly, participants)
			if len(agents) == 0 {
				return kernel.ToolAugmentation{}, nil
			}
			metadata := map[string]any{}
			if systemPrompt := stringFromMap(effectiveBaseMetadata, "system_prompt"); systemPrompt != "" {
				metadata["system_prompt"] = systemPromptWithDelegationGuidance(systemPrompt)
			}
			return kernel.ToolAugmentation{
				Tools:    spawnTools(agents),
				Metadata: metadata,
			}, nil
		},
	})
	if err != nil {
		_ = sandboxRuntime.Close()
		return err
	}
	gw, err := kernel.New(kernel.Config{
		Sessions:         s.Sessions,
		Runtime:          rt,
		Resolver:         resolver,
		ApprovalApprover: agentreview.Approver{Reviewer: newModelApprovalReviewer(s.Sessions)},
	})
	if err != nil {
		_ = sandboxRuntime.Close()
		return err
	}
	if err := rejectReconfigureWithActiveTurns(oldGateway, "rebuild gateway"); err != nil {
		_ = sandboxRuntime.Close()
		return err
	}
	s.mu.Lock()
	oldExec := s.exec
	currentRuntime := s.runtime
	currentRuntime.EstimatedPromptPrefixTokens = estimatedPrefixTokens
	s.runtime = currentRuntime
	s.Gateway = gw
	s.exec = sandboxRuntime
	s.engine = rt
	s.mu.Unlock()
	if oldExec != nil {
		_ = oldExec.Close()
	}
	return nil
}
