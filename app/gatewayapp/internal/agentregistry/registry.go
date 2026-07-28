package agentregistry

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/internal/acpagentenv"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
	"github.com/caelis-labs/caelis/internal/controlprompt"
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

func BuiltinAdapterPackageFor(name string) (BuiltinAdapterPackage, bool) {
	registered, ok := lookupRegistryNPXAgent(name)
	managedBin := managedBinFor(name)
	if !ok || managedBin == "" {
		return BuiltinAdapterPackage{}, false
	}
	versionSuffix := "@" + strings.TrimSpace(registered.Agent.Version)
	if !strings.HasSuffix(registered.Package, versionSuffix) {
		return BuiltinAdapterPackage{}, false
	}
	return BuiltinAdapterPackage{
		Package: strings.TrimSuffix(registered.Package, versionSuffix),
		Version: registered.Agent.Version,
		Bin:     managedBin,
	}, true
}

func ReservedSlashCommandName(name string) bool {
	name = strings.TrimSpace(name)
	return controlprompt.IsKnown(name) || strings.EqualFold(name, "sandbox") || strings.EqualFold(name, "lead")
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
