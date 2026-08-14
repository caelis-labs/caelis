package gatewayapp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/policy/presets"
	"github.com/caelis-labs/caelis/agent-sdk/runtime"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/skill"
	skillfs "github.com/caelis-labs/caelis/agent-sdk/skill/fs"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/sendmessage"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/spawn"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolsearch"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	"github.com/caelis-labs/caelis/app/gatewayapp/internal/configstore"
	"github.com/caelis-labs/caelis/control/agentbinding"
	"github.com/caelis-labs/caelis/control/plugin"
	"github.com/caelis-labs/caelis/control/sessionvisibility"
	acpassembly "github.com/caelis-labs/caelis/internal/acpagentbridge/assembly"
	"github.com/caelis-labs/caelis/internal/acpbridge"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
	"github.com/caelis-labs/caelis/internal/controlplane"
	kernelimpl "github.com/caelis-labs/caelis/internal/kernel"
	"github.com/caelis-labs/caelis/internal/sandboxrouter"
)

func (s *Stack) loadSandboxConfigDocument(ctx context.Context, expected *uint64) (AppConfig, error) {
	if s == nil || s.store == nil {
		if s == nil {
			return AppConfig{}, fmt.Errorf("gatewayapp: stack is unavailable")
		}
		s.mu.RLock()
		doc := AppConfig{
			SchemaVersion:         configstore.SchemaVersionV2,
			ConfigurationRevision: s.sandboxRevision,
			Sandbox:               cloneSandboxConfig(s.sandboxPersisted),
		}
		s.mu.RUnlock()
		if expected != nil && doc.ConfigurationRevision != *expected {
			return doc, &configstore.ConfigurationRevisionConflict{Expected: *expected, Actual: doc.ConfigurationRevision}
		}
		return doc, nil
	}
	doc, err := s.store.LoadContext(ctx)
	if err != nil {
		return AppConfig{}, err
	}
	if expected != nil && doc.ConfigurationRevision != *expected {
		return doc, &configstore.ConfigurationRevisionConflict{
			Expected: *expected,
			Actual:   doc.ConfigurationRevision,
		}
	}
	return doc, nil
}

func (s *Stack) persistSandboxConfigDocument(ctx context.Context, doc AppConfig) (AppConfig, error) {
	if s == nil || s.store == nil {
		return doc, nil
	}
	return s.store.CompareAndSave(ctx, doc.ConfigurationRevision, doc)
}

