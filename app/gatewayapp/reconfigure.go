package gatewayapp

import (
	"context"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/skill"
	skillfs "github.com/caelis-labs/caelis/agent-sdk/skill/fs"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/spawn"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolsearch"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	"github.com/caelis-labs/caelis/control/agentbinding"
	"github.com/caelis-labs/caelis/control/modelprofile"
	acpassembly "github.com/caelis-labs/caelis/internal/acpagentbridge/assembly"
	"github.com/caelis-labs/caelis/internal/acpbridge"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
	"github.com/caelis-labs/caelis/internal/controlplane"
	kernelimpl "github.com/caelis-labs/caelis/internal/kernel"
	"github.com/caelis-labs/caelis/internal/sandboxrouter"
	"github.com/caelis-labs/caelis/ports/plugin"
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
	doc.ModelProfiles.DefaultProfileID = modelprofile.BuildProviderID(s.lookup.DefaultID())
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
	if err := s.rejectRemovedAssemblyAgents(context.Background(), runtimeCfg.Assembly.Agents, bundle.RuntimeConfig.Assembly.Agents); err != nil {
		bundle.Close()
		return err
	}
	return s.installGatewayRuntimeBundle(oldGateway, bundle)
}

func (s *Stack) rejectRemovedAssemblyAgents(ctx context.Context, before []assembly.AgentConfig, after []assembly.AgentConfig) error {
	afterIDs := make(map[string]struct{}, len(after))
	for _, agent := range after {
		afterIDs[strings.ToLower(strings.TrimSpace(agent.Name))] = struct{}{}
	}
	for _, agent := range before {
		if _, retained := afterIDs[strings.ToLower(strings.TrimSpace(agent.Name))]; retained {
			continue
		}
		if err := s.rejectBoundACPAgent(ctx, agent.Name); err != nil {
			return err
		}
	}
	return nil
}

type gatewayBuildPlan struct {
	SandboxConfig SandboxConfig
	RuntimeConfig stackRuntimeConfig
	Plugins       pluginContributions
}

