// Package local wires the default local application stack for the new Caelis
// architecture.
package local

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/OnslaughtSnail/caelis/core/config"
	"github.com/OnslaughtSnail/caelis/core/model"
	"github.com/OnslaughtSnail/caelis/core/plugin"
	"github.com/OnslaughtSnail/caelis/core/sandbox"
	"github.com/OnslaughtSnail/caelis/core/session"
	"github.com/OnslaughtSnail/caelis/core/tool"
	acpexternal "github.com/OnslaughtSnail/caelis/internal/adapters/acpagent/external"
	modelcodefree "github.com/OnslaughtSnail/caelis/internal/adapters/model/codefree"
	toolfilesystem "github.com/OnslaughtSnail/caelis/internal/adapters/tools/filesystem"
	toolplan "github.com/OnslaughtSnail/caelis/internal/adapters/tools/plan"
	toolregistry "github.com/OnslaughtSnail/caelis/internal/adapters/tools/registry"
	tooltask "github.com/OnslaughtSnail/caelis/internal/adapters/tools/task"
	appagents "github.com/OnslaughtSnail/caelis/internal/app/agents"
	appmodelrouter "github.com/OnslaughtSnail/caelis/internal/app/modelrouter"
	appprompt "github.com/OnslaughtSnail/caelis/internal/app/prompt"
	appregistry "github.com/OnslaughtSnail/caelis/internal/app/registry"
	appresources "github.com/OnslaughtSnail/caelis/internal/app/resources"
	"github.com/OnslaughtSnail/caelis/internal/app/services"
	appsettings "github.com/OnslaughtSnail/caelis/internal/app/settings"
	"github.com/OnslaughtSnail/caelis/internal/engine/approval"
	"github.com/OnslaughtSnail/caelis/internal/engine/control"
	enginegateway "github.com/OnslaughtSnail/caelis/internal/engine/gateway"
	"github.com/OnslaughtSnail/caelis/internal/engine/loop"
	"github.com/OnslaughtSnail/caelis/protocol/acp/schema"
)

type Config struct {
	Runtime           config.Runtime
	Model             config.ModelProfile
	Store             session.Store
	Provider          model.Provider
	Sandbox           sandbox.Runtime
	Tools             tool.Registry
	ToolList          []tool.Tool
	Approval          approval.Policy
	ExternalACPAgents []acpexternal.Config
	Contributions     []plugin.Contribution
	Settings          *appsettings.Manager
	SystemPrompt      string
	BuiltinTools      bool
	MaxToolSteps      int
}

type Stack struct {
	cfg      config.Runtime
	store    session.Store
	provider model.Provider
	sandbox  sandbox.Runtime
	tools    tool.Registry
	engine   *enginegateway.Gateway
	services services.Services
}

func New(cfg Config) (*Stack, error) {
	return NewWithContext(context.Background(), cfg)
}

