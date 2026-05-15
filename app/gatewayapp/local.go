package gatewayapp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/OnslaughtSnail/caelis/app/gatewayapp/internal/agentregistry"
	"github.com/OnslaughtSnail/caelis/app/gatewayapp/internal/modelregistry"
	"github.com/OnslaughtSnail/caelis/app/gatewayapp/internal/sandboxpolicy"
	"github.com/OnslaughtSnail/caelis/impl/agent/local"
	"github.com/OnslaughtSnail/caelis/impl/model/providers"
	"github.com/OnslaughtSnail/caelis/impl/policy/presets"
	_ "github.com/OnslaughtSnail/caelis/impl/sandbox/bwrap"
	_ "github.com/OnslaughtSnail/caelis/impl/sandbox/host"
	_ "github.com/OnslaughtSnail/caelis/impl/sandbox/landlock"
	_ "github.com/OnslaughtSnail/caelis/impl/sandbox/seatbelt"
	sessionfile "github.com/OnslaughtSnail/caelis/impl/session/file"
	taskfile "github.com/OnslaughtSnail/caelis/impl/task/file"
	"github.com/OnslaughtSnail/caelis/impl/tool/builtin/spawn"
	"github.com/OnslaughtSnail/caelis/kernel"
	"github.com/OnslaughtSnail/caelis/ports/agent"
	"github.com/OnslaughtSnail/caelis/ports/assembly"
	"github.com/OnslaughtSnail/caelis/ports/compact"
	"github.com/OnslaughtSnail/caelis/ports/controller"
	"github.com/OnslaughtSnail/caelis/ports/delegation"
	"github.com/OnslaughtSnail/caelis/ports/sandbox"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/ports/task"
	"github.com/OnslaughtSnail/caelis/ports/tool"
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
	Assembly       assembly.ResolvedAssembly
	Model          ModelConfig
	Sandbox        SandboxConfig
}

type ModelConfig = modelregistry.Config
type ModelProfileConfig = modelregistry.ProfileConfig
type ModelChoice = modelregistry.Choice

type Stack struct {
	Gateway       *kernel.Gateway
	Sessions      session.Service
	AppName       string
	UserID        string
	Workspace     session.WorkspaceRef
	lookup        *modelLookup
	store         *appConfigStore
	storeDir      string
	mu            sync.RWMutex
	reconfigureMu sync.Mutex
	runtime       stackRuntimeConfig
	sandbox       SandboxConfig
	exec          sandbox.Runtime
	engine        *local.Runtime
	taskStore     *taskfile.Store
}

func (s *Stack) CurrentGateway() *kernel.Gateway {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Gateway
}

type SessionRuntimeState struct {
	ModelID         string
	ModelAlias      string
	ReasoningEffort string
	SessionMode     string
	SandboxMode     string
}

type SandboxStatus struct {
	RequestedBackend   string
	ResolvedBackend    string
	Route              string
	FallbackReason     string
	InstallHint        string
	SecuritySummary    string
	AutoReviewDisabled bool
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

type ACPControllerStatus = controller.ControllerStatus
type ACPControllerCommand = controller.ControllerCommand
type ACPControllerConfigChoice = controller.ControllerConfigChoice
type ACPControllerMode = controller.ControllerMode

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
	ApprovalRequester agent.ApprovalRequester
}

type stackRuntimeConfig struct {
	PermissionMode              string
	ContextWindow               int
	Model                       ModelConfig
	BaseAssembly                assembly.ResolvedAssembly
	Assembly                    assembly.ResolvedAssembly
	BaseMetadata                map[string]any
	EstimatedPromptPrefixTokens int
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
	baseAssembly := assembly.CloneResolvedAssembly(cfg.Assembly)
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
		Workspace: session.WorkspaceRef{
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
			Assembly:       assembly.CloneResolvedAssembly(cfg.Assembly),
			BaseMetadata:   cloneMap(baseMetadata),
		},
		sandbox: sandboxCfg,
	}
	if err := stack.rebuildGateway(); err != nil {
		return nil, err
	}
	return stack, nil
}