type gatewayRuntimeBundle struct {
	Gateway                     *kernelimpl.Gateway
	Exec                        sandbox.Runtime
	Engine                      *runtime.Runtime
	Placement                   controlplane.PlacementExecutor
	ACPControlPlane             *acpassembly.ControlPlane
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
	configuredAssembly, err := s.configuredAssemblyWithPluginAgents(runtimeCfg.BaseAssembly, contribs.Agents, runtimeCfg)
	if err != nil {
		return gatewayBuildPlan{}, err
	}
	runtimeCfg.Assembly = configuredAssembly
	runtimeCfg.Plugins = clonePluginConfigs(doc.Plugins)
	runtimeCfg.PluginSkills = skill.ClonePluginBundles(contribs.SkillBundles)
	baseMetadata, err := buildStackBaseMetadata(s.AppName, s.Workspace.CWD, runtimeCfg.SystemPrompt, runtimeCfg.Model, sandboxCfg, skillDirs, runtimeCfg.PluginSkills)
	if err != nil {
		return gatewayBuildPlan{}, err
	}
	runtimeCfg.BaseMetadata = baseMetadata.Metadata
	runtimeCfg.SkillCatalog = baseMetadata.SkillCatalog
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
	tools, err := builtin.BuildCoreTools(builtin.CoreToolsConfig{
		Runtime:      sandboxRuntime,
		SkillLoader:  skillfs.Loader{},
		SkillCatalog: runtimeCfg.SkillCatalog,
	})
	if err != nil {
		bundle.Close()
		return nil, err
	}
	mcpTools := mcpMgr.Tools()
	if searchTool := toolsearch.New(mcpTools); searchTool != nil {
		tools = append(tools, searchTool)
	}
	tools = append(tools, mcpTools...)
	executionValidator, err := controlplane.NewExecutionValidator(controlplane.ExecutionValidatorConfig{
		Sandbox: sandboxRuntime,
	})
	if err != nil {
		bundle.Close()
		return nil, err
	}

	estimatedPrefixTokens := estimateModelPromptPrefixTokens(effectiveBaseMetadata, tools)
	compactionCfg := defaultCompactionConfig(runtimeCfg.ContextWindow)
	compactionCfg.EstimatedPromptPrefixTokens = estimatedPrefixTokens
	contextRouter, err := controlplane.NewContextRouter(s.Sessions)
	if err != nil {
		bundle.Close()
		return nil, err
	}
	watchdogLifecycle := controlplane.NewWatchdogLifecycleObserver()
	localCfg := runtime.Config{
		Sessions:                 s.Sessions,
		AgentFactory:             chat.Factory{},
		DefaultPolicyMode:        effectivePolicyProfile,
		DefaultApprovalMode:      string(kernelimpl.NormalizeApprovalMode(runtimeCfg.ApprovalMode)),
		Compaction:               compactionCfg,
		ControllerContextRouter:  contextRouter,
		ControllerEventForwarder: acpbridge.NewControllerForwarder(s.Sessions),
		TaskStore:                s.taskStore,
		TraceSink:                watchdogLifecycle,
	}
	var acpControlPlane *acpassembly.ControlPlane
	localCfg, acpControlPlane, err = injectACPControlPlane(
		localCfg,
		runtimeCfg.Assembly,
		s.delegationPlacementResolver(runtimeCfg),
	)
	if err != nil {
		bundle.Close()
		return nil, err
	}
	controlCoordinator, err := controlplane.NewCoordinator(controlplane.CoordinatorConfig{
		Sessions:              s.Sessions,
		Controllers:           localCfg.Controllers,
		Context:               contextRouter,
		ControllerBindingGate: s.assemblyMutationMu.RLocker(),
	})
	if err != nil {
		bundle.Close()
		return nil, err
	}
	localCfg.ControllerRecovery = controlCoordinator
	rt, err := runtime.New(localCfg)
	if err != nil {
		bundle.Close()
		return nil, err
	}
	bundle.Engine = rt
	leaseService, ok := s.Sessions.(session.SessionLeaseService)
	if !ok {
		bundle.Close()
		return nil, fmt.Errorf("gatewayapp: production session service does not support execution leases")
	}
	leasedRuntime, err := controlplane.NewLeasedRuntime(controlplane.LeasedRuntimeConfig{
		Runtime: rt,
		Leases:  leaseService,
		OwnerID: strings.TrimSpace(s.leaseOwnerID),
	})
	if err != nil {
		bundle.Close()
		return nil, err
	}
	watchdogRuntime, err := controlplane.NewWatchdogRuntime(controlplane.WatchdogRuntimeConfig{
		Runtime:  leasedRuntime,
		Sessions: s.Sessions,
	})
	if err != nil {
		bundle.Close()
		return nil, err
	}
	bundle.ACPControlPlane = acpControlPlane
	bundle.Placement = watchdogRuntime
	sessionControl, err := controlplane.NewSessionControl(controlCoordinator, watchdogRuntime)
	if err != nil {
		bundle.Close()
		return nil, err
	}
	resolver, err := kernelimpl.NewAssemblyResolver(kernelimpl.AssemblyResolverConfig{
		Sessions:          s.Sessions,
		Assembly:          runtimeCfg.Assembly,
		DefaultModelAlias: s.lookup.DefaultID(),
		ContextWindow:     runtimeCfg.ContextWindow,
		ModelLookup:       s.lookup,
		Tools:             tools,
		BaseMetadata:      cloneMap(effectiveBaseMetadata),
		ApprovalModelResolver: func(ctx context.Context, _ session.SessionRef) (model.LLM, bool, error) {
			resolved, bound, err := s.resolveSystemAgentModel(ctx, agentbinding.HandleGuardian, runtimeCfg.ContextWindow)
			if err != nil {
				return nil, false, err
			}
			return withSystemAgentReasoningEffort(resolved), bound, nil
		},
		ToolAugmenter: func(ctx context.Context, req kernelimpl.ToolAugmentContext) (kernelimpl.ToolAugmentation, error) {
			s.assemblyMutationMu.RLock()
			agents, targets, err := s.delegationSpawnConfiguration(req.Session)
			s.assemblyMutationMu.RUnlock()
			if err != nil {
				return kernelimpl.ToolAugmentation{}, err
			}
			metadata := map[string]any{}
			if systemPrompt := stringFromMap(effectiveBaseMetadata, "system_prompt"); systemPrompt != "" {
				metadata["system_prompt"] = systemPromptWithDelegationGuidance(systemPrompt)
			}
			return kernelimpl.ToolAugmentation{
				Tools:    []tool.Tool{spawn.NewWithTargets(agents, targets)},
				Metadata: metadata,
			}, nil
		},
	})
	if err != nil {
		bundle.Close()
		return nil, err
	}
	approvalReviewer := s.newModelApprovalReviewer()
	gw, err := kernelimpl.New(kernelimpl.Config{
		Sessions:             s.Sessions,
		Runtime:              watchdogRuntime,
		TurnStartGate:        s.approvalRecovery,
		Control:              sessionControl,
		Resolver:             resolver,
		ExecutionValidator:   executionValidator,
		DefaultApprovalMode:  kernelimpl.NormalizeApprovalMode(runtimeCfg.ApprovalMode),
		ApprovalApprover:     approval.ReviewerAdapter{Reviewer: approvalReviewer},
		ApprovalReviewer:     approvalReviewer,
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
	// A caller may already hold the old Gateway pointer and begin a handoff once
	// the assembly mutation gate opens. Revoke removed Agents from that shared
	// registry before publishing the replacement runtime as well.
	s.mu.RLock()
	oldControlPlane := s.acpControlPlane
	s.mu.RUnlock()
	if oldControlPlane != nil {
		if err := oldControlPlane.Updater.UpdateAgents(bundle.RuntimeConfig.Assembly.Agents); err != nil {
			bundle.Close()
			return err
		}
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
	currentRuntime.PluginSkills = skill.ClonePluginBundles(bundle.RuntimeConfig.PluginSkills)
	currentRuntime.SkillCatalog = bundle.RuntimeConfig.SkillCatalog
	currentRuntime.Plugins = clonePluginConfigs(bundle.RuntimeConfig.Plugins)
	currentRuntime.BaseMetadata = cloneMap(bundle.RuntimeConfig.BaseMetadata)
	currentRuntime.EstimatedPromptPrefixTokens = bundle.EstimatedPromptPrefixTokens
	s.runtime = currentRuntime
	s.gateway = bundle.Gateway
	s.exec = bundle.Exec
	s.engine = bundle.Engine
	s.placement = bundle.Placement
	s.acpControlPlane = bundle.ACPControlPlane
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
		p, err := parseConfiguredPlugin(pCfg)
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
	contribs, err := s.resolvePluginContributions(configs)
	if err != nil {
		return nil, err
	}
	return contribs.Agents, nil
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
