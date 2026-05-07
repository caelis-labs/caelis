package gatewayapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"strings"
	"sync"
	"time"

	appgateway "github.com/OnslaughtSnail/caelis/gateway"
	sdkcompact "github.com/OnslaughtSnail/caelis/sdk/compact"
	sdkcontroller "github.com/OnslaughtSnail/caelis/sdk/controller"
	sdkdelegation "github.com/OnslaughtSnail/caelis/sdk/delegation"
	sdkproviders "github.com/OnslaughtSnail/caelis/sdk/model/providers"
	sdkplugin "github.com/OnslaughtSnail/caelis/sdk/plugin"
	sdkpolicy "github.com/OnslaughtSnail/caelis/sdk/policy/presets"
	sdkruntime "github.com/OnslaughtSnail/caelis/sdk/runtime"
	"github.com/OnslaughtSnail/caelis/sdk/runtime/agents/chat"
	localruntime "github.com/OnslaughtSnail/caelis/sdk/runtime/local"
	sdksandbox "github.com/OnslaughtSnail/caelis/sdk/sandbox"
	_ "github.com/OnslaughtSnail/caelis/sdk/sandbox/bwrap"
	_ "github.com/OnslaughtSnail/caelis/sdk/sandbox/host"
	_ "github.com/OnslaughtSnail/caelis/sdk/sandbox/landlock"
	_ "github.com/OnslaughtSnail/caelis/sdk/sandbox/seatbelt"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
	sessionfile "github.com/OnslaughtSnail/caelis/sdk/session/file"
	sdktask "github.com/OnslaughtSnail/caelis/sdk/task"
	taskfile "github.com/OnslaughtSnail/caelis/sdk/task/file"
	sdktool "github.com/OnslaughtSnail/caelis/sdk/tool"
	sdkbuiltin "github.com/OnslaughtSnail/caelis/sdk/tool/builtin"
	spawntool "github.com/OnslaughtSnail/caelis/sdk/tool/builtin/spawn"
)

var errAmbiguousModelAlias = errors.New("ambiguous model alias")

type Config struct {
	AppName        string
	UserID         string
	StoreDir       string
	WorkspaceKey   string
	WorkspaceCWD   string
	PermissionMode string
	ContextWindow  int
	SystemPrompt   string
	Assembly       sdkplugin.ResolvedAssembly
	Model          ModelConfig
	Sandbox        SandboxConfig
}

type ModelConfig struct {
	ID         string               `json:"id,omitempty"`
	Alias      string               `json:"alias,omitempty"`
	Provider   string               `json:"provider,omitempty"`
	ProfileID  string               `json:"profile_id,omitempty"`
	EndpointID string               `json:"endpoint_id,omitempty"`
	API        sdkproviders.APIType `json:"api,omitempty"`
	Model      string               `json:"model,omitempty"`
	BaseURL    string               `json:"base_url,omitempty"`
	// HTTPClient is an in-memory transport override for this process. It is
	// intentionally never persisted.
	HTTPClient *http.Client `json:"-"`
	// Token is an in-memory secret used for the current process. It is not
	// persisted unless PersistToken is explicitly enabled.
	Token    string `json:"token,omitempty"`
	TokenEnv string `json:"token_env,omitempty"`
	// PersistToken explicitly opts into persisting Token in plaintext config.
	// Prefer TokenEnv instead.
	PersistToken           bool                  `json:"persist_token,omitempty"`
	AuthType               sdkproviders.AuthType `json:"auth_type,omitempty"`
	HeaderKey              string                `json:"header_key,omitempty"`
	ContextWindowTokens    int                   `json:"context_window_tokens,omitempty"`
	ReasoningEffort        string                `json:"reasoning_effort,omitempty"`
	DefaultReasoningEffort string                `json:"default_reasoning_effort,omitempty"`
	ReasoningLevels        []string              `json:"reasoning_levels,omitempty"`
	ReasoningMode          string                `json:"reasoning_mode,omitempty"`
	MaxOutputTok           int                   `json:"max_output_tokens,omitempty"`
	Timeout                time.Duration         `json:"timeout,omitempty"`
}

type ModelProfileConfig struct {
	ID           string                `json:"id,omitempty"`
	Provider     string                `json:"provider,omitempty"`
	EndpointID   string                `json:"endpoint_id,omitempty"`
	API          sdkproviders.APIType  `json:"api,omitempty"`
	BaseURL      string                `json:"base_url,omitempty"`
	HTTPClient   *http.Client          `json:"-"`
	Token        string                `json:"token,omitempty"`
	TokenEnv     string                `json:"token_env,omitempty"`
	PersistToken bool                  `json:"persist_token,omitempty"`
	AuthType     sdkproviders.AuthType `json:"auth_type,omitempty"`
	HeaderKey    string                `json:"header_key,omitempty"`
	Timeout      time.Duration         `json:"timeout,omitempty"`
}

type ModelChoice struct {
	ID         string
	Alias      string
	Provider   string
	Model      string
	ProfileID  string
	EndpointID string
	BaseURL    string
	Detail     string
}

type Stack struct {
	Gateway   *appgateway.Gateway
	Sessions  sdksession.Service
	AppName   string
	UserID    string
	Workspace sdksession.WorkspaceRef
	lookup    *modelLookup
	store     *appConfigStore
	storeDir  string
	mu        sync.RWMutex
	runtime   stackRuntimeConfig
	sandbox   SandboxConfig
	exec      sdksandbox.Runtime
	engine    *localruntime.Runtime
	taskStore *taskfile.Store
}

type SessionRuntimeState struct {
	ModelID         string
	ModelAlias      string
	ReasoningEffort string
	SessionMode     string
	SandboxMode     string
}

type SandboxStatus struct {
	RequestedBackend string
	ResolvedBackend  string
	Route            string
	FallbackReason   string
	SecuritySummary  string
}

type ACPAgentInfo struct {
	Name        string
	Description string
}

type ACPAgentAddOption struct {
	Value   string
	Display string
	Detail  string
}

type ACPControllerStatus = sdkcontroller.ControllerStatus
type ACPControllerCommand = sdkcontroller.ControllerCommand
type ACPControllerConfigChoice = sdkcontroller.ControllerConfigChoice
type ACPControllerMode = sdkcontroller.ControllerMode

type RegisterBuiltinACPAgentOptions struct {
	Install bool
}

type ACPAgentInstallError struct {
	Agent   string
	Command []string
	Output  string
	Err     error
}

func (e *ACPAgentInstallError) Error() string {
	if e == nil {
		return ""
	}
	agent := strings.TrimSpace(e.Agent)
	if agent == "" {
		agent = "unknown"
	}
	errText := "failed"
	if e.Err != nil {
		errText = e.Err.Error()
	}
	msg := fmt.Sprintf("gatewayapp: install ACP agent %q: %s", agent, errText)
	if out := strings.TrimSpace(e.Output); out != "" {
		msg += "\n" + out
	}
	return msg
}