func NewWithContext(ctx context.Context, cfg Config) (*Stack, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	runtimeCfg := normalizeRuntimeConfig(cfg.Runtime)
	reg, err := appregistry.NewDefault()
	if err != nil {
		return nil, err
	}
	if err := reg.Apply(ctx, cfg.Contributions...); err != nil {
		return nil, err
	}
	resourceCatalog, err := appresources.Discover(ctx, appresources.Request{
		WorkspaceDir:  runtimeCfg.WorkspaceCWD,
		PluginSources: runtimeCfg.Plugins,
	})
	if err != nil {
		return nil, err
	}
	if err := reg.ApplyCatalog(resourceCatalog); err != nil {
		return nil, err
	}
	externalAgents := append([]acpexternal.Config(nil), cfg.ExternalACPAgents...)
	externalAgents = append(externalAgents, pluginACPAgentConfigs(resourceCatalog)...)
	externalAgents = append(externalAgents, settingsACPAgentConfigs(cfg.Settings)...)
	externalAgents = appendDefaultSelfACPAgent(ctx, runtimeCfg, cfg.Model, cfg.Settings, externalAgents)
	spawnAgentDescriptors := pluginACPAgentDescriptors(externalAgents)
	provider := cfg.Provider
	if provider == nil {
		var err error
		if cfg.Settings != nil {
			provider, err = appmodelrouter.New(cfg.Settings, reg)
		} else {
			provider, err = providerFromConfig(ctx, reg, runtimeCfg, cfg.Model)
		}
		if err != nil {
			return nil, err
		}
	}
	store := cfg.Store
	if store == nil {
		var err error
		store, err = storeFromConfig(ctx, reg, runtimeCfg.Store)
		if err != nil {
			return nil, err
		}
	}
	sandboxRuntime := cfg.Sandbox
	var liveSandbox *liveSandboxRuntime
	if sandboxRuntime == nil && (cfg.BuiltinTools || strings.TrimSpace(runtimeCfg.Sandbox.Backend) != "") {
		var err error
		sandboxRuntime, err = sandboxFromConfig(ctx, reg, runtimeCfg)
		if err != nil {
			return nil, err
		}
	}
	if sandboxRuntime != nil {
		liveSandbox, err = newLiveSandboxRuntime(sandboxRuntime)
		if err != nil {
			return nil, err
		}
		sandboxRuntime = liveSandbox
	}
	spawnTasks := newSpawnTaskManager(store, externalAgents, taskStateDir(runtimeCfg.Store))
	tools := cfg.Tools
	if tools == nil {
		toolList := append([]tool.Tool(nil), cfg.ToolList...)
		if cfg.BuiltinTools {
			if sandboxRuntime == nil {
				return nil, fmt.Errorf("app/local: sandbox runtime is required for builtin tools")
			}
			for _, name := range builtinToolNames(len(externalAgents) > 0) {
				factory, ok := reg.Tool(name)
				if !ok {
					return nil, fmt.Errorf("app/local: builtin tool %q is not registered", name)
				}
				var item tool.Tool
				if name == tooltask.ToolName {
					item, err = tooltask.NewWithResolver(sandboxRuntime, spawnTasks)
				} else {
					item, err = factory(ctx, plugin.ToolConfig{Name: name, Sandbox: sandboxRuntime, ACPAgents: spawnAgentDescriptors})
				}
				if err != nil {
					return nil, err
				}
				toolList = append(toolList, item)
			}
		}
		reg, err := toolregistry.New(toolList...)
		if err != nil {
			return nil, err
		}
		tools = reg
	}
	resourceCatalog = mergeRegistryResources(resourceCatalog, reg)
	instructions, err := appprompt.BuildInstructions(ctx, appprompt.Config{
		AppName:      runtimeCfg.AppName,
		WorkspaceDir: runtimeCfg.WorkspaceCWD,
		BasePrompt:   firstNonEmpty(cfg.SystemPrompt, runtimeMetaString(runtimeCfg.Meta, "system_prompt")),
		Catalog:      resourceCatalog,
		SkillPolicy:  skillPolicyFromSettings(cfg.Settings),
		ACPAgents:    spawnAgentDescriptors,
	})
	if err != nil {
		return nil, err
	}
	approvalPolicy := cfg.Approval
	if approvalPolicy == nil && cfg.BuiltinTools {
		approvalPolicy = approval.WithModelReview(
			approval.BuiltinToolsPolicy(),
			provider,
		)
	}
	approvalPolicy = approval.WithSessionMode(approvalPolicy)
	approvalPolicy = approval.WithSandboxEscalation(approvalPolicy)
	runner, err := loop.New(loop.Config{
		Provider:     provider,
		Tools:        tools,
		Approval:     approvalPolicy,
		Spawner:      newSpawnDelegator(externalAgents, spawnTasks),
		Instructions: instructions,
		MaxToolSteps: cfg.MaxToolSteps,
	})
	if err != nil {
		return nil, err
	}
	engine, err := enginegateway.New(enginegateway.Config{
		Store:  store,
		Runner: runner,
	})
	if err != nil {
		return nil, err
	}
	svc, err := services.New(services.Config{
		Runtime:        runtimeCfg,
		Engine:         engine,
		Sandbox:        sandboxRuntime,
		TaskResolver:   spawnTasks,
		ModelProvider:  modelProviderFactory(reg),
		Agents:         agentDescriptors(externalAgents),
		BuiltinAgents:  pluginAgentDescriptors(appagents.BuiltinACPAgents()),
		Invokers:       agentInvokers(store, externalAgents),
		InvokerFactory: externalAgentInvokerFactory(store),
		AgentInstaller: newBuiltinAgentInstaller(runtimeCfg),
		Resources:      resourceCatalog,
		Settings:       cfg.Settings,
		CodeFree:       codeFreeAuthAdapter{},
		ApplyRuntime:   sandboxRuntimeApplier(reg, liveSandbox),
	})
	if err != nil {
		return nil, err
	}
	return &Stack{
		cfg:      runtimeCfg,
		store:    store,
		provider: provider,
		sandbox:  sandboxRuntime,
		tools:    tools,
		engine:   engine,
		services: svc,
	}, nil
}

