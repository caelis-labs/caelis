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
	"github.com/OnslaughtSnail/caelis/impl/tool/builtin/toolsearch"
	"github.com/OnslaughtSnail/caelis/impl/tool/mcp"
	kernelimpl "github.com/OnslaughtSnail/caelis/internal/kernel"
	"github.com/OnslaughtSnail/caelis/internal/sandboxrouter"
	"github.com/OnslaughtSnail/caelis/ports/assembly"
	"github.com/OnslaughtSnail/caelis/ports/plugin"
	"github.com/OnslaughtSnail/caelis/ports/sandbox"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/ports/skill"
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

	if err := guardNoActiveTurns(oldGateway, "rebuild gateway"); err != nil {
		return err
	}
	plan, err := s.loadGatewayBuildPlan(sandboxCfg, runtimeCfg)
	if err != nil {
		return err
	}
	bundle, err := s.buildGatewayRuntime(plan)
	if err != nil {
		return err
	}
	return s.installGatewayRuntimeBundle(oldGateway, bundle)
}

type gatewayBuildPlan struct {
	SandboxConfig SandboxConfig
	RuntimeConfig stackRuntimeConfig
	Plugins       pluginContributions
}

type gatewayRuntimeBundle struct {
	Gateway                     *kernelimpl.Gateway
	Exec                        sandbox.Runtime
	Engine                      *local.Runtime
	MCP                         *mcp.Manager
	RuntimeConfig               stackRuntimeConfig
	EstimatedPromptPrefixTokens int
}

func (b *gatewayRuntimeBundle) Close() {
	if b == nil {
		return
	}
	if b.Gateway != nil {
		closeIfSupported(b.Gateway)
	}
	b.Gateway = nil
	if b.Engine != nil {
		closeIfSupported(b.Engine)
	}
	b.Engine = nil
	if b.Exec != nil {
		_ = b.Exec.Close()
		b.Exec = nil
	}
	if b.MCP != nil {
		_ = b.MCP.Close()
		b.MCP = nil
	}
}

func closeIfSupported(v any) {
	closer, ok := v.(interface{ Close() error })
	if !ok || closer == nil {
		return
	}
	_ = closer.Close()
}

func guardNoActiveTurns(gw *kernelimpl.Gateway, action string) error {
	return rejectReconfigureWithActiveTurns(gw, action)
}

func (s *Stack) loadGatewayBuildPlan(sandboxCfg SandboxConfig, runtimeCfg stackRuntimeConfig) (gatewayBuildPlan, error) {
	doc, err := s.store.Load()
	if err != nil {
		return gatewayBuildPlan{}, err
	}
	skillDirs := stackSkillDiscoveryDirs(s.Workspace.CWD, runtimeCfg.SkillDirs)
	contribs, err := s.resolvePluginContributions(doc.Plugins)
	if err != nil {
		return gatewayBuildPlan{}, err
	}
	configuredAssembly, err := s.configuredAssemblyWithPluginAgents(runtimeCfg.BaseAssembly, doc.Agents, contribs.Agents, runtimeCfg)
	if err != nil {
		return gatewayBuildPlan{}, err
	}
	runtimeCfg.Assembly = configuredAssembly
	runtimeCfg.Plugins = clonePluginConfigs(doc.Plugins)
	runtimeCfg.PluginSkills = clonePluginSkillBundles(contribs.SkillBundles)
	baseMetadata, err := buildStackBaseMetadata(s.AppName, s.Workspace.CWD, runtimeCfg.SystemPrompt, runtimeCfg.Model, sandboxCfg, skillDirs, runtimeCfg.PluginSkills)
	if err != nil {
		return gatewayBuildPlan{}, err
	}
	runtimeCfg.BaseMetadata = baseMetadata
	return gatewayBuildPlan{
		SandboxConfig: sandboxCfg,
		RuntimeConfig: runtimeCfg,
		Plugins:       contribs,
	}, nil
}

func (s *Stack) buildGatewayRuntime(plan gatewayBuildPlan) (*gatewayRuntimeBundle, error) {
	runtimeCfg := plan.RuntimeConfig
	sandboxCfg := plan.SandboxConfig
	route, err := sandboxrouter.Current(sandbox.Backend(sandboxCfg.RequestedType))
	if err != nil {
		return nil, err
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
		return nil, err
	}
	bundle := &gatewayRuntimeBundle{Exec: sandboxRuntime}

	mcpMgr, err := mcp.NewManager(context.Background(), plan.Plugins.MCPServerSpecs)
	if err != nil {
		bundle.Close()
		return nil, fmt.Errorf("gatewayapp: failed to initialize MCP servers: %w", err)
	}
	bundle.MCP = mcpMgr

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
		bundle.Close()
		return nil, err
	}
	mcpTools := mcpMgr.Tools()
	if searchTool := toolsearch.New(mcpTools); searchTool != nil {
		tools = append(tools, searchTool)
	}
	tools = append(tools, mcpTools...)

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
		bundle.Close()
		return nil, err
	}
	bundle.Engine = rt
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
		bundle.Close()
		return nil, err
	}
	gw, err := kernelimpl.New(kernelimpl.Config{
		Sessions:             s.Sessions,
		Runtime:              rt,
		Resolver:             resolver,
		DefaultApprovalMode:  kernelimpl.NormalizeApprovalMode(runtimeCfg.ApprovalMode),
		ApprovalApprover:     agentreview.Approver{Reviewer: s.newModelApprovalReviewer()},
		SubmissionReferences: s.submissionReferenceProjector(),
		SessionStartHooks:    plan.Plugins.SessionStartHooks,
	})
	if err != nil {
		bundle.Close()
		return nil, err
	}
	bundle.Gateway = gw
	bundle.RuntimeConfig = runtimeCfg
	bundle.EstimatedPromptPrefixTokens = estimatedPrefixTokens
	return bundle, nil
}

