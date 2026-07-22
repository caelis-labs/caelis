package gatewayapp

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"strings"

	sdkplacement "github.com/caelis-labs/caelis/agent-sdk/placement"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/app/gatewayapp/internal/agentregistry"
	"github.com/caelis-labs/caelis/control/agentbinding"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	controlplacement "github.com/caelis-labs/caelis/control/placement"
	controller "github.com/caelis-labs/caelis/internal/acpagentbridge/controller"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
	pluginapi "github.com/caelis-labs/caelis/ports/plugin"
)

type ACPAgentInfo struct {
	Name        string
	Description string
}

const systemSceneEnvKey = "CAELIS_SYSTEM_SCENE"

type ACPControllerStatus = controller.ControllerStatus
type ACPControllerCommand = controller.ControllerCommand
type ACPControllerConfigChoice = controller.ControllerConfigChoice
type ACPControllerMode = controller.ControllerMode

func withSelfACPAgent(assembly assembly.ResolvedAssembly, self assembly.AgentConfig) assembly.ResolvedAssembly {
	return agentregistry.WithSelfAgent(assembly, self)
}

type defaultSpawnedSelfACPAgentConfig struct {
	Config       Config
	AppName      string
	UserID       string
	StoreDir     string
	WorkspaceKey string
	WorkspaceCWD string
}

func defaultSpawnedSelfACPAgent(cfg defaultSpawnedSelfACPAgentConfig) (assembly.AgentConfig, error) {
	childConfig := cfg.Config
	// Spawned Caelis children must bridge permission requests back to the
	// parent session instead of performing their own automatic approval review.
	childConfig.ApprovalMode = "manual"
	return agentregistry.DefaultSelfAgent(agentregistry.DefaultSelfConfig{
		Config:       agentRuntimeConfig(childConfig),
		AppName:      cfg.AppName,
		UserID:       cfg.UserID,
		StoreDir:     cfg.StoreDir,
		WorkspaceKey: cfg.WorkspaceKey,
		WorkspaceCWD: cfg.WorkspaceCWD,
	})
}

func configuredModelSpawnedSelfACPAgent(cfg defaultSpawnedSelfACPAgentConfig) (assembly.AgentConfig, error) {
	childConfig := cfg.Config
	childConfig.ApprovalMode = "manual"
	return agentregistry.ConfiguredModelSelfAgent(agentregistry.DefaultSelfConfig{
		Config:       agentRuntimeConfig(childConfig),
		AppName:      cfg.AppName,
		UserID:       cfg.UserID,
		StoreDir:     cfg.StoreDir,
		WorkspaceKey: cfg.WorkspaceKey,
		WorkspaceCWD: cfg.WorkspaceCWD,
	})
}

func agentRuntimeConfig(cfg Config) agentregistry.RuntimeConfig {
	return agentregistry.RuntimeConfig{
		AppName:                   cfg.AppName,
		UserID:                    cfg.UserID,
		StoreDir:                  cfg.StoreDir,
		WorkspaceKey:              cfg.WorkspaceKey,
		WorkspaceCWD:              cfg.WorkspaceCWD,
		ApprovalMode:              cfg.ApprovalMode,
		PolicyProfile:             cfg.PolicyProfile,
		ControlOperationRetention: cfg.ControlOperationRetention,
		ContextWindow:             cfg.ContextWindow,
		SystemPrompt:              cfg.SystemPrompt,
		Model:                     cfg.Model,
	}
}

type builtinACPAdapterPackage = agentregistry.BuiltinAdapterPackage

func builtinACPAdapterPackageFor(name string) (builtinACPAdapterPackage, bool) {
	return agentregistry.BuiltinAdapterPackageFor(name)
}

