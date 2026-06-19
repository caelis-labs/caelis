package gatewayapp

import (
	"context"
	"fmt"
	"strings"

	"github.com/OnslaughtSnail/caelis/app/gatewayapp/internal/pluginregistry"
	"github.com/OnslaughtSnail/caelis/impl/agent/local"
	"github.com/OnslaughtSnail/caelis/impl/agent/local/chat"
	"github.com/OnslaughtSnail/caelis/impl/approval/agentreview"
	"github.com/OnslaughtSnail/caelis/impl/tool/builtin"
	"github.com/OnslaughtSnail/caelis/impl/tool/mcp"
	kernelimpl "github.com/OnslaughtSnail/caelis/internal/kernel"
	"github.com/OnslaughtSnail/caelis/internal/sandboxrouter"
	"github.com/OnslaughtSnail/caelis/ports/assembly"
	"github.com/OnslaughtSnail/caelis/ports/plugin"
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
	oldGateway := s.gateway
	sandboxCfg := effectiveSandboxConfig(s.sandbox, s.Workspace.CWD)
	runtimeCfg := s.runtime
	s.mu.RUnlock()

	doc, err := s.store.Load()
	if err != nil {
		return err
	}
	var sessionStartHooks []plugin.HookSpec
	var mcpServerSpecs []plugin.MCPServerSpec
	skillDirs := stackSkillDiscoveryDirs(s.Workspace.CWD, runtimeCfg.SkillDirs)
	for _, pCfg := range doc.Plugins {
		if !pCfg.Enabled {
			continue
		}
		p, err := pluginregistry.ParsePlugin(pCfg.Root)
		if err != nil {
			return fmt.Errorf("gatewayapp: parse enabled plugin %q failed: %w", pCfg.ID, err)
		}
		for _, hook := range p.Hooks {
			if hook.Event == plugin.HookEventSessionStart {
				sessionStartHooks = append(sessionStartHooks, hook)
			}
		}
		for _, sc := range p.Skills {
			skillDirs = append(skillDirs, sc.Root)
		}
		mcpServerSpecs = append(mcpServerSpecs, p.MCPServers...)
	}
	configuredAssembly, err := s.configuredAssembly(runtimeCfg.BaseAssembly, doc.Agents, doc.Plugins, runtimeCfg)
	if err != nil {
		return err
	}
	runtimeCfg.Assembly = configuredAssembly
	runtimeCfg.Plugins = clonePluginConfigs(doc.Plugins)
	baseMetadata, err := buildStackBaseMetadata(s.AppName, s.Workspace.CWD, runtimeCfg.SystemPrompt, runtimeCfg.Model, sandboxCfg, skillDirs)
	if err != nil {
		return err
	}
	runtimeCfg.BaseMetadata = baseMetadata
	if err := rejectReconfigureWithActiveTurns(oldGateway, "rebuild gateway"); err != nil {
		return err
	}
	route, err := sandboxrouter.Current(sandbox.Backend(sandboxCfg.RequestedType))
	if err != nil {
		return err
	}
	sandboxRuntime, err := sandbox.New(sandbox.Config{
		CWD:                 s.Workspace.CWD,
		RequestedBackend:    sandbox.Backend(sandboxCfg.RequestedType),
		BackendCandidates:   route.BackendCandidates,
		FallbackInstallHint: route.FallbackInstallHint,
		HelperPath:          sandboxCfg.HelperPath,
		StateDir:            s.storeDir,
		ReadableRoots:       append([]string(nil), sandboxCfg.ReadableRoots...),
		WritableRoots:       append([]string(nil), sandboxCfg.WritableRoots...),
		ReadOnlySubpaths:    append([]string(nil), sandboxCfg.ReadOnlySubpaths...),
	})
	if err != nil {
		return err
	}

	mcpMgr, err := mcp.NewManager(context.Background(), mcpServerSpecs)
	if err != nil {
		_ = sandboxRuntime.Close()
		return fmt.Errorf("gatewayapp: failed to initialize MCP servers: %w", err)
	}

	effectivePolicyProfile := policyProfile(runtimeCfg.PolicyProfile)
	effectiveBaseMetadata := cloneMap(runtimeCfg.BaseMetadata)
	sandboxStatus := sandbox.SelectionStatus(sandboxRuntime)
	if sandboxStatus.FallbackToHost {
		if effectiveBaseMetadata == nil {
			effectiveBaseMetadata = map[string]any{}
		}
		if hint := strings.TrimSpace(sandboxStatus.FallbackInstallHint); hint != "" {
			effectiveBaseMetadata["sandbox_install_hint"] = hint
		}
		if reason := strings.TrimSpace(sandboxStatus.FallbackReason); reason != "" {
			effectiveBaseMetadata["sandbox_fallback_reason"] = reason
		}
	}
	tools, err := builtin.BuildCoreTools(builtin.CoreToolsConfig{Runtime: sandboxRuntime})
	if err != nil {
		_ = sandboxRuntime.Close()
		_ = mcpMgr.Close()
		return err
	}
	tools = append(tools, mcpMgr.Tools()...)

	estimatedPrefixTokens := estimateModelPromptPrefixTokens(effectiveBaseMetadata, tools)
	compactionCfg := defaultCompactionConfig(runtimeCfg.ContextWindow)
	compactionCfg.EstimatedPromptPrefixTokens = estimatedPrefixTokens
	rt, err := local.New(local.Config{
		Sessions:            s.Sessions,
		AgentFactory:        chat.Factory{},
		DefaultPolicyMode:   effectivePolicyProfile,
		DefaultApprovalMode: string(kernelimpl.NormalizeApprovalMode(runtimeCfg.ApprovalMode)),
		Compaction:          compactionCfg,
		Assembly:            runtimeCfg.Assembly,
		TaskStore:           s.taskStore,
	})
	if err != nil {
		_ = sandboxRuntime.Close()
		_ = mcpMgr.Close()
		return err
	}
	resolver, err := kernelimpl.NewAssemblyResolver(kernelimpl.AssemblyResolverConfig{
		Sessions:          s.Sessions,
		Assembly:          runtimeCfg.Assembly,
		DefaultModelAlias: s.lookup.DefaultID(),
		ContextWindow:     runtimeCfg.ContextWindow,
		ModelLookup:       s.lookup,
		Tools:             tools,
		BaseMetadata:      cloneMap(effectiveBaseMetadata),
		ToolAugmenter: func(ctx context.Context, req kernelimpl.ToolAugmentContext) (kernelimpl.ToolAugmentation, error) {
			s.mu.RLock()
			runtimeCfg := s.runtime
			s.mu.RUnlock()
			var participants []session.ParticipantBinding
			if strings.TrimSpace(req.SessionRef.SessionID) != "" {
				session, err := s.Sessions.Session(ctx, req.SessionRef)
				if err != nil {
					return kernelimpl.ToolAugmentation{}, err
				}
				participants = session.Participants
			}
			agents := delegationAgentsForSpawn(runtimeCfg.Assembly, participants)
			if len(agents) == 0 {
				return kernelimpl.ToolAugmentation{}, nil
			}
			metadata := map[string]any{}
			if systemPrompt := stringFromMap(effectiveBaseMetadata, "system_prompt"); systemPrompt != "" {
				metadata["system_prompt"] = systemPromptWithDelegationGuidance(systemPrompt)
			}
			return kernelimpl.ToolAugmentation{
				Tools:    spawnTools(agents),
				Metadata: metadata,
			}, nil
		},
	})
	if err != nil {
		_ = sandboxRuntime.Close()
		_ = mcpMgr.Close()
		return err
	}
	gw, err := kernelimpl.New(kernelimpl.Config{
		Sessions:            s.Sessions,
		Runtime:             rt,
		Resolver:            resolver,
		DefaultApprovalMode: kernelimpl.NormalizeApprovalMode(runtimeCfg.ApprovalMode),
		ApprovalApprover:    agentreview.Approver{Reviewer: s.newModelApprovalReviewer()},
		SessionStartHooks:   sessionStartHooks,
	})
	if err != nil {
		_ = sandboxRuntime.Close()
		_ = mcpMgr.Close()
		return err
	}
	if err := rejectReconfigureWithActiveTurns(oldGateway, "rebuild gateway"); err != nil {
		_ = sandboxRuntime.Close()
		_ = mcpMgr.Close()
		return err
	}
	s.mu.Lock()
	oldExec := s.exec
	oldMcpMgr := s.mcpMgr
	currentRuntime := s.runtime
	currentRuntime.Assembly = assembly.CloneResolvedAssembly(runtimeCfg.Assembly)
	currentRuntime.SkillDirs = cloneStringSlicePreserveNil(runtimeCfg.SkillDirs)
	currentRuntime.Plugins = clonePluginConfigs(runtimeCfg.Plugins)
	currentRuntime.BaseMetadata = cloneMap(runtimeCfg.BaseMetadata)
	currentRuntime.EstimatedPromptPrefixTokens = estimatedPrefixTokens
	s.runtime = currentRuntime
	s.gateway = gw
	s.exec = sandboxRuntime
	s.engine = rt
	s.mcpMgr = mcpMgr
	s.mu.Unlock()
	if oldExec != nil {
		_ = oldExec.Close()
	}
	if oldMcpMgr != nil {
		_ = oldMcpMgr.Close()
	}
	return nil
}

func stackSkillDiscoveryDirs(workspaceDir string, configured []string) []string {
	if configured != nil {
		return cloneStringSlicePreserveNil(configured)
	}
	return DefaultSkillDiscoveryDirs(workspaceDir)
}
