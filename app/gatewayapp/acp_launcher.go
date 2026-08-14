package gatewayapp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/caelis-labs/caelis/app/gatewayapp/internal/agentregistry"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/control/plugin"
)

type globalACPAgentInstallRequest struct {
	InstallSpec string
}

var runGlobalACPAgentInstall = defaultRunGlobalACPAgentInstall

var installedACPAdapterPackageMatches = defaultInstalledACPAdapterPackageMatches

var acpAgentInstallMu sync.Mutex

func (s *Stack) resolveACPConnectionLauncher(ctx context.Context, req controlagents.ConnectRequest) (controlagents.Connection, error) {
	req = controlagents.NormalizeConnectRequest(req)
	if req.AdapterID == "" {
		return controlagents.Connection{}, fmt.Errorf("gatewayapp: ACP adapter is required")
	}
	connection := controlagents.Connection{ID: req.AdapterID, Name: req.AdapterID}
	if _, ok := agentregistry.LookupConnectableAgent(req.AdapterID); !ok {
		return controlagents.Connection{}, fmt.Errorf("gatewayapp: unknown ACP adapter %q", req.AdapterID)
	}
	if !agentregistry.SupportsLauncher(req.AdapterID, req.Launcher) {
		return controlagents.Connection{}, fmt.Errorf(
			"gatewayapp: ACP adapter %q does not support launcher %q",
			req.AdapterID,
			req.Launcher,
		)
	}
	switch req.Launcher {
	case controlagents.LauncherChoiceCommand:
		command, args, err := splitACPCommandLine(req.CommandLine)
		if err != nil {
			return controlagents.Connection{}, err
		}
		connection.Name = filepath.Base(command)
		connection.Launcher = controlagents.Launcher{Kind: controlagents.LaunchKindExecutable, Command: command, Args: args}
		connection.ID = controlagents.CustomConnectionID(command, connection.Launcher)
	case controlagents.LauncherChoiceNPX:
		preset, ok := agentregistry.LookupNPXAgent(req.AdapterID)
		if !ok {
			return controlagents.Connection{}, catalogLauncherInconsistency(req.AdapterID, req.Launcher, "npx preset")
		}
		resolved, err := exec.LookPath("npx")
		if err != nil || strings.TrimSpace(resolved) == "" {
			return controlagents.Connection{}, fmt.Errorf("gatewayapp: npx is required for ACP adapter %q", req.AdapterID)
		}
		connection.Launcher = controlagents.Launcher{
			Kind: controlagents.LaunchKindPackageExec, Command: resolved, Args: append([]string(nil), preset.Args...),
			Env: preset.Env,
		}
	case controlagents.LauncherChoiceGlobal:
		command, err := ensureGlobalACPAgent(ctx, req.AdapterID)
		if err != nil {
			return controlagents.Connection{}, err
		}
		connection.Launcher = controlagents.Launcher{Kind: controlagents.LaunchKindExecutable, Command: command}
	case controlagents.LauncherChoiceManaged:
		pkg, ok := builtinACPAdapterPackageFor(req.AdapterID)
		if !ok {
			return controlagents.Connection{}, catalogLauncherInconsistency(req.AdapterID, req.Launcher, "managed package")
		}
		preset, ok := agentregistry.LookupNPXAgent(req.AdapterID)
		if !ok {
			return controlagents.Connection{}, catalogLauncherInconsistency(req.AdapterID, req.Launcher, "npx preset")
		}
		root := managedACPAdapterRoot(s.managedACPAgentRoot(), pkg)
		bin := managedACPAgentBinPath(root, pkg.Bin)
		if validateManagedACPAdapterRoot(root, pkg) != nil {
			installed, installErr := s.installBuiltinACPAgent(ctx, req.AdapterID, preset)
			if installErr != nil {
				return controlagents.Connection{}, installErr
			}
			bin = installed.Command
			if validateManagedACPAdapterRoot(root, pkg) != nil {
				return controlagents.Connection{}, fmt.Errorf("gatewayapp: managed install of ACP adapter %q did not provide %s", req.AdapterID, builtinACPAdapterInstallSpec(pkg))
			}
		}
		connection.Launcher = controlagents.Launcher{Kind: controlagents.LaunchKindManaged, Command: bin, Env: preset.Env}
	case controlagents.LauncherChoiceInstalled:
		preset, ok := agentregistry.LookupInstalledAgent(req.AdapterID)
		if !ok {
			return controlagents.Connection{}, catalogLauncherInconsistency(req.AdapterID, req.Launcher, "installed-command preset")
		}
		command, err := exec.LookPath(preset.Command)
		if err != nil || strings.TrimSpace(command) == "" {
			return controlagents.Connection{}, fmt.Errorf("gatewayapp: install ACP agent %q so %q is available on PATH", req.AdapterID, preset.Command)
		}
		command, err = absoluteExecutablePath(command)
		if err != nil {
			return controlagents.Connection{}, err
		}
		connection.Launcher = controlagents.Launcher{
			Kind: controlagents.LaunchKindExecutable, Command: command, Args: append([]string(nil), preset.Args...),
			Env: preset.Env,
		}
	default:
		return controlagents.Connection{}, fmt.Errorf("gatewayapp: unsupported launcher %q for ACP adapter %q", req.Launcher, req.AdapterID)
	}
	return connection, controlagents.ValidateConnection(connection)
}

func catalogLauncherInconsistency(
	adapterID string,
	launcher controlagents.LauncherChoice,
	missing string,
) error {
	return fmt.Errorf(
		"gatewayapp: ACP adapter %q catalog declares launcher %q but has no %s",
		strings.TrimSpace(adapterID),
		launcher,
		strings.TrimSpace(missing),
	)
}