func (e *ACPAgentInstallError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func (e *ACPAgentInstallError) CommandString() string {
	if e == nil {
		return ""
	}
	return strings.Join(e.Command, " ")
}

type StartSubagentOptions struct {
	ApprovalRequester sdkruntime.ApprovalRequester
}

type stackRuntimeConfig struct {
	PermissionMode string
	ContextWindow  int
	Model          ModelConfig
	BaseAssembly   sdkplugin.ResolvedAssembly
	Assembly       sdkplugin.ResolvedAssembly
	BaseMetadata   map[string]any
}

func NewLocalStack(cfg Config) (*Stack, error) {
	appName := firstNonEmpty(strings.TrimSpace(cfg.AppName), "caelis")
	userID := firstNonEmpty(strings.TrimSpace(cfg.UserID), "local-user")
	workspaceCWD := firstNonEmpty(strings.TrimSpace(cfg.WorkspaceCWD), mustGetwd())
	workspaceKey := firstNonEmpty(strings.TrimSpace(cfg.WorkspaceKey), "workspace")
	storeDir := strings.TrimSpace(cfg.StoreDir)
	if storeDir == "" {
		storeDir = defaultStoreDir()
	}
	configStore := newAppConfigStore(storeDir)
	doc, err := configStore.Load()
	if err != nil {
		return nil, err
	}
	baseAssembly := sdkplugin.CloneResolvedAssembly(cfg.Assembly)
	cfg.Assembly = withConfiguredACPAgents(cfg.Assembly, doc.Agents, defaultSelfACPAgent(defaultSelfACPAgentConfig{
		Config:       cfg,
		AppName:      appName,
		UserID:       userID,
		StoreDir:     storeDir,
		WorkspaceKey: workspaceKey,
		WorkspaceCWD: workspaceCWD,
	}))
	sessions := sessionfile.NewService(sessionfile.NewStore(sessionfile.Config{
		RootDir: filepath.Join(storeDir, "sessions"),
	}))
	taskStore := taskfile.NewStore(taskfile.Config{RootDir: filepath.Join(storeDir, "tasks")})
	lookup, err := newModelLookup(configStore, cfg.Model, cfg.ContextWindow)
	if err != nil {
		return nil, err
	}
	sandboxCfg := mergeSandboxConfig(doc.Sandbox, cfg.Sandbox)
	baseMetadata := map[string]any{}
	systemPrompt, err := buildSystemPrompt(promptConfig{
		AppName:      appName,
		WorkspaceDir: workspaceCWD,
		BasePrompt:   cfg.SystemPrompt,
	})
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(systemPrompt) != "" {
		baseMetadata["system_prompt"] = systemPrompt
	}
	if reasoning := strings.TrimSpace(cfg.Model.ReasoningEffort); reasoning != "" {
		baseMetadata["reasoning_effort"] = reasoning
	}
	baseMetadata = withSandboxPolicyRootMetadata(baseMetadata, sandboxCfg, workspaceCWD)
	stack := &Stack{
		Sessions: sessions,
		AppName:  appName,
		UserID:   userID,
		Workspace: sdksession.WorkspaceRef{
			Key: workspaceKey,
			CWD: workspaceCWD,
		},
		lookup:    lookup,
		store:     configStore,
		storeDir:  storeDir,
		taskStore: taskStore,
		runtime: stackRuntimeConfig{
			PermissionMode: cfg.PermissionMode,
			ContextWindow:  cfg.ContextWindow,
			Model:          cfg.Model,
			BaseAssembly:   baseAssembly,
			Assembly:       sdkplugin.CloneResolvedAssembly(cfg.Assembly),
			BaseMetadata:   cloneMap(baseMetadata),
		},
		sandbox: sandboxCfg,
	}
	if err := stack.rebuildGateway(); err != nil {
		return nil, err
	}
	return stack, nil
}

func delegationAgentsFromAssembly(assembly sdkplugin.ResolvedAssembly) []sdkdelegation.Agent {
	out := make([]sdkdelegation.Agent, 0, len(assembly.Agents))
	for _, one := range assembly.Agents {
		agent := sdkdelegation.NormalizeAgent(sdkdelegation.Agent{
			Name:        one.Name,
			Description: one.Description,
		})
		if agent.Name == "" {
			continue
		}
		out = append(out, agent)
	}
	return out
}

func delegationAgentsForSpawn(assembly sdkplugin.ResolvedAssembly, _ []sdksession.ParticipantBinding) []sdkdelegation.Agent {
	if len(assembly.Agents) == 0 {
		return nil
	}
	return delegationAgentsFromAssembly(assembly)
}

func systemPromptWithDelegationGuidance(systemPrompt string) string {
	systemPrompt = strings.TrimRight(strings.TrimSpace(systemPrompt), "\n")
	guidance := "- Delegation: use SPAWN for bounded child ACP work that can run independently. Use TASK wait for progress, TASK cancel to stop a running child, and TASK write to send stdin to a running BASH task or a follow-up prompt to a completed SPAWN child."
	if strings.Contains(systemPrompt, "SPAWN for bounded child ACP work") {
		return systemPrompt
	}
	if systemPrompt == "" {
		return guidance
	}
	return systemPrompt + "\n" + guidance
}

func withConfiguredACPAgents(assembly sdkplugin.ResolvedAssembly, configured []AgentConfig, self sdkplugin.AgentConfig) sdkplugin.ResolvedAssembly {
	out := sdkplugin.CloneResolvedAssembly(assembly)
	seen := map[string]struct{}{}
	for _, agent := range out.Agents {
		name := strings.ToLower(strings.TrimSpace(agent.Name))
		if name != "" {
			seen[name] = struct{}{}
		}
	}
	if name := strings.ToLower(strings.TrimSpace(self.Name)); name != "" {
		if _, exists := seen[name]; !exists {
			out.Agents = append(out.Agents, self)
			seen[name] = struct{}{}
		}
	}
	for _, agent := range configured {
		cfg := agentConfigToPlugin(agent)
		name := strings.ToLower(strings.TrimSpace(cfg.Name))
		if name != "" {
			if _, exists := seen[name]; !exists {
				out.Agents = append(out.Agents, cfg)
				seen[name] = struct{}{}
			}
		}
	}
	return out
}

func agentConfigToPlugin(in AgentConfig) sdkplugin.AgentConfig {
	in = normalizeAgentConfig(in)
	return sdkplugin.AgentConfig{
		Name:        in.Name,
		Description: in.Description,
		Command:     in.Command,
		Args:        append([]string(nil), in.Args...),
		Env:         cloneStringMap(in.Env),
		WorkDir:     in.WorkDir,
	}
}

func pluginAgentToConfig(in sdkplugin.AgentConfig, builtin bool) AgentConfig {
	return normalizeAgentConfig(AgentConfig{
		Name:        in.Name,
		Description: in.Description,
		Command:     in.Command,
		Args:        append([]string(nil), in.Args...),
		Env:         cloneStringMap(in.Env),
		WorkDir:     in.WorkDir,
		Builtin:     builtin,
	})
}

type defaultSelfACPAgentConfig struct {
	Config       Config
	AppName      string
	UserID       string
	StoreDir     string
	WorkspaceKey string
	WorkspaceCWD string
}

func defaultSelfACPAgent(cfg defaultSelfACPAgentConfig) sdkplugin.AgentConfig {
	if cmd := strings.TrimSpace(os.Getenv("CAELIS_ACP_SELF_AGENT_CMD")); cmd != "" {
		name := strings.TrimSpace(os.Getenv("CAELIS_ACP_SELF_AGENT_NAME"))
		if name == "" {
			name = "self"
		}
		return sdkplugin.AgentConfig{
			Name:        name,
			Description: firstNonEmpty(strings.TrimSpace(os.Getenv("CAELIS_ACP_SELF_AGENT_DESC")), "Caelis self ACP agent"),
			Command:     "bash",
			Args:        []string{"-lc", cmd},
			WorkDir:     strings.TrimSpace(os.Getenv("CAELIS_ACP_SELF_AGENT_WORKDIR")),
		}
	}
	executable, err := os.Executable()
	if err != nil || strings.TrimSpace(executable) == "" {
		executable = os.Args[0]
	}
	return sdkplugin.AgentConfig{
		Name:        "self",
		Description: "Caelis self ACP agent",
		Command:     executable,
		Args: append([]string{
			"acp",
			"-app", strings.TrimSpace(cfg.AppName),
			"-user", strings.TrimSpace(cfg.UserID),
			"-store-dir", strings.TrimSpace(cfg.StoreDir),
			"-workspace-key", strings.TrimSpace(cfg.WorkspaceKey),
			"-workspace-cwd", strings.TrimSpace(cfg.WorkspaceCWD),
			"-permission-mode", strings.TrimSpace(cfg.Config.PermissionMode),
		}, selfRuntimeArgs(cfg.Config)...),
	}
}

func selfRuntimeArgs(cfg Config) []string {
	args := []string{}
	appendFlag := func(name string, value string) {
		if strings.TrimSpace(value) != "" {
			args = append(args, name, strings.TrimSpace(value))
		}
	}
	model := cfg.Model
	appendFlag("-model-alias", model.Alias)
	appendFlag("-provider", model.Provider)
	appendFlag("-api", string(model.API))
	appendFlag("-model", model.Model)
	appendFlag("-base-url", model.BaseURL)
	appendFlag("-token", model.Token)
	appendFlag("-token-env", model.TokenEnv)
	appendFlag("-auth-type", string(model.AuthType))
	appendFlag("-header-key", model.HeaderKey)
	if cfg.ContextWindow > 0 {
		args = append(args, "-context-window", fmt.Sprintf("%d", cfg.ContextWindow))
	}
	if model.MaxOutputTok > 0 {
		args = append(args, "-max-output-tokens", fmt.Sprintf("%d", model.MaxOutputTok))
	}
	return args
}

func builtInACPAgents() []sdkplugin.AgentConfig {
	return []sdkplugin.AgentConfig{
		npxACPAgentConfig("codex", "OpenAI Codex ACP agent", "@zed-industries/codex-acp"),
		npxACPAgentConfig("claude", "Claude Code ACP agent", "@agentclientprotocol/claude-agent-acp"),
		{
			Name:        "copilot",
			Description: "GitHub Copilot ACP agent",
			Command:     "copilot",
			Args:        []string{"--acp", "--stdio"},
		},
		{
			Name:        "gemini",
			Description: "Gemini ACP agent",
			Command:     "gemini",
			Args:        []string{"--acp"},
		},
	}
}

type builtinACPAdapterPackage struct {
	Package string
	Bin     string
}

func npxACPAgentConfig(name string, description string, pkg string) sdkplugin.AgentConfig {
	return sdkplugin.AgentConfig{
		Name:        strings.TrimSpace(name),
		Description: strings.TrimSpace(description),
		Command:     "npx",
		Args:        []string{"-y", strings.TrimSpace(pkg)},
	}
}

func builtinACPAdapterPackageFor(name string) (builtinACPAdapterPackage, bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "codex":
		return builtinACPAdapterPackage{Package: "@zed-industries/codex-acp", Bin: "codex-acp"}, true
	case "claude":
		return builtinACPAdapterPackage{Package: "@agentclientprotocol/claude-agent-acp", Bin: "claude-agent-acp"}, true
	default:
		return builtinACPAdapterPackage{}, false
	}
}

func (s *Stack) RegisterBuiltinACPAgent(name string) error {
	return s.RegisterBuiltinACPAgentWithOptions(context.Background(), name, RegisterBuiltinACPAgentOptions{})
}

func (s *Stack) RegisterACPAgent(ctx context.Context, cfg AgentConfig) error {
	if s == nil || s.store == nil {
		return fmt.Errorf("gatewayapp: app config store unavailable")
	}
	cfg = normalizeAgentConfig(cfg)
	if cfg.Name == "" {
		return fmt.Errorf("gatewayapp: ACP agent name is required")
	}
	if reservedSlashCommandName(cfg.Name) {
		return fmt.Errorf("gatewayapp: ACP agent %q conflicts with an existing slash command", cfg.Name)
	}
	if strings.TrimSpace(cfg.Command) == "" {
		return fmt.Errorf("gatewayapp: command is required for ACP agent %q", cfg.Name)
	}
	cfg.Builtin = false
	doc, err := s.store.Load()
	if err != nil {
		return err
	}
	replaced := false
	next := make([]AgentConfig, 0, len(doc.Agents)+1)
	for _, existing := range doc.Agents {
		if strings.EqualFold(strings.TrimSpace(existing.Name), cfg.Name) {
			next = append(next, cfg)
			replaced = true
			continue
		}
		next = append(next, existing)
	}
	if !replaced {
		next = append(next, cfg)
	}
	doc.Agents = next
	if err := s.store.Save(doc); err != nil {
		return err
	}
	return s.setConfiguredAgents(doc.Agents)
}

func (s *Stack) RegisterBuiltinACPAgentWithOptions(ctx context.Context, name string, opts RegisterBuiltinACPAgentOptions) error {
	if s == nil || s.store == nil {
		return fmt.Errorf("gatewayapp: app config store unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if reservedSlashCommandName(name) {
		return fmt.Errorf("gatewayapp: ACP agent %q conflicts with an existing slash command", strings.TrimSpace(name))
	}
	preset, ok := s.lookupRegisterableACPAgent(name)
	if !ok {
		return fmt.Errorf("gatewayapp: unknown builtin ACP agent %q", strings.TrimSpace(name))
	}
	if opts.Install {
		installed, err := s.installBuiltinACPAgent(ctx, name, preset)
		if err != nil {
			return err
		}
		preset = installed
	}
	doc, err := s.store.Load()
	if err != nil {
		return err
	}
	cfg := pluginAgentToConfig(preset, true)
	replaced := false
	next := make([]AgentConfig, 0, len(doc.Agents)+1)
	for _, existing := range doc.Agents {
		if strings.EqualFold(strings.TrimSpace(existing.Name), cfg.Name) {
			next = append(next, cfg)
			replaced = true
			continue
		}
		next = append(next, existing)
	}
	if !replaced {
		next = append(next, cfg)
	}
	doc.Agents = next
	if err := s.store.Save(doc); err != nil {
		return err
	}
	return s.setConfiguredAgents(doc.Agents)
}

func (s *Stack) lookupRegisterableACPAgent(name string) (sdkplugin.AgentConfig, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	if preset, ok := lookupBuiltInACPAgent(name); ok {
		return preset, true
	}
	return s.lookupRuntimeACPAgent(name)
}

func (s *Stack) lookupRuntimeACPAgent(name string) (sdkplugin.AgentConfig, bool) {
	if s == nil {
		return sdkplugin.AgentConfig{}, false
	}
	name = strings.ToLower(strings.TrimSpace(name))
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, agent := range s.runtime.Assembly.Agents {
		if strings.EqualFold(strings.TrimSpace(agent.Name), name) {
			return sdkplugin.CloneAgentConfig(agent), true
		}
	}
	return sdkplugin.AgentConfig{}, false
}

func (s *Stack) installBuiltinACPAgent(ctx context.Context, name string, base sdkplugin.AgentConfig) (sdkplugin.AgentConfig, error) {
	pkg, ok := builtinACPAdapterPackageFor(name)
	if !ok {
		return sdkplugin.AgentConfig{}, fmt.Errorf("gatewayapp: ACP agent %q does not support local npm install", strings.TrimSpace(name))
	}
	root := s.managedACPAgentRoot()
	installCommand := []string{"npm", "install", "--prefix", root, pkg.Package + "@latest"}
	npm, err := exec.LookPath("npm")
	if err != nil || strings.TrimSpace(npm) == "" {
		return sdkplugin.AgentConfig{}, &ACPAgentInstallError{
			Agent:   strings.TrimSpace(name),
			Command: installCommand,
			Err:     fmt.Errorf("npm is required"),
		}
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return sdkplugin.AgentConfig{}, err
	}
	cmd := exec.CommandContext(ctx, npm, "install", "--prefix", root, pkg.Package+"@latest")
	cmd.Env = append(os.Environ(), "npm_config_cache="+filepath.Join(root, "npm-cache"))
	output, err := cmd.CombinedOutput()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			err = ctxErr
		}
		return sdkplugin.AgentConfig{}, &ACPAgentInstallError{
			Agent:   strings.TrimSpace(name),
			Command: installCommand,
			Output:  strings.TrimSpace(string(output)),
			Err:     err,
		}
	}
	bin := managedACPAgentBinPath(root, pkg.Bin)
	if info, err := os.Stat(bin); err != nil || info.IsDir() {
		if err == nil {
			err = fmt.Errorf("installed path is a directory")
		}
		return sdkplugin.AgentConfig{}, fmt.Errorf("gatewayapp: install ACP agent %q did not produce %s: %w", strings.TrimSpace(name), bin, err)
	}
	base.Command = bin
	base.Args = nil
	return base, nil
}

