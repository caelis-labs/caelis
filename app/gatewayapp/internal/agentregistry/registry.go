package agentregistry

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/internal/acpagentenv"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
	commands "github.com/caelis-labs/caelis/ports/controlcommand"
)

type RuntimeConfig struct {
	AppName                   string
	UserID                    string
	StoreDir                  string
	WorkspaceKey              string
	WorkspaceCWD              string
	ApprovalMode              string
	PolicyProfile             string
	ControlOperationRetention time.Duration
	ContextWindow             int
	SystemPrompt              string
	Model                     modelconfig.Config
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

const (
	codexACPAdapterVersion  = "1.1.2"
	claudeACPAdapterVersion = "0.59.0"
)

// WithSelfAgent adds the private Caelis child endpoint when the host did not
// already provide one.
func WithSelfAgent(resolved assembly.ResolvedAssembly, self assembly.AgentConfig) assembly.ResolvedAssembly {
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
	return out
}

func DefaultSelfAgent(cfg DefaultSelfConfig) (assembly.AgentConfig, error) {
	agent, err := acpagentenv.SelfAgentFromOS("Caelis self ACP agent")
	if err != nil {
		return assembly.AgentConfig{}, err
	}
	if agent != nil {
		return *agent, nil
	}
	return configuredSelfAgent(cfg)
}

// ConfiguredModelSelfAgent builds the generic Caelis ACP runtime for one
// configured model. Environment self-agent replacement is intentionally not
// used because it cannot guarantee the selected ModelConfig reaches the child.
func ConfiguredModelSelfAgent(cfg DefaultSelfConfig) (assembly.AgentConfig, error) {
	return configuredSelfAgent(cfg)
}

func configuredSelfAgent(cfg DefaultSelfConfig) (assembly.AgentConfig, error) {
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
	}, nil
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
	appendFlag("-reasoning-effort", model.ReasoningEffort)
	appendFlag("-default-reasoning-effort", model.DefaultReasoningEffort)
	appendFlag("-reasoning-mode", model.ReasoningMode)
	if len(model.ReasoningLevels) > 0 {
		appendFlag("-reasoning-levels", strings.Join(model.ReasoningLevels, ","))
	}
	appendFlag("-system-prompt", cfg.SystemPrompt)
	if cfg.ControlOperationRetention > 0 {
		args = append(args, "-control-operation-retention", cfg.ControlOperationRetention.String())
	}
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
		npxAgentConfig("codex", "OpenAI Codex ACP agent", "@agentclientprotocol/codex-acp@"+codexACPAdapterVersion),
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
			Name:        "grok",
			Description: "Grok Build ACP agent",
			Command:     "grok",
			Args:        []string{"agent", "stdio"},
		},
	}
}

// ConnectableBuiltInAgents returns the curated ACP endpoints exposed by the
// guided /connect flow. Copilot remains available to existing assembly users
// but is not part of this onboarding catalog.
func ConnectableBuiltInAgents() []assembly.AgentConfig {
	names := []string{"codex", "claude", "opencode", "codefree-o", "grok"}
	out := make([]assembly.AgentConfig, 0, len(names))
	for _, name := range names {
		if agent, ok := LookupBuiltInAgent(name); ok {
			out = append(out, agent)
		}
	}
	return out
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
		return BuiltinAdapterPackage{Package: "@agentclientprotocol/codex-acp", Version: codexACPAdapterVersion, Bin: "codex-acp"}, true
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
	name = strings.TrimSpace(name)
	return commands.IsKnown(name) || strings.EqualFold(name, "sandbox") || strings.EqualFold(name, "lead")
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
