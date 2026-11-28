package shellproc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/host"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/internal/sandboxrouter"
	"github.com/caelis-labs/caelis/internal/workspaceidentity"
)

func execute(ctx context.Context, req appserver.ShellRequest, store, helper, authority string) (appserver.ShellResult, error) {
	if !filepath.IsAbs(req.Workspace) {
		return appserver.ShellResult{}, errorcode.New(errorcode.InvalidArgument, "workspace must be an absolute directory")
	}
	workspace, err := workspaceidentity.CanonicalDirectory(req.Workspace)
	if err != nil {
		return appserver.ShellResult{}, errorcode.New(errorcode.InvalidArgument, "workspace must be an existing directory")
	}
	store, err = workspaceidentity.CanonicalDirectory(store)
	if err != nil {
		return appserver.ShellResult{}, errorcode.New(errorcode.Unavailable, "Host Store is unavailable")
	}
	if pathWithin(workspace, store) || pathWithin(store, workspace) {
		return appserver.ShellResult{}, errorcode.New(errorcode.PermissionDenied, "workspace overlaps the Host Store")
	}
	if err := ctx.Err(); err != nil {
		return appserver.ShellResult{}, err
	}
	env := shellEnvironment(workspace)
	if req.Location == appserver.ShellHost {
		return runControlShell(ctx, sandbox.Config{CWD: workspace, BaseEnv: env, InheritProcessGroup: runtime.GOOS != "windows"}, req.Location, req.Command)
	}
	route, err := sandboxrouter.Current("")
	if err != nil {
		return appserver.ShellResult{}, err
	}
	cfg := sandbox.Config{
		CWD: workspace, RequestedBackend: route.Backend, HelperPath: helper, BaseEnv: env,
		StateDir: store, HostAuthorityDir: authority,
	}
	if route.Backend == sandbox.BackendWindows {
		// Keep the ordinary workspace ACL receipts and Store-scoped temporary
		// environment. This backend implements write isolation and online access.
		cfg.WritableRoots = []string{workspace}
		cfg.BoundedRun = true
	} else {
		cfg.InheritProcessGroup = true
		read := []string{workspace, "/bin", "/usr/bin", "/usr/lib", "/lib", "/lib64"}
		write := []string{workspace}
		if route.Backend == sandbox.BackendSeatbelt {
			executable, err := os.Executable()
			if err != nil {
				return appserver.ShellResult{}, err
			}
			if cfg.HelperPath == "" {
				cfg.HelperPath = executable
			}
			read = append(read, cfg.HelperPath, executable, "/System/Library", "/System/Cryptexes", "/System/Volumes/Preboot/Cryptexes", os.DevNull)
			write = append(write, os.DevNull)
		}
		cfg.ResourceLimits = &sandbox.ResourceLimits{ReadPaths: read, WritePaths: write, Network: sandbox.NetworkDisabled}
	}
	return runControlShell(ctx, cfg, req.Location, req.Command)
}

func runControlShell(ctx context.Context, cfg sandbox.Config, location appserver.ShellLocation, command string) (appserver.ShellResult, error) {
	var rt sandbox.Runtime
	var err error
	expectedRoute, expectedBackend := sandbox.RouteSandbox, cfg.RequestedBackend
	if location == appserver.ShellHost {
		expectedRoute, expectedBackend = sandbox.RouteHost, sandbox.BackendHost
		rt, err = host.New(host.Config{CWD: cfg.CWD, BaseEnv: cfg.BaseEnv, BoundedRun: true, InheritProcessGroup: cfg.InheritProcessGroup})
	} else {
		rt, err = sandbox.New(cfg)
	}
	if err != nil {
		return appserver.ShellResult{}, errorcode.Wrap(errorcode.Unavailable, "Control Shell backend is unavailable", err)
	}
	defer rt.Close()
	if status := sandbox.SelectionStatus(rt); status.FallbackToHost || status.ResolvedBackend != expectedBackend {
		return appserver.ShellResult{}, errorcode.New(errorcode.Unavailable, "Control Shell backend resolution changed")
	}
	if err := ctx.Err(); err != nil {
		return appserver.ShellResult{}, err
	}
	result, runErr := rt.Run(ctx, sandbox.CommandRequest{
		Command:     command,
		Constraints: sandbox.Constraints{Route: expectedRoute, Backend: expectedBackend},
	})
	if err := ctx.Err(); err != nil {
		return appserver.ShellResult{}, err
	}
	if errors.Is(runErr, sandbox.ErrCommandOutputLimit) {
		return appserver.ShellResult{}, errorcode.Wrap(errorcode.ResourceExhausted, "Control Shell output limit exceeded", runErr)
	}
	if result.Route != expectedRoute || result.Backend != expectedBackend ||
		result.ExitCode < 0 || (runErr != nil && !sandbox.IsCommandExit(runErr)) {
		return appserver.ShellResult{}, errorcode.Wrap(errorcode.Unavailable, "Control Shell ended without an exit receipt", runErr)
	}
	return appserver.ShellResult{
		Location: location, Backend: string(result.Backend), ExitCode: result.ExitCode,
		Stdout: result.Stdout, Stderr: result.Stderr,
	}, nil
}

func shellEnvironment(workspace string) []string {
	if runtime.GOOS != "windows" {
		return []string{"PATH=/usr/bin:/bin", "HOME=" + workspace, "TMPDIR=" + workspace}
	}
	root := os.Getenv("SystemRoot")
	system := filepath.Join(root, "System32")
	return []string{
		"SystemRoot=" + root, "WINDIR=" + root,
		"PATH=" + strings.Join([]string{system, root, filepath.Join(system, "WindowsPowerShell", "v1.0")}, ";"),
		"HOME=" + workspace, "TEMP=" + workspace, "TMP=" + workspace,
	}
}

func pathWithin(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