func (s *Stack) refreshAgentAssembly() error {
	if s == nil {
		return fmt.Errorf("gatewayapp: stack is unavailable")
	}
	s.mu.RLock()
	runtimeCfg := s.runtime
	controlPlane := s.acpControlPlane
	s.mu.RUnlock()
	resolvedAssembly, err := s.configuredAssembly(runtimeCfg.BaseAssembly, runtimeCfg.Plugins, runtimeCfg)
	if err != nil {
		return err
	}
	runtimeCfg.Assembly = resolvedAssembly
	if len(runtimeCfg.Assembly.Agents) > 0 && controlPlane == nil {
		return fmt.Errorf("gatewayapp: ACP control plane is unavailable")
	}
	if controlPlane != nil {
		if err := controlPlane.Updater.UpdateAgents(runtimeCfg.Assembly.Agents); err != nil {
			return err
		}
	}
	s.mu.Lock()
	current := s.runtime
	current.Assembly = runtimeCfg.Assembly
	s.runtime = current
	s.mu.Unlock()
	return nil
}

func (s *Stack) configuredAssembly(base assembly.ResolvedAssembly, plugins []PluginConfig, runtimeCfg stackRuntimeConfig) (assembly.ResolvedAssembly, error) {
	pluginAgents, err := s.resolvePluginAgentContributions(plugins)
	if err != nil {
		return assembly.ResolvedAssembly{}, err
	}
	return s.configuredAssemblyWithPluginAgents(base, pluginAgents, runtimeCfg)
}

func (s *Stack) configuredAssemblyWithPluginAgents(base assembly.ResolvedAssembly, pluginAgents []pluginAgentContribution, runtimeCfg stackRuntimeConfig) (assembly.ResolvedAssembly, error) {
	self, err := defaultSpawnedSelfACPAgent(defaultSpawnedSelfACPAgentConfig{
		Config: Config{
			AppName:                   s.AppName,
			UserID:                    s.UserID,
			StoreDir:                  s.storeDir,
			ControlOperationRetention: s.controlOperationRetention,
			WorkspaceKey:              s.Workspace.Key,
			WorkspaceCWD:              s.Workspace.CWD,
			PolicyProfile:             runtimeCfg.PolicyProfile,
			ContextWindow:             runtimeCfg.ContextWindow,
			SystemPrompt:              runtimeCfg.SystemPrompt,
			Model:                     runtimeCfg.Model,
		},
		AppName:      s.AppName,
		UserID:       s.UserID,
		StoreDir:     s.storeDir,
		WorkspaceKey: s.Workspace.Key,
		WorkspaceCWD: s.Workspace.CWD,
	})
	if err != nil {
		return assembly.ResolvedAssembly{}, err
	}
	resolved := withSelfACPAgent(base, self)
	resolved, err = s.withReviewerAgent(resolved, runtimeCfg)
	if err != nil {
		return assembly.ResolvedAssembly{}, err
	}
	resolved, err = s.withPluginACPAgents(resolved, pluginAgents)
	if err != nil {
		return assembly.ResolvedAssembly{}, err
	}
	resolved, err = s.withExternalACPAgents(resolved, runtimeCfg)
	if err != nil {
		return assembly.ResolvedAssembly{}, err
	}
	return s.withDirectProfileAgents(resolved, runtimeCfg)
}

func (s *Stack) withDirectProfileAgents(resolved assembly.ResolvedAssembly, runtimeCfg stackRuntimeConfig) (assembly.ResolvedAssembly, error) {
	out := assembly.CloneResolvedAssembly(resolved)
	if s == nil || s.store == nil || s.lookup == nil {
		return out, nil
	}
	snapshot, err := s.placementSnapshot(context.Background())
	if err != nil {
		return assembly.ResolvedAssembly{}, err
	}
	for _, handle := range agentbinding.DirectRunHandles() {
		if _, ok := agentbinding.Lookup(snapshot.placement.Bindings, handle); !ok {
			continue
		}
		placement, err := s.resolveHandlePlacement(context.Background(), controlplacement.HandleRequest{
			Handle: handle, Purpose: controlplacement.PurposeDirect,
		})
		if err != nil {
			return assembly.ResolvedAssembly{}, err
		}
		if placement.Kind != sdkplacement.KindModel {
			continue
		}
		configured, err := s.lookup.ResolveConfig(placement.Model)
		if err != nil {
			return assembly.ResolvedAssembly{}, err
		}
		configured.ReasoningEffort = placement.ReasoningEffort
		materialized, err := s.materializeDelegatedModel(string(handle), configured, runtimeCfg)
		if err != nil {
			return assembly.ResolvedAssembly{}, err
		}
		out.Agents = append(out.Agents, materialized)
	}
	return out, nil
}

