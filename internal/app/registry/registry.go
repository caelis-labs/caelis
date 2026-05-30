// Package registry provides deterministic application registries for runtime
// contributions. It implements core/plugin.Registry without owning engine
// orchestration.
package registry

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/OnslaughtSnail/caelis/core/model"
	"github.com/OnslaughtSnail/caelis/core/plugin"
	"github.com/OnslaughtSnail/caelis/core/sandbox"
	"github.com/OnslaughtSnail/caelis/core/session"
	"github.com/OnslaughtSnail/caelis/core/tool"
	acpexternal "github.com/OnslaughtSnail/caelis/internal/adapters/acpagent/external"
	modelanthropic "github.com/OnslaughtSnail/caelis/internal/adapters/model/anthropic"
	modelollama "github.com/OnslaughtSnail/caelis/internal/adapters/model/ollama"
	modelopenai "github.com/OnslaughtSnail/caelis/internal/adapters/model/openai"
	sandboxhost "github.com/OnslaughtSnail/caelis/internal/adapters/sandbox/host"
	storejsonl "github.com/OnslaughtSnail/caelis/internal/adapters/store/jsonl"
	storememory "github.com/OnslaughtSnail/caelis/internal/adapters/store/memory"
	storesqlite "github.com/OnslaughtSnail/caelis/internal/adapters/store/sqlite"
	toolfilesystem "github.com/OnslaughtSnail/caelis/internal/adapters/tools/filesystem"
	toolplan "github.com/OnslaughtSnail/caelis/internal/adapters/tools/plan"
	toolshell "github.com/OnslaughtSnail/caelis/internal/adapters/tools/shell"
	tooltask "github.com/OnslaughtSnail/caelis/internal/adapters/tools/task"
	appresources "github.com/OnslaughtSnail/caelis/internal/app/resources"
)

type Registry struct {
	modelProviders map[string]plugin.ModelProviderFactory
	stores         map[string]plugin.StoreFactory
	sandboxes      map[string]sandbox.BackendFactory
	tools          map[string]plugin.ToolFactory
	acpAgents      []plugin.ACPAgentDescriptor
	prompts        []plugin.PromptFragment
	skills         []plugin.SkillDescriptor
	rendererHints  []plugin.RendererHint
}

func New() *Registry {
	return &Registry{
		modelProviders: map[string]plugin.ModelProviderFactory{},
		stores:         map[string]plugin.StoreFactory{},
		sandboxes:      map[string]sandbox.BackendFactory{},
		tools:          map[string]plugin.ToolFactory{},
	}
}

func NewDefault() (*Registry, error) {
	r := New()
	if err := RegisterDefaults(r); err != nil {
		return nil, err
	}
	return r, nil
}

func RegisterDefaults(r *Registry) error {
	if r == nil {
		return errors.New("app/registry: registry is nil")
	}
	for _, name := range []string{"openai", "openai_compatible", "openai-compatible"} {
		if err := r.RegisterModelProvider(name, openAIProviderFactory); err != nil {
			return err
		}
	}
	for _, name := range []string{"anthropic", "anthropic-compatible"} {
		if err := r.RegisterModelProvider(name, anthropicProviderFactory); err != nil {
			return err
		}
	}
	if err := r.RegisterModelProvider("minimax", miniMaxProviderFactory); err != nil {
		return err
	}
	if err := r.RegisterModelProvider("deepseek", deepSeekProviderFactory); err != nil {
		return err
	}
	if err := r.RegisterModelProvider("openrouter", openRouterProviderFactory); err != nil {
		return err
	}
	for _, name := range []string{"mimo", "xiaomi"} {
		if err := r.RegisterModelProvider(name, mimoProviderFactory); err != nil {
			return err
		}
	}
	if err := r.RegisterModelProvider("volcengine", volcengineProviderFactory); err != nil {
		return err
	}
	for _, name := range []string{"volcengine-coding-plan", "volcengine_coding_plan"} {
		if err := r.RegisterModelProvider(name, volcengineCodingProviderFactory); err != nil {
			return err
		}
	}
	if err := r.RegisterModelProvider("ollama", ollamaProviderFactory); err != nil {
		return err
	}
	if err := r.RegisterStore("memory", memoryStoreFactory); err != nil {
		return err
	}
	if err := r.RegisterStore("jsonl", jsonlStoreFactory); err != nil {
		return err
	}
	if err := r.RegisterStore("sqlite", sqliteStoreFactory); err != nil {
		return err
	}
	if err := r.RegisterSandboxBackend("host", sandboxhost.Factory{}); err != nil {
		return err
	}
	for _, item := range []struct {
		name    string
		factory plugin.ToolFactory
	}{
		{toolfilesystem.ReadFileToolName, readFileToolFactory},
		{toolfilesystem.ListDirectoryToolName, listDirectoryToolFactory},
		{toolfilesystem.GlobFilesToolName, globFilesToolFactory},
		{toolfilesystem.SearchFilesToolName, searchFilesToolFactory},
		{toolfilesystem.WriteFileToolName, writeFileToolFactory},
		{toolfilesystem.PatchFileToolName, patchFileToolFactory},
		{toolplan.ToolName, planToolFactory},
		{toolshell.RunCommandToolName, runCommandToolFactory},
		{tooltask.ToolName, taskToolFactory},
	} {
		if err := r.RegisterTool(item.name, item.factory); err != nil {
			return err
		}
	}
	return nil
}

