//go:build windows

package gatewayapp

import (
	"github.com/OnslaughtSnail/caelis/internal/sandboxrouter"
	"github.com/OnslaughtSnail/caelis/ports/sandbox"
)

func windowsSandboxRuntime(cfg SandboxConfig, workspaceCWD string, storeDir string) (sandbox.Runtime, SandboxConfig, bool, error) {
	cfg = effectiveSandboxConfig(cfg, workspaceCWD)
	cfg.RequestedType = string(sandbox.BackendWindows)
	route, err := sandboxrouter.ForGOOS("windows", sandbox.BackendWindows)
	if err != nil {
		return nil, cfg, false, err
	}
	runtime, err := sandbox.New(sandbox.Config{
		CWD:                 workspaceCWD,
		RequestedBackend:    sandbox.BackendWindows,
		BackendCandidates:   route.BackendCandidates,
		FallbackInstallHint: route.FallbackInstallHint,
		HelperPath:          cfg.HelperPath,
		StateDir:            storeDir,
		ReadableRoots:       append([]string(nil), cfg.ReadableRoots...),
		WritableRoots:       append([]string(nil), cfg.WritableRoots...),
		ReadOnlySubpaths:    append([]string(nil), cfg.ReadOnlySubpaths...),
	})
	if err != nil {
		return nil, cfg, false, err
	}
	return runtime, cfg, true, nil
}
