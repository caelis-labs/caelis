//go:build !windows

package gatewayapp

import "github.com/OnslaughtSnail/caelis/ports/sandbox"

func windowsSandboxRuntime(cfg SandboxConfig, _ string, _ string) (sandbox.Runtime, SandboxConfig, bool, error) {
	return nil, cfg, false, nil
}