func (r *Registry) Apply(ctx context.Context, contributions ...plugin.Contribution) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, contribution := range contributions {
		if contribution == nil {
			continue
		}
		if err := contribution.Register(ctx, r); err != nil {
			return err
		}
	}
	return nil
}

func (r *Registry) ApplyCatalog(catalog appresources.Catalog) error {
	if r == nil {
		return errors.New("app/registry: registry is nil")
	}
	for _, alias := range catalog.ModelProviders {
		source, ok := r.ModelProvider(alias.Uses)
		if !ok {
			return fmt.Errorf("app/registry: model provider alias %q uses unknown factory %q", alias.Name, alias.Uses)
		}
		if err := r.RegisterModelProvider(alias.Name, source); err != nil {
			return err
		}
	}
	for _, alias := range catalog.Stores {
		source, ok := r.Store(alias.Uses)
		if !ok {
			return fmt.Errorf("app/registry: store alias %q uses unknown factory %q", alias.Name, alias.Uses)
		}
		if err := r.RegisterStore(alias.Name, source); err != nil {
			return err
		}
	}
	for _, alias := range catalog.Sandboxes {
		source, ok := r.SandboxBackend(alias.Uses)
		if !ok {
			return fmt.Errorf("app/registry: sandbox backend alias %q uses unknown factory %q", alias.Name, alias.Uses)
		}
		if err := r.RegisterSandboxBackend(alias.Name, source); err != nil {
			return err
		}
	}
	for _, alias := range catalog.Tools {
		source, ok := r.Tool(alias.Uses)
		if !ok {
			return fmt.Errorf("app/registry: tool alias %q uses unknown factory %q", alias.Name, alias.Uses)
		}
		if err := r.RegisterTool(alias.Name, source); err != nil {
			return err
		}
	}
	return nil
}

func (r *Registry) RegisterModelProvider(name string, factory plugin.ModelProviderFactory) error {
	key, err := key(name, "model provider")
	if err != nil {
		return err
	}
	if factory == nil {
		return fmt.Errorf("app/registry: model provider %q factory is nil", key)
	}
	if _, exists := r.modelProviders[key]; exists {
		return fmt.Errorf("app/registry: model provider %q already registered", key)
	}
	r.modelProviders[key] = factory
	return nil
}

func (r *Registry) RegisterStore(name string, factory plugin.StoreFactory) error {
	key, err := key(name, "store")
	if err != nil {
		return err
	}
	if factory == nil {
		return fmt.Errorf("app/registry: store %q factory is nil", key)
	}
	if _, exists := r.stores[key]; exists {
		return fmt.Errorf("app/registry: store %q already registered", key)
	}
	r.stores[key] = factory
	return nil
}

func (r *Registry) RegisterSandboxBackend(name string, factory sandbox.BackendFactory) error {
	key, err := key(name, "sandbox backend")
	if err != nil {
		return err
	}
	if factory == nil {
		return fmt.Errorf("app/registry: sandbox backend %q factory is nil", key)
	}
	if _, exists := r.sandboxes[key]; exists {
		return fmt.Errorf("app/registry: sandbox backend %q already registered", key)
	}
	r.sandboxes[key] = factory
	return nil
}

func (r *Registry) RegisterTool(name string, factory plugin.ToolFactory) error {
	key, err := key(name, "tool")
	if err != nil {
		return err
	}
	if factory == nil {
		return fmt.Errorf("app/registry: tool %q factory is nil", key)
	}
	if _, exists := r.tools[key]; exists {
		return fmt.Errorf("app/registry: tool %q already registered", key)
	}
	r.tools[key] = factory
	return nil
}

