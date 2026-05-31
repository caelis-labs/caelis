//go:build !windows

package windows

import (
	"fmt"
	"runtime"

	"github.com/OnslaughtSnail/caelis/ports/sandbox"
)

func newRuntime(cfg Config) (sandbox.Runtime, error) {
	_ = cfg
	return nil, fmt.Errorf("internal/adapters/sandbox/windows: Windows restricted-token sandbox backend is only supported on windows (current=%s)", runtime.GOOS)
}
