package agentregistry

import (
	"os"
	"strings"

	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/internal/acpagentenv"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
	"github.com/caelis-labs/caelis/internal/controlprompt"
)

type DefaultSelfConfig struct {
	StoreDir         string
	WorkspaceKey     string
	WorkspaceCWD     string
	SessionOptions   controlagents.SessionOptions
	ControlURL       string
	ControlTokenFile string
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

// ConfiguredModelSelfAgent builds the generic Caelis ACP surface that attaches
// to the existing product Host. Environment self-agent replacement is
// intentionally not used because it cannot guarantee that Control-selected
// Session options reach the child before its first prompt.
func ConfiguredModelSelfAgent(cfg DefaultSelfConfig) (assembly.AgentConfig, error) {
	return configuredSelfAgent(cfg)
}

func configuredSelfAgent(cfg DefaultSelfConfig) (assembly.AgentConfig, error) {
	executable, err := os.Executable()
	if err != nil || strings.TrimSpace(executable) == "" {
		executable = os.Args[0]
	}
	args := []string{
		"acp",
		"-store-dir", strings.TrimSpace(cfg.StoreDir),
	}
	if controlURL := strings.TrimSpace(cfg.ControlURL); controlURL != "" {
		args = append(args, "-control-url", controlURL)
		if tokenFile := strings.TrimSpace(cfg.ControlTokenFile); tokenFile != "" {
			args = append(args, "-control-token-file", tokenFile)
		}
	}
	env := map[string]string{
		"CAELIS_CONTROL_TOKEN":                    "",
		"CAELIS_CONTROL_TOKEN_FILE":               "",
		acpagentenv.EnvManagedSessionHistoryToken: "",
		acpagentenv.EnvWorkspaceKey:               strings.TrimSpace(cfg.WorkspaceKey),
		acpagentenv.EnvWorkspaceCWD:               strings.TrimSpace(cfg.WorkspaceCWD),
	}
	// Built-in children never inherit parent Host credentials through stdio's
	// base environment. When a child bridge is available, authentication is
	// supplied explicitly through the protected token-file argument above.
	return assembly.AgentConfig{
		Name:           "self",
		Description:    "Caelis self ACP agent",
		Command:        executable,
		Args:           args,
		Env:            env,
		WorkDir:        strings.TrimSpace(cfg.WorkspaceCWD),
		SessionOptions: controlagents.NormalizeSessionOptions(cfg.SessionOptions),
	}, nil
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