func (r *Registry) RegisterACPAgent(agent plugin.ACPAgentDescriptor) error {
	if strings.TrimSpace(agent.Name) == "" && strings.TrimSpace(agent.Command) == "" {
		return errors.New("app/registry: acp agent name or command is required")
	}
	agent.Args = slices.Clone(agent.Args)
	agent.Env = maps.Clone(agent.Env)
	agent.Roles = slices.Clone(agent.Roles)
	r.acpAgents = append(r.acpAgents, agent)
	return nil
}

func (r *Registry) RegisterPrompt(prompt plugin.PromptFragment) error {
	if strings.TrimSpace(prompt.ID) == "" && strings.TrimSpace(prompt.Text) == "" && len(prompt.Paths) == 0 {
		return errors.New("app/registry: prompt id, text, or path is required")
	}
	prompt.Paths = slices.Clone(prompt.Paths)
	r.prompts = append(r.prompts, prompt)
	return nil
}

func (r *Registry) RegisterSkill(skill plugin.SkillDescriptor) error {
	if strings.TrimSpace(skill.Name) == "" {
		return errors.New("app/registry: skill name is required")
	}
	skill.Paths = slices.Clone(skill.Paths)
	r.skills = append(r.skills, skill)
	return nil
}

func (r *Registry) RegisterRendererHint(hint plugin.RendererHint) error {
	hint.Meta = maps.Clone(hint.Meta)
	r.rendererHints = append(r.rendererHints, hint)
	return nil
}

func (r *Registry) ModelProvider(name string) (plugin.ModelProviderFactory, bool) {
	if r == nil {
		return nil, false
	}
	factory, ok := r.modelProviders[normalizeKey(name)]
	return factory, ok
}

func (r *Registry) Store(name string) (plugin.StoreFactory, bool) {
	if r == nil {
		return nil, false
	}
	factory, ok := r.stores[normalizeKey(name)]
	return factory, ok
}

func (r *Registry) SandboxBackend(name string) (sandbox.BackendFactory, bool) {
	if r == nil {
		return nil, false
	}
	factory, ok := r.sandboxes[normalizeKey(name)]
	return factory, ok
}

func (r *Registry) Tool(name string) (plugin.ToolFactory, bool) {
	if r == nil {
		return nil, false
	}
	factory, ok := r.tools[normalizeKey(name)]
	return factory, ok
}

func (r *Registry) ACPAgents() []plugin.ACPAgentDescriptor {
	if r == nil || len(r.acpAgents) == 0 {
		return nil
	}
	out := make([]plugin.ACPAgentDescriptor, 0, len(r.acpAgents))
	for _, item := range r.acpAgents {
		item.Args = slices.Clone(item.Args)
		item.Env = maps.Clone(item.Env)
		item.Roles = slices.Clone(item.Roles)
		out = append(out, item)
	}
	return out
}

func (r *Registry) Prompts() []plugin.PromptFragment {
	if r == nil || len(r.prompts) == 0 {
		return nil
	}
	out := make([]plugin.PromptFragment, 0, len(r.prompts))
	for _, item := range r.prompts {
		item.Paths = slices.Clone(item.Paths)
		out = append(out, item)
	}
	return out
}

func (r *Registry) Skills() []plugin.SkillDescriptor {
	if r == nil || len(r.skills) == 0 {
		return nil
	}
	out := make([]plugin.SkillDescriptor, 0, len(r.skills))
	for _, item := range r.skills {
		item.Paths = slices.Clone(item.Paths)
		out = append(out, item)
	}
	return out
}

func (r *Registry) RendererHints() []plugin.RendererHint {
	if r == nil || len(r.rendererHints) == 0 {
		return nil
	}
	out := make([]plugin.RendererHint, 0, len(r.rendererHints))
	for _, item := range r.rendererHints {
		item.Meta = maps.Clone(item.Meta)
		out = append(out, item)
	}
	return out
}

const (
	anthropicDefaultBaseURL            = "https://api.anthropic.com"
	deepSeekDefaultBaseURL             = "https://api.deepseek.com/v1"
	miniMaxDefaultBaseURL              = "https://api.minimaxi.com/anthropic"
	mimoDefaultBaseURL                 = "https://api.xiaomimimo.com/v1"
	openRouterDefaultBaseURL           = "https://openrouter.ai/api/v1"
	volcengineDefaultBaseURL           = "https://ark.cn-beijing.volces.com/api/v3"
	volcengineCodingPlanDefaultBaseURL = "https://ark.cn-beijing.volces.com/api/coding/v3"
)

