//go:build !windows

package windows

import (
	"fmt"
	"runtime"

	"github.com/OnslaughtSnail/caelis/ports/sandbox"
)

func newRuntime(cfg Config) (sandbox.Runtime, error) {
	_ = cfg
	return nil, fmt.Errorf("impl/sandbox/windows: elevated Windows sandbox backend is only supported on windows (current=%s)", runtime.GOOS)
}