func (s *Stack) managedACPAgentRoot() string {
	if s == nil {
		return ""
	}
	return filepath.Join(s.storeDir, "acp-agents", "npm")
}

func managedACPAgentBinPath(root string, bin string) string {
	bin = strings.TrimSpace(bin)
	if goruntime.GOOS == "windows" && !strings.HasSuffix(strings.ToLower(bin), ".cmd") {
		bin += ".cmd"
	}
	return filepath.Join(strings.TrimSpace(root), "node_modules", ".bin", bin)
}

func (s *Stack) UnregisterACPAgent(name string) error {
	if s == nil || s.store == nil {
		return fmt.Errorf("gatewayapp: app config store unavailable")
	}
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return fmt.Errorf("gatewayapp: agent name is required")
	}
	doc, err := s.store.Load()
	if err != nil {
		return err
	}
	next := make([]AgentConfig, 0, len(doc.Agents))
	removed := false
	for _, existing := range doc.Agents {
		if strings.EqualFold(strings.TrimSpace(existing.Name), name) {
			removed = true
			continue
		}
		next = append(next, existing)
	}
	s.mu.Lock()
	runtimeCfg := s.runtime
	baseAgents := make([]sdkplugin.AgentConfig, 0, len(runtimeCfg.BaseAssembly.Agents))
	for _, agent := range runtimeCfg.BaseAssembly.Agents {
		if strings.EqualFold(strings.TrimSpace(agent.Name), name) {
			removed = true
			continue
		}
		baseAgents = append(baseAgents, agent)
	}
	s.mu.Unlock()
	if !removed {
		return fmt.Errorf("gatewayapp: ACP agent %q is not registered", name)
	}
	doc.Agents = next
	if err := s.store.Save(doc); err != nil {
		return err
	}
	runtimeCfg.BaseAssembly.Agents = baseAgents
	return s.setConfiguredAgentsWithBase(runtimeCfg.BaseAssembly, doc.Agents)
}

func (s *Stack) setConfiguredAgents(configured []AgentConfig) error {
	if s == nil {
		return fmt.Errorf("gatewayapp: stack is unavailable")
	}
	s.mu.RLock()
	base := sdkplugin.CloneResolvedAssembly(s.runtime.BaseAssembly)
	s.mu.RUnlock()
	return s.setConfiguredAgentsWithBase(base, configured)
}

func (s *Stack) setConfiguredAgentsWithBase(base sdkplugin.ResolvedAssembly, configured []AgentConfig) error {
	if s == nil {
		return fmt.Errorf("gatewayapp: stack is unavailable")
	}
	s.mu.RLock()
	runtimeCfg := s.runtime
	engine := s.engine
	s.mu.RUnlock()
	runtimeCfg.BaseAssembly = sdkplugin.CloneResolvedAssembly(base)
	runtimeCfg.Assembly = s.configuredAssembly(runtimeCfg.BaseAssembly, configured, runtimeCfg)
	if engine == nil {
		return fmt.Errorf("gatewayapp: runtime is unavailable")
	}
	if err := engine.UpdateACPAgents(runtimeCfg.Assembly.Agents); err != nil {
		return err
	}
	s.mu.Lock()
	current := s.runtime
	current.BaseAssembly = runtimeCfg.BaseAssembly
	current.Assembly = runtimeCfg.Assembly
	s.runtime = current
	s.mu.Unlock()
	return nil
}

func (s *Stack) configuredAssembly(base sdkplugin.ResolvedAssembly, configured []AgentConfig, runtimeCfg stackRuntimeConfig) sdkplugin.ResolvedAssembly {
	return withConfiguredACPAgents(base, configured, defaultSelfACPAgent(defaultSelfACPAgentConfig{
		Config: Config{
			AppName:        s.AppName,
			UserID:         s.UserID,
			StoreDir:       s.storeDir,
			WorkspaceKey:   s.Workspace.Key,
			WorkspaceCWD:   s.Workspace.CWD,
			PermissionMode: runtimeCfg.PermissionMode,
			ContextWindow:  runtimeCfg.ContextWindow,
			Model:          runtimeCfg.Model,
		},
		AppName:      s.AppName,
		UserID:       s.UserID,
		StoreDir:     s.storeDir,
		WorkspaceKey: s.Workspace.Key,
		WorkspaceCWD: s.Workspace.CWD,
	}))
}

