// Package local wires the default local application stack for the new Caelis
// architecture.
package local

import (
	"context"
	"fmt"
	"maps"
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
	if sandboxRuntime == nil && (cfg.BuiltinTools || strings.TrimSpace(runtimeCfg.Sandbox.Backend) != "") {
		var err error
		sandboxRuntime, err = sandboxFromConfig(ctx, reg, runtimeCfg)
		if err != nil {
			return nil, err
		}
	}
	tools := cfg.Tools
	if tools == nil {
		toolList := append([]tool.Tool(nil), cfg.ToolList...)
		if cfg.BuiltinTools {
			if sandboxRuntime == nil {
				return nil, fmt.Errorf("app/local: sandbox runtime is required for builtin tools")
			}
			for _, name := range builtinToolNames() {
				factory, ok := reg.Tool(name)
				if !ok {
					return nil, fmt.Errorf("app/local: builtin tool %q is not registered", name)
				}
				item, err := factory(ctx, plugin.ToolConfig{Name: name, Sandbox: sandboxRuntime})
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
		AppName: runtimeCfg.AppName,
		Catalog: resourceCatalog,
	})
	if err != nil {
		return nil, err
	}
	approvalPolicy := cfg.Approval
	if approvalPolicy == nil && cfg.BuiltinTools {
		approvalPolicy = approval.AskTools(toolfilesystem.WriteFileToolName, toolfilesystem.PatchFileToolName)
	}
	approvalPolicy = approval.WithSessionMode(approvalPolicy)
	runner, err := loop.New(loop.Config{
		Provider:     provider,
		Tools:        tools,
		Approval:     approvalPolicy,
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
	externalAgents := append([]acpexternal.Config(nil), cfg.ExternalACPAgents...)
	externalAgents = append(externalAgents, pluginACPAgentConfigs(resourceCatalog)...)
	externalAgents = append(externalAgents, settingsACPAgentConfigs(cfg.Settings)...)
	svc, err := services.New(services.Config{
		Runtime:        runtimeCfg,
		Engine:         engine,
		Agents:         agentDescriptors(externalAgents),
		Invokers:       agentInvokers(store, externalAgents),
		InvokerFactory: externalAgentInvokerFactory(store),
		Resources:      resourceCatalog,
		Settings:       cfg.Settings,
		CodeFree:       codeFreeAuthAdapter{},
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

func externalAgentInvoker(store session.Store, cfg acpexternal.Config) services.AgentInvoker {
	id := firstNonEmpty(cfg.AgentID, cfg.AgentName, cfg.Command)
	return services.AgentInvokerFunc(func(ctx context.Context, req services.AgentInvokeRequest) (services.AgentInvokeResult, error) {
		client, err := acpexternal.StartProcess(ctx, cfg)
		if err != nil {
			return services.AgentInvokeResult{}, err
		}
		defer client.Close()
		adapter := externalAgentSession{client: client}
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

func (s externalAgentSession) Prompt(ctx context.Context, sessionID string, parts []model.ContentPart) ([]session.Event, error) {
	return s.client.PromptCore(ctx, sessionID, parts)
}

func (s externalAgentSession) Close() error {
	return s.client.Close()
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
			ID:      id,
			Name:    name,
			Kind:    services.AgentKindExternalACP,
			Command: strings.TrimSpace(cfg.Command),
			Args:    append([]string(nil), cfg.Args...),
			Env:     envMap(cfg.Env),
			WorkDir: strings.TrimSpace(cfg.WorkDir),
		})
	}
	return out
}

func builtinToolNames() []string {
	return []string{
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
	backend := strings.ToLower(firstNonEmpty(runtimeCfg.Sandbox.Backend, "host"))
	factory, ok := reg.SandboxBackend(backend)
	if !ok {
		return nil, fmt.Errorf("app/local: unsupported sandbox backend %q", runtimeCfg.Sandbox.Backend)
	}
	return factory.NewRuntime(ctx, sandbox.Config{
		CWD:           runtimeCfg.WorkspaceCWD,
		ReadableRoots: runtimeCfg.Sandbox.ReadableRoots,
		WritableRoots: runtimeCfg.Sandbox.WritableRoots,
		HelperPath:    runtimeCfg.Sandbox.HelperPath,
	})
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