func (s *Stack) withReviewerAgent(resolved assembly.ResolvedAssembly, runtimeCfg stackRuntimeConfig) (assembly.ResolvedAssembly, error) {
	out := assembly.CloneResolvedAssembly(resolved)
	for _, existing := range out.Agents {
		if strings.EqualFold(strings.TrimSpace(existing.Name), ReviewerAgentID) {
			return assembly.ResolvedAssembly{}, fmt.Errorf("gatewayapp: host agent %q conflicts with the fixed Reviewer scene", ReviewerAgentID)
		}
	}
	if s.store == nil {
		return assembly.ResolvedAssembly{}, fmt.Errorf("gatewayapp: resolve Reviewer placement: app config store unavailable")
	}
	placement, resolveErr := s.resolveHandlePlacement(context.Background(), controlplacement.HandleRequest{
		Handle: agentbinding.HandleReviewer, Purpose: controlplacement.PurposeReviewer,
	})
	if resolveErr != nil {
		var unavailable *controlplacement.DefaultProfileError
		if errors.As(resolveErr, &unavailable) {
			return out, nil
		}
		return assembly.ResolvedAssembly{}, fmt.Errorf("gatewayapp: resolve Reviewer placement: %w", resolveErr)
	}
	if s.lookup == nil {
		return assembly.ResolvedAssembly{}, fmt.Errorf("gatewayapp: resolve Reviewer model: model lookup unavailable")
	}
	reviewerModel, resolveErr := s.lookup.ResolveConfig(placement.Model)
	if resolveErr != nil {
		return assembly.ResolvedAssembly{}, fmt.Errorf("gatewayapp: resolve Reviewer placement %q: %w", placement.ProfileID, resolveErr)
	}
	if placement.ReasoningEffort != "" {
		reviewerModel.ReasoningEffort = placement.ReasoningEffort
	}
	reviewer, err := configuredModelSpawnedSelfACPAgent(defaultSpawnedSelfACPAgentConfig{
		Config: Config{
			AppName:                   s.AppName,
			UserID:                    s.UserID,
			StoreDir:                  s.storeDir,
			ControlOperationRetention: s.controlOperationRetention,
			WorkspaceKey:              s.Workspace.Key,
			WorkspaceCWD:              s.Workspace.CWD,
			PolicyProfile:             runtimeCfg.PolicyProfile,
			ContextWindow:             runtimeCfg.ContextWindow,
			SystemPrompt:              fixedReviewerSystemPrompt(runtimeCfg.SystemPrompt),
			Model:                     reviewerModel,
		},
		AppName:      s.AppName,
		UserID:       s.UserID,
		StoreDir:     s.storeDir,
		WorkspaceKey: s.Workspace.Key,
		WorkspaceCWD: s.Workspace.CWD,
	})
	if err != nil {
		return assembly.ResolvedAssembly{}, fmt.Errorf("gatewayapp: materialize fixed Reviewer scene: %w", err)
	}
	reviewer.Name = ReviewerAgentID
	reviewer.Description = "Review current workspace changes"
	reviewer.Env = withSystemSceneEnv(reviewer.Env, ReviewerAgentID)
	out.Agents = append(out.Agents, reviewer)
	return out, nil
}