func (s *Stack) ListBuiltinACPAgents() []ACPAgentInfo {
	builtins := builtInACPAgents()
	out := make([]ACPAgentInfo, 0, len(builtins))
	for _, agent := range builtins {
		if name := strings.TrimSpace(agent.Name); name != "" {
			out = append(out, ACPAgentInfo{Name: name, Description: strings.TrimSpace(agent.Description)})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

func (s *Stack) ListBuiltinACPAgentAddOptions() []ACPAgentAddOption {
	builtins := builtInACPAgents()
	out := make([]ACPAgentAddOption, 0, len(builtins))
	for _, agent := range builtins {
		name := strings.TrimSpace(agent.Name)
		if name == "" {
			continue
		}
		if _, ok := builtinACPAdapterPackageFor(name); ok {
			out = append(out, ACPAgentAddOption{
				Value:   name,
				Display: name + " (npx)",
				Detail:  strings.Join(append([]string{agent.Command}, agent.Args...), " "),
			})
			continue
		}
		out = append(out, ACPAgentAddOption{
			Value:   name,
			Display: name,
			Detail:  firstNonEmpty(strings.TrimSpace(agent.Description), "built-in ACP agent"),
		})
	}
	return out
}

func (s *Stack) ListInstallableACPAgentOptions() []ACPAgentAddOption {
	builtins := builtInACPAgents()
	out := make([]ACPAgentAddOption, 0, len(builtins))
	for _, agent := range builtins {
		name := strings.TrimSpace(agent.Name)
		if name == "" {
			continue
		}
		pkg, ok := builtinACPAdapterPackageFor(name)
		if !ok {
			continue
		}
		out = append(out, ACPAgentAddOption{
			Value:   name,
			Display: name + " (npm install)",
			Detail:  s.builtinACPAgentInstallCommand(pkg),
		})
	}
	return out
}

func (s *Stack) builtinACPAgentInstallCommand(pkg builtinACPAdapterPackage) string {
	return strings.Join([]string{"npm", "install", "--prefix", s.managedACPAgentRoot(), pkg.Package + "@latest"}, " ")
}

func lookupBuiltInACPAgent(name string) (sdkplugin.AgentConfig, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, agent := range builtInACPAgents() {
		if strings.EqualFold(strings.TrimSpace(agent.Name), name) {
			return agent, true
		}
	}
	return sdkplugin.AgentConfig{}, false
}

func reservedSlashCommandName(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "help", "agent", "connect", "model", "sandbox", "status", "new", "resume", "compact", "exit", "quit":
		return true
	default:
		return false
	}
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func defaultStoreDir() string {
	home, err := os.UserHomeDir()
	if err == nil && strings.TrimSpace(home) != "" {
		return filepath.Join(home, ".caelis")
	}
	cwd := mustGetwd()
	return filepath.Join(cwd, ".caelis")
}

func (s *Stack) StartSession(ctx context.Context, preferredSessionID string, bindingKey string) (sdksession.Session, error) {
	if s == nil || s.Gateway == nil {
		return sdksession.Session{}, fmt.Errorf("gatewayapp: stack is unavailable")
	}
	return s.Gateway.StartSession(ctx, appgateway.StartSessionRequest{
		AppName:            s.AppName,
		UserID:             s.UserID,
		Workspace:          s.Workspace,
		PreferredSessionID: strings.TrimSpace(preferredSessionID),
		BindingKey:         strings.TrimSpace(bindingKey),
		Binding: appgateway.BindingDescriptor{
			Surface: strings.TrimSpace(bindingKey),
			Owner:   s.AppName,
		},
	})
}

func (s *Stack) ACPControllerStatus(ctx context.Context, ref sdksession.SessionRef) (sdkcontroller.ControllerStatus, bool, error) {
	if s == nil || s.engine == nil {
		return sdkcontroller.ControllerStatus{}, false, nil
	}
	return s.engine.ACPControllerStatus(ctx, sdksession.NormalizeSessionRef(ref))
}

func (s *Stack) SetACPControllerModel(ctx context.Context, ref sdksession.SessionRef, model string, reasoningEffort string) (sdkcontroller.ControllerStatus, error) {
	if s == nil || s.engine == nil {
		return sdkcontroller.ControllerStatus{}, fmt.Errorf("gatewayapp: runtime engine unavailable")
	}
	if err := s.rejectReconfigureWhileActive("switch ACP model"); err != nil {
		return sdkcontroller.ControllerStatus{}, err
	}
	return s.engine.SetACPControllerModel(ctx, sdkcontroller.SetControllerModelRequest{
		SessionRef:      sdksession.NormalizeSessionRef(ref),
		Model:           strings.TrimSpace(model),
		ReasoningEffort: strings.TrimSpace(reasoningEffort),
	})
}

func (s *Stack) SetACPControllerMode(ctx context.Context, ref sdksession.SessionRef, mode string) (sdkcontroller.ControllerStatus, error) {
	if s == nil || s.engine == nil {
		return sdkcontroller.ControllerStatus{}, fmt.Errorf("gatewayapp: runtime engine unavailable")
	}
	if err := s.rejectReconfigureWhileActive("switch ACP mode"); err != nil {
		return sdkcontroller.ControllerStatus{}, err
	}
	return s.engine.SetACPControllerMode(ctx, sdkcontroller.SetControllerModeRequest{
		SessionRef: sdksession.NormalizeSessionRef(ref),
		Mode:       strings.TrimSpace(mode),
	})
}

// Connect reconfigures the model provider on the live stack. The new config
// takes effect for subsequent turns.
func (s *Stack) Connect(cfg ModelConfig) (string, error) {
	if s == nil || s.Gateway == nil {
		return "", fmt.Errorf("gatewayapp: stack is unavailable")
	}
	if err := s.rejectReconfigureWhileActive("connect model"); err != nil {
		return "", err
	}
	if s.lookup == nil {
		return "", fmt.Errorf("gatewayapp: model lookup unavailable")
	}
	resolver := s.Gateway.Resolver()
	if resolver == nil {
		return "", fmt.Errorf("gatewayapp: resolver not available")
	}
	modelID, err := s.lookup.Upsert(cfg)
	if err != nil {
		return "", fmt.Errorf("gatewayapp: invalid model config: %w", err)
	}
	cfg, _ = s.lookup.Config(modelID)
	s.mu.Lock()
	runtimeCfg := s.runtime
	runtimeCfg.Model = cfg
	s.runtime = runtimeCfg
	s.mu.Unlock()
	resolver.SetModelLookup(s.lookup, s.lookup.DefaultID())
	if err := s.saveModelConfigs(); err != nil {
		return "", err
	}
	return modelID, nil
}

// UseModel persists one per-session model alias override for subsequent turns.
func (s *Stack) UseModel(ctx context.Context, ref sdksession.SessionRef, alias string, reasoningEffort ...string) error {
	if s == nil || s.Sessions == nil {
		return fmt.Errorf("gatewayapp: sessions service unavailable")
	}
	if err := s.rejectReconfigureWhileActive("switch model"); err != nil {
		return err
	}
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return fmt.Errorf("gatewayapp: model alias is required")
	}
	if s.lookup == nil {
		return fmt.Errorf("gatewayapp: model lookup unavailable")
	}
	cfg, err := s.lookup.ResolveConfig(alias)
	if err != nil {
		return err
	}
	reasoning := ""
	if len(reasoningEffort) > 0 {
		reasoning = strings.TrimSpace(reasoningEffort[0])
		if reasoning != "" {
			if !modelConfigSupportsReasoningEffort(cfg, reasoning) {
				return fmt.Errorf("gatewayapp: model %q does not support reasoning level %q", alias, reasoning)
			}
		}
	}
	if s.lookup != nil {
		if reasoning != "" {
			cfg, err := s.lookup.ResolveConfig(alias)
			if err != nil {
				return err
			}
			cfg.ReasoningEffort = reasoning
			if _, err := s.lookup.Upsert(cfg); err != nil {
				return err
			}
		}
		s.lookup.SetDefault(cfg.ID)
		if resolver := s.Gateway.Resolver(); resolver != nil {
			resolver.SetModelLookup(s.lookup, s.lookup.DefaultID())
		}
		if err := s.saveModelConfigs(); err != nil {
			return err
		}
	}
	return s.Sessions.UpdateState(ctx, ref, func(state map[string]any) (map[string]any, error) {
		next := sdksession.CloneState(state)
		if next == nil {
			next = map[string]any{}
		}
		next[appgateway.StateCurrentModelAlias] = cfg.ID
		if reasoning != "" {
			next[appgateway.StateCurrentReasoningEffort] = reasoning
		} else {
			delete(next, appgateway.StateCurrentReasoningEffort)
		}
		return next, nil
	})
}

// DeleteModel clears one per-session model alias override when it matches the
// supplied alias. This reverts the session back to the resolver default.
func (s *Stack) DeleteModel(ctx context.Context, ref sdksession.SessionRef, alias string) error {
	if s == nil || s.Sessions == nil {
		return fmt.Errorf("gatewayapp: sessions service unavailable")
	}
	if err := s.rejectReconfigureWhileActive("delete model"); err != nil {
		return err
	}
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return fmt.Errorf("gatewayapp: model alias is required")
	}
	if s.lookup == nil {
		return fmt.Errorf("gatewayapp: model lookup unavailable")
	}
	cfg, err := s.lookup.ResolveConfig(alias)
	if err != nil {
		return err
	}
	if err := s.lookup.Delete(alias); err != nil {
		return err
	}
	hasDefault := strings.TrimSpace(s.lookup.DefaultID()) != ""
	if resolver := s.Gateway.Resolver(); resolver != nil {
		resolver.SetModelLookup(s.lookup, s.lookup.DefaultID())
	}
	if err := s.saveModelConfigs(); err != nil {
		return err
	}
	return s.Sessions.UpdateState(ctx, ref, func(state map[string]any) (map[string]any, error) {
		next := sdksession.CloneState(state)
		if next == nil {
			next = map[string]any{}
		}
		current, _ := next[appgateway.StateCurrentModelAlias].(string)
		if alias == "" || strings.EqualFold(strings.TrimSpace(current), cfg.ID) || strings.EqualFold(strings.TrimSpace(current), cfg.Alias) || !hasDefault {
			delete(next, appgateway.StateCurrentModelAlias)
			delete(next, appgateway.StateCurrentReasoningEffort)
		}
		return next, nil
	})
}

// SetSessionMode persists one per-session execution mode override for
// subsequent turns and returns the normalized display label.
func (s *Stack) SetSessionMode(ctx context.Context, ref sdksession.SessionRef, mode string) (string, error) {
	if s == nil || s.Sessions == nil {
		return "", fmt.Errorf("gatewayapp: sessions service unavailable")
	}
	if err := s.rejectReconfigureWhileActive("change session mode"); err != nil {
		return "", err
	}
	normalized, err := normalizeSessionMode(mode)
	if err != nil {
		return "", err
	}
	err = s.Sessions.UpdateState(ctx, ref, func(state map[string]any) (map[string]any, error) {
		next := sdksession.CloneState(state)
		if next == nil {
			next = map[string]any{}
		}
		next[appgateway.StateCurrentSessionMode] = normalized
		delete(next, appgateway.StateCurrentSandboxMode)
		return next, nil
	})
	if err != nil {
		return "", err
	}
	return normalized, nil
}

func (s *Stack) CycleSessionMode(ctx context.Context, ref sdksession.SessionRef) (string, error) {
	state, err := s.SessionRuntimeState(ctx, ref)
	if err != nil {
		return "", err
	}
	next := nextSessionMode(state.SessionMode)
	return s.SetSessionMode(ctx, ref, next)
}

// SetSandboxMode is the legacy compatibility wrapper. New callers should use
// SetSessionMode for mode changes and SetSandboxBackend for backend changes.
func (s *Stack) SetSandboxMode(ctx context.Context, ref sdksession.SessionRef, mode string) (string, error) {
	return s.SetSessionMode(ctx, ref, mode)
}

func (s *Stack) SetSandboxBackend(_ context.Context, backend string) (SandboxStatus, error) {
	if s == nil {
		return SandboxStatus{}, fmt.Errorf("gatewayapp: stack is unavailable")
	}
	if err := s.rejectReconfigureWhileActive("change sandbox backend"); err != nil {
		return SandboxStatus{}, err
	}
	normalized, err := normalizeSandboxBackend(backend)
	if err != nil {
		return SandboxStatus{}, err
	}
	s.mu.Lock()
	previous := s.sandbox
	s.sandbox.RequestedType = normalized
	s.mu.Unlock()
	if err := s.rebuildGateway(); err != nil {
		s.mu.Lock()
		s.sandbox = previous
		s.mu.Unlock()
		return SandboxStatus{}, err
	}
	if err := s.saveSandboxConfig(); err != nil {
		return SandboxStatus{}, err
	}
	return s.SandboxStatus(), nil
}

// SessionRuntimeState returns the current per-session runtime overrides backed
// by session state.
func (s *Stack) SessionRuntimeState(ctx context.Context, ref sdksession.SessionRef) (SessionRuntimeState, error) {
	if s == nil || s.Sessions == nil {
		return SessionRuntimeState{}, fmt.Errorf("gatewayapp: sessions service unavailable")
	}
	state, err := s.Sessions.SnapshotState(ctx, ref)
	if err != nil {
		return SessionRuntimeState{}, err
	}
	modelRef := appgateway.CurrentModelAlias(state)
	modelID := ""
	modelAlias := ""
	if s.lookup != nil && modelRef != "" {
		if cfg, ok := s.lookup.Config(modelRef); ok {
			modelID = cfg.ID
			modelAlias = cfg.Alias
		}
	}
	return SessionRuntimeState{
		ModelID:         modelID,
		ModelAlias:      modelAlias,
		ReasoningEffort: appgateway.CurrentReasoningEffort(state),
		SessionMode:     appgateway.CurrentSessionMode(state),
		SandboxMode:     appgateway.CurrentSandboxMode(state),
	}, nil
}

func (s *Stack) SandboxStatus() SandboxStatus {
	if s == nil {
		return SandboxStatus{}
	}
	s.mu.RLock()
	cfg := s.sandbox
	exec := s.exec
	s.mu.RUnlock()
	status := SandboxStatus{
		RequestedBackend: cfg.RequestedType,
		Route:            string(sdksandbox.RouteSandbox),
		SecuritySummary:  "sandbox",
	}
	if status.RequestedBackend == "" {
		status.RequestedBackend = "auto"
	}
	if exec == nil {
		status.ResolvedBackend = status.RequestedBackend
		return status
	}
	rtStatus := exec.Status()
	if strings.TrimSpace(string(rtStatus.RequestedBackend)) != "" {
		status.RequestedBackend = string(rtStatus.RequestedBackend)
	}
	if strings.TrimSpace(string(rtStatus.ResolvedBackend)) != "" {
		status.ResolvedBackend = string(rtStatus.ResolvedBackend)
	}
	status.FallbackReason = strings.TrimSpace(rtStatus.FallbackReason)
	if rtStatus.FallbackToHost {
		status.Route = string(sdksandbox.RouteHost)
		status.SecuritySummary = "host fallback"
		if status.ResolvedBackend == "" {
			status.ResolvedBackend = string(sdksandbox.BackendHost)
		}
	} else if status.ResolvedBackend != "" {
		status.SecuritySummary = status.ResolvedBackend
	}
	if status.ResolvedBackend == "" {
		status.ResolvedBackend = status.RequestedBackend
	}
	return status
}

// ListModelAliases returns the current session override plus resolver-known
// model aliases for picker surfaces such as the TUI `/model` command.
func (s *Stack) ListModelAliases(ctx context.Context, ref sdksession.SessionRef) ([]string, error) {
	choices, err := s.ListModelChoices(ctx, ref)
	if err != nil {
		return nil, err
	}
	aliases := make([]string, 0, len(choices))
	for _, choice := range choices {
		aliases = append(aliases, choice.Alias)
	}
	return dedupeNonEmptyStrings(aliases), nil
}

func (s *Stack) ListModelChoices(ctx context.Context, ref sdksession.SessionRef) ([]ModelChoice, error) {
	if s == nil || s.Sessions == nil {
		return nil, fmt.Errorf("gatewayapp: stack is unavailable")
	}
	if s.lookup == nil {
		return nil, fmt.Errorf("gatewayapp: model lookup unavailable")
	}
	choices := make([]ModelChoice, 0, len(s.lookup.ListModelChoices())+1)
	if strings.TrimSpace(ref.SessionID) != "" {
		state, err := s.Sessions.SnapshotState(ctx, ref)
		if err != nil {
			return nil, err
		}
		if modelRef := appgateway.CurrentModelAlias(state); modelRef != "" {
			if cfg, ok := s.lookup.Config(modelRef); ok {
				choices = append(choices, modelChoiceFromConfig(cfg))
			}
		}
	}
	choices = append(choices, s.lookup.ListModelChoices()...)
	return dedupeModelChoices(choices), nil
}

func (s *Stack) DefaultModelAlias() string {
	if s == nil || s.lookup == nil {
		return ""
	}
	return s.lookup.DefaultAlias()
}

func (s *Stack) DefaultModelID() string {
	if s == nil || s.lookup == nil {
		return ""
	}
	return s.lookup.DefaultID()
}

func (s *Stack) ModelConfig(alias string) (ModelConfig, bool) {
	if s == nil || s.lookup == nil {
		return ModelConfig{}, false
	}
	return s.lookup.Config(alias)
}

func (s *Stack) HasModelAlias(alias string) bool {
	if s == nil || s.lookup == nil {
		return false
	}
	return s.lookup.HasAlias(alias)
}

// ListProviderModels returns configured raw model names for a provider.
func (s *Stack) ListProviderModels(provider string) []string {
	if s == nil || s.lookup == nil {
		return nil
	}
	return s.lookup.ListProviderModels(provider)
}

func (s *Stack) ListACPAgents() []ACPAgentInfo {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	agents := append([]sdkplugin.AgentConfig(nil), s.runtime.Assembly.Agents...)
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

func (s *Stack) StartSubagent(
	ctx context.Context,
	ref sdksession.SessionRef,
	agent string,
	prompt string,
	source string,
) (sdktask.Snapshot, error) {
	return s.StartSubagentWithOptions(ctx, ref, agent, prompt, source, StartSubagentOptions{})
}

func (s *Stack) StartSubagentWithOptions(
	ctx context.Context,
	ref sdksession.SessionRef,
	agent string,
	prompt string,
	source string,
	opts StartSubagentOptions,
) (sdktask.Snapshot, error) {
	if s == nil {
		return sdktask.Snapshot{}, fmt.Errorf("gatewayapp: stack is unavailable")
	}
	s.mu.RLock()
	engine := s.engine
	s.mu.RUnlock()
	if engine == nil {
		return sdktask.Snapshot{}, fmt.Errorf("gatewayapp: runtime is unavailable")
	}
	return engine.StartSubagentWithOptions(ctx, ref, agent, prompt, source, localruntime.StartSubagentOptions{
		ApprovalRequester: opts.ApprovalRequester,
	})
}

func (s *Stack) ContinueSubagentByHandle(
	ctx context.Context,
	ref sdksession.SessionRef,
	handle string,
	prompt string,
	yield time.Duration,
) (sdktask.Snapshot, error) {
	if s == nil {
		return sdktask.Snapshot{}, fmt.Errorf("gatewayapp: stack is unavailable")
	}
	s.mu.RLock()
	engine := s.engine
	s.mu.RUnlock()
	if engine == nil {
		return sdktask.Snapshot{}, fmt.Errorf("gatewayapp: runtime is unavailable")
	}
	return engine.ContinueSubagentByHandle(ctx, ref, handle, prompt, yield)
}

func (s *Stack) WaitSubagentTask(
	ctx context.Context,
	ref sdksession.SessionRef,
	taskID string,
	yield time.Duration,
) (sdktask.Snapshot, error) {
	if s == nil {
		return sdktask.Snapshot{}, fmt.Errorf("gatewayapp: stack is unavailable")
	}
	s.mu.RLock()
	engine := s.engine
	s.mu.RUnlock()
	if engine == nil {
		return sdktask.Snapshot{}, fmt.Errorf("gatewayapp: runtime is unavailable")
	}
	return engine.WaitSubagentTask(ctx, ref, taskID, yield)
}

// CompactSession forces a model-backed checkpoint compaction for the given
// session.
func (s *Stack) CompactSession(ctx context.Context, ref sdksession.SessionRef) error {
	if s == nil {
		return fmt.Errorf("gatewayapp: stack is unavailable")
	}
	s.mu.RLock()
	engine := s.engine
	gateway := s.Gateway
	s.mu.RUnlock()
	if engine == nil {
		return fmt.Errorf("gatewayapp: runtime is unavailable")
	}
	if gateway == nil || gateway.Resolver() == nil {
		return fmt.Errorf("gatewayapp: resolver is unavailable")
	}
	resolved, err := gateway.Resolver().ResolveTurn(ctx, appgateway.TurnIntent{SessionRef: ref})
	if err != nil {
		return err
	}
	_, err = engine.Compact(ctx, localruntime.CompactRequest{
		SessionRef: ref,
		Model:      resolved.RunRequest.AgentSpec.Model,
		Trigger:    "manual",
	})
	return err
}

func defaultCompactionConfig(contextWindow int) localruntime.CompactionConfig {
	return localruntime.CompactionConfig{
		Enabled:                    true,
		DefaultContextWindowTokens: contextWindow,
	}
}

type modelLookup struct {
	mu            sync.RWMutex
	configs       map[string]ModelConfig
	profiles      map[string]ModelProfileConfig
	contextWindow int
	defaultID     string
}

func newModelLookup(store *appConfigStore, cfg ModelConfig, contextWindow int) (*modelLookup, error) {
	lookup := &modelLookup{
		configs:       map[string]ModelConfig{},
		profiles:      map[string]ModelProfileConfig{},
		contextWindow: contextWindow,
	}
	if store != nil {
		doc, err := store.Load()
		if err != nil {
			return nil, err
		}
		for _, item := range doc.Models.Profiles {
			if _, err := lookup.UpsertProfile(item); err != nil {
				return nil, err
			}
		}
		defaultFallback := ""
		for _, item := range doc.Models.Configs {
			id, err := lookup.upsert(item, false)
			if err != nil {
				return nil, err
			}
			if defaultFallback == "" {
				defaultFallback = id
			}
		}
		if strings.TrimSpace(doc.Models.DefaultID) != "" {
			lookup.SetDefault(doc.Models.DefaultID)
		} else if strings.TrimSpace(doc.Models.DefaultAlias) != "" {
			lookup.SetDefault(doc.Models.DefaultAlias)
		} else if defaultFallback != "" {
			lookup.SetDefault(defaultFallback)
		}
	}
	cfg = normalizeModelConfig(cfg)
	if cfg.Provider != "" && cfg.Model != "" {
		if _, err := lookup.Upsert(cfg); err != nil {
			return nil, err
		}
	}
	return lookup, nil
}

func (l *modelLookup) DefaultAlias() string {
	if l == nil {
		return ""
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	cfg, ok := l.configs[strings.ToLower(strings.TrimSpace(l.defaultID))]
	if !ok {
		return ""
	}
	return cfg.Alias
}

func (l *modelLookup) DefaultID() string {
	if l == nil {
		return ""
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.defaultID
}

func (l *modelLookup) ListModelAliases() []string {
	choices := l.ListModelChoices()
	aliases := make([]string, 0, len(choices))
	for _, choice := range choices {
		aliases = append(aliases, choice.Alias)
	}
	return dedupeNonEmptyStrings(aliases)
}

func (l *modelLookup) ListModelChoices() []ModelChoice {
	if l == nil {
		return nil
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	choices := make([]ModelChoice, 0, len(l.configs))
	if l.defaultID != "" {
		if cfg, ok := l.configs[strings.ToLower(l.defaultID)]; ok {
			choices = append(choices, l.modelChoiceLocked(cfg))
		}
	}
	rest := make([]ModelConfig, 0, len(l.configs))
	for id, cfg := range l.configs {
		if strings.EqualFold(id, l.defaultID) {
			continue
		}
		rest = append(rest, cfg)
	}
	sort.Slice(rest, func(i, j int) bool {
		left := strings.ToLower(strings.TrimSpace(rest[i].Alias + " " + rest[i].ID))
		right := strings.ToLower(strings.TrimSpace(rest[j].Alias + " " + rest[j].ID))
		return left < right
	})
	for _, cfg := range rest {
		choices = append(choices, l.modelChoiceLocked(cfg))
	}
	return choices
}

func (l *modelLookup) modelChoiceLocked(cfg ModelConfig) ModelChoice {
	cfg = l.hydrateModelConfigLocked(cfg)
	return ModelChoice{
		ID:         cfg.ID,
		Alias:      cfg.Alias,
		Provider:   cfg.Provider,
		Model:      cfg.Model,
		ProfileID:  cfg.ProfileID,
		EndpointID: cfg.EndpointID,
		BaseURL:    cfg.BaseURL,
		Detail:     modelChoiceDetail(cfg),
	}
}

func (l *modelLookup) ListProviderModels(provider string) []string {
	if l == nil {
		return nil
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		return nil
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	models := make([]string, 0, len(l.configs))
	for _, cfg := range l.configs {
		if strings.EqualFold(strings.TrimSpace(cfg.Provider), provider) && strings.TrimSpace(cfg.Model) != "" {
			models = append(models, strings.TrimSpace(cfg.Model))
		}
	}
	sort.Strings(models)
	return dedupeNonEmptyStrings(models)
}

func (l *modelLookup) ResolveModel(ctx context.Context, alias string, contextWindow int) (appgateway.ModelResolution, error) {
	if l == nil {
		return appgateway.ModelResolution{}, fmt.Errorf("gatewayapp: model lookup is nil")
	}
	l.mu.RLock()
	ref := firstNonEmpty(strings.TrimSpace(alias), l.defaultID)
	if ref == "" || len(l.configs) == 0 {
		l.mu.RUnlock()
		return appgateway.ModelResolution{}, fmt.Errorf("gatewayapp: no model configured; use /connect")
	}
	cfg, ok, resolveErr := l.resolveConfigLocked(ref)
	fallbackContextWindow := l.contextWindow
	l.mu.RUnlock()
	if resolveErr != nil {
		return appgateway.ModelResolution{}, resolveErr
	}
	if !ok {
		return appgateway.ModelResolution{}, fmt.Errorf("gatewayapp: unknown model alias %q", alias)
	}
	effectiveContextWindow := fallbackContextWindow
	if cfg.ContextWindowTokens > 0 {
		effectiveContextWindow = cfg.ContextWindowTokens
	}
	if contextWindow > 0 {
		effectiveContextWindow = contextWindow
	}
	factory := sdkproviders.NewFactory()
	record := sdkproviders.Config{
		Alias:                     cfg.ID,
		Provider:                  cfg.Provider,
		API:                       cfg.API,
		Model:                     cfg.Model,
		BaseURL:                   cfg.BaseURL,
		HTTPClient:                cfg.HTTPClient,
		Timeout:                   cfg.Timeout,
		MaxOutputTok:              cfg.MaxOutputTok,
		ContextWindowTokens:       effectiveContextWindow,
		ReasoningLevels:           append([]string(nil), cfg.ReasoningLevels...),
		ReasoningMode:             cfg.ReasoningMode,
		DefaultReasoningEffort:    cfg.DefaultReasoningEffort,
		ReasoningEffort:           cfg.ReasoningEffort,
		SupportedReasoningEfforts: append([]string(nil), cfg.ReasoningLevels...),
		Auth: sdkproviders.AuthConfig{
			Type:      cfg.AuthType,
			Token:     cfg.Token,
			TokenEnv:  cfg.TokenEnv,
			HeaderKey: cfg.HeaderKey,
		},
	}
	if err := factory.Register(record); err != nil {
		return appgateway.ModelResolution{}, err
	}
	llm, err := factory.NewByAlias(cfg.ID)
	if err != nil {
		return appgateway.ModelResolution{}, err
	}
	return appgateway.ModelResolution{
		Model:                  llm,
		ReasoningEffort:        cfg.ReasoningEffort,
		DefaultReasoningEffort: cfg.DefaultReasoningEffort,
	}, nil
}

func (l *modelLookup) HasAlias(alias string) bool {
	if l == nil {
		return false
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	_, ok, err := l.resolveConfigLocked(alias)
	return ok || errors.Is(err, errAmbiguousModelAlias)
}

func (l *modelLookup) UpsertProfile(profile ModelProfileConfig) (string, error) {
	if l == nil {
		return "", fmt.Errorf("gatewayapp: model lookup is nil")
	}
	profile = normalizeModelProfileConfig(profile)
	if profile.Provider == "" {
		return "", fmt.Errorf("gatewayapp: provider is required")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.profiles == nil {
		l.profiles = map[string]ModelProfileConfig{}
	}
	l.profiles[strings.ToLower(profile.ID)] = profile
	return profile.ID, nil
}

func (l *modelLookup) Upsert(cfg ModelConfig) (string, error) {
	return l.upsert(cfg, true)
}

func (l *modelLookup) upsert(cfg ModelConfig, setDefault bool) (string, error) {
	if l == nil {
		return "", fmt.Errorf("gatewayapp: model lookup is nil")
	}
	updatesProfileAuth := modelConfigCarriesProfileAuth(cfg)
	cfg = normalizeModelConfig(cfg)
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.configs == nil {
		l.configs = map[string]ModelConfig{}
	}
	if l.profiles == nil {
		l.profiles = map[string]ModelProfileConfig{}
	}
	profile, ok := l.profiles[strings.ToLower(strings.TrimSpace(cfg.ProfileID))]
	if ok {
		cfg.Provider = firstNonEmpty(cfg.Provider, profile.Provider)
		cfg.EndpointID = firstNonEmpty(cfg.EndpointID, profile.EndpointID)
		cfg = normalizeModelConfig(cfg)
	}
	if cfg.Provider == "" || cfg.Model == "" {
		return "", fmt.Errorf("gatewayapp: provider and model are required")
	}
	if !ok || updatesProfileAuth {
		profile = modelProfileFromModelConfig(cfg)
	}
	l.profiles[strings.ToLower(profile.ID)] = profile
	cfg.ProfileID = profile.ID
	cfg = mergeModelConfigProfile(cfg, profile)
	l.configs[strings.ToLower(cfg.ID)] = cfg
	if setDefault {
		l.defaultID = cfg.ID
	}
	if cfg.ContextWindowTokens > 0 {
		l.contextWindow = cfg.ContextWindowTokens
	}
	return cfg.ID, nil
}

func (l *modelLookup) Delete(alias string) error {
	if l == nil {
		return fmt.Errorf("gatewayapp: model lookup is nil")
	}
	key := strings.ToLower(strings.TrimSpace(alias))
	if key == "" {
		return fmt.Errorf("gatewayapp: model alias is required")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	cfg, ok, resolveErr := l.resolveConfigLocked(alias)
	if resolveErr != nil {
		return resolveErr
	}
	if !ok {
		return fmt.Errorf("gatewayapp: unknown model alias %q", alias)
	}
	delete(l.configs, strings.ToLower(cfg.ID))
	if !l.profileReferencedLocked(cfg.ProfileID) {
		delete(l.profiles, strings.ToLower(strings.TrimSpace(cfg.ProfileID)))
	}
	if strings.EqualFold(l.defaultID, cfg.ID) {
		l.defaultID = ""
		ids := make([]string, 0, len(l.configs))
		for id := range l.configs {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		if len(ids) > 0 {
			l.defaultID = l.configs[ids[0]].ID
		}
	}
	return nil
}

func (l *modelLookup) SetDefault(alias string) {
	if l == nil {
		return
	}
	key := strings.ToLower(strings.TrimSpace(alias))
	if key == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if cfg, ok, err := l.resolveConfigLocked(alias); err == nil && ok {
		l.defaultID = cfg.ID
	}
}

func (s *Stack) SessionUsageSnapshot(ctx context.Context, ref sdksession.SessionRef, modelAlias string) (sdkcompact.UsageSnapshot, error) {
	if s == nil || s.Sessions == nil {
		return sdkcompact.UsageSnapshot{}, fmt.Errorf("gatewayapp: sessions service unavailable")
	}
	if strings.TrimSpace(ref.SessionID) == "" {
		return sdkcompact.UsageSnapshot{}, nil
	}
	events, err := s.Sessions.Events(ctx, sdksession.EventsRequest{SessionRef: ref})
	if err != nil {
		return sdkcompact.UsageSnapshot{}, err
	}
	alias := strings.TrimSpace(modelAlias)
	if alias == "" && s.lookup != nil {
		alias = strings.TrimSpace(s.lookup.DefaultAlias())
	}
	contextWindow := s.currentContextWindowTokensForAlias(alias)
	return localruntime.ComputeUsageSnapshot(events, nil, contextWindow, localruntime.CompactionConfig{
		DefaultContextWindowTokens: contextWindow,
	}), nil
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

func (l *modelLookup) Snapshot() persistedModelConfig {
	if l == nil {
		return persistedModelConfig{}
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	configs := make([]ModelConfig, 0, len(l.configs))
	for _, cfg := range l.configs {
		configs = append(configs, cfg)
	}
	sort.Slice(configs, func(i, j int) bool {
		return strings.ToLower(strings.TrimSpace(configs[i].Alias+" "+configs[i].ID)) < strings.ToLower(strings.TrimSpace(configs[j].Alias+" "+configs[j].ID))
	})
	profiles := make([]ModelProfileConfig, 0, len(l.profiles))
	for _, profile := range l.profiles {
		profiles = append(profiles, profile)
	}
	sort.Slice(profiles, func(i, j int) bool {
		return strings.ToLower(strings.TrimSpace(profiles[i].ID)) < strings.ToLower(strings.TrimSpace(profiles[j].ID))
	})
	defaultAlias := ""
	if cfg, ok := l.configs[strings.ToLower(strings.TrimSpace(l.defaultID))]; ok {
		defaultAlias = cfg.Alias
	}
	return persistedModelConfig{
		DefaultAlias: defaultAlias,
		DefaultID:    l.defaultID,
		Profiles:     profiles,
		Configs:      configs,
	}
}

func (l *modelLookup) Config(alias string) (ModelConfig, bool) {
	if l == nil {
		return ModelConfig{}, false
	}
	key := strings.ToLower(strings.TrimSpace(alias))
	if key == "" {
		return ModelConfig{}, false
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	cfg, ok, err := l.resolveConfigLocked(key)
	if err != nil || !ok {
		return ModelConfig{}, false
	}
	return cfg, true
}

func (l *modelLookup) ResolveConfig(alias string) (ModelConfig, error) {
	if l == nil {
		return ModelConfig{}, fmt.Errorf("gatewayapp: model lookup is nil")
	}
	key := strings.ToLower(strings.TrimSpace(alias))
	if key == "" {
		return ModelConfig{}, fmt.Errorf("gatewayapp: model alias is required")
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	cfg, ok, err := l.resolveConfigLocked(key)
	if err != nil {
		return ModelConfig{}, err
	}
	if !ok {
		return ModelConfig{}, fmt.Errorf("gatewayapp: unknown model alias %q", alias)
	}
	return cfg, nil
}

func (l *modelLookup) resolveConfigLocked(ref string) (ModelConfig, bool, error) {
	ref = strings.ToLower(strings.TrimSpace(ref))
	if ref == "" {
		return ModelConfig{}, false, nil
	}
	if cfg, ok := l.configs[ref]; ok {
		return l.hydrateModelConfigLocked(cfg), true, nil
	}
	var match ModelConfig
	matches := 0
	for _, cfg := range l.configs {
		if strings.EqualFold(strings.TrimSpace(cfg.Alias), ref) {
			match = cfg
			matches++
		}
	}
	if matches > 1 {
		return ModelConfig{}, false, fmt.Errorf("gatewayapp: %w %q; use a profile-qualified model id", errAmbiguousModelAlias, ref)
	}
	if matches == 0 {
		return ModelConfig{}, false, nil
	}
	return l.hydrateModelConfigLocked(match), true, nil
}

func (l *modelLookup) profileReferencedLocked(profileID string) bool {
	profileID = strings.ToLower(strings.TrimSpace(profileID))
	if profileID == "" {
		return false
	}
	for _, cfg := range l.configs {
		if strings.EqualFold(strings.TrimSpace(cfg.ProfileID), profileID) {
			return true
		}
	}
	return false
}

func (l *modelLookup) hydrateModelConfigLocked(cfg ModelConfig) ModelConfig {
	cfg = normalizeModelConfig(cfg)
	if l == nil || strings.TrimSpace(cfg.ProfileID) == "" {
		return cfg
	}
	profile, ok := l.profiles[strings.ToLower(strings.TrimSpace(cfg.ProfileID))]
	if !ok {
		return cfg
	}
	return mergeModelConfigProfile(cfg, profile)
}

func modelChoiceDetail(cfg ModelConfig) string {
	parts := []string{}
	if profileID := strings.TrimSpace(cfg.ProfileID); profileID != "" {
		parts = append(parts, "profile:"+profileID)
	}
	if endpoint := strings.TrimSpace(cfg.EndpointID); endpoint != "" && endpoint != "default" {
		parts = append(parts, endpoint)
	}
	if baseURL := strings.TrimSpace(cfg.BaseURL); baseURL != "" {
		parts = append(parts, baseURL)
	}
	if tokenEnv := strings.TrimSpace(cfg.TokenEnv); tokenEnv != "" {
		parts = append(parts, "env:"+tokenEnv)
	}
	if len(parts) == 0 {
		return "configured model"
	}
	return strings.Join(parts, " · ")
}

func modelChoiceFromConfig(cfg ModelConfig) ModelChoice {
	cfg = normalizeModelConfig(cfg)
	return ModelChoice{
		ID:         cfg.ID,
		Alias:      cfg.Alias,
		Provider:   cfg.Provider,
		Model:      cfg.Model,
		ProfileID:  cfg.ProfileID,
		EndpointID: cfg.EndpointID,
		BaseURL:    cfg.BaseURL,
		Detail:     modelChoiceDetail(cfg),
	}
}

func dedupeModelChoices(choices []ModelChoice) []ModelChoice {
	if len(choices) == 0 {
		return nil
	}
	out := make([]ModelChoice, 0, len(choices))
	seen := map[string]struct{}{}
	for _, choice := range choices {
		id := strings.ToLower(strings.TrimSpace(choice.ID))
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, choice)
	}
	return out
}

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
	if s == nil || s.store == nil {
		return nil
	}
	doc, err := s.store.Load()
	if err != nil {
		return err
	}
	s.mu.RLock()
	doc.Sandbox = s.sandbox
	s.mu.RUnlock()
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
	sandboxRuntime, err := sdksandbox.New(sdksandbox.Config{
		CWD:              s.Workspace.CWD,
		RequestedBackend: sdksandbox.Backend(sandboxCfg.RequestedType),
		HelperPath:       sandboxCfg.HelperPath,
		ReadableRoots:    append([]string(nil), sandboxCfg.ReadableRoots...),
		WritableRoots:    append([]string(nil), sandboxCfg.WritableRoots...),
		ReadOnlySubpaths: append([]string(nil), sandboxCfg.ReadOnlySubpaths...),
	})
	if err != nil {
		return err
	}
	tools, err := sdkbuiltin.BuildCoreTools(sdkbuiltin.CoreToolsConfig{Runtime: sandboxRuntime})
	if err != nil {
		_ = sandboxRuntime.Close()
		return err
	}
	rt, err := localruntime.New(localruntime.Config{
		Sessions:          s.Sessions,
		AgentFactory:      chat.Factory{},
		DefaultPolicyMode: policyMode(runtimeCfg.PermissionMode),
		Compaction:        defaultCompactionConfig(runtimeCfg.ContextWindow),
		Assembly:          runtimeCfg.Assembly,
		TaskStore:         s.taskStore,
	})
	if err != nil {
		_ = sandboxRuntime.Close()
		return err
	}
	resolver, err := appgateway.NewAssemblyResolver(appgateway.AssemblyResolverConfig{
		Sessions:          s.Sessions,
		Assembly:          runtimeCfg.Assembly,
		DefaultModelAlias: s.lookup.DefaultID(),
		ContextWindow:     runtimeCfg.ContextWindow,
		ModelLookup:       s.lookup,
		Tools:             tools,
		BaseMetadata:      cloneMap(runtimeCfg.BaseMetadata),
		ToolAugmenter: func(ctx context.Context, req appgateway.ToolAugmentContext) (appgateway.ToolAugmentation, error) {
			s.mu.RLock()
			runtimeCfg := s.runtime
			s.mu.RUnlock()
			var participants []sdksession.ParticipantBinding
			if strings.TrimSpace(req.SessionRef.SessionID) != "" {
				session, err := s.Sessions.Session(ctx, req.SessionRef)
				if err != nil {
					return appgateway.ToolAugmentation{}, err
				}
				participants = session.Participants
			}
			agents := delegationAgentsForSpawn(runtimeCfg.Assembly, participants)
			if len(agents) == 0 {
				return appgateway.ToolAugmentation{}, nil
			}
			metadata := map[string]any{}
			if systemPrompt := stringFromMap(runtimeCfg.BaseMetadata, "system_prompt"); systemPrompt != "" {
				metadata["system_prompt"] = systemPromptWithDelegationGuidance(systemPrompt)
			}
			return appgateway.ToolAugmentation{
				Tools:    []sdktool.Tool{spawntool.New(agents)},
				Metadata: metadata,
			}, nil
		},
	})
	if err != nil {
		_ = sandboxRuntime.Close()
		return err
	}
	gw, err := appgateway.New(appgateway.Config{
		Sessions:         s.Sessions,
		Runtime:          rt,
		Resolver:         resolver,
		ApprovalReviewer: newModelApprovalReviewer(s.Sessions),
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
	s.Gateway = gw
	s.exec = sandboxRuntime
	s.engine = rt
	s.mu.Unlock()
	if oldExec != nil {
		_ = oldExec.Close()
	}
	return nil
}

func normalizeModelConfig(cfg ModelConfig) ModelConfig {
	cfg.ID = strings.ToLower(strings.TrimSpace(cfg.ID))
	cfg.Provider = strings.ToLower(strings.TrimSpace(cfg.Provider))
	cfg.Model = strings.TrimSpace(cfg.Model)
	if cfg.Provider == "minimax" && cfg.API == sdkproviders.APIAnthropicCompatible {
		cfg.API = sdkproviders.APIMiniMax
	}
	cfg.EndpointID = normalizeEndpointID(cfg.Provider, cfg.EndpointID, cfg.BaseURL, cfg.API)
	cfg.ProfileID = strings.ToLower(strings.TrimSpace(cfg.ProfileID))
	if cfg.ProfileID == "" {
		cfg.ProfileID = buildProfileID(cfg.Provider, cfg.EndpointID, cfg.BaseURL)
	}
	cfg.Alias = strings.ToLower(strings.TrimSpace(cfg.Alias))
	if cfg.Alias == "" {
		cfg.Alias = buildAlias(cfg.Provider, cfg.Model)
	}
	if id := buildModelID(cfg.ProfileID, cfg.Alias); id != "" {
		cfg.ID = id
	}
	if cfg.API == "" {
		cfg.API = defaultModelAPIForProvider(cfg.Provider)
	}
	if cfg.AuthType == "" {
		cfg.AuthType = defaultAuthTypeForProvider(cfg.Provider)
	}
	if cfg.DefaultReasoningEffort == "" && cfg.ReasoningEffort != "" {
		cfg.DefaultReasoningEffort = cfg.ReasoningEffort
	}
	if cfg.MaxOutputTok <= 0 {
		cfg.MaxOutputTok = 4096
	}
	if cfg.ContextWindowTokens < 0 {
		cfg.ContextWindowTokens = 0
	}
	cfg.ReasoningLevels = dedupeNonEmptyStrings(cfg.ReasoningLevels)
	if cfg.Token == "" && strings.TrimSpace(cfg.TokenEnv) != "" {
		cfg.Token = strings.TrimSpace(os.Getenv(strings.TrimSpace(cfg.TokenEnv)))
	}
	return cfg
}

func normalizeModelProfileConfig(profile ModelProfileConfig) ModelProfileConfig {
	profile.ID = strings.ToLower(strings.TrimSpace(profile.ID))
	profile.Provider = strings.ToLower(strings.TrimSpace(profile.Provider))
	if profile.Provider == "minimax" && profile.API == sdkproviders.APIAnthropicCompatible {
		profile.API = sdkproviders.APIMiniMax
	}
	profile.EndpointID = normalizeEndpointID(profile.Provider, profile.EndpointID, profile.BaseURL, profile.API)
	if profile.ID == "" {
		profile.ID = buildProfileID(profile.Provider, profile.EndpointID, profile.BaseURL)
	}
	if profile.API == "" {
		profile.API = defaultModelAPIForProvider(profile.Provider)
	}
	if profile.AuthType == "" {
		profile.AuthType = defaultAuthTypeForProvider(profile.Provider)
	}
	if profile.Token == "" && strings.TrimSpace(profile.TokenEnv) != "" {
		profile.Token = strings.TrimSpace(os.Getenv(strings.TrimSpace(profile.TokenEnv)))
	}
	return profile
}

func modelProfileFromModelConfig(cfg ModelConfig) ModelProfileConfig {
	cfg = normalizeModelConfig(cfg)
	return normalizeModelProfileConfig(ModelProfileConfig{
		ID:           cfg.ProfileID,
		Provider:     cfg.Provider,
		EndpointID:   cfg.EndpointID,
		API:          cfg.API,
		BaseURL:      cfg.BaseURL,
		HTTPClient:   cfg.HTTPClient,
		Token:        cfg.Token,
		TokenEnv:     cfg.TokenEnv,
		PersistToken: cfg.PersistToken,
		AuthType:     cfg.AuthType,
		HeaderKey:    cfg.HeaderKey,
		Timeout:      cfg.Timeout,
	})
}

func modelConfigCarriesProfileFields(cfg ModelConfig) bool {
	return strings.TrimSpace(cfg.Provider) != "" ||
		strings.TrimSpace(cfg.EndpointID) != "" ||
		strings.TrimSpace(cfg.BaseURL) != "" ||
		strings.TrimSpace(cfg.Token) != "" ||
		strings.TrimSpace(cfg.TokenEnv) != "" ||
		strings.TrimSpace(cfg.HeaderKey) != "" ||
		cfg.HTTPClient != nil ||
		cfg.API != "" ||
		cfg.AuthType != "" ||
		cfg.Timeout > 0
}

func modelConfigCarriesProfileAuth(cfg ModelConfig) bool {
	return strings.TrimSpace(cfg.Token) != "" ||
		strings.TrimSpace(cfg.TokenEnv) != "" ||
		strings.TrimSpace(cfg.HeaderKey) != "" ||
		cfg.PersistToken ||
		cfg.HTTPClient != nil
}

func mergeModelConfigProfile(cfg ModelConfig, profile ModelProfileConfig) ModelConfig {
	cfg = normalizeModelConfig(cfg)
	profile = normalizeModelProfileConfig(profile)
	cfg.ProfileID = profile.ID
	cfg.Provider = firstNonEmpty(profile.Provider, cfg.Provider)
	cfg.EndpointID = profile.EndpointID
	cfg.API = firstNonEmptyAPI(profile.API, cfg.API)
	cfg.BaseURL = firstNonEmpty(profile.BaseURL, cfg.BaseURL)
	cfg.HTTPClient = firstNonNilHTTPClient(profile.HTTPClient, cfg.HTTPClient)
	cfg.Token = firstNonEmpty(profile.Token, cfg.Token)
	cfg.TokenEnv = firstNonEmpty(profile.TokenEnv, cfg.TokenEnv)
	cfg.PersistToken = profile.PersistToken || cfg.PersistToken
	cfg.AuthType = firstNonEmptyAuthType(profile.AuthType, cfg.AuthType)
	cfg.HeaderKey = firstNonEmpty(profile.HeaderKey, cfg.HeaderKey)
	if profile.Timeout > 0 {
		cfg.Timeout = profile.Timeout
	}
	return normalizeModelConfig(cfg)
}

func modelConfigSupportsReasoningEffort(cfg ModelConfig, effort string) bool {
	effort = strings.ToLower(strings.TrimSpace(effort))
	if effort == "" {
		return true
	}
	for _, level := range cfg.ReasoningLevels {
		if strings.EqualFold(strings.TrimSpace(level), effort) {
			return true
		}
	}
	mode := strings.ToLower(strings.TrimSpace(cfg.ReasoningMode))
	switch mode {
	case "toggle":
		return effort == "none" || effort == "high" || effort == "max" || effort == "enabled"
	case "fixed":
		return effort == "low" || effort == "medium" || effort == "high"
	case "":
		return true
	default:
		return false
	}
}

func defaultModelAPIForProvider(provider string) sdkproviders.APIType {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "openai":
		return sdkproviders.APIOpenAI
	case "openai-compatible":
		return sdkproviders.APIOpenAICompatible
	case "openrouter":
		return sdkproviders.APIOpenRouter
	case "codefree":
		return sdkproviders.APICodeFree
	case "gemini":
		return sdkproviders.APIGemini
	case "anthropic":
		return sdkproviders.APIAnthropic
	case "anthropic-compatible":
		return sdkproviders.APIAnthropicCompatible
	case "minimax":
		return sdkproviders.APIMiniMax
	case "deepseek":
		return sdkproviders.APIDeepSeek
	case "xiaomi":
		return sdkproviders.APIMimo
	case "volcengine":
		return sdkproviders.APIVolcengine
	case "volcengine-coding-plan", "volcengine_coding_plan":
		return sdkproviders.APIVolcengineCoding
	case "ollama":
		return sdkproviders.APIOllama
	default:
		return ""
	}
}

func sanitizePersistedModelConfig(cfg ModelConfig) ModelConfig {
	cfg = normalizeModelConfig(cfg)
	if cfg.ProfileID != "" {
		cfg.Provider = ""
		cfg.EndpointID = ""
		cfg.API = ""
		cfg.BaseURL = ""
		cfg.HTTPClient = nil
		cfg.Token = ""
		cfg.TokenEnv = ""
		cfg.PersistToken = false
		cfg.AuthType = ""
		cfg.HeaderKey = ""
		cfg.Timeout = 0
	}
	if !cfg.PersistToken {
		cfg.Token = ""
	}
	if cfg.API == defaultModelAPIForProvider(cfg.Provider) {
		cfg.API = ""
	}
	if cfg.AuthType == defaultAuthTypeForProvider(cfg.Provider) {
		cfg.AuthType = ""
	}
	if cfg.DefaultReasoningEffort == cfg.ReasoningEffort {
		cfg.DefaultReasoningEffort = ""
	}
	if cfg.MaxOutputTok == 4096 {
		cfg.MaxOutputTok = 0
	}
	cfg.PersistToken = false
	cfg.HTTPClient = nil
	cfg.Timeout = 0
	return cfg
}

func sanitizePersistedModelProfile(profile ModelProfileConfig) ModelProfileConfig {
	profile = normalizeModelProfileConfig(profile)
	if !profile.PersistToken {
		profile.Token = ""
	}
	if profile.API == defaultModelAPIForProvider(profile.Provider) {
		profile.API = ""
	}
	if profile.AuthType == defaultAuthTypeForProvider(profile.Provider) {
		profile.AuthType = ""
	}
	profile.PersistToken = false
	profile.HTTPClient = nil
	profile.Timeout = 0
	return profile
}

func defaultAuthTypeForProvider(provider string) sdkproviders.AuthType {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "minimax":
		return sdkproviders.AuthBearerToken
	case "ollama", "codefree":
		return sdkproviders.AuthNone
	default:
		return sdkproviders.AuthAPIKey
	}
}

func (s *Stack) rejectReconfigureWhileActive(action string) error {
	if s == nil {
		return fmt.Errorf("gatewayapp: stack is unavailable")
	}
	return rejectReconfigureWithActiveTurns(s.Gateway, action)
}

func rejectReconfigureWithActiveTurns(gw *appgateway.Gateway, action string) error {
	if gw == nil {
		return nil
	}
	active := gw.ActiveTurns()
	if len(active) == 0 {
		return nil
	}
	sessions := make([]string, 0, len(active))
	for _, item := range active {
		if sessionID := strings.TrimSpace(item.SessionRef.SessionID); sessionID != "" {
			sessions = append(sessions, sessionID)
		}
	}
	label := strings.TrimSpace(action)
	if label == "" {
		label = "reconfigure runtime"
	}
	if len(sessions) > 0 {
		return fmt.Errorf(
			"gatewayapp: cannot %s while %d turn(s) are active (session(s): %s); wait for completion or interrupt the running turn first",
			label,
			len(active),
			strings.Join(dedupeNonEmptyStrings(sessions), ", "),
		)
	}
	return fmt.Errorf(
		"gatewayapp: cannot %s while %d turn(s) are active; wait for completion or interrupt the running turn first",
		label,
		len(active),
	)
}

func buildAlias(provider string, modelName string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	modelName = strings.TrimSpace(modelName)
	if provider == "" {
		return strings.ToLower(modelName)
	}
	if modelName == "" {
		return provider
	}
	return strings.ToLower(provider + "/" + modelName)
}

func buildProfileID(provider string, endpointID string, baseURL string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	endpointID = sanitizeConfigIDPart(firstNonEmpty(strings.TrimSpace(endpointID), "default"))
	if endpointID == "custom" || strings.HasPrefix(endpointID, "custom-") {
		endpointID = "custom-" + shortConfigHash(normalizedConfigBaseURL(baseURL))
	}
	if provider == "" {
		return endpointID
	}
	return provider + "@" + endpointID
}

func buildModelID(profileID string, alias string) string {
	profileID = strings.ToLower(strings.TrimSpace(profileID))
	alias = strings.ToLower(strings.TrimSpace(alias))
	if profileID == "" {
		return alias
	}
	if alias == "" {
		return profileID
	}
	return profileID + "/" + alias
}

func normalizeEndpointID(provider string, endpointID string, baseURL string, api sdkproviders.APIType) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	endpointID = sanitizeConfigIDPart(endpointID)
	if endpointID != "" {
		return endpointID
	}
	normalizedBaseURL := normalizedConfigBaseURL(baseURL)
	switch provider {
	case "openai":
		if normalizedBaseURL == "" || normalizedBaseURL == "https://api.openai.com/v1" {
			return "default"
		}
		return "custom-" + shortConfigHash(normalizedBaseURL)
	case "openai-compatible":
		if normalizedBaseURL == "" || normalizedBaseURL == "https://api.openai.com/v1" {
			return "default"
		}
		return "custom-" + shortConfigHash(normalizedBaseURL)
	case "openrouter":
		if normalizedBaseURL == "" || normalizedBaseURL == "https://openrouter.ai/api/v1" {
			return "default"
		}
		return "custom-" + shortConfigHash(normalizedBaseURL)
	case "gemini":
		if normalizedBaseURL == "" || normalizedBaseURL == "https://generativelanguage.googleapis.com/v1beta" {
			return "default"
		}
		return "custom-" + shortConfigHash(normalizedBaseURL)
	case "anthropic":
		if normalizedBaseURL == "" || normalizedBaseURL == "https://api.anthropic.com" {
			return "default"
		}
		return "custom-" + shortConfigHash(normalizedBaseURL)
	case "anthropic-compatible":
		if normalizedBaseURL == "" || normalizedBaseURL == "https://api.anthropic.com" {
			return "default"
		}
		return "custom-" + shortConfigHash(normalizedBaseURL)
	case "deepseek":
		if normalizedBaseURL == "" || normalizedBaseURL == "https://api.deepseek.com/v1" {
			return "default"
		}
		return "custom-" + shortConfigHash(normalizedBaseURL)
	case "minimax":
		if normalizedBaseURL == "" || normalizedBaseURL == "https://api.minimaxi.com/anthropic" {
			return "default"
		}
		return "custom-" + shortConfigHash(normalizedBaseURL)
	case "codefree":
		if normalizedBaseURL == "" || normalizedBaseURL == "https://www.srdcloud.cn" {
			return "default"
		}
		return "custom-" + shortConfigHash(normalizedBaseURL)
	case "ollama":
		if normalizedBaseURL == "" || normalizedBaseURL == "http://localhost:11434" {
			return "default"
		}
		return "custom-" + shortConfigHash(normalizedBaseURL)
	case "xiaomi":
		switch normalizedBaseURL {
		case "https://api.xiaomimimo.com/v1", "":
			return "api-cn"
		case "https://token-plan-cn.xiaomimimo.com/v1":
			return "token-plan-cn"
		default:
			return "custom-" + shortConfigHash(normalizedBaseURL)
		}
	case "volcengine":
		if api == sdkproviders.APIVolcengineCoding || normalizedBaseURL == "https://ark.cn-beijing.volces.com/api/coding/v3" {
			return "coding-plan"
		}
		if normalizedBaseURL == "" || normalizedBaseURL == "https://ark.cn-beijing.volces.com/api/v3" {
			return "standard"
		}
		return "custom-" + shortConfigHash(normalizedBaseURL)
	default:
		if normalizedBaseURL == "" {
			return "default"
		}
		return "custom-" + shortConfigHash(normalizedBaseURL)
	}
}

func normalizedConfigBaseURL(baseURL string) string {
	return strings.ToLower(strings.TrimRight(strings.TrimSpace(baseURL), "/"))
}

func sanitizeConfigIDPart(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-'
		if ok {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func shortConfigHash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "default"
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:10]
}

func firstNonEmptyAPI(values ...sdkproviders.APIType) sdkproviders.APIType {
	for _, value := range values {
		if strings.TrimSpace(string(value)) != "" {
			return value
		}
	}
	return ""
}

func firstNonEmptyAuthType(values ...sdkproviders.AuthType) sdkproviders.AuthType {
	for _, value := range values {
		if strings.TrimSpace(string(value)) != "" {
			return value
		}
	}
	return ""
}

func firstNonNilHTTPClient(values ...*http.Client) *http.Client {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func normalizeSessionMode(mode string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "manual":
		return "manual", nil
	case "", "auto", "default", "auto-review", "auto_review", "autoreview", "plan", "full_control", "full_access":
		return "auto-review", nil
	default:
		return "auto-review", nil
	}
}

func normalizeSessionModeOrDefault(mode string) string {
	normalized, err := normalizeSessionMode(mode)
	if err != nil {
		return "auto-review"
	}
	return normalized
}

func nextSessionMode(mode string) string {
	switch normalizeSessionModeOrDefault(mode) {
	case "manual":
		return "auto-review"
	default:
		return "manual"
	}
}

func normalizeSandboxBackend(backend string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(backend)) {
	case "", "auto":
		return "auto", nil
	case "seatbelt":
		return "seatbelt", nil
	case "bwrap":
		return "bwrap", nil
	case "landlock":
		return "landlock", nil
	default:
		return "", fmt.Errorf("gatewayapp: unknown sandbox backend %q", backend)
	}
}

func mergeSandboxConfig(stored SandboxConfig, override SandboxConfig) SandboxConfig {
	stored = normalizeSandboxConfig(stored)
	override = normalizeSandboxConfig(override)
	if override.RequestedType != "" {
		stored.RequestedType = override.RequestedType
	}
	if override.HelperPath != "" {
		stored.HelperPath = override.HelperPath
	}
	if len(override.ReadableRoots) > 0 {
		stored.ReadableRoots = append([]string(nil), override.ReadableRoots...)
	}
	if len(override.WritableRoots) > 0 {
		stored.WritableRoots = append([]string(nil), override.WritableRoots...)
	}
	if len(override.ReadOnlySubpaths) > 0 {
		stored.ReadOnlySubpaths = append([]string(nil), override.ReadOnlySubpaths...)
	}
	if stored.RequestedType == "" {
		stored.RequestedType = "auto"
	}
	return stored
}

func effectiveSandboxConfig(cfg SandboxConfig, workspaceDir string) SandboxConfig {
	cfg = normalizeSandboxConfig(cfg)
	cfg.WritableRoots = dedupeStrings(append(cfg.WritableRoots, defaultSkillSandboxRoots(workspaceDir)...))
	return cfg
}

func withSandboxPolicyRootMetadata(metadata map[string]any, cfg SandboxConfig, workspaceDir string) map[string]any {
	out := cloneMap(metadata)
	if out == nil {
		out = map[string]any{}
	}
	effective := effectiveSandboxConfig(cfg, workspaceDir)
	if len(effective.ReadableRoots) > 0 {
		out["policy_extra_read_roots"] = mergePolicyRootMetadata(out["policy_extra_read_roots"], effective.ReadableRoots)
	}
	if len(effective.WritableRoots) > 0 {
		out["policy_extra_write_roots"] = mergePolicyRootMetadata(out["policy_extra_write_roots"], effective.WritableRoots)
	}
	return out
}

func mergePolicyRootMetadata(existing any, values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	appendOne := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		if _, ok := seen[value]; ok {
			return
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	switch typed := existing.(type) {
	case []string:
		for _, one := range typed {
			appendOne(one)
		}
	case []any:
		for _, one := range typed {
			text, _ := one.(string)
			appendOne(text)
		}
	}
	for _, one := range values {
		appendOne(one)
	}
	return out
}

func defaultSkillSandboxRoots(workspaceDir string) []string {
	dirs := DefaultSkillDiscoveryDirs(workspaceDir)
	out := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		resolved, err := resolvePromptPath(dir)
		if err != nil {
			continue
		}
		out = append(out, resolved)
	}
	return dedupeStrings(out)
}

func dedupeNonEmptyStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(trimmed)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func policyMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "manual":
		return sdkpolicy.ModeManual
	default:
		return sdkpolicy.ModeAutoReview
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func cloneMap(values map[string]any) map[string]any {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]any, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func stringFromMap(values map[string]any, key string) string {
	if len(values) == 0 {
		return ""
	}
	value, _ := values[key].(string)
	return strings.TrimSpace(value)
}

func mustGetwd() string {
	cwd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return cwd
}