func delegationAgentsFromAssembly(assembly assembly.ResolvedAssembly) []delegation.Agent {
	out := make([]delegation.Agent, 0, len(assembly.Agents))
	for _, one := range assembly.Agents {
		agent := delegation.NormalizeAgent(delegation.Agent{
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

func delegationAgentsForSpawn(assembly assembly.ResolvedAssembly, _ []session.ParticipantBinding) []delegation.Agent {
	if len(assembly.Agents) == 0 {
		return nil
	}
	return delegationAgentsFromAssembly(assembly)
}

func systemPromptWithDelegationGuidance(systemPrompt string) string {
	systemPrompt = strings.TrimRight(strings.TrimSpace(systemPrompt), "\n")
	guidance := "- Use SPAWN for bounded child ACP work that can run independently; use TASK wait, cancel, or write to control yielded work."
	if strings.Contains(systemPrompt, "SPAWN for bounded child ACP work") {
		return systemPrompt
	}
	if systemPrompt == "" {
		return guidance
	}
	return systemPrompt + "\n" + guidance
}

func withConfiguredACPAgents(assembly assembly.ResolvedAssembly, configured []AgentConfig, self assembly.AgentConfig) assembly.ResolvedAssembly {
	return agentregistry.WithConfiguredAgents(assembly, configured, self)
}

func agentConfigToPlugin(in AgentConfig) assembly.AgentConfig {
	return agentregistry.AgentConfigToPlugin(in)
}

func pluginAgentToConfig(in assembly.AgentConfig, builtin bool) AgentConfig {
	return agentregistry.PluginAgentToConfig(in, builtin)
}

type defaultSelfACPAgentConfig struct {
	Config       Config
	AppName      string
	UserID       string
	StoreDir     string
	WorkspaceKey string
	WorkspaceCWD string
}

func defaultSelfACPAgent(cfg defaultSelfACPAgentConfig) assembly.AgentConfig {
	return agentregistry.DefaultSelfAgent(agentregistry.DefaultSelfConfig{
		Config:       agentRuntimeConfig(cfg.Config),
		AppName:      cfg.AppName,
		UserID:       cfg.UserID,
		StoreDir:     cfg.StoreDir,
		WorkspaceKey: cfg.WorkspaceKey,
		WorkspaceCWD: cfg.WorkspaceCWD,
	})
}

func selfRuntimeArgs(cfg Config) []string {
	return agentregistry.SelfRuntimeArgs(agentRuntimeConfig(cfg))
}

func selfRuntimeInvocation(cfg Config) ([]string, map[string]string) {
	return agentregistry.SelfRuntimeInvocation(agentRuntimeConfig(cfg))
}

func agentRuntimeConfig(cfg Config) agentregistry.RuntimeConfig {
	return agentregistry.RuntimeConfig{
		AppName:        cfg.AppName,
		UserID:         cfg.UserID,
		StoreDir:       cfg.StoreDir,
		WorkspaceKey:   cfg.WorkspaceKey,
		WorkspaceCWD:   cfg.WorkspaceCWD,
		PermissionMode: cfg.PermissionMode,
		ContextWindow:  cfg.ContextWindow,
		Model:          cfg.Model,
	}
}

func builtInACPAgents() []assembly.AgentConfig {
	return agentregistry.BuiltInAgents()
}

type builtinACPAdapterPackage = agentregistry.BuiltinAdapterPackage

func builtinACPAdapterPackageFor(name string) (builtinACPAdapterPackage, bool) {
	return agentregistry.BuiltinAdapterPackageFor(name)
}

func (s *Stack) RegisterBuiltinACPAgent(name string) error {
	return s.RegisterBuiltinACPAgentWithOptions(context.Background(), name, RegisterBuiltinACPAgentOptions{})
}

func (s *Stack) RegisterACPAgent(ctx context.Context, cfg AgentConfig) error {
	if s == nil || s.store == nil {
		return fmt.Errorf("gatewayapp: app config store unavailable")
	}
	s.reconfigureMu.Lock()
	defer s.reconfigureMu.Unlock()
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
	s.reconfigureMu.Lock()
	defer s.reconfigureMu.Unlock()
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

func (s *Stack) lookupRegisterableACPAgent(name string) (assembly.AgentConfig, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	if preset, ok := lookupBuiltInACPAgent(name); ok {
		return preset, true
	}
	return s.lookupRuntimeACPAgent(name)
}

func (s *Stack) lookupRuntimeACPAgent(name string) (assembly.AgentConfig, bool) {
	if s == nil {
		return assembly.AgentConfig{}, false
	}
	name = strings.ToLower(strings.TrimSpace(name))
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, agent := range s.runtime.Assembly.Agents {
		if strings.EqualFold(strings.TrimSpace(agent.Name), name) {
			return assembly.CloneAgentConfig(agent), true
		}
	}
	return assembly.AgentConfig{}, false
}

func (s *Stack) installBuiltinACPAgent(ctx context.Context, name string, base assembly.AgentConfig) (assembly.AgentConfig, error) {
	pkg, ok := builtinACPAdapterPackageFor(name)
	if !ok {
		return assembly.AgentConfig{}, fmt.Errorf("gatewayapp: ACP agent %q does not support local npm install", strings.TrimSpace(name))
	}
	root := s.managedACPAgentRoot()
	installSpec := builtinACPAdapterInstallSpec(pkg)
	installCommand := []string{"npm", "install", "--prefix", root, installSpec}
	npm, err := exec.LookPath("npm")
	if err != nil || strings.TrimSpace(npm) == "" {
		return assembly.AgentConfig{}, &ACPAgentInstallError{
			Agent:   strings.TrimSpace(name),
			Command: installCommand,
			Err:     fmt.Errorf("npm is required"),
		}
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return assembly.AgentConfig{}, err
	}
	cmd := exec.CommandContext(ctx, npm, "install", "--prefix", root, installSpec)
	cmd.Env = append(os.Environ(), "npm_config_cache="+filepath.Join(root, "npm-cache"))
	output, err := cmd.CombinedOutput()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			err = ctxErr
		}
		return assembly.AgentConfig{}, &ACPAgentInstallError{
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
		return assembly.AgentConfig{}, fmt.Errorf("gatewayapp: install ACP agent %q did not produce %s: %w", strings.TrimSpace(name), bin, err)
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
	s.reconfigureMu.Lock()
	defer s.reconfigureMu.Unlock()
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
	baseAgents := make([]assembly.AgentConfig, 0, len(runtimeCfg.BaseAssembly.Agents))
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
	base := assembly.CloneResolvedAssembly(s.runtime.BaseAssembly)
	s.mu.RUnlock()
	return s.setConfiguredAgentsWithBase(base, configured)
}

func (s *Stack) setConfiguredAgentsWithBase(base assembly.ResolvedAssembly, configured []AgentConfig) error {
	if s == nil {
		return fmt.Errorf("gatewayapp: stack is unavailable")
	}
	s.mu.RLock()
	runtimeCfg := s.runtime
	engine := s.engine
	s.mu.RUnlock()
	runtimeCfg.BaseAssembly = assembly.CloneResolvedAssembly(base)
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

func (s *Stack) configuredAssembly(base assembly.ResolvedAssembly, configured []AgentConfig, runtimeCfg stackRuntimeConfig) assembly.ResolvedAssembly {
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
	return strings.Join([]string{"npm", "install", "--prefix", s.managedACPAgentRoot(), builtinACPAdapterInstallSpec(pkg)}, " ")
}

func builtinACPAdapterInstallSpec(pkg builtinACPAdapterPackage) string {
	if strings.TrimSpace(pkg.Version) != "" {
		return strings.TrimSpace(pkg.Package) + "@" + strings.TrimSpace(pkg.Version)
	}
	return strings.TrimSpace(pkg.Package) + "@latest"
}

func lookupBuiltInACPAgent(name string) (assembly.AgentConfig, bool) {
	return agentregistry.LookupBuiltInAgent(name)
}

func reservedSlashCommandName(name string) bool {
	return agentregistry.ReservedSlashCommandName(name)
}

func defaultStoreDir() string {
	home, err := os.UserHomeDir()
	if err == nil && strings.TrimSpace(home) != "" {
		return filepath.Join(home, ".caelis")
	}
	cwd := mustGetwd()
	return filepath.Join(cwd, ".caelis")
}

func (s *Stack) StartSession(ctx context.Context, preferredSessionID string, bindingKey string) (session.Session, error) {
	if s == nil {
		return session.Session{}, fmt.Errorf("gatewayapp: stack is unavailable")
	}
	gw := s.CurrentGateway()
	if gw == nil {
		return session.Session{}, fmt.Errorf("gatewayapp: gateway is unavailable")
	}
	return gw.StartSession(ctx, kernel.StartSessionRequest{
		AppName:            s.AppName,
		UserID:             s.UserID,
		Workspace:          s.Workspace,
		PreferredSessionID: strings.TrimSpace(preferredSessionID),
		BindingKey:         strings.TrimSpace(bindingKey),
		Binding: kernel.BindingDescriptor{
			Surface: strings.TrimSpace(bindingKey),
			Owner:   s.AppName,
		},
	})
}

func (s *Stack) ACPControllerStatus(ctx context.Context, ref session.SessionRef) (controller.ControllerStatus, bool, error) {
	if s == nil || s.engine == nil {
		return controller.ControllerStatus{}, false, nil
	}
	return s.engine.ACPControllerStatus(ctx, session.NormalizeSessionRef(ref))
}

func (s *Stack) SetACPControllerModel(ctx context.Context, ref session.SessionRef, model string, reasoningEffort string) (controller.ControllerStatus, error) {
	if s == nil || s.engine == nil {
		return controller.ControllerStatus{}, fmt.Errorf("gatewayapp: runtime engine unavailable")
	}
	if err := s.rejectReconfigureWhileActive("switch ACP model"); err != nil {
		return controller.ControllerStatus{}, err
	}
	return s.engine.SetACPControllerModel(ctx, controller.SetControllerModelRequest{
		SessionRef:      session.NormalizeSessionRef(ref),
		Model:           strings.TrimSpace(model),
		ReasoningEffort: strings.TrimSpace(reasoningEffort),
	})
}

func (s *Stack) SetACPControllerMode(ctx context.Context, ref session.SessionRef, mode string) (controller.ControllerStatus, error) {
	if s == nil || s.engine == nil {
		return controller.ControllerStatus{}, fmt.Errorf("gatewayapp: runtime engine unavailable")
	}
	if err := s.rejectReconfigureWhileActive("switch ACP mode"); err != nil {
		return controller.ControllerStatus{}, err
	}
	return s.engine.SetACPControllerMode(ctx, controller.SetControllerModeRequest{
		SessionRef: session.NormalizeSessionRef(ref),
		Mode:       strings.TrimSpace(mode),
	})
}

// Connect reconfigures the model provider on the live stack. The new config
// takes effect for subsequent turns.
func (s *Stack) Connect(cfg ModelConfig) (string, error) {
	if s == nil {
		return "", fmt.Errorf("gatewayapp: stack is unavailable")
	}
	s.reconfigureMu.Lock()
	defer s.reconfigureMu.Unlock()
	if err := s.rejectReconfigureWhileActive("connect model"); err != nil {
		return "", err
	}
	if s.lookup == nil {
		return "", fmt.Errorf("gatewayapp: model lookup unavailable")
	}
	gw := s.CurrentGateway()
	if gw == nil {
		return "", fmt.Errorf("gatewayapp: gateway is unavailable")
	}
	resolver := gw.Resolver()
	if resolver == nil {
		return "", fmt.Errorf("gatewayapp: resolver not available")
	}
	previousLookup := s.lookup.Snapshot()
	s.lookup.mu.RLock()
	previousContextWindow := s.lookup.contextWindow
	s.lookup.mu.RUnlock()
	s.mu.RLock()
	previousRuntime := s.runtime
	s.mu.RUnlock()
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
		s.lookup.Restore(previousLookup, previousContextWindow)
		s.mu.Lock()
		s.runtime = previousRuntime
		s.mu.Unlock()
		resolver.SetModelLookup(s.lookup, s.lookup.DefaultID())
		return "", err
	}
	return modelID, nil
}

// UseModel persists one per-session model alias override for subsequent turns.
func (s *Stack) UseModel(ctx context.Context, ref session.SessionRef, alias string, reasoningEffort ...string) error {
	if s == nil || s.Sessions == nil {
		return fmt.Errorf("gatewayapp: sessions service unavailable")
	}
	s.reconfigureMu.Lock()
	defer s.reconfigureMu.Unlock()
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
		previousLookup := s.lookup.Snapshot()
		s.lookup.mu.RLock()
		previousContextWindow := s.lookup.contextWindow
		s.lookup.mu.RUnlock()
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
		gw := s.CurrentGateway()
		if gw == nil {
			s.lookup.Restore(previousLookup, previousContextWindow)
			return fmt.Errorf("gatewayapp: gateway is unavailable")
		}
		if resolver := gw.Resolver(); resolver != nil {
			resolver.SetModelLookup(s.lookup, s.lookup.DefaultID())
		}
		if err := s.saveModelConfigs(); err != nil {
			s.lookup.Restore(previousLookup, previousContextWindow)
			if resolver := gw.Resolver(); resolver != nil {
				resolver.SetModelLookup(s.lookup, s.lookup.DefaultID())
			}
			return err
		}
	}
	return s.Sessions.UpdateState(ctx, ref, func(state map[string]any) (map[string]any, error) {
		next := session.CloneState(state)
		if next == nil {
			next = map[string]any{}
		}
		next[kernel.StateCurrentModelAlias] = cfg.ID
		if reasoning != "" {
			next[kernel.StateCurrentReasoningEffort] = reasoning
		} else {
			delete(next, kernel.StateCurrentReasoningEffort)
		}
		return next, nil
	})
}

// DeleteModel clears one per-session model alias override when it matches the
// supplied alias. This reverts the session back to the resolver default.
func (s *Stack) DeleteModel(ctx context.Context, ref session.SessionRef, alias string) error {
	if s == nil || s.Sessions == nil {
		return fmt.Errorf("gatewayapp: sessions service unavailable")
	}
	s.reconfigureMu.Lock()
	defer s.reconfigureMu.Unlock()
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
	previousLookup := s.lookup.Snapshot()
	s.lookup.mu.RLock()
	previousContextWindow := s.lookup.contextWindow
	s.lookup.mu.RUnlock()
	if err := s.lookup.Delete(alias); err != nil {
		return err
	}
	hasDefault := strings.TrimSpace(s.lookup.DefaultID()) != ""
	gw := s.CurrentGateway()
	if gw == nil {
		s.lookup.Restore(previousLookup, previousContextWindow)
		return fmt.Errorf("gatewayapp: gateway is unavailable")
	}
	if resolver := gw.Resolver(); resolver != nil {
		resolver.SetModelLookup(s.lookup, s.lookup.DefaultID())
	}
	if err := s.saveModelConfigs(); err != nil {
		s.lookup.Restore(previousLookup, previousContextWindow)
		if resolver := gw.Resolver(); resolver != nil {
			resolver.SetModelLookup(s.lookup, s.lookup.DefaultID())
		}
		return err
	}
	return s.Sessions.UpdateState(ctx, ref, func(state map[string]any) (map[string]any, error) {
		next := session.CloneState(state)
		if next == nil {
			next = map[string]any{}
		}
		current, _ := next[kernel.StateCurrentModelAlias].(string)
		if alias == "" || strings.EqualFold(strings.TrimSpace(current), cfg.ID) || strings.EqualFold(strings.TrimSpace(current), cfg.Alias) || !hasDefault {
			delete(next, kernel.StateCurrentModelAlias)
			delete(next, kernel.StateCurrentReasoningEffort)
		}
		return next, nil
	})
}

func (s *Stack) SetSandboxBackend(_ context.Context, backend string) (SandboxStatus, error) {
	if s == nil {
		return SandboxStatus{}, fmt.Errorf("gatewayapp: stack is unavailable")
	}
	s.reconfigureMu.Lock()
	defer s.reconfigureMu.Unlock()
	if err := s.rejectReconfigureWhileActive("change sandbox backend"); err != nil {
		return SandboxStatus{}, err
	}
	normalized, err := normalizeSandboxBackend(backend)
	if err != nil {
		return SandboxStatus{}, err
	}
	s.mu.RLock()
	previous := s.sandbox
	s.mu.RUnlock()
	next := previous
	next.RequestedType = normalized
	if err := s.saveSandboxConfigValue(next); err != nil {
		return SandboxStatus{}, err
	}
	s.mu.Lock()
	s.sandbox = next
	s.mu.Unlock()
	if err := s.rebuildGateway(); err != nil {
		s.mu.Lock()
		s.sandbox = previous
		s.mu.Unlock()
		if rollbackErr := s.saveSandboxConfigValue(previous); rollbackErr != nil {
			return SandboxStatus{}, errors.Join(err, rollbackErr)
		}
		return SandboxStatus{}, err
	}
	return s.SandboxStatus(), nil
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
		Route:            string(sandbox.RouteSandbox),
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
	status.InstallHint = strings.TrimSpace(rtStatus.FallbackInstallHint)
	if rtStatus.FallbackToHost {
		status.Route = string(sandbox.RouteHost)
		status.SecuritySummary = "host fallback"
		status.AutoReviewDisabled = true
		if status.ResolvedBackend == "" {
			status.ResolvedBackend = string(sandbox.BackendHost)
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
func (s *Stack) ListModelAliases(ctx context.Context, ref session.SessionRef) ([]string, error) {
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

func (s *Stack) ListModelChoices(ctx context.Context, ref session.SessionRef) ([]ModelChoice, error) {
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
		if modelRef := kernel.CurrentModelAlias(state); modelRef != "" {
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
	ref session.SessionRef,
	agent string,
	prompt string,
	source string,
) (task.Snapshot, error) {
	return s.StartSubagentWithOptions(ctx, ref, agent, prompt, source, StartSubagentOptions{})
}

func (s *Stack) StartSubagentWithOptions(
	ctx context.Context,
	ref session.SessionRef,
	agent string,
	prompt string,
	source string,
	opts StartSubagentOptions,
) (task.Snapshot, error) {
	if s == nil {
		return task.Snapshot{}, fmt.Errorf("gatewayapp: stack is unavailable")
	}
	s.mu.RLock()
	engine := s.engine
	s.mu.RUnlock()
	if engine == nil {
		return task.Snapshot{}, fmt.Errorf("gatewayapp: runtime is unavailable")
	}
	return engine.StartSubagentWithOptions(ctx, ref, agent, prompt, source, local.StartSubagentOptions{
		ApprovalRequester: opts.ApprovalRequester,
	})
}

func (s *Stack) ContinueSubagentByHandle(
	ctx context.Context,
	ref session.SessionRef,
	handle string,
	prompt string,
	yield time.Duration,
) (task.Snapshot, error) {
	if s == nil {
		return task.Snapshot{}, fmt.Errorf("gatewayapp: stack is unavailable")
	}
	s.mu.RLock()
	engine := s.engine
	s.mu.RUnlock()
	if engine == nil {
		return task.Snapshot{}, fmt.Errorf("gatewayapp: runtime is unavailable")
	}
	return engine.ContinueSubagentByHandle(ctx, ref, handle, prompt, yield)
}

func (s *Stack) WaitSubagentTask(
	ctx context.Context,
	ref session.SessionRef,
	taskID string,
	yield time.Duration,
) (task.Snapshot, error) {
	if s == nil {
		return task.Snapshot{}, fmt.Errorf("gatewayapp: stack is unavailable")
	}
	s.mu.RLock()
	engine := s.engine
	s.mu.RUnlock()
	if engine == nil {
		return task.Snapshot{}, fmt.Errorf("gatewayapp: runtime is unavailable")
	}
	return engine.WaitSubagentTask(ctx, ref, taskID, yield)
}

// CompactSession forces a model-backed checkpoint compaction for the given
// session.
func (s *Stack) CompactSession(ctx context.Context, ref session.SessionRef) error {
	if s == nil {
		return fmt.Errorf("gatewayapp: stack is unavailable")
	}
	s.mu.RLock()
	engine := s.engine
	gw := s.Gateway
	s.mu.RUnlock()
	if engine == nil {
		return fmt.Errorf("gatewayapp: runtime is unavailable")
	}
	if gw == nil || gw.Resolver() == nil {
		return fmt.Errorf("gatewayapp: resolver is unavailable")
	}
	resolved, err := gw.Resolver().ResolveTurn(ctx, kernel.TurnIntent{SessionRef: ref})
	if err != nil {
		return err
	}
	_, err = engine.Compact(ctx, local.CompactRequest{
		SessionRef: ref,
		Model:      resolved.RunRequest.AgentSpec.Model,
		Trigger:    "manual",
	})
	return err
}

func defaultCompactionConfig(contextWindow int) local.CompactionConfig {
	return local.CompactionConfig{
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

func (l *modelLookup) ResolveModel(ctx context.Context, alias string, contextWindow int) (kernel.ModelResolution, error) {
	if l == nil {
		return kernel.ModelResolution{}, fmt.Errorf("gatewayapp: model lookup is nil")
	}
	l.mu.RLock()
	ref := firstNonEmpty(strings.TrimSpace(alias), l.defaultID)
	if ref == "" || len(l.configs) == 0 {
		l.mu.RUnlock()
		return kernel.ModelResolution{}, fmt.Errorf("gatewayapp: no model configured; use /connect")
	}
	cfg, ok, resolveErr := l.resolveConfigLocked(ref)
	fallbackContextWindow := l.contextWindow
	l.mu.RUnlock()
	if resolveErr != nil {
		return kernel.ModelResolution{}, resolveErr
	}
	if !ok {
		return kernel.ModelResolution{}, fmt.Errorf("gatewayapp: unknown model alias %q", alias)
	}
	effectiveContextWindow := fallbackContextWindow
	if cfg.ContextWindowTokens > 0 {
		effectiveContextWindow = cfg.ContextWindowTokens
	}
	if contextWindow > 0 {
		effectiveContextWindow = contextWindow
	}
	factory := providers.NewFactory()
	record := providers.Config{
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
		Auth: providers.AuthConfig{
			Type:      cfg.AuthType,
			Token:     cfg.Token,
			TokenEnv:  cfg.TokenEnv,
			HeaderKey: cfg.HeaderKey,
		},
	}
	if err := factory.Register(record); err != nil {
		return kernel.ModelResolution{}, err
	}
	llm, err := factory.NewByAlias(cfg.ID)
	if err != nil {
		return kernel.ModelResolution{}, err
	}
	return kernel.ModelResolution{
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

func (s *Stack) SessionUsageSnapshot(ctx context.Context, ref session.SessionRef, modelAlias string) (compact.UsageSnapshot, error) {
	if s == nil || s.Sessions == nil {
		return compact.UsageSnapshot{}, fmt.Errorf("gatewayapp: sessions service unavailable")
	}
	if strings.TrimSpace(ref.SessionID) == "" {
		return compact.UsageSnapshot{}, nil
	}
	events, err := s.Sessions.Events(ctx, session.EventsRequest{SessionRef: ref})
	if err != nil {
		return compact.UsageSnapshot{}, err
	}
	alias := strings.TrimSpace(modelAlias)
	if alias == "" && s.lookup != nil {
		alias = strings.TrimSpace(s.lookup.DefaultAlias())
	}
	contextWindow := s.currentContextWindowTokensForAlias(alias)
	cfg := defaultCompactionConfig(contextWindow)
	cfg.EstimatedPromptPrefixTokens = s.estimatedPromptPrefixTokens(ctx, ref)
	return local.ComputeUsageSnapshot(events, nil, contextWindow, cfg), nil
}

func (s *Stack) estimatedPromptPrefixTokens(ctx context.Context, ref session.SessionRef) int {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	runtimeCfg := s.runtime
	runtimeCfg.Assembly = assembly.CloneResolvedAssembly(runtimeCfg.Assembly)
	runtimeCfg.BaseMetadata = cloneMap(runtimeCfg.BaseMetadata)
	base := runtimeCfg.EstimatedPromptPrefixTokens
	s.mu.RUnlock()
	if base < 0 {
		base = 0
	}

	var participants []session.ParticipantBinding
	if s.Sessions != nil && strings.TrimSpace(ref.SessionID) != "" {
		if session, err := s.Sessions.Session(ctx, ref); err == nil {
			participants = session.Participants
		}
	}
	agents := delegationAgentsForSpawn(runtimeCfg.Assembly, participants)
	if len(agents) == 0 {
		return base
	}

	extra := 0
	baseSystemPrompt := stringFromMap(runtimeCfg.BaseMetadata, "system_prompt")
	withDelegation := systemPromptWithDelegationGuidance(baseSystemPrompt)
	if delta := estimatePromptTextTokens(withDelegation) - estimatePromptTextTokens(baseSystemPrompt); delta > 0 {
		extra += delta
	}
	extra += estimateToolPromptTokens(spawnTools(agents))
	return base + extra
}

func spawnTools(agents []delegation.Agent) []tool.Tool {
	if len(agents) == 0 {
		return nil
	}
	return []tool.Tool{spawn.New(agents)}
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

func (l *modelLookup) Restore(snapshot persistedModelConfig, contextWindow int) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.configs = map[string]ModelConfig{}
	l.profiles = map[string]ModelProfileConfig{}
	for _, profile := range snapshot.Profiles {
		profile = normalizeModelProfileConfig(profile)
		if profile.ID != "" {
			l.profiles[strings.ToLower(profile.ID)] = profile
		}
	}
	for _, cfg := range snapshot.Configs {
		cfg = normalizeModelConfig(cfg)
		if cfg.ID != "" {
			l.configs[strings.ToLower(cfg.ID)] = cfg
		}
	}
	l.defaultID = strings.TrimSpace(snapshot.DefaultID)
	l.contextWindow = contextWindow
	if l.defaultID == "" && strings.TrimSpace(snapshot.DefaultAlias) != "" {
		if cfg, ok, err := l.resolveConfigLocked(snapshot.DefaultAlias); err == nil && ok {
			l.defaultID = cfg.ID
		}
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
	return modelregistry.ChoiceDetail(cfg)
}

func modelChoiceFromConfig(cfg ModelConfig) ModelChoice {
	return modelregistry.ChoiceFromConfig(cfg)
}

func dedupeModelChoices(choices []ModelChoice) []ModelChoice {
	return modelregistry.DedupeChoices(choices)
}

func normalizeModelConfig(cfg ModelConfig) ModelConfig {
	return modelregistry.NormalizeConfig(cfg)
}

func normalizeModelProfileConfig(profile ModelProfileConfig) ModelProfileConfig {
	return modelregistry.NormalizeProfileConfig(profile)
}

func modelProfileFromModelConfig(cfg ModelConfig) ModelProfileConfig {
	return modelregistry.ProfileFromConfig(cfg)
}

func modelConfigCarriesProfileFields(cfg ModelConfig) bool {
	return modelregistry.ConfigCarriesProfileFields(cfg)
}

func modelConfigCarriesProfileAuth(cfg ModelConfig) bool {
	return modelregistry.ConfigCarriesProfileAuth(cfg)
}

func mergeModelConfigProfile(cfg ModelConfig, profile ModelProfileConfig) ModelConfig {
	return modelregistry.MergeConfigProfile(cfg, profile)
}

func modelConfigSupportsReasoningEffort(cfg ModelConfig, effort string) bool {
	return modelregistry.SupportsReasoningEffort(cfg, effort)
}

func defaultModelAPIForProvider(provider string) providers.APIType {
	return modelregistry.DefaultAPIForProvider(provider)
}

func sanitizePersistedModelConfig(cfg ModelConfig) ModelConfig {
	return modelregistry.SanitizePersistedConfig(cfg)
}

func sanitizePersistedModelProfile(profile ModelProfileConfig) ModelProfileConfig {
	return modelregistry.SanitizePersistedProfile(profile)
}

func defaultAuthTypeForProvider(provider string) providers.AuthType {
	return modelregistry.DefaultAuthTypeForProvider(provider)
}

func (s *Stack) rejectReconfigureWhileActive(action string) error {
	if s == nil {
		return fmt.Errorf("gatewayapp: stack is unavailable")
	}
	return rejectReconfigureWithActiveTurns(s.CurrentGateway(), action)
}

func rejectReconfigureWithActiveTurns(gw *kernel.Gateway, action string) error {
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
	return modelregistry.BuildAlias(provider, modelName)
}

func buildProfileID(provider string, endpointID string, baseURL string) string {
	return modelregistry.BuildProfileID(provider, endpointID, baseURL)
}

func buildModelID(profileID string, alias string) string {
	return modelregistry.BuildModelID(profileID, alias)
}

func normalizeEndpointID(provider string, endpointID string, baseURL string, api providers.APIType) string {
	return modelregistry.NormalizeEndpointID(provider, endpointID, baseURL, api)
}

func firstNonEmptyAPI(values ...providers.APIType) providers.APIType {
	return modelregistry.FirstNonEmptyAPI(values...)
}

func firstNonEmptyAuthType(values ...providers.AuthType) providers.AuthType {
	return modelregistry.FirstNonEmptyAuthType(values...)
}

func normalizeSandboxBackend(backend string) (string, error) {
	return sandboxpolicy.NormalizeBackend(backend)
}

func mergeSandboxConfig(stored SandboxConfig, override SandboxConfig) SandboxConfig {
	return sandboxpolicy.MergeConfig(stored, override)
}

func effectiveSandboxConfig(cfg SandboxConfig, workspaceDir string) SandboxConfig {
	return sandboxpolicy.EffectiveConfig(cfg, workspaceDir)
}

func withSandboxPolicyRootMetadata(metadata map[string]any, cfg SandboxConfig, workspaceDir string) map[string]any {
	return sandboxpolicy.WithPolicyRootMetadata(metadata, cfg, workspaceDir)
}

func defaultSkillSandboxRoots(workspaceDir string) []string {
	return sandboxpolicy.DefaultSkillRoots(workspaceDir)
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
		return presets.ModeManual
	case "", "auto", "auto-review", "auto_review", "autoreview", "default", "plan", "full_control", "full_access":
		return presets.ModeAutoReview
	default:
		return presets.ModeDefault
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