func sandboxRuntimeApplier(reg *appregistry.Registry, live *liveSandboxRuntime) services.RuntimeApplier {
	if reg == nil || live == nil {
		return nil
	}
	return func(ctx context.Context, runtimeCfg config.Runtime) (config.Runtime, error) {
		runtimeCfg = normalizeRuntimeConfig(runtimeCfg)
		next, err := sandboxFromConfig(ctx, reg, runtimeCfg)
		if err != nil {
			return config.Runtime{}, err
		}
		if err := live.replace(next); err != nil {
			_ = next.Close()
			return config.Runtime{}, err
		}
		return runtimeCfg, nil
	}
}

func agentInvokers(store session.Store, configs []acpexternal.Config) map[string]services.AgentInvoker {
	if len(configs) == 0 {
		return nil
	}
	out := map[string]services.AgentInvoker{}
	for _, cfg := range configs {
		cfg := cfg
		id := firstNonEmpty(cfg.AgentID, cfg.AgentName, cfg.Command)
		if id == "" {
			continue
		}
		out[id] = externalAgentInvoker(store, cfg)
	}
	return out
}

func externalAgentInvokerFactory(store session.Store) services.AgentInvokerFactory {
	return func(agent services.AgentDescriptor) (services.AgentInvoker, error) {
		cfg := acpexternal.Config{
			AgentID:   firstNonEmpty(agent.ID, agent.Name, agent.Command),
			AgentName: firstNonEmpty(agent.Name, agent.ID, agent.Command),
			Command:   strings.TrimSpace(agent.Command),
			Args:      append([]string(nil), agent.Args...),
			WorkDir:   strings.TrimSpace(agent.WorkDir),
			Env:       envList(agent.Env),
		}
		if strings.TrimSpace(cfg.AgentID) == "" || strings.TrimSpace(cfg.Command) == "" {
			return nil, fmt.Errorf("app/local: external ACP agent id and command are required")
		}
		return externalAgentInvoker(store, cfg), nil
	}
}

func modelProviderFactory(reg *appregistry.Registry) services.ModelProviderFactory {
	return func(ctx context.Context, cfg appsettings.ModelConfig) (model.Provider, error) {
		if reg == nil {
			return nil, fmt.Errorf("app/local: model provider registry is required")
		}
		cfg = appsettings.NormalizeModelConfig(cfg)
		providerName := strings.ToLower(firstNonEmpty(cfg.Provider, "openai_compatible"))
		factory, ok := reg.ModelProvider(providerName)
		if !ok {
			return nil, fmt.Errorf("app/local: unsupported model provider %q", cfg.Provider)
		}
		return factory(ctx, plugin.ModelProviderConfig{
			ID:              cfg.ID,
			Profile:         cfg.ProfileID,
			Provider:        providerName,
			Endpoint:        cfg.BaseURL,
			Model:           cfg.Model,
			Token:           cfg.Token,
			TokenEnv:        cfg.TokenEnv,
			AuthType:        cfg.AuthType,
			HeaderKey:       cfg.HeaderKey,
			MaxOutputTokens: cfg.MaxOutputTokens,
			Meta:            maps.Clone(cfg.Meta),
		})
	}
}

