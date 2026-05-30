package local

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"

	"github.com/OnslaughtSnail/caelis/core/config"
	appagents "github.com/OnslaughtSnail/caelis/internal/app/agents"
	"github.com/OnslaughtSnail/caelis/internal/app/services"
)

type builtinAgentInstaller struct {
	root string
}

func newBuiltinAgentInstaller(runtimeCfg config.Runtime) builtinAgentInstaller {
	return builtinAgentInstaller{root: managedACPAgentRoot(runtimeCfg)}
}

func (i builtinAgentInstaller) InstallableBuiltinACPAgentOptions(_ context.Context, builtins []services.AgentDescriptor) ([]services.AgentInstallOption, error) {
	if strings.TrimSpace(i.root) == "" {
		return nil, nil
	}
	out := make([]services.AgentInstallOption, 0)
	for _, agent := range builtins {
		name := strings.TrimSpace(firstNonEmpty(agent.Name, agent.ID))
		if name == "" {
			continue
		}
		pkg, ok := appagents.LookupBuiltinACPAdapterPackage(name)
		if !ok {
			continue
		}
		out = append(out, services.AgentInstallOption{
			Value:   name,
			Display: name + " (npm install)",
			Detail:  strings.Join([]string{"npm", "install", "--prefix", i.root, appagents.BuiltinACPAdapterInstallSpec(pkg)}, " "),
		})
	}
	return out, nil
}

func (i builtinAgentInstaller) InstallBuiltinACPAgent(ctx context.Context, agent services.AgentDescriptor) (services.AgentDescriptor, error) {
	name := strings.TrimSpace(firstNonEmpty(agent.Name, agent.ID))
	pkg, ok := appagents.LookupBuiltinACPAdapterPackage(name)
	if !ok {
		return services.AgentDescriptor{}, fmt.Errorf("app/local: ACP agent %q does not support local npm install", name)
	}
	root := strings.TrimSpace(i.root)
	if root == "" {
		return services.AgentDescriptor{}, fmt.Errorf("app/local: ACP agent install root is unavailable")
	}
	installSpec := appagents.BuiltinACPAdapterInstallSpec(pkg)
	installCommand := []string{"npm", "install", "--prefix", root, installSpec}
	npm, err := exec.LookPath("npm")
	if err != nil || strings.TrimSpace(npm) == "" {
		return services.AgentDescriptor{}, &services.AgentInstallError{
			Agent:   name,
			Command: installCommand,
			Err:     fmt.Errorf("npm is required"),
		}
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return services.AgentDescriptor{}, err
	}
	cmd := exec.CommandContext(ctx, npm, "install", "--prefix", root, npmInstallSpecForExec(npm, installSpec))
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "npm_config_cache="+filepath.Join(root, "npm-cache"))
	output, err := cmd.CombinedOutput()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			err = ctxErr
		}
		return services.AgentDescriptor{}, &services.AgentInstallError{
			Agent:   name,
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
		return services.AgentDescriptor{}, fmt.Errorf("app/local: install ACP agent %q did not produce %s: %w", name, bin, err)
	}
	agent.Command = bin
	agent.Args = nil
	return agent, nil
}

func managedACPAgentRoot(runtimeCfg config.Runtime) string {
	base := strings.TrimSpace(runtimeCfg.Store.URI)
	if base == "" {
		return ""
	}
	if strings.EqualFold(strings.TrimSpace(runtimeCfg.Store.Backend), "sqlite") && filepath.Ext(base) != "" {
		base = filepath.Dir(base)
	}
	return filepath.Join(base, "acp-agents", "npm")
}

func managedACPAgentBinPath(root string, bin string) string {
	bin = strings.TrimSpace(bin)
	if goruntime.GOOS == "windows" && !strings.HasSuffix(strings.ToLower(bin), ".cmd") {
		bin += ".cmd"
	}
	return filepath.Join(strings.TrimSpace(root), "node_modules", ".bin", bin)
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

var _ services.AgentInstaller = builtinAgentInstaller{}
