package agentregistry

import (
	"fmt"
	"os"
	"strings"

	"github.com/OnslaughtSnail/caelis/app/gatewayapp/internal/configstore"
	"github.com/OnslaughtSnail/caelis/app/gatewayapp/internal/modelregistry"
	"github.com/OnslaughtSnail/caelis/ports/assembly"
)

type RuntimeConfig struct {
	AppName        string
	UserID         string
	StoreDir       string
	WorkspaceKey   string
	WorkspaceCWD   string
	ApprovalMode   string
	PolicyProfile  string
	PermissionMode string
	ContextWindow  int
	SystemPrompt   string
	Model          modelregistry.Config
}

type DefaultSelfConfig struct {
	Config       RuntimeConfig
	AppName      string
	UserID       string
	StoreDir     string
	WorkspaceKey string
	WorkspaceCWD string
}

type BuiltinAdapterPackage struct {
	Package string
	Version string
	Bin     string
}

const claudeACPAdapterVersion = "^0.31.0"

func WithConfiguredAgents(resolved assembly.ResolvedAssembly, configured []configstore.AgentConfig, self assembly.AgentConfig) assembly.ResolvedAssembly {
	out := assembly.CloneResolvedAssembly(resolved)
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
		cfg := AgentConfigToPlugin(agent)
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

func AgentConfigToPlugin(in configstore.AgentConfig) assembly.AgentConfig {
	in = configstore.NormalizeAgentConfig(in)
	return assembly.AgentConfig{
		Name:        in.Name,
		Description: in.Description,
		Command:     in.Command,
		Args:        append([]string(nil), in.Args...),
		Env:         cloneStringMap(in.Env),
		WorkDir:     in.WorkDir,
	}
}

func PluginAgentToConfig(in assembly.AgentConfig, builtin bool) configstore.AgentConfig {
	return configstore.NormalizeAgentConfig(configstore.AgentConfig{
		Name:        in.Name,
		Description: in.Description,
		Command:     in.Command,
		Args:        append([]string(nil), in.Args...),
		Env:         cloneStringMap(in.Env),
		WorkDir:     in.WorkDir,
		Builtin:     builtin,
	})
}

func DefaultSelfAgent(cfg DefaultSelfConfig) assembly.AgentConfig {
	if cmd := strings.TrimSpace(os.Getenv("CAELIS_ACP_SELF_AGENT_CMD")); cmd != "" {
		name := strings.TrimSpace(os.Getenv("CAELIS_ACP_SELF_AGENT_NAME"))
		if name == "" {
			name = "self"
		}
		return assembly.AgentConfig{
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
	args, env := SelfRuntimeInvocation(cfg.Config)
	return assembly.AgentConfig{
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
			"-approval-mode", strings.TrimSpace(cfg.Config.ApprovalMode),
			"-policy-profile", strings.TrimSpace(cfg.Config.PolicyProfile),
		}, args...),
		Env: env,
	}
}

func SelfRuntimeArgs(cfg RuntimeConfig) []string {
	args, _ := SelfRuntimeInvocation(cfg)
	return args
}

func SelfRuntimeInvocation(cfg RuntimeConfig) ([]string, map[string]string) {
	args := []string{}
	env := map[string]string{}
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
	if strings.TrimSpace(model.Token) != "" {
		env["CAELIS_SELF_MODEL_TOKEN"] = model.Token
		appendFlag("-token-env", "CAELIS_SELF_MODEL_TOKEN")
	} else {
		appendFlag("-token-env", model.TokenEnv)
	}
	appendFlag("-auth-type", string(model.AuthType))
	appendFlag("-header-key", model.HeaderKey)
	appendFlag("-system-prompt", cfg.SystemPrompt)
	if cfg.ContextWindow > 0 {
		args = append(args, "-context-window", fmt.Sprintf("%d", cfg.ContextWindow))
	}
	if model.MaxOutputTok > 0 {
		args = append(args, "-max-output-tokens", fmt.Sprintf("%d", model.MaxOutputTok))
	}
	if len(env) == 0 {
		env = nil
	}
	return args, env
}

func BuiltInAgents() []assembly.AgentConfig {
	return []assembly.AgentConfig{
		npxAgentConfig("codex", "OpenAI Codex ACP agent", "@zed-industries/codex-acp"),
		npxAgentConfig("claude", "Claude Code ACP agent", "@agentclientprotocol/claude-agent-acp@"+claudeACPAdapterVersion),
		nativeACPAgentConfig("opencode", "OpenCode ACP agent", "opencode", "acp"),
		nativeACPAgentConfig("codefree-o", "CodeFree-O ACP agent", "codefree-o", "acp"),
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

func nativeACPAgentConfig(name string, description string, command string, args ...string) assembly.AgentConfig {
	return assembly.AgentConfig{
		Name:        strings.TrimSpace(name),
		Description: strings.TrimSpace(description),
		Command:     strings.TrimSpace(command),
		Args:        append([]string(nil), args...),
	}
}

func npxAgentConfig(name string, description string, pkg string) assembly.AgentConfig {
	return assembly.AgentConfig{
		Name:        strings.TrimSpace(name),
		Description: strings.TrimSpace(description),
		Command:     "npx",
		Args:        []string{"-y", strings.TrimSpace(pkg)},
	}
}

func BuiltinAdapterPackageFor(name string) (BuiltinAdapterPackage, bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "codex":
		return BuiltinAdapterPackage{Package: "@zed-industries/codex-acp", Bin: "codex-acp"}, true
	case "claude":
		return BuiltinAdapterPackage{Package: "@agentclientprotocol/claude-agent-acp", Version: claudeACPAdapterVersion, Bin: "claude-agent-acp"}, true
	default:
		return BuiltinAdapterPackage{}, false
	}
}

func LookupBuiltInAgent(name string) (assembly.AgentConfig, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, agent := range BuiltInAgents() {
		if strings.EqualFold(strings.TrimSpace(agent.Name), name) {
			return agent, true
		}
	}
	return assembly.AgentConfig{}, false
}

func ReservedSlashCommandName(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "help", "agent", "subagent", "connect", "model", "sandbox", "status", "doctor", "new", "resume", "compact", "exit", "quit":
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