func externalAgentInvoker(store session.Store, cfg acpexternal.Config) services.AgentInvoker {
	id := firstNonEmpty(cfg.AgentID, cfg.AgentName, cfg.Command)
	return services.AgentInvokerFunc(func(ctx context.Context, req services.AgentInvokeRequest) (services.AgentInvokeResult, error) {
		client, err := acpexternal.StartProcess(ctx, cfg)
		if err != nil {
			return services.AgentInvokeResult{}, err
		}
		defer client.Close()
		adapter := externalAgentSession{client: client}
		if req.Controller.Kind == session.ControllerACP || strings.TrimSpace(req.Controller.ID) != "" {
			runner := control.ControllerRunner{Store: store}
			controller := req.Controller
			if strings.TrimSpace(controller.ID) == "" {
				controller.ID = id
			}
			if controller.Kind == "" {
				controller.Kind = session.ControllerACP
			}
			controller.AgentName = firstNonEmpty(controller.AgentName, cfg.AgentName, id)
			controller.Label = firstNonEmpty(controller.Label, cfg.AgentName, id)
			controller.Source = firstNonEmpty(controller.Source, "external_acp")
			result, err := runner.Invoke(ctx, control.ControllerRequest{
				SessionRef:                req.SessionRef,
				Input:                     req.Input,
				ContentParts:              req.ContentParts,
				Controller:                controller,
				ControllerModel:           req.ControllerModel,
				ControllerReasoningEffort: req.ControllerReasoningEffort,
				ControllerMode:            req.ControllerMode,
				Agent:                     adapter,
			})
			if err != nil {
				return services.AgentInvokeResult{}, err
			}
			return services.AgentInvokeResult{
				StopReason:              "",
				Events:                  result.Events,
				Recorded:                true,
				ControllerConfigOptions: result.ConfigOptions,
			}, nil
		}
		runner := control.ParticipantRunner{Store: store}
		participant := req.Participant
		if strings.TrimSpace(participant.ID) == "" {
			participant.ID = id
		}
		if participant.Kind == "" {
			participant.Kind = session.ParticipantACP
		}
		if participant.Role == "" {
			participant.Role = session.ParticipantDelegated
		}
		participant.AgentName = firstNonEmpty(participant.AgentName, cfg.AgentName, id)
		participant.Label = firstNonEmpty(participant.Label, cfg.AgentName, id)
		participant.Source = firstNonEmpty(participant.Source, "external_acp")
		result, err := runner.Invoke(ctx, control.ParticipantRequest{
			SessionRef:   req.SessionRef,
			Input:        req.Input,
			ContentParts: req.ContentParts,
			Participant:  participant,
			Agent:        adapter,
		})
		if err != nil {
			return services.AgentInvokeResult{}, err
		}
		return services.AgentInvokeResult{
			StopReason: "",
			Events:     result.Events,
			Recorded:   true,
		}, nil
	})
}

type externalAgentSession struct {
	client *acpexternal.Client
}

type codeFreeAuthAdapter struct{}

func (codeFreeAuthAdapter) EnsureAuth(ctx context.Context, req services.CodeFreeAuthRequest) (services.CodeFreeAuthResult, error) {
	result, err := modelcodefree.EnsureAuth(ctx, modelcodefree.AuthOptions{
		BaseURL:         req.BaseURL,
		OpenBrowser:     req.OpenBrowser,
		CallbackTimeout: req.CallbackTimeout,
	})
	return codeFreeAuthResult(result, false), err
}

func (codeFreeAuthAdapter) EnsureModelSelectionAuth(ctx context.Context, req services.CodeFreeAuthRequest) (services.CodeFreeAuthResult, error) {
	started, err := modelcodefree.EnsureModelSelectionAuth(ctx, modelcodefree.AuthOptions{
		BaseURL:         req.BaseURL,
		OpenBrowser:     req.OpenBrowser,
		CallbackTimeout: req.CallbackTimeout,
	})
	if err != nil {
		return services.CodeFreeAuthResult{}, err
	}
	result, err := modelcodefree.EnsureAuth(ctx, modelcodefree.AuthOptions{
		BaseURL:         req.BaseURL,
		OpenBrowser:     false,
		CallbackTimeout: req.CallbackTimeout,
	})
	if err != nil {
		return services.CodeFreeAuthResult{}, err
	}
	return codeFreeAuthResult(result, started), nil
}