func (s *Stack) withExternalACPAgents(resolved assembly.ResolvedAssembly, runtimeCfg stackRuntimeConfig) (assembly.ResolvedAssembly, error) {
	out := assembly.CloneResolvedAssembly(resolved)
	if s == nil || s.store == nil {
		return out, nil
	}
	doc, err := s.store.Load()
	if err != nil {
		return assembly.ResolvedAssembly{}, fmt.Errorf("gatewayapp: load external Agent configuration: %w", err)
	}
	if err := controlagents.ValidateConfiguration(doc.ExternalAgents); err != nil {
		return assembly.ResolvedAssembly{}, fmt.Errorf("gatewayapp: invalid external Agent configuration: %w", err)
	}
	seen := make(map[string]struct{}, len(out.Agents))
	for _, existing := range out.Agents {
		if id := strings.ToLower(strings.TrimSpace(existing.Name)); id != "" {
			seen[id] = struct{}{}
		}
	}
	for _, agent := range controlagents.ListAgents(doc.ExternalAgents) {
		if forbiddenExternalAgentID(agent.ID) {
			return assembly.ResolvedAssembly{}, fmt.Errorf("gatewayapp: external Agent %q conflicts with a product command or system Agent", agent.ID)
		}
		if _, exists := seen[agent.ID]; exists {
			return assembly.ResolvedAssembly{}, fmt.Errorf("gatewayapp: external Agent %q conflicts with an existing Agent", agent.ID)
		}
		resolvedAgent, connection, err := controlagents.ResolveAgent(doc.ExternalAgents, agent.ID)
		if err != nil {
			return assembly.ResolvedAssembly{}, fmt.Errorf("gatewayapp: resolve external Agent %q: %w", agent.ID, err)
		}
		materialized, err := s.materializeExternalAgent(resolvedAgent, connection)
		if err != nil {
			return assembly.ResolvedAssembly{}, err
		}
		out.Agents = append(out.Agents, materialized)
		seen[agent.ID] = struct{}{}
	}
	return out, nil
}

func (s *Stack) materializeExternalAgent(agent controlagents.Agent, connection controlagents.Connection) (assembly.AgentConfig, error) {
	if strings.TrimSpace(agent.ConnectionID) == "" || !strings.EqualFold(agent.ConnectionID, connection.ID) {
		return assembly.AgentConfig{}, fmt.Errorf("gatewayapp: external Agent %q has no matching ACP connection", agent.ID)
	}
	return assembly.AgentConfig{
		Name:        agent.ID,
		Description: agent.Name,
		Command:     connection.Launcher.Command,
		Args:        append([]string(nil), connection.Launcher.Args...),
		Env:         maps.Clone(connection.Launcher.Env),
		WorkDir:     connection.Launcher.WorkDir,
	}, nil
}

func (s *Stack) withPluginACPAgents(resolved assembly.ResolvedAssembly, pluginAgents []pluginAgentContribution) (assembly.ResolvedAssembly, error) {
	out := assembly.CloneResolvedAssembly(resolved)
	seen := map[string]struct{}{}
	for _, agent := range out.Agents {
		if name := strings.ToLower(strings.TrimSpace(agent.Name)); name != "" {
			seen[name] = struct{}{}
		}
	}
	for _, contributed := range pluginAgents {
		agent, err := pluginAgentContributionToAssembly(contributed.PluginID, contributed.Agent)
		if err != nil {
			return out, err
		}
		nameKey := strings.ToLower(strings.TrimSpace(agent.Name))
		if nameKey == "" {
			continue
		}
		if _, exists := seen[nameKey]; exists {
			return assembly.ResolvedAssembly{}, fmt.Errorf("gatewayapp: plugin %q agent %q conflicts with an existing Agent", strings.TrimSpace(contributed.PluginID), agent.Name)
		}
		out.Agents = append(out.Agents, agent)
		seen[nameKey] = struct{}{}
	}
	return out, nil
}

func pluginAgentContributionToAssembly(pluginID string, in pluginapi.AgentContribution) (assembly.AgentConfig, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return assembly.AgentConfig{}, fmt.Errorf("gatewayapp: plugin %q agent name is required", strings.TrimSpace(pluginID))
	}
	if forbiddenExternalAgentID(name) {
		return assembly.AgentConfig{}, fmt.Errorf("gatewayapp: plugin %q agent %q conflicts with a product command or system Agent", strings.TrimSpace(pluginID), name)
	}
	command := strings.TrimSpace(in.Command)
	if command == "" {
		return assembly.AgentConfig{}, fmt.Errorf("gatewayapp: command is required for plugin %q agent %q", strings.TrimSpace(pluginID), name)
	}
	return assembly.AgentConfig{
		Name:        name,
		Description: strings.TrimSpace(in.Description),
		Command:     command,
		Args:        append([]string(nil), in.Args...),
		Env:         maps.Clone(in.Env),
		WorkDir:     strings.TrimSpace(in.WorkDir),
	}, nil
}