// buildInitialGatewayRuntime constructs the root or detached Session Runtime
// exactly once from its fixed configuration snapshot. Host configuration
// mutations never call this method.
func (s *Stack) buildInitialGatewayRuntime(ctx context.Context) error {
	if s == nil {
		return fmt.Errorf("gatewayapp: stack is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.RLock()
	oldGateway := s.gateway
	sandboxCfg := s.sandbox
	runtimeCfg := s.runtime
	s.mu.RUnlock()

	if oldGateway != nil {
		return errors.New("gatewayapp: Runtime is already initialized")
	}
	plan, err := s.loadGatewayBuildPlan(sandboxCfg, runtimeCfg)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	bundle, err := s.buildGatewayRuntimeContext(ctx, plan)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		bundle.Close()
		return err
	}
	return s.installGatewayRuntimeBundle(oldGateway, bundle)
}

type gatewayBuildPlan struct {
	SandboxConfig      SandboxConfig
	RuntimeConfig      stackRuntimeConfig
	Plugins            plugin.Contributions
	ReleasePluginCache func() error
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
	ReleasePluginCache          func() error
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
	if b.ReleasePluginCache != nil {
		_ = b.ReleasePluginCache()
		b.ReleasePluginCache = nil
	}
}

func closeIfSupported(v any) {
	closer, ok := v.(interface{ Close() error })
	if !ok || closer == nil {
		return
	}
	_ = closer.Close()
}

func (s *Stack) loadGatewayBuildPlan(sandboxCfg SandboxConfig, runtimeCfg stackRuntimeConfig) (gatewayBuildPlan, error) {
	sandboxCfg = configstore.DefaultSandboxConfig(sandboxCfg)
	doc, err := s.loadGatewayAppConfig()
	if err != nil {
		return gatewayBuildPlan{}, err
	}
	releasePluginCache := s.retainManagedPluginCaches(doc.Plugins)
	releaseOnError := true
	defer func() {
		if releaseOnError {
			_ = releasePluginCache()
		}
	}()
	skillDirs := stackSkillDiscoveryDirs(s.Workspace.CWD, runtimeCfg.SkillDirs)
	contribs, err := resolveGatewayPluginContributions(doc.Plugins)
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
	releaseOnError = false
	return gatewayBuildPlan{
		SandboxConfig:      sandboxCfg,
		RuntimeConfig:      runtimeCfg,
		Plugins:            contribs,
		ReleasePluginCache: releasePluginCache,
	}, nil
}

func (s *Stack) retainManagedPluginCaches(configs []PluginConfig) func() error {
	release := plugin.RetainManagedPluginCaches(s.storeDir, configs)
	var releaseOnce sync.Once
	return func() error {
		releaseOnce.Do(release)
		return s.Plugins().ReclaimManagedCaches(context.Background())
	}
}

func (s *Stack) loadGatewayAppConfig() (AppConfig, error) {
	if s != nil && s.appConfigSnapshot != nil {
		return configstore.Normalize(*s.appConfigSnapshot), nil
	}
	if s == nil || s.store == nil {
		return AppConfig{}, fmt.Errorf("gatewayapp: app config store unavailable")
	}
	return s.store.Load()
}

func (s *Stack) buildGatewayRuntime(plan gatewayBuildPlan) (*gatewayRuntimeBundle, error) {
	return s.buildGatewayRuntimeContext(context.Background(), plan)
}

func (s *Stack) buildGatewayRuntimeContext(
	ctx context.Context,
	plan gatewayBuildPlan,
) (*gatewayRuntimeBundle, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	runtimeCfg := plan.RuntimeConfig
	sandboxCfg := plan.SandboxConfig
	bundle := &gatewayRuntimeBundle{ReleasePluginCache: plan.ReleasePluginCache}
	route, err := sandboxrouter.Current(sandbox.Backend(sandboxCfg.RequestedType))
	if err != nil {
		bundle.Close()
		return nil, err
	}
	sandboxRuntime, err := sandbox.New(sandbox.Config{
		CWD:                 s.Workspace.CWD,
		RequestedBackend:    route.Backend,
		BackendCandidates:   route.BackendCandidates,
		FallbackInstallHint: route.InstallHint,
		HelperPath:          sandboxCfg.HelperPath,
		StateDir:            s.storeDir,
		WritableRoots:       append([]string(nil), sandboxCfg.WritableRoots...),
		ReadOnlySubpaths:    append([]string(nil), sandboxCfg.ReadOnlySubpaths...),
	})
	if err != nil {
		bundle.Close()
		return nil, err
	}
	bundle.Exec = sandboxRuntime

	if err := ctx.Err(); err != nil {
		bundle.Close()
		return nil, err
	}
	mcpMgr, err := mcp.NewManager(ctx, plan.Plugins.MCPServerSpecs)
	if err != nil {
		bundle.Close()
		return nil, fmt.Errorf("gatewayapp: failed to initialize MCP servers: %w", err)
	}
	bundle.MCP = mcpMgr
	if err := ctx.Err(); err != nil {
		bundle.Close()
		return nil, err
	}

	securityPosture := resolveProcessSecurityPosture(runtimeCfg)
	effectivePolicyProfile := securityPosture.PolicyMode
	var policyRegistry policy.Registry
	if securityPosture.FullAccessMode {
		registry, registryErr := presets.NewRegistry()
		if registryErr != nil {
			bundle.Close()
			return nil, registryErr
		}
		policyRegistry = registry
		if err := registry.Register(presets.DangerFullAccessMode()); err != nil {
			bundle.Close()
			return nil, err
		}
	}
	effectiveBaseMetadata := cloneMap(runtimeCfg.BaseMetadata)
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
	localCfg := runtime.Config{
		Sessions:                 s.Sessions,
		AgentFactory:             chat.Factory{},
		DefaultPolicyMode:        effectivePolicyProfile,
		PolicyRegistry:           policyRegistry,
		DefaultApprovalMode:      string(kernelimpl.NormalizeApprovalMode(runtimeCfg.ApprovalMode)),
		Compaction:               compactionCfg,
		ControllerContextRouter:  contextRouter,
		ControllerEventForwarder: acpbridge.NewControllerForwarder(s.Sessions),
		TaskStore:                s.taskStore,
		TaskActivityChanged:      s.runtimeTaskChanged,
	}
	var acpControlPlane *acpassembly.ControlPlane
	localCfg, acpControlPlane, err = injectACPControlPlane(
		localCfg,
		runtimeCfg.Assembly,
		s.delegationPlacementResolver(runtimeCfg),
		s.prepareSpawnedACPSession,
	)
	if err != nil {
		bundle.Close()
		return nil, err
	}
	controlCoordinator, err := controlplane.NewCoordinator(controlplane.CoordinatorConfig{
		Sessions:    s.Sessions,
		Controllers: localCfg.Controllers,
		Context:     contextRouter,
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
	bindSubagentMessageRouter(acpControlPlane, rt)
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
	bundle.ACPControlPlane = acpControlPlane
	bundle.Placement = leasedRuntime
	sessionControl, err := controlplane.NewSessionControl(controlplane.SessionControlConfig{
		Controllers:  controlCoordinator,
		Participants: leasedRuntime,
	})
	if err != nil {
		bundle.Close()
		return nil, err
	}
	resolver, err := kernelimpl.NewAssemblyResolver(kernelimpl.AssemblyResolverConfig{
		Sessions:          s.Sessions,
		Assembly:          runtimeCfg.Assembly,
		DefaultModelAlias: runtimeDefaultModelAlias(runtimeCfg, s.lookup),
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
			activeSession, err := s.Sessions.Session(ctx, req.SessionRef)
			if err != nil {
				return kernelimpl.ToolAugmentation{}, err
			}
			spawnedChild := sessionvisibility.IsSpawnedSubagentSession(activeSession)
			augmentedTools := []tool.Tool{sendmessage.New()}
			if !spawnedChild {
				agents, targets, resolveErr := s.delegationSpawnConfiguration(req.Session)
				if resolveErr != nil {
					return kernelimpl.ToolAugmentation{}, resolveErr
				}
				augmentedTools = append([]tool.Tool{spawn.NewWithTargets(agents, targets)}, augmentedTools...)
			}
			metadata := map[string]any{}
			if systemPrompt := stringFromMap(effectiveBaseMetadata, "system_prompt"); systemPrompt != "" {
				if !spawnedChild {
					systemPrompt = systemPromptWithDelegationGuidance(systemPrompt)
				}
				metadata["system_prompt"] = systemPrompt
			}
			return kernelimpl.ToolAugmentation{
				Tools:    augmentedTools,
				Metadata: metadata,
			}, nil
		},
	})
	if err != nil {
		bundle.Close()
		return nil, err
	}
	guardianApprover := s.newGuardianApprover()
	gw, err := kernelimpl.New(kernelimpl.Config{
		Sessions:             s.Sessions,
		Runtime:              leasedRuntime,
		TurnStartGate:        s.approvalRecovery,
		Control:              sessionControl,
		Resolver:             resolver,
		ExecutionValidator:   executionValidator,
		DefaultApprovalMode:  kernelimpl.NormalizeApprovalMode(runtimeCfg.ApprovalMode),
		ApprovalApprover:     guardianApprover,
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

func runtimeDefaultModelAlias(runtimeCfg stackRuntimeConfig, lookup *modelLookup) string {
	if modelID := strings.TrimSpace(runtimeCfg.Model.ID); modelID != "" {
		return modelID
	}
	if lookup == nil {
		return ""
	}
	return lookup.DefaultID()
}

func (s *Stack) installGatewayRuntimeBundle(oldGateway *kernelimpl.Gateway, bundle *gatewayRuntimeBundle) error {
	if oldGateway != nil {
		bundle.Close()
		return errors.New("gatewayapp: refusing to replace an initialized Runtime")
	}
	// Initial construction keeps the default controller backend aligned with
	// the Runtime snapshot. Host mutations never enter this path.
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
	s.workspaceCloseMu.Lock()
	defer s.workspaceCloseMu.Unlock()

	s.mu.Lock()
	oldExec := s.exec
	oldMcpMgr := s.mcpMgr
	oldPluginCacheRelease := s.pluginCacheRelease
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
	s.pluginCacheRelease = bundle.ReleasePluginCache
	bundle.ReleasePluginCache = nil
	s.mu.Unlock()
	if oldExec != nil {
		_ = oldExec.Close()
	}
	if oldMcpMgr != nil {
		_ = oldMcpMgr.Close()
	}
	if oldPluginCacheRelease != nil {
		_ = oldPluginCacheRelease()
	}
}

func stackSkillDiscoveryDirs(workspaceDir string, configured []string) []string {
	if configured != nil {
		return cloneStringSlicePreserveNil(configured)
	}
	return DefaultSkillDiscoveryDirs(workspaceDir)
}

// resolveGatewayPluginContributions gives persisted configuration failures an
// application-boundary prefix at initial assembly and pre-commit validation.
func resolveGatewayPluginContributions(configs []PluginConfig) (plugin.Contributions, error) {
	contributions, err := plugin.ResolveContributions(configs)
	if err != nil {
		return plugin.Contributions{}, fmt.Errorf("gatewayapp: %w", err)
	}
	return contributions, nil
}
