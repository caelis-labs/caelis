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
	route, err := sandboxrouter.Current("")
	if err != nil {
		return appserver.ShellResult{}, err
	}
	cfg := sandbox.Config{
		CWD: workspace, RequestedBackend: route.Backend, HelperPath: helper, BaseEnv: shellEnvironment(workspace),
		StateDir: store, HostAuthorityDir: authority, WritableRoots: []string{workspace},
	}
	if route.Backend == sandbox.BackendWindows {
		// Reuse ordinary workspace-write ACLs and their Store-managed temp.
		cfg.BoundedRun = true
	} else {
		cfg.InheritProcessGroup = true
		cfg.ReadOnlySubpaths = []string{".git"}
		// The workspace also provides temporary space. Ambient temp and cache
		// mounts must not add writable host directories.
		cfg.ResourceLimits = &sandbox.ResourceLimits{WritePaths: []string{workspace}, Network: sandbox.NetworkEnabled}
		if route.Backend == sandbox.BackendSeatbelt {
			cfg.ResourceLimits.WritePaths = append(cfg.ResourceLimits.WritePaths, os.DevNull)
		}
	}
	rt, err := sandbox.New(cfg)
	if err != nil {
		return appserver.ShellResult{}, errorcode.Wrap(errorcode.Unavailable, "Control Shell backend is unavailable", err)
	}
	defer rt.Close()
	if status := sandbox.SelectionStatus(rt); status.FallbackToHost || status.ResolvedBackend != route.Backend {
		return appserver.ShellResult{}, errorcode.New(errorcode.Unavailable, "Control Shell backend resolution changed")
	}
	if err := ctx.Err(); err != nil {
		return appserver.ShellResult{}, err
	}
	result, runErr := rt.Run(ctx, sandbox.CommandRequest{
		Command: req.Command,
		Constraints: sandbox.Constraints{
			Route: sandbox.RouteSandbox, Backend: route.Backend, Permission: sandbox.PermissionWorkspaceWrite, Network: sandbox.NetworkEnabled,
		},
	})
	if err := ctx.Err(); err != nil {
		return appserver.ShellResult{}, err
	}
	if errors.Is(runErr, sandbox.ErrCommandOutputLimit) {
		return appserver.ShellResult{}, errorcode.Wrap(errorcode.ResourceExhausted, "Control Shell output limit exceeded", runErr)
	}
	if errorcode.CodeOf(runErr) == errorcode.InvalidArgument {
		return appserver.ShellResult{}, runErr
	}
	if result.Route != sandbox.RouteSandbox || result.Backend != route.Backend ||
		result.ExitCode < 0 || (runErr != nil && !sandbox.IsCommandExit(runErr)) {
		return appserver.ShellResult{}, errorcode.Wrap(errorcode.Unavailable, "Control Shell ended without an exit receipt", runErr)
	}
	return appserver.ShellResult{
		Location: string(sandbox.RouteSandbox), Backend: string(result.Backend), ExitCode: result.ExitCode,
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