func withSystemSceneEnv(env map[string]string, sceneID string) map[string]string {
	out := map[string]string{}
	for key, value := range env {
		out[key] = value
	}
	out[systemSceneEnvKey] = strings.TrimSpace(sceneID)
	out["SDK_ACP_ENABLE_SPAWN"] = "0"
	out["SDK_ACP_CHILD_NO_SPAWN"] = "1"
	return out
}

func isSystemSceneAgent(agent assembly.AgentConfig) bool {
	return strings.TrimSpace(agent.Env[systemSceneEnvKey]) != ""
}

func builtinACPAdapterInstallSpec(pkg builtinACPAdapterPackage) string {
	if strings.TrimSpace(pkg.Version) != "" {
		return strings.TrimSpace(pkg.Package) + "@" + strings.TrimSpace(pkg.Version)
	}
	return strings.TrimSpace(pkg.Package) + "@latest"
}

func npmInstallSpecForExec(npmPath string, spec string) string {
	if goruntime.GOOS != "windows" {
		return spec
	}
	switch strings.ToLower(filepath.Ext(strings.TrimSpace(npmPath))) {
	case ".bat", ".cmd":
		return strings.ReplaceAll(spec, "^", "^^^^")
	default:
		return spec
	}
}

func lookupBuiltInACPAgent(name string) (assembly.AgentConfig, bool) {
	return agentregistry.LookupBuiltInAgent(name)
}

func reservedSlashCommandName(name string) bool {
	return agentregistry.ReservedSlashCommandName(name)
}

func (s *Stack) ACPControllerStatus(ctx context.Context, ref session.SessionRef) (controller.ControllerStatus, bool, error) {
	if s == nil {
		return controller.ControllerStatus{}, false, nil
	}
	s.mu.RLock()
	controlPlane := s.acpControlPlane
	s.mu.RUnlock()
	if controlPlane == nil {
		return controller.ControllerStatus{}, false, nil
	}
	return controlPlane.ControllerStatus(ctx, session.NormalizeSessionRef(ref))
}

func (s *Stack) SetACPControllerModel(ctx context.Context, ref session.SessionRef, model string, reasoningEffort string) (controller.ControllerStatus, error) {
	if s == nil {
		return controller.ControllerStatus{}, fmt.Errorf("gatewayapp: runtime engine unavailable")
	}
	if err := s.rejectReconfigureWhileActive("switch ACP model"); err != nil {
		return controller.ControllerStatus{}, err
	}
	s.mu.RLock()
	controlPlane := s.acpControlPlane
	s.mu.RUnlock()
	if controlPlane == nil {
		return controller.ControllerStatus{}, fmt.Errorf("gatewayapp: ACP control plane unavailable")
	}
	return controlPlane.SetControllerModel(ctx, controller.SetControllerModelRequest{
		SessionRef:      session.NormalizeSessionRef(ref),
		Model:           strings.TrimSpace(model),
		ReasoningEffort: strings.TrimSpace(reasoningEffort),
	})
}

func (s *Stack) SetACPControllerMode(ctx context.Context, ref session.SessionRef, mode string) (controller.ControllerStatus, error) {
	if s == nil {
		return controller.ControllerStatus{}, fmt.Errorf("gatewayapp: runtime engine unavailable")
	}
	s.mu.RLock()
	controlPlane := s.acpControlPlane
	s.mu.RUnlock()
	if controlPlane == nil {
		return controller.ControllerStatus{}, fmt.Errorf("gatewayapp: ACP control plane unavailable")
	}
	return controlPlane.SetControllerMode(ctx, controller.SetControllerModeRequest{
		SessionRef: session.NormalizeSessionRef(ref),
		Mode:       strings.TrimSpace(mode),
	})
}

func (s *Stack) ListACPAgents() []ACPAgentInfo {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	agents := append([]assembly.AgentConfig(nil), s.runtime.Assembly.Agents...)
	s.mu.RUnlock()
	if len(agents) == 0 {
		return nil
	}
	out := make([]ACPAgentInfo, 0, len(agents))
	for _, agent := range agents {
		name := strings.TrimSpace(agent.Name)
		if name == "" {
			continue
		}
		if strings.EqualFold(name, "self") {
			continue
		}
		if isSystemSceneAgent(agent) {
			continue
		}
		out = append(out, ACPAgentInfo{
			Name:        name,
			Description: strings.TrimSpace(agent.Description),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}