func (s *Stack) installGatewayRuntimeBundle(oldGateway *kernelimpl.Gateway, bundle *gatewayRuntimeBundle) error {
	// Re-check because a turn may have started while the replacement runtime was being built.
	if err := guardNoActiveTurns(oldGateway, "rebuild gateway"); err != nil {
		bundle.Close()
		return err
	}
	s.swapGatewayRuntime(bundle)
	return nil
}

func (s *Stack) swapGatewayRuntime(bundle *gatewayRuntimeBundle) {
	s.mu.Lock()
	oldExec := s.exec
	oldMcpMgr := s.mcpMgr
	currentRuntime := s.runtime
	currentRuntime.Assembly = assembly.CloneResolvedAssembly(bundle.RuntimeConfig.Assembly)
	currentRuntime.SkillDirs = cloneStringSlicePreserveNil(bundle.RuntimeConfig.SkillDirs)
	currentRuntime.PluginSkills = clonePluginSkillBundles(bundle.RuntimeConfig.PluginSkills)
	currentRuntime.Plugins = clonePluginConfigs(bundle.RuntimeConfig.Plugins)
	currentRuntime.BaseMetadata = cloneMap(bundle.RuntimeConfig.BaseMetadata)
	currentRuntime.EstimatedPromptPrefixTokens = bundle.EstimatedPromptPrefixTokens
	s.runtime = currentRuntime
	s.gateway = bundle.Gateway
	s.exec = bundle.Exec
	s.engine = bundle.Engine
	s.mcpMgr = bundle.MCP
	s.mu.Unlock()
	if oldExec != nil {
		_ = oldExec.Close()
	}
	if oldMcpMgr != nil {
		_ = oldMcpMgr.Close()
	}
}

func stackSkillDiscoveryDirs(workspaceDir string, configured []string) []string {
	if configured != nil {
		return cloneStringSlicePreserveNil(configured)
	}
	return DefaultSkillDiscoveryDirs(workspaceDir)
}

type pluginContributions struct {
	SkillBundles      []skill.PluginBundle
	SessionStartHooks []plugin.HookSpec
	MCPServerSpecs    []plugin.MCPServerSpec
	Agents            []pluginAgentContribution
}

type pluginAgentContribution struct {
	PluginID string
	Agent    plugin.AgentContribution
}

func (s *Stack) resolvePluginContributions(configs []PluginConfig) (pluginContributions, error) {
	var out pluginContributions
	for _, pCfg := range configs {
		p, err := pluginregistry.ParsePlugin(pCfg.Root)
		if err != nil {
			if pCfg.Enabled {
				return out, fmt.Errorf("gatewayapp: parse enabled plugin %q failed: %w", pCfg.ID, err)
			}
			continue
		}
		bundles := pluginSkillBundles(p, pCfg.Enabled)
		out.SkillBundles = append(out.SkillBundles, bundles...)
		if !pCfg.Enabled {
			continue
		}
		for _, hook := range p.Hooks {
			if hook.Event == plugin.HookEventSessionStart {
				out.SessionStartHooks = append(out.SessionStartHooks, hook)
			}
		}
		out.MCPServerSpecs = append(out.MCPServerSpecs, p.MCPServers...)
		for _, contributed := range p.Agents {
			out.Agents = append(out.Agents, pluginAgentContribution{
				PluginID: p.ID,
				Agent:    contributed,
			})
		}
	}
	return out, nil
}

func (s *Stack) resolvePluginAgentContributions(configs []PluginConfig) ([]pluginAgentContribution, error) {
	var out []pluginAgentContribution
	for _, pCfg := range configs {
		if !pCfg.Enabled {
			continue
		}
		p, err := pluginregistry.ParsePlugin(pCfg.Root)
		if err != nil {
			return nil, fmt.Errorf("gatewayapp: parse enabled plugin %q failed: %w", pCfg.ID, err)
		}
		for _, contributed := range p.Agents {
			out = append(out, pluginAgentContribution{
				PluginID: p.ID,
				Agent:    contributed,
			})
		}
	}
	return out, nil
}

func pluginSkillBundles(p plugin.InstalledPlugin, enabled bool) []skill.PluginBundle {
	if len(p.Skills) == 0 {
		return nil
	}
	out := make([]skill.PluginBundle, 0, len(p.Skills))
	pluginID := strings.TrimSpace(p.ID)
	for _, sc := range p.Skills {
		root := strings.TrimSpace(sc.Root)
		if root == "" {
			continue
		}
		namespace := strings.TrimSpace(sc.Namespace)
		if namespace == "" {
			namespace = pluginID
		}
		out = append(out, skill.PluginBundle{
			Plugin:    pluginID,
			Namespace: namespace,
			Root:      root,
			Disabled:  append([]string(nil), sc.Disabled...),
			Enabled:   enabled,
		})
	}
	return out
}