func ensureGlobalACPAgent(ctx context.Context, adapterID string) (string, error) {
	acpAgentInstallMu.Lock()
	defer acpAgentInstallMu.Unlock()
	pkg, ok := builtinACPAdapterPackageFor(adapterID)
	if !ok {
		return "", fmt.Errorf("gatewayapp: ACP adapter %q does not support global npm install", adapterID)
	}
	if command, err := exec.LookPath(pkg.Bin); err == nil && strings.TrimSpace(command) != "" && installedACPAdapterPackageMatches(command, pkg) {
		return absoluteExecutablePath(command)
	}
	installSpec := builtinACPAdapterInstallSpec(pkg)
	if err := runGlobalACPAgentInstall(ctx, globalACPAgentInstallRequest{InstallSpec: installSpec}); err != nil {
		return "", fmt.Errorf("gatewayapp: globally install ACP adapter %q: %w", adapterID, err)
	}
	command, err := exec.LookPath(pkg.Bin)
	if err != nil || strings.TrimSpace(command) == "" {
		return "", fmt.Errorf("gatewayapp: global install of ACP adapter %q did not provide %q on PATH", adapterID, pkg.Bin)
	}
	if !installedACPAdapterPackageMatches(command, pkg) {
		return "", fmt.Errorf("gatewayapp: %q on PATH is not the curated %s adapter after global install", pkg.Bin, builtinACPAdapterInstallSpec(pkg))
	}
	return absoluteExecutablePath(command)
}

type npmPackageManifest struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

func defaultInstalledACPAdapterPackageMatches(command string, pkg builtinACPAdapterPackage) bool {
	command = strings.TrimSpace(command)
	if command == "" || strings.TrimSpace(pkg.Package) == "" {
		return false
	}
	resolved, err := filepath.EvalSymlinks(command)
	if err == nil {
		if manifest, ok := findAncestorNPMPackageManifest(resolved, pkg.Package); ok {
			return npmPackageManifestMatches(manifest, pkg)
		}
	}
	manifestPath := filepath.Join(filepath.Dir(command), "node_modules", filepath.FromSlash(pkg.Package), "package.json")
	manifest, ok := readNPMPackageManifest(manifestPath)
	return ok && npmPackageManifestMatches(manifest, pkg)
}

func managedACPAdapterPackageMatches(root string, pkg builtinACPAdapterPackage) bool {
	manifestPath := filepath.Join(strings.TrimSpace(root), "node_modules", filepath.FromSlash(pkg.Package), "package.json")
	manifest, ok := readNPMPackageManifest(manifestPath)
	return ok && npmPackageManifestMatches(manifest, pkg)
}

func findAncestorNPMPackageManifest(command string, packageName string) (npmPackageManifest, bool) {
	dir := filepath.Dir(command)
	for range 10 {
		if manifest, ok := readNPMPackageManifest(filepath.Join(dir, "package.json")); ok && strings.EqualFold(strings.TrimSpace(manifest.Name), strings.TrimSpace(packageName)) {
			return manifest, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return npmPackageManifest{}, false
}

func readNPMPackageManifest(path string) (npmPackageManifest, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return npmPackageManifest{}, false
	}
	var manifest npmPackageManifest
	if json.Unmarshal(raw, &manifest) != nil {
		return npmPackageManifest{}, false
	}
	return manifest, true
}

func npmPackageManifestMatches(manifest npmPackageManifest, pkg builtinACPAdapterPackage) bool {
	if !strings.EqualFold(strings.TrimSpace(manifest.Name), strings.TrimSpace(pkg.Package)) {
		return false
	}
	version := strings.TrimSpace(pkg.Version)
	if version == "" || strings.HasPrefix(version, "^") || strings.HasPrefix(version, "~") {
		return true
	}
	return strings.TrimSpace(manifest.Version) == version
}

func defaultRunGlobalACPAgentInstall(ctx context.Context, req globalACPAgentInstallRequest) error {
	npm, err := exec.LookPath("npm")
	if err != nil || strings.TrimSpace(npm) == "" {
		return fmt.Errorf("npm is required")
	}
	cmd := exec.CommandContext(ctx, npm, "install", "-g", npmInstallSpecForExec(npm, strings.TrimSpace(req.InstallSpec)))
	output, err := cmd.CombinedOutput()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			err = ctxErr
		}
		if detail := strings.TrimSpace(string(output)); detail != "" {
			return fmt.Errorf("%w\n%s", err, detail)
		}
		return err
	}
	return nil
}

func absoluteExecutablePath(command string) (string, error) {
	command = strings.TrimSpace(command)
	if filepath.IsAbs(command) {
		return command, nil
	}
	return filepath.Abs(command)
}

func splitACPCommandLine(commandLine string) (string, []string, error) {
	command, args, err := plugin.SplitCommand(commandLine)
	if err != nil {
		return "", nil, fmt.Errorf("gatewayapp: parse custom ACP command: %w", err)
	}
	if strings.TrimSpace(command) == "" {
		return "", nil, fmt.Errorf("gatewayapp: custom ACP command is required")
	}
	requested := command
	command, err = exec.LookPath(command)
	if err != nil || strings.TrimSpace(command) == "" {
		return "", nil, fmt.Errorf("gatewayapp: custom ACP command %q was not found", requested)
	}
	command, err = absoluteExecutablePath(command)
	if err != nil {
		return "", nil, err
	}
	return command, append([]string(nil), args...), nil
}
