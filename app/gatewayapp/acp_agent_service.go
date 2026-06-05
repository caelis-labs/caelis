package gatewayapp

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"strings"

	"github.com/OnslaughtSnail/caelis/app/gatewayapp/internal/agentprofiles"
	"github.com/OnslaughtSnail/caelis/app/gatewayapp/internal/agentregistry"
	"github.com/OnslaughtSnail/caelis/ports/agentprofile"
	"github.com/OnslaughtSnail/caelis/ports/assembly"
	"github.com/OnslaughtSnail/caelis/ports/controller"
	"github.com/OnslaughtSnail/caelis/ports/session"
)

type ACPAgentInfo struct {
	Name        string
	Description string
}

type ACPAgentAddOption struct {
	Value   string
	Display string
	Detail  string
}

const subagentProfileEnvKey = "CAELIS_SUBAGENT_PROFILE_ID"

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
		ApprovalMode:   cfg.ApprovalMode,
		PolicyProfile:  cfg.PolicyProfile,
		PermissionMode: cfg.PermissionMode,
		ContextWindow:  cfg.ContextWindow,
		SystemPrompt:   cfg.SystemPrompt,
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
	cmd := exec.CommandContext(ctx, npm, "install", "--prefix", root, npmInstallSpecForExec(npm, installSpec))
	cmd.Dir = root
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
	resolved := withConfiguredACPAgents(base, configured, defaultSelfACPAgent(defaultSelfACPAgentConfig{
		Config: Config{
			AppName:        s.AppName,
			UserID:         s.UserID,
			StoreDir:       s.storeDir,
			WorkspaceKey:   s.Workspace.Key,
			WorkspaceCWD:   s.Workspace.CWD,
			ApprovalMode:   runtimeCfg.ApprovalMode,
			PolicyProfile:  runtimeCfg.PolicyProfile,
			PermissionMode: runtimeCfg.PermissionMode,
			ContextWindow:  runtimeCfg.ContextWindow,
			SystemPrompt:   runtimeCfg.SystemPrompt,
			Model:          runtimeCfg.Model,
		},
		AppName:      s.AppName,
		UserID:       s.UserID,
		StoreDir:     s.storeDir,
		WorkspaceKey: s.Workspace.Key,
		WorkspaceCWD: s.Workspace.CWD,
	}))
	return s.withAgentProfileACPAgents(resolved, runtimeCfg)
}

func (s *Stack) withAgentProfileACPAgents(resolved assembly.ResolvedAssembly, runtimeCfg stackRuntimeConfig) assembly.ResolvedAssembly {
	out := assembly.CloneResolvedAssembly(resolved)
	if s == nil || s.store == nil {
		return out
	}
	profileStatus, err := agentprofiles.LoadDirStatus(filepath.Join(s.storeDir, agentprofiles.DefaultAgentsDirName))
	if err != nil || len(profileStatus.Profiles) == 0 {
		return out
	}
	doc, err := s.store.Load()
	if err != nil {
		return out
	}
	seen := map[string]struct{}{}
	sourceAgents := map[string]assembly.AgentConfig{}
	for _, agent := range out.Agents {
		name := strings.ToLower(strings.TrimSpace(agent.Name))
		if name == "" {
			continue
		}
		seen[name] = struct{}{}
		sourceAgents[name] = assembly.CloneAgentConfig(agent)
	}
	for _, profile := range profileStatus.Profiles {
		profile = agentprofile.NormalizeProfile(profile)
		if profile.ID == "" || profile.ID == guardianProfileID {
			continue
		}
		if _, exists := seen[profile.ID]; exists {
			continue
		}
		binding, ok := agentprofile.LookupBinding(doc.AgentBindings, profile.ID)
		if !ok {
			binding = defaultAgentProfileBinding(profile.ID)
		}
		binding = agentprofile.NormalizeBinding(binding)
		if binding.Enabled != nil && !*binding.Enabled {
			continue
		}
		agent, ok := s.agentProfileACPAgent(profile, binding, runtimeCfg, sourceAgents)
		if !ok {
			continue
		}
		out.Agents = append(out.Agents, agent)
		seen[profile.ID] = struct{}{}
	}
	return out
}