func openAIProviderFactory(_ context.Context, cfg plugin.ModelProviderConfig) (model.Provider, error) {
	token := modelToken(cfg)
	return modelopenai.New(modelopenai.Config{
		ID:              firstNonEmpty(cfg.ID, cfg.Provider, cfg.Profile, "openai_compatible"),
		BaseURL:         cfg.Endpoint,
		APIKey:          token,
		AuthHeader:      cfg.HeaderKey,
		Model:           cfg.Model,
		MaxOutputTokens: cfg.MaxOutputTokens,
	})
}

func anthropicProviderFactory(_ context.Context, cfg plugin.ModelProviderConfig) (model.Provider, error) {
	token := modelToken(cfg)
	return modelanthropic.New(modelanthropic.Config{
		ID:              firstNonEmpty(cfg.ID, cfg.Provider, cfg.Profile, "anthropic"),
		BaseURL:         cfg.Endpoint,
		DefaultBaseURL:  anthropicDefaultBaseURL,
		APIKey:          token,
		AuthHeader:      firstNonEmpty(cfg.HeaderKey, "x-api-key"),
		Model:           cfg.Model,
		MaxOutputTokens: cfg.MaxOutputTokens,
	})
}

func miniMaxProviderFactory(_ context.Context, cfg plugin.ModelProviderConfig) (model.Provider, error) {
	token := modelToken(cfg)
	return modelanthropic.New(modelanthropic.Config{
		ID:                     firstNonEmpty(cfg.ID, cfg.Provider, cfg.Profile, "minimax"),
		BaseURL:                cfg.Endpoint,
		DefaultBaseURL:         miniMaxDefaultBaseURL,
		APIKey:                 token,
		AuthHeader:             firstNonEmpty(cfg.HeaderKey, "Authorization"),
		Model:                  cfg.Model,
		MaxOutputTokens:        firstPositive(cfg.MaxOutputTokens, 4096),
		DefaultReasoningBudget: 4096,
	})
}

func deepSeekProviderFactory(_ context.Context, cfg plugin.ModelProviderConfig) (model.Provider, error) {
	token := modelToken(cfg)
	return modelopenai.New(modelopenai.Config{
		ID:              firstNonEmpty(cfg.ID, cfg.Provider, cfg.Profile, "deepseek"),
		BaseURL:         cfg.Endpoint,
		DefaultBaseURL:  deepSeekDefaultBaseURL,
		APIKey:          token,
		AuthHeader:      cfg.HeaderKey,
		Model:           cfg.Model,
		MaxOutputTokens: cfg.MaxOutputTokens,
		Flavor:          modelopenai.FlavorDeepSeek,
	})
}

func openRouterProviderFactory(_ context.Context, cfg plugin.ModelProviderConfig) (model.Provider, error) {
	token := modelToken(cfg)
	return modelopenai.New(modelopenai.Config{
		ID:              firstNonEmpty(cfg.ID, cfg.Provider, cfg.Profile, "openrouter"),
		BaseURL:         cfg.Endpoint,
		DefaultBaseURL:  openRouterDefaultBaseURL,
		APIKey:          token,
		AuthHeader:      cfg.HeaderKey,
		Model:           cfg.Model,
		MaxOutputTokens: cfg.MaxOutputTokens,
		Flavor:          modelopenai.FlavorOpenRouter,
	})
}

func mimoProviderFactory(_ context.Context, cfg plugin.ModelProviderConfig) (model.Provider, error) {
	token := modelToken(cfg)
	return modelopenai.New(modelopenai.Config{
		ID:              firstNonEmpty(cfg.ID, cfg.Provider, cfg.Profile, "xiaomi"),
		BaseURL:         cfg.Endpoint,
		DefaultBaseURL:  mimoDefaultBaseURL,
		APIKey:          token,
		AuthHeader:      cfg.HeaderKey,
		Model:           cfg.Model,
		MaxOutputTokens: cfg.MaxOutputTokens,
		Flavor:          modelopenai.FlavorMimo,
	})
}

func volcengineProviderFactory(_ context.Context, cfg plugin.ModelProviderConfig) (model.Provider, error) {
	token := modelToken(cfg)
	return modelopenai.New(modelopenai.Config{
		ID:              firstNonEmpty(cfg.ID, cfg.Provider, cfg.Profile, "volcengine"),
		BaseURL:         cfg.Endpoint,
		DefaultBaseURL:  volcengineDefaultBaseURL,
		APIKey:          token,
		AuthHeader:      cfg.HeaderKey,
		Model:           cfg.Model,
		MaxOutputTokens: cfg.MaxOutputTokens,
		Flavor:          modelopenai.FlavorVolcengine,
	})
}