func (codeFreeAuthAdapter) Refresh(ctx context.Context, req services.CodeFreeAuthRequest) (services.CodeFreeAuthResult, error) {
	result, err := modelcodefree.Refresh(ctx, modelcodefree.RefreshOptions{
		BaseURL: req.BaseURL,
	})
	return codeFreeAuthResult(result, false), err
}

func codeFreeAuthResult(in modelcodefree.AuthResult, loginStarted bool) services.CodeFreeAuthResult {
	return services.CodeFreeAuthResult{
		CredentialPath:   strings.TrimSpace(in.CredentialPath),
		BaseURL:          strings.TrimSpace(in.BaseURL),
		UserID:           strings.TrimSpace(in.UserID),
		ExpiresAt:        in.ExpiresAt,
		RefreshExpiresAt: in.RefreshExpiresAt,
		HasRefreshToken:  in.HasRefreshToken,
		LoginStarted:     loginStarted,
	}
}

func (s externalAgentSession) Initialize(ctx context.Context) error {
	return s.client.InitializeSession(ctx)
}

func (s externalAgentSession) NewSession(ctx context.Context, workspace session.Workspace) (string, error) {
	return s.client.NewCoreSession(ctx, workspace)
}

func (s externalAgentSession) NewSessionState(ctx context.Context, workspace session.Workspace) (control.AgentSessionState, error) {
	resp, err := s.client.NewSession(ctx, workspace.CWD)
	if err != nil {
		return control.AgentSessionState{}, err
	}
	return control.AgentSessionState{
		RemoteSessionID: strings.TrimSpace(resp.SessionID),
		ConfigOptions:   controlConfigOptions(resp.ConfigOptions),
	}, nil
}

func (s externalAgentSession) ResumeSessionState(ctx context.Context, remoteSessionID string, workspace session.Workspace) (control.AgentSessionState, error) {
	resp, err := s.client.ResumeSession(ctx, remoteSessionID, workspace.CWD)
	if err != nil {
		return control.AgentSessionState{}, err
	}
	return control.AgentSessionState{
		RemoteSessionID: strings.TrimSpace(remoteSessionID),
		ConfigOptions:   controlConfigOptions(resp.ConfigOptions),
	}, nil
}

func (s externalAgentSession) SetConfigOption(ctx context.Context, remoteSessionID string, configID string, value any) (control.AgentSessionState, error) {
	resp, err := s.client.SetConfigOption(ctx, remoteSessionID, configID, value)
	if err != nil {
		return control.AgentSessionState{}, err
	}
	return control.AgentSessionState{
		RemoteSessionID: strings.TrimSpace(remoteSessionID),
		ConfigOptions:   controlConfigOptions(resp.ConfigOptions),
	}, nil
}

func (s externalAgentSession) Prompt(ctx context.Context, sessionID string, parts []model.ContentPart) ([]session.Event, error) {
	return s.client.PromptCore(ctx, sessionID, parts)
}

func (s externalAgentSession) Close() error {
	return s.client.Close()
}

func controlConfigOptions(in []schema.SessionConfigOption) []control.ConfigOption {
	if len(in) == 0 {
		return nil
	}
	out := make([]control.ConfigOption, 0, len(in))
	for _, option := range in {
		item := control.ConfigOption{
			Type:         strings.TrimSpace(option.Type),
			ID:           strings.TrimSpace(option.ID),
			Name:         strings.TrimSpace(option.Name),
			Description:  strings.TrimSpace(option.Description),
			Category:     strings.TrimSpace(option.Category),
			CurrentValue: controlConfigCurrentValue(option.CurrentValue),
			Options:      controlConfigChoices(option.Options),
		}
		if item.ID == "" {
			continue
		}
		out = append(out, item)
	}
	return out
}