func (s *Stack) agentProfileACPAgent(profile agentprofile.Profile, binding agentprofile.Binding, runtimeCfg stackRuntimeConfig, sourceAgents map[string]assembly.AgentConfig) (assembly.AgentConfig, bool) {
	switch binding.Target {
	case agentprofile.BindingTargetACP:
		sourceName := strings.ToLower(strings.TrimSpace(binding.ACPAgent))
		source, ok := sourceAgents[sourceName]
		if !ok {
			return assembly.AgentConfig{}, false
		}
		agent := assembly.CloneAgentConfig(source)
		agent.Name = profile.ID
		agent.Description = firstNonEmpty(profile.Description, profile.Name, agent.Description)
		agent.Env = withSubagentProfileEnv(agent.Env, profile.ID)
		return agent, true
	case agentprofile.BindingTargetSelf, agentprofile.BindingTargetBuiltIn:
		model := runtimeCfg.Model
		if binding.Model != "" {
			if s.lookup == nil {
				return assembly.AgentConfig{}, false
			}
			cfg, err := s.lookup.ResolveConfig(binding.Model)
			if err != nil {
				return assembly.AgentConfig{}, false
			}
			model = cfg
			if reasoning := strings.TrimSpace(binding.ReasoningEffort); reasoning != "" {
				model.ReasoningEffort = reasoning
				model.DefaultReasoningEffort = reasoning
			}
		}
		agent := defaultSelfACPAgent(defaultSelfACPAgentConfig{
			Config: Config{
				AppName:        s.AppName,
				UserID:         s.UserID,
				StoreDir:       s.storeDir,
				WorkspaceKey:   s.Workspace.Key,
				WorkspaceCWD:   s.Workspace.CWD,
				ApprovalMode:   runtimeCfg.ApprovalMode,
				PolicyProfile:  runtimeCfg.PolicyProfile,
				PermissionMode: runtimeCfg.PermissionMode,
				ContextWindow:  runtimeCfg.ContextWindow,
				SystemPrompt:   strings.Join(compactAgentProfilePrompts(runtimeCfg.SystemPrompt, agentProfileSystemPrompt(profile)), "\n\n"),
				Model:          model,
			},
			AppName:      s.AppName,
			UserID:       s.UserID,
			StoreDir:     s.storeDir,
			WorkspaceKey: s.Workspace.Key,
			WorkspaceCWD: s.Workspace.CWD,
		})
		agent.Name = profile.ID
		agent.Description = firstNonEmpty(profile.Description, profile.Name, agent.Description)
		agent.Env = withSubagentProfileEnv(agent.Env, profile.ID)
		return agent, true
	default:
		return assembly.AgentConfig{}, false
	}
}

func agentProfileSystemPrompt(profile agentprofile.Profile) string {
	profile = agentprofile.NormalizeProfile(profile)
	parts := []string{}
	if profile.Name != "" {
		parts = append(parts, "Subagent profile: "+profile.Name)
	}
	if profile.Description != "" {
		parts = append(parts, "Description: "+profile.Description)
	}
	if len(profile.Capabilities) > 0 {
		parts = append(parts, "Capabilities: "+strings.Join(profile.Capabilities, ", "))
	}
	if instructions := strings.TrimSpace(profile.Instructions); instructions != "" {
		parts = append(parts, "Instructions:\n"+instructions)
	}
	return strings.Join(parts, "\n\n")
}

func withSubagentProfileEnv(env map[string]string, profileID string) map[string]string {
	out := map[string]string{}
	for key, value := range env {
		out[key] = value
	}
	out[subagentProfileEnvKey] = strings.TrimSpace(profileID)
	out["SDK_ACP_ENABLE_SPAWN"] = "0"
	out["SDK_ACP_CHILD_NO_SPAWN"] = "1"
	return out
}

func isSubagentProfileAgent(agent assembly.AgentConfig) bool {
	return strings.TrimSpace(agent.Env[subagentProfileEnvKey]) != ""
}

func compactAgentProfilePrompts(values ...string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
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
	return s.engine.SetACPControllerMode(ctx, controller.SetControllerModeRequest{
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
		if isSubagentProfileAgent(agent) {
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