func volcengineCodingProviderFactory(_ context.Context, cfg plugin.ModelProviderConfig) (model.Provider, error) {
	token := modelToken(cfg)
	return modelopenai.New(modelopenai.Config{
		ID:              firstNonEmpty(cfg.ID, cfg.Provider, cfg.Profile, "volcengine-coding-plan"),
		BaseURL:         cfg.Endpoint,
		DefaultBaseURL:  volcengineCodingPlanDefaultBaseURL,
		APIKey:          token,
		AuthHeader:      cfg.HeaderKey,
		Model:           cfg.Model,
		MaxOutputTokens: cfg.MaxOutputTokens,
		Flavor:          modelopenai.FlavorVolcengine,
	})
}

func modelToken(cfg plugin.ModelProviderConfig) string {
	token := strings.TrimSpace(cfg.Token)
	if env := strings.TrimSpace(cfg.TokenEnv); env != "" {
		if token == "" {
			token = strings.TrimSpace(os.Getenv(env))
		}
	}
	if strings.EqualFold(strings.TrimSpace(cfg.AuthType), "none") {
		token = ""
	}
	return token
}

func ollamaProviderFactory(_ context.Context, cfg plugin.ModelProviderConfig) (model.Provider, error) {
	return modelollama.New(modelollama.Config{
		ID:              firstNonEmpty(cfg.ID, cfg.Provider, cfg.Profile, "ollama"),
		BaseURL:         cfg.Endpoint,
		Model:           cfg.Model,
		MaxOutputTokens: cfg.MaxOutputTokens,
	})
}

func memoryStoreFactory(context.Context, plugin.StoreConfig) (session.Store, error) {
	return storememory.New(), nil
}

func jsonlStoreFactory(_ context.Context, cfg plugin.StoreConfig) (session.Store, error) {
	uri := strings.TrimSpace(cfg.URI)
	if uri == "" {
		return nil, errors.New("app/registry: jsonl store uri is required")
	}
	return storejsonl.New(uri)
}

func sqliteStoreFactory(_ context.Context, cfg plugin.StoreConfig) (session.Store, error) {
	uri := strings.TrimSpace(cfg.URI)
	if uri == "" {
		return nil, errors.New("app/registry: sqlite store uri is required")
	}
	return storesqlite.New(uri)
}

func runCommandToolFactory(_ context.Context, cfg plugin.ToolConfig) (tool.Tool, error) {
	return toolshell.NewRunCommandTool(cfg.Sandbox)
}

func taskToolFactory(_ context.Context, cfg plugin.ToolConfig) (tool.Tool, error) {
	return tooltask.New(cfg.Sandbox)
}

func readFileToolFactory(_ context.Context, cfg plugin.ToolConfig) (tool.Tool, error) {
	return toolfilesystem.NewReadFileTool(cfg.Sandbox)
}

func listDirectoryToolFactory(_ context.Context, cfg plugin.ToolConfig) (tool.Tool, error) {
	return toolfilesystem.NewListDirectoryTool(cfg.Sandbox)
}

func globFilesToolFactory(_ context.Context, cfg plugin.ToolConfig) (tool.Tool, error) {
	return toolfilesystem.NewGlobFilesTool(cfg.Sandbox)
}

func searchFilesToolFactory(_ context.Context, cfg plugin.ToolConfig) (tool.Tool, error) {
	return toolfilesystem.NewSearchFilesTool(cfg.Sandbox)
}

func writeFileToolFactory(_ context.Context, cfg plugin.ToolConfig) (tool.Tool, error) {
	return toolfilesystem.NewWriteFileTool(cfg.Sandbox)
}

func patchFileToolFactory(_ context.Context, cfg plugin.ToolConfig) (tool.Tool, error) {
	return toolfilesystem.NewPatchFileTool(cfg.Sandbox)
}

func planToolFactory(context.Context, plugin.ToolConfig) (tool.Tool, error) {
	return toolplan.New(), nil
}

func key(name string, kind string) (string, error) {
	value := normalizeKey(name)
	if value == "" {
		return "", fmt.Errorf("app/registry: %s name is required", kind)
	}
	return value, nil
}

func normalizeKey(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func firstPositive(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

var _ plugin.Registry = (*Registry)(nil)

func ACPAgentConfig(agent plugin.ACPAgentDescriptor) acpexternal.Config {
	return acpexternal.Config{
		AgentID:   firstNonEmpty(agent.Name, agent.Command),
		AgentName: firstNonEmpty(agent.Name, agent.Command),
		Command:   strings.TrimSpace(agent.Command),
		Args:      slices.Clone(agent.Args),
		WorkDir:   strings.TrimSpace(agent.WorkDir),
		Env:       envList(agent.Env),
	}
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