func controlConfigCurrentValue(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func controlConfigChoices(in []schema.SessionConfigSelectOption) []control.ConfigChoice {
	if len(in) == 0 {
		return nil
	}
	out := make([]control.ConfigChoice, 0, len(in))
	for _, choice := range in {
		value := strings.TrimSpace(choice.Value)
		name := strings.TrimSpace(choice.Name)
		if value == "" && name == "" {
			continue
		}
		out = append(out, control.ConfigChoice{
			Value:       value,
			Name:        name,
			Description: strings.TrimSpace(choice.Description),
		})
	}
	return out
}

func agentDescriptors(configs []acpexternal.Config) []services.AgentDescriptor {
	if len(configs) == 0 {
		return nil
	}
	out := make([]services.AgentDescriptor, 0, len(configs))
	for _, cfg := range configs {
		id := firstNonEmpty(cfg.AgentID, cfg.AgentName, cfg.Command)
		name := firstNonEmpty(cfg.AgentName, id)
		out = append(out, services.AgentDescriptor{
			ID:          id,
			Name:        name,
			Kind:        services.AgentKindExternalACP,
			Description: strings.TrimSpace(cfg.Description),
			Command:     strings.TrimSpace(cfg.Command),
			Args:        append([]string(nil), cfg.Args...),
			Env:         envMap(cfg.Env),
			WorkDir:     strings.TrimSpace(cfg.WorkDir),
		})
	}
	return out
}

func pluginAgentDescriptors(agents []plugin.ACPAgentDescriptor) []services.AgentDescriptor {
	if len(agents) == 0 {
		return nil
	}
	out := make([]services.AgentDescriptor, 0, len(agents))
	for _, agent := range agents {
		name := strings.ToLower(strings.TrimSpace(agent.Name))
		command := strings.TrimSpace(agent.Command)
		id := firstNonEmpty(name, command)
		if id == "" {
			continue
		}
		out = append(out, services.AgentDescriptor{
			ID:          id,
			Name:        firstNonEmpty(name, id),
			Kind:        services.AgentKindExternalACP,
			Command:     command,
			Args:        append([]string(nil), agent.Args...),
			Env:         maps.Clone(agent.Env),
			WorkDir:     strings.TrimSpace(agent.WorkDir),
			Description: strings.TrimSpace(agent.Description),
		})
	}
	return out
}

func builtinToolNames(includeSpawn bool) []string {
	names := []string{
		toolfilesystem.ReadFileToolName,
		toolfilesystem.ListDirectoryToolName,
		toolfilesystem.GlobFilesToolName,
		toolfilesystem.SearchFilesToolName,
		toolfilesystem.WriteFileToolName,
		toolfilesystem.PatchFileToolName,
		toolplan.ToolName,
		"run_command",
		tooltask.ToolName,
	}
	if includeSpawn {
		names = append(names, "SPAWN")
	}
	return names
}

func pluginACPAgentConfigs(catalog appresources.Catalog) []acpexternal.Config {
	if len(catalog.ACPAgents) == 0 {
		return nil
	}
	out := make([]acpexternal.Config, 0, len(catalog.ACPAgents))
	for _, agent := range catalog.ACPAgents {
		cfg := appregistry.ACPAgentConfig(agent)
		if strings.TrimSpace(cfg.AgentID) == "" || strings.TrimSpace(cfg.Command) == "" {
			continue
		}
		out = append(out, cfg)
	}
	return out
}

func pluginACPAgentDescriptors(configs []acpexternal.Config) []plugin.ACPAgentDescriptor {
	if len(configs) == 0 {
		return nil
	}
	out := make([]plugin.ACPAgentDescriptor, 0, len(configs))
	for _, cfg := range configs {
		name := firstNonEmpty(cfg.AgentName, cfg.AgentID, cfg.Command)
		if name == "" {
			continue
		}
		out = append(out, plugin.ACPAgentDescriptor{
			Name:        name,
			Description: strings.TrimSpace(cfg.Description),
			Command:     strings.TrimSpace(cfg.Command),
			Args:        append([]string(nil), cfg.Args...),
			Env:         envMap(cfg.Env),
			WorkDir:     strings.TrimSpace(cfg.WorkDir),
		})
	}
	return out
}

func settingsACPAgentConfigs(manager *appsettings.Manager) []acpexternal.Config {
	if manager == nil {
		return nil
	}
	agents := manager.ListACPAgents()
	if len(agents) == 0 {
		return nil
	}
	out := make([]acpexternal.Config, 0, len(agents))
	for _, agent := range agents {
		cfg := appregistry.ACPAgentConfig(agent)
		if strings.TrimSpace(cfg.AgentID) == "" || strings.TrimSpace(cfg.Command) == "" {
			continue
		}
		out = append(out, cfg)
	}
	return out
}

func envList(values map[string]string) []string {
	if len(values) == 0 {
		return nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		if strings.TrimSpace(key) != "" {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, strings.TrimSpace(key)+"="+values[key])
	}
	return out
}

func envMap(values []string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := map[string]string{}
	for _, value := range values {
		key, val, ok := strings.Cut(value, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			continue
		}
		out[key] = val
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func mergeRegistryResources(catalog appresources.Catalog, reg *appregistry.Registry) appresources.Catalog {
	catalog.Prompts = append(catalog.Prompts, reg.Prompts()...)
	catalog.Skills = append(catalog.Skills, reg.Skills()...)
	catalog.ACPAgents = append(catalog.ACPAgents, reg.ACPAgents()...)
	catalog.RendererHints = append(catalog.RendererHints, reg.RendererHints()...)
	return appresources.CloneCatalog(catalog)
}

func providerFromConfig(ctx context.Context, reg *appregistry.Registry, runtimeCfg config.Runtime, profile config.ModelProfile) (model.Provider, error) {
	profile.Provider = strings.TrimSpace(profile.Provider)
	profile.Model = firstNonEmpty(profile.Model, runtimeCfg.Model)
	if profile.Provider == "" && profile.Model == "" && strings.TrimSpace(profile.BaseURL) == "" {
		return nil, fmt.Errorf("app/local: model provider is required")
	}
	providerName := strings.ToLower(firstNonEmpty(profile.Provider, "openai_compatible"))
	factory, ok := reg.ModelProvider(providerName)
	if !ok {
		return nil, fmt.Errorf("app/local: unsupported model provider %q", profile.Provider)
	}
	return factory(ctx, plugin.ModelProviderConfig{
		ID:              profile.ID,
		Profile:         profile.Provider,
		Provider:        providerName,
		Endpoint:        profile.BaseURL,
		Model:           profile.Model,
		Token:           profile.Token,
		TokenEnv:        profile.TokenEnv,
		AuthType:        profile.AuthType,
		HeaderKey:       profile.HeaderKey,
		MaxOutputTokens: profile.MaxOutputTokens,
		Meta:            maps.Clone(profile.Meta),
	})
}

func storeFromConfig(ctx context.Context, reg *appregistry.Registry, cfg config.Store) (session.Store, error) {
	backend := strings.ToLower(firstNonEmpty(cfg.Backend, "memory"))
	factory, ok := reg.Store(backend)
	if !ok {
		return nil, fmt.Errorf("app/local: unsupported store backend %q", cfg.Backend)
	}
	return factory(ctx, plugin.StoreConfig{
		Backend: backend,
		URI:     strings.TrimSpace(cfg.URI),
		Meta:    maps.Clone(cfg.Meta),
	})
}

func sandboxFromConfig(ctx context.Context, reg *appregistry.Registry, runtimeCfg config.Runtime) (sandbox.Runtime, error) {
	backend := normalizeSandboxBackendName(runtimeCfg.Sandbox.Backend)
	factory, ok := reg.SandboxBackend(backend)
	if !ok {
		return nil, fmt.Errorf("app/local: unsupported sandbox backend %q", runtimeCfg.Sandbox.Backend)
	}
	return factory.NewRuntime(ctx, sandbox.Config{
		CWD:              runtimeCfg.WorkspaceCWD,
		RequestedBackend: sandbox.Backend(backend),
		StateDir:         sandboxStateDir(runtimeCfg.Store),
		ReadableRoots:    slices.Clone(runtimeCfg.Sandbox.ReadableRoots),
		WritableRoots:    effectiveSandboxWritableRoots(runtimeCfg),
		HelperPath:       runtimeCfg.Sandbox.HelperPath,
	})
}

func normalizeSandboxBackendName(backend string) string {
	switch strings.ToLower(strings.TrimSpace(backend)) {
	case "", "auto", "default":
		return string(sandbox.BackendHost)
	case "windows", "windows-restricted-token", "windows_restricted_token", "windows-elevated", "windows_elevated", "windows elevated", "elevated":
		return string(sandbox.BackendWindows)
	default:
		return strings.ToLower(strings.TrimSpace(backend))
	}
}

func effectiveSandboxWritableRoots(runtimeCfg config.Runtime) []string {
	roots := slices.Clone(runtimeCfg.Sandbox.WritableRoots)
	homeDir, err := os.UserHomeDir()
	if err != nil {
		homeDir = ""
	}
	roots = append(roots, appresources.SkillRoots(homeDir, runtimeCfg.WorkspaceCWD, nil)...)
	return dedupeRootPaths(roots)
}

func skillPolicyFromSettings(manager *appsettings.Manager) appsettings.SkillPolicy {
	if manager == nil {
		return appsettings.SkillPolicy{}
	}
	return manager.SkillPolicy()
}

func dedupeRootPaths(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	out := make([]string, 0, len(paths))
	seen := map[string]struct{}{}
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		clean := filepath.Clean(path)
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		out = append(out, clean)
	}
	return out
}

func sandboxStateDir(store config.Store) string {
	backend := strings.ToLower(strings.TrimSpace(store.Backend))
	if backend == "memory" {
		return ""
	}
	uri := strings.TrimSpace(store.URI)
	if uri == "" {
		return ""
	}
	if backend == "sqlite" {
		return filepath.Join(filepath.Dir(uri), "sandbox")
	}
	return filepath.Join(uri, "sandbox")
}

func taskStateDir(store config.Store) string {
	backend := strings.ToLower(firstNonEmpty(store.Backend, "memory"))
	if backend == "memory" {
		return ""
	}
	uri := strings.TrimSpace(store.URI)
	if uri == "" {
		return ""
	}
	if backend == "sqlite" {
		return filepath.Join(filepath.Dir(uri), "tasks")
	}
	return filepath.Join(uri, "tasks")
}

func (s *Stack) Services() services.Services {
	if s == nil {
		return services.Services{}
	}
	return s.services
}

func (s *Stack) Engine() *enginegateway.Gateway {
	if s == nil {
		return nil
	}
	return s.engine
}

func (s *Stack) Store() session.Store {
	if s == nil {
		return nil
	}
	return s.store
}

func (s *Stack) Tools() tool.Registry {
	if s == nil {
		return nil
	}
	return s.tools
}

func (s *Stack) Provider() model.Provider {
	if s == nil {
		return nil
	}
	return s.provider
}

func (s *Stack) Sandbox() sandbox.Runtime {
	if s == nil {
		return nil
	}
	return s.sandbox
}

func (s *Stack) Runtime() config.Runtime {
	if s == nil {
		return config.Runtime{}
	}
	return cloneRuntimeConfig(s.cfg)
}

func normalizeRuntimeConfig(cfg config.Runtime) config.Runtime {
	cfg = cloneRuntimeConfig(cfg)
	cfg.AppName = firstNonEmpty(cfg.AppName, "caelis")
	cfg.UserID = firstNonEmpty(cfg.UserID, "local-user")
	cfg.WorkspaceKey = strings.TrimSpace(cfg.WorkspaceKey)
	cfg.WorkspaceCWD = strings.TrimSpace(cfg.WorkspaceCWD)
	return cfg
}

func cloneRuntimeConfig(in config.Runtime) config.Runtime {
	out := in
	out.AppName = strings.TrimSpace(in.AppName)
	out.UserID = strings.TrimSpace(in.UserID)
	out.WorkspaceKey = strings.TrimSpace(in.WorkspaceKey)
	out.WorkspaceCWD = strings.TrimSpace(in.WorkspaceCWD)
	out.Model = strings.TrimSpace(in.Model)
	out.Store.Meta = maps.Clone(in.Store.Meta)
	out.Sandbox.ReadableRoots = append([]string(nil), in.Sandbox.ReadableRoots...)
	out.Sandbox.WritableRoots = append([]string(nil), in.Sandbox.WritableRoots...)
	out.Plugins = append([]config.Plugin(nil), in.Plugins...)
	for i := range out.Plugins {
		out.Plugins[i].Meta = maps.Clone(in.Plugins[i].Meta)
	}
	out.Meta = maps.Clone(in.Meta)
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func runtimeMetaString(meta map[string]any, key string) string {
	if len(meta) == 0 {
		return ""
	}
	value, ok := meta[key]
	if !ok {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}
